from __future__ import annotations

import argparse
import dataclasses as dc
import html
import json
import re
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from runner.agent_client import AgentClient
from runner.html_report import upload_report
from runner.report import git_sha, now_iso, write_manifest


REPO_ROOT = Path(__file__).resolve().parents[2]


@dc.dataclass
class UnitCaseResult:
    suite_name: str
    suite_path: str
    test_id: str
    target_type: str
    target_name: str
    status: str
    input: Any
    output: str
    output_json: Any
    is_error: bool
    duration_ms: int
    error: str = ""


def is_unit_suite(path: str | Path) -> bool:
    try:
        data = json.loads(Path(path).read_text(encoding="utf-8"))
    except Exception:
        return False
    return data.get("kind") == "unit"


def cmd_unit(args: argparse.Namespace) -> int:
    suite_paths = _collect_suites(args)
    if not suite_paths:
        print("no unit suites found", file=sys.stderr)
        return 2

    run_id = datetime.now(timezone.utc).strftime("%Y-%m-%d_%H%M%S")
    run_dir = Path(args.out) / run_id
    started = now_iso()
    sha, dirty = git_sha(REPO_ROOT)
    client = AgentClient(base_url=args.agent_url)
    if not client.health():
        print(f"agent at {args.agent_url} is not reachable", file=sys.stderr)
        return 2

    results: list[UnitCaseResult] = []
    try:
        for suite_path in suite_paths:
            suite_results = _run_suite(client, suite_path)
            results.extend(suite_results)
            passed = sum(1 for r in suite_results if r.status == "passed")
            print(f"{suite_path.name}: {passed}/{len(suite_results)} passed", flush=True)
            for r in suite_results:
                label = "PASS" if r.status == "passed" else "FAIL"
                suffix = f" - {r.error}" if r.error else ""
                print(f"  {label:4s} {r.test_id} {r.duration_ms}ms{suffix}", flush=True)
    finally:
        client.close()

    totals = {
        "tasks": len(results),
        "passed": sum(1 for r in results if r.status == "passed"),
        "failed": sum(1 for r in results if r.status == "failed"),
        "skipped": 0,
        "judge_error": 0,
        "timeout": 0,
    }
    manifest = {
        "run_id": run_id,
        "benchmark_type": "unit",
        "git_sha": sha,
        "git_dirty": dirty,
        "suite_path": str(args.suite or args.suite_dir),
        "suite_paths": [str(p) for p in suite_paths],
        "agent_url": args.agent_url,
        "judge_config": None,
        "started_at": started,
        "finished_at": now_iso(),
        "totals": totals,
    }
    write_manifest(run_dir / "manifest.json", manifest)
    _write_results(run_dir / "results.jsonl", results)
    _write_summary(run_dir / "summary.md", manifest, results)
    report_html = _generate_html(manifest, results)
    (run_dir / "report.html").write_text(report_html, encoding="utf-8")

    upload_client = AgentClient(base_url=args.agent_url)
    if upload_report(upload_client, report_html, run_dir):
        print(f"Report uploaded -> http://{args.agent_url.split('//')[1].split(':')[0]}:80/benchmark")
    else:
        print("Warning: failed to upload report to board")
    upload_client.close()
    print(f"Report: {run_dir / 'report.html'}")
    return 0 if totals["failed"] == 0 else 1


def _collect_suites(args: argparse.Namespace) -> list[Path]:
    if bool(args.suite) == bool(args.suite_dir):
        raise SystemExit("exactly one of --suite or --suite-dir is required")
    if args.suite:
        path = Path(args.suite)
        if not is_unit_suite(path):
            raise SystemExit(f"not a unit suite: {path}")
        return [path]
    root = Path(args.suite_dir)
    return sorted(p for p in root.rglob("*.json") if is_unit_suite(p))


def _run_suite(client: AgentClient, suite_path: Path) -> list[UnitCaseResult]:
    data = json.loads(suite_path.read_text(encoding="utf-8"))
    if data.get("kind") != "unit":
        raise ValueError(f"{suite_path} is not a unit suite")
    target = data.get("target") or {}
    target_type = target.get("type")
    target_name = target.get("name")
    if target_type != "tool" or not target_name:
        raise ValueError(f"{suite_path}: only target.type=tool is supported")
    defaults = data.get("defaults") or {}
    suite_name = data.get("name") or suite_path.stem
    results = []
    for test in data.get("tests") or []:
        results.append(_run_case(client, suite_name, suite_path, target_name, defaults, test))
    return results


def _run_case(
    client: AgentClient,
    suite_name: str,
    suite_path: Path,
    target_name: str,
    defaults: dict[str, Any],
    test: dict[str, Any],
) -> UnitCaseResult:
    test_id = str(test.get("id") or "unnamed")
    test_input = test.get("input") or {}
    timeout_ms = int(test.get("timeout_ms") or defaults.get("timeout_ms") or 10000)
    started = time.monotonic()
    try:
        result = client.invoke_tool(target_name, test_input, timeout=max(1, int(timeout_ms / 1000)))
        normalized_output = _unwrap_tool_output(result.output)
        output_json = _parse_json(normalized_output)
        error = _check_expectation(
            expect=test.get("expect") or {},
            output=normalized_output,
            output_json=output_json,
            is_error=result.is_error,
        )
        return UnitCaseResult(
            suite_name=suite_name,
            suite_path=str(suite_path),
            test_id=test_id,
            target_type="tool",
            target_name=target_name,
            status="failed" if error else "passed",
            input=test_input,
            output=normalized_output,
            output_json=output_json,
            is_error=result.is_error,
            duration_ms=result.duration_ms or int((time.monotonic() - started) * 1000),
            error=error,
        )
    except Exception as e:
        return UnitCaseResult(
            suite_name=suite_name,
            suite_path=str(suite_path),
            test_id=test_id,
            target_type="tool",
            target_name=target_name,
            status="failed",
            input=test_input,
            output="",
            output_json=None,
            is_error=True,
            duration_ms=int((time.monotonic() - started) * 1000),
            error=f"exception: {e}",
        )


def _parse_json(value: str) -> Any:
    try:
        return json.loads(value)
    except Exception:
        return None


def _unwrap_tool_output(value: str) -> str:
    """Extract successful tool JSON from post-action wrapper errors.

    Some tools are wrapped with a screenshot/stable-screen check. The underlying
    tool can succeed while the wrapper returns is_error=true because capture is
    temporarily unavailable. Unit assertions should inspect the tool payload.
    """
    if not isinstance(value, str):
        return value
    match = re.search(r'completed with output "((?:\\.|[^"\\])*)"', value, re.DOTALL)
    if not match:
        return value
    try:
        return json.loads('"' + match.group(1) + '"')
    except Exception:
        return value


def _check_expectation(
    expect: dict[str, Any], output: str, output_json: Any, is_error: bool
) -> str:
    if "ok" in expect:
        expected_ok = bool(expect["ok"])
        actual_ok = not is_error
        if isinstance(output_json, dict) and isinstance(output_json.get("ok"), bool):
            actual_ok = output_json["ok"]
        if actual_ok != expected_ok:
            return f"expected ok={expected_ok}, got ok={actual_ok}"
    text = output if isinstance(output, str) else json.dumps(output, ensure_ascii=False)
    for needle in expect.get("contains") or []:
        if str(needle).lower() not in text.lower():
            return f"output missing text: {needle}"
    for needle in expect.get("not_contains") or []:
        if str(needle).lower() in text.lower():
            return f"output unexpectedly contains text: {needle}"
    json_expect = expect.get("json") or {}
    if json_expect:
        if output_json is None:
            return "output is not JSON"
        for path, rule in json_expect.items():
            exists, value = _resolve_path(output_json, path)
            if rule.get("exists") is True and not exists:
                return f"missing JSON path: {path}"
            if not exists:
                return f"missing JSON path: {path}"
            if "equals" in rule and value != rule["equals"]:
                return f"{path} expected {rule['equals']!r}, got {value!r}"
            if "type" in rule:
                actual = _json_type(value)
                if actual != rule["type"]:
                    return f"{path} expected type {rule['type']}, got {actual}"
            if "min_len" in rule or "max_len" in rule:
                try:
                    actual_len = len(value)
                except TypeError:
                    return f"{path} has no length"
                if "min_len" in rule and actual_len < int(rule["min_len"]):
                    return f"{path} length below {rule['min_len']}"
                if "max_len" in rule and actual_len > int(rule["max_len"]):
                    return f"{path} length above {rule['max_len']}"
    return ""


def _resolve_path(data: Any, path: str) -> tuple[bool, Any]:
    parts = path[2:].split(".") if path.startswith("$.") else path.split(".")
    current = data
    for part in parts:
        if not part:
            continue
        if isinstance(current, dict) and part in current:
            current = current[part]
            continue
        return False, None
    return True, current


def _json_type(value: Any) -> str:
    if isinstance(value, bool):
        return "bool"
    if isinstance(value, list):
        return "array"
    if isinstance(value, dict):
        return "object"
    if value is None:
        return "null"
    if isinstance(value, (int, float)):
        return "number"
    return "string"


def _write_results(path: Path, results: list[UnitCaseResult]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as fp:
        for result in results:
            fp.write(json.dumps(dc.asdict(result), ensure_ascii=False, sort_keys=True) + "\n")


def _write_summary(path: Path, manifest: dict[str, Any], results: list[UnitCaseResult]) -> None:
    totals = manifest["totals"]
    lines = [
        f"# Unit tests - {manifest['run_id']}",
        "",
        f"Agent: {manifest.get('agent_url', '')}",
        f"Total: {totals['passed']}/{totals['tasks']} passed",
        "",
        "## Suites",
        "",
        "| suite | passed | total |",
        "|---|---:|---:|",
    ]
    suite_names = sorted({r.suite_name for r in results})
    for suite in suite_names:
        suite_results = [r for r in results if r.suite_name == suite]
        passed = sum(1 for r in suite_results if r.status == "passed")
        lines.append(f"| {suite} | {passed} | {len(suite_results)} |")
    lines += ["", "## Failures", ""]
    failures = [r for r in results if r.status != "passed"]
    if not failures:
        lines.append("None")
    for result in failures:
        lines.append(f"- **{result.suite_name}.{result.test_id}** - {result.error}")
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def _generate_html(manifest: dict[str, Any], results: list[UnitCaseResult]) -> str:
    totals = manifest["totals"]
    rows = ""
    for r in results:
        cls = "pass" if r.status == "passed" else "fail"
        rows += (
            "<tr>"
            f"<td>{html.escape(r.suite_name)}</td>"
            f"<td><code>{html.escape(r.test_id)}</code></td>"
            f"<td class='{cls}'>{html.escape(r.status)}</td>"
            f"<td>{r.duration_ms}ms</td>"
            f"<td>{html.escape(r.error)}</td>"
            f"<td><pre>{html.escape(json.dumps(r.input, ensure_ascii=False, indent=2))}</pre></td>"
            f"<td><pre>{html.escape(r.output[:2000])}</pre></td>"
            "</tr>"
        )
    return f"""<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Unit Tests - {html.escape(manifest['run_id'])}</title>
<style>
body{{font-family:system-ui,-apple-system,sans-serif;margin:24px;background:#f8fafc;color:#111827}}
.card{{background:white;border:1px solid #e5e7eb;border-radius:12px;padding:16px;margin-bottom:16px}}
table{{width:100%;border-collapse:collapse;background:white;font-size:13px}}
th,td{{text-align:left;vertical-align:top;border-bottom:1px solid #e5e7eb;padding:8px}}
th{{color:#6b7280;font-size:11px;text-transform:uppercase}}
pre{{white-space:pre-wrap;word-break:break-word;max-width:360px;margin:0;font-size:12px}}
.pass{{color:#16a34a;font-weight:700}}.fail{{color:#dc2626;font-weight:700}}
</style></head><body>
<div class="card"><h1>Unit Tests</h1><p>Run: {html.escape(manifest['run_id'])}</p>
<p>Total: {totals['passed']}/{totals['tasks']} passed, Failed: {totals['failed']}</p></div>
<table><thead><tr><th>Suite</th><th>Test</th><th>Status</th><th>Latency</th><th>Error</th><th>Input</th><th>Output</th></tr></thead><tbody>{rows}</tbody></table>
</body></html>"""
