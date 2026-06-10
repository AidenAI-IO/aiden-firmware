from __future__ import annotations

import argparse
import html
import json
import sys
from pathlib import Path
from typing import Any


TASK_STATUSES = ("passed", "failed", "error", "unknown", "worker_failed")
SUMMARY_STATUSES = TASK_STATUSES + ("empty",)


def generate_reports(batch_dir: str | Path) -> dict[str, Any]:
    batch = Path(batch_dir)
    if not batch.exists() or not batch.is_dir():
        raise FileNotFoundError(f"batch directory not found: {batch}")

    suite_summaries = []
    all_rows: list[dict[str, Any]] = []
    for suite_dir in sorted(path for path in batch.iterdir() if path.is_dir()):
        shard_dirs = _suite_shard_dirs(suite_dir)
        if not shard_dirs:
            continue
        rows: list[dict[str, Any]] = []
        shard_metadatas: list[dict[str, Any]] = []
        for shard_dir in shard_dirs:
            shard_rows, metadata = _normalize_shard(shard_dir)
            rows.extend(shard_rows)
            shard_metadatas.append(metadata)
        summary = _summary_for(suite_dir.name, rows, shard_metadatas)
        _write_suite_report(suite_dir, rows, summary)
        suite_summaries.append(summary)
        all_rows.extend(rows)

    batch_summary = _batch_summary(batch.name, suite_summaries, all_rows)
    _write_batch_report(batch, suite_summaries, batch_summary)
    return batch_summary


def _suite_shard_dirs(suite_dir: Path) -> list[Path]:
    return sorted(path.parent for path in suite_dir.glob("*/shard.json") if path.is_file())


def _normalize_shard(shard_dir: Path) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    metadata = _read_json(shard_dir / "shard.json")
    raw_dir = shard_dir / "raw"
    results, result_links = _read_results(raw_dir)
    errors, error_links = _read_errors(raw_dir)
    console_links = [path.relative_to(shard_dir).as_posix() for path in sorted(raw_dir.glob("**/console.log"))]

    selected_task_ids = [str(task_id) for task_id in metadata.get("selected_task_ids") or []]
    selected_task_count = int(metadata.get("selected_task_count") or len(selected_task_ids))
    exit_code = int(metadata.get("exit_code") or 0)
    shard_name = shard_dir.name
    suite = str(metadata.get("suite") or shard_dir.parent.name)

    def _prefixed(path: str) -> str:
        return f"{shard_name}/{path}" if path else ""

    links = {
        "runner": _prefixed("runner.log") if (shard_dir / "runner.log").exists() else "",
        "compose": _prefixed("compose.log") if (shard_dir / "compose.log").exists() else "",
        "results": [_prefixed(path) for path in result_links],
        "errors": [_prefixed(path) for path in error_links],
        "console": [_prefixed(path) for path in console_links],
    }

    if selected_task_count == 0 and exit_code == 0:
        return [
            {
                "task_id": "",
                "suite": suite,
                "shard": shard_name,
                "status": "empty",
                "reason": "empty shard",
                "links": links,
            }
        ], metadata

    task_ids = list(dict.fromkeys(selected_task_ids + sorted(set(results) | set(errors))))
    rows = []
    for task_id in task_ids:
        result = results.get(task_id)
        error = errors.get(task_id)
        status, reason = _status_for(result, error)
        if result is None and error is None:
            status = "worker_failed" if exit_code != 0 else "unknown"
            reason = "missing result"
        rows.append(
            {
                "task_id": task_id,
                "suite": suite,
                "shard": shard_name,
                "status": status,
                "reason": reason,
                "links": links,
            }
        )
    return rows, metadata


def _read_results(raw_dir: Path) -> tuple[dict[str, dict[str, Any]], list[str]]:
    results: dict[str, dict[str, Any]] = {}
    links = []
    for path in sorted(raw_dir.glob("**/results.jsonl")):
        links.append(path.relative_to(raw_dir.parent).as_posix())
        for row in _read_jsonl(path):
            task_id = _row_task_id(row)
            if task_id:
                results[task_id] = row
    return results, links


def _read_errors(raw_dir: Path) -> tuple[dict[str, dict[str, Any]], list[str]]:
    errors: dict[str, dict[str, Any]] = {}
    links = []
    for path in sorted(raw_dir.glob("**/errors.jsonl")):
        links.append(path.relative_to(raw_dir.parent).as_posix())
        for row in _read_jsonl(path):
            task_id = _row_task_id(row)
            if task_id:
                errors[task_id] = row
    return errors, links


def _read_jsonl(path: Path) -> list[dict[str, Any]]:
    rows = []
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(row, dict):
            rows.append(row)
    return rows


def _status_for(result: dict[str, Any] | None, error: dict[str, Any] | None) -> tuple[str, str]:
    if error is not None:
        return "error", str(error.get("error") or error.get("message") or "errors.jsonl")
    if result is None:
        return "unknown", "missing result"

    stop_reason = _stop_reason(result)
    if result.get("is_error") is True or stop_reason in {"overdue_termination", "timeout", "crash", "exception"}:
        return "error", stop_reason or "is_error"
    if stop_reason == "false_complete":
        return "failed", stop_reason
    if result.get("is_success") is True:
        return "passed", "is_success"
    if result.get("is_success") is False:
        return "failed", "is_success false"

    status = result.get("status")
    if status == "passed":
        return "passed", "status"
    if status == "failed":
        return "failed", "status"
    if result.get("success") is True or result.get("passed") is True:
        return "passed", "fallback"
    if result.get("success") is False or result.get("passed") is False:
        return "failed", "fallback false"
    for key in ("assertion_passed", "assertions_passed", "evaluation_passed", "eval_passed", "rubric_passed"):
        if result.get(key) is False:
            return "failed", key
    return "unknown", "unrecognized result"


def _stop_reason(result: dict[str, Any]) -> str:
    execution = result.get("execution")
    if isinstance(execution, dict) and execution.get("stop_reason"):
        return str(execution["stop_reason"])
    if result.get("stop_reason"):
        return str(result["stop_reason"])
    return ""


def _row_task_id(row: dict[str, Any]) -> str:
    for key in ("id", "task_id", "name"):
        if row.get(key):
            return str(row[key])
    return ""


def _summary_for(suite: str, rows: list[dict[str, Any]], shard_metadatas: list[dict[str, Any]]) -> dict[str, Any]:
    summary: dict[str, Any] = {
        "suite": suite,
        "shards": len(shard_metadatas),
        "tasks": sum(1 for row in rows if row["status"] != "empty"),
        "cleanup_failed": sum(1 for metadata in shard_metadatas if int(metadata.get("cleanup_failed") or 0) != 0),
    }
    for status in SUMMARY_STATUSES:
        summary[status] = sum(1 for row in rows if row["status"] == status)
    denominator = sum(int(summary[status]) for status in TASK_STATUSES)
    summary["pass_rate"] = (summary["passed"] / denominator) if denominator else 0.0
    return summary


def _batch_summary(batch_id: str, suite_summaries: list[dict[str, Any]], rows: list[dict[str, Any]]) -> dict[str, Any]:
    summary: dict[str, Any] = {"batch_id": batch_id, "suites": suite_summaries}
    for key in ("shards", "tasks", "cleanup_failed") + SUMMARY_STATUSES:
        summary[key] = sum(int(suite.get(key) or 0) for suite in suite_summaries)
    denominator = sum(int(summary[status]) for status in TASK_STATUSES)
    summary["pass_rate"] = (summary["passed"] / denominator) if denominator else 0.0
    summary["rows"] = rows
    return summary


def _write_suite_report(suite_dir: Path, rows: list[dict[str, Any]], summary: dict[str, Any]) -> None:
    (suite_dir / "summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8")
    (suite_dir / "index.html").write_text(_html_page(summary["suite"], summary, rows, suite_dir), encoding="utf-8")


def _write_batch_report(batch_dir: Path, suite_summaries: list[dict[str, Any]], summary: dict[str, Any]) -> None:
    serializable = dict(summary)
    serializable.pop("rows", None)
    (batch_dir / "summary.json").write_text(json.dumps(serializable, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8")
    rows = []
    for suite in suite_summaries:
        rows.append(
            {
                "task_id": suite["suite"],
                "suite": suite["suite"],
                "shard": f"{suite['shards']} shards",
                "status": "passed" if suite.get("failed", 0) == suite.get("error", 0) == suite.get("worker_failed", 0) == 0 else "failed",
                "reason": f"{suite['passed']}/{suite['tasks']} passed",
                "links": {"suite": [f"{suite['suite']}/index.html"]},
            }
        )
    (batch_dir / "index.html").write_text(_html_page(summary["batch_id"], serializable, rows, batch_dir), encoding="utf-8")


def _html_page(title: str, summary: dict[str, Any], rows: list[dict[str, Any]], base_dir: Path) -> str:
    del base_dir
    row_html = "".join(_row_html(row) for row in rows)
    return f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>MobileGym {html.escape(title)}</title>
<style>
body {{ font-family: -apple-system, BlinkMacSystemFont, sans-serif; margin: 32px; color: #17202a; }}
.cards {{ display: flex; flex-wrap: wrap; gap: 12px; margin-bottom: 24px; }}
.card {{ border: 1px solid #d8dee4; border-radius: 10px; padding: 12px 14px; min-width: 110px; }}
.label {{ color: #667085; font-size: 12px; }}
.value {{ font-size: 24px; font-weight: 700; margin-top: 4px; }}
table {{ border-collapse: collapse; width: 100%; font-size: 13px; }}
th, td {{ border-bottom: 1px solid #e5e7eb; padding: 10px; text-align: left; vertical-align: top; }}
.passed {{ color: #067647; }} .failed,.error,.worker_failed {{ color: #b42318; }} .unknown,.empty {{ color: #93370d; }}
a {{ color: #175cd3; }}
</style>
</head>
<body>
<h1>MobileGym {html.escape(title)}</h1>
<div class="cards">
{_summary_cards(summary)}
</div>
<table>
<thead><tr><th>Task</th><th>Suite</th><th>Shard</th><th>Status</th><th>Reason</th><th>Artifacts</th></tr></thead>
<tbody>{row_html}</tbody>
</table>
</body>
</html>
"""


def _summary_cards(summary: dict[str, Any]) -> str:
    keys = ["tasks", "passed", "failed", "error", "unknown", "worker_failed", "empty", "pass_rate"]
    cards = []
    for key in keys:
        value = summary.get(key, 0)
        if key == "pass_rate":
            value = f"{float(value) * 100:.1f}%"
        cards.append(f'<div class="card"><div class="label">{html.escape(key)}</div><div class="value">{html.escape(str(value))}</div></div>')
    return "\n".join(cards)


def _row_html(row: dict[str, Any]) -> str:
    status = str(row.get("status", "unknown"))
    return "<tr>" + "".join(
        [
            f"<td>{html.escape(str(row.get('task_id') or '-'))}</td>",
            f"<td>{html.escape(str(row.get('suite') or ''))}</td>",
            f"<td>{html.escape(str(row.get('shard') or ''))}</td>",
            f'<td class="{html.escape(status)}">{html.escape(status)}</td>',
            f"<td>{html.escape(str(row.get('reason') or ''))}</td>",
            f"<td>{_links_html(row.get('links') or {})}</td>",
        ]
    ) + "</tr>\n"


def _links_html(links: dict[str, Any]) -> str:
    parts = []
    for key, value in sorted(links.items()):
        values = value if isinstance(value, list) else [value]
        for item in values:
            if item:
                escaped = html.escape(str(item))
                parts.append(f'<a href="{escaped}">{html.escape(str(key))}: {escaped}</a>')
    return "<br>".join(parts)


def _read_json(path: Path) -> dict[str, Any]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}
    return data if isinstance(data, dict) else {}


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Generate MobileGym batch reports.")
    parser.add_argument("batch_dir", type=Path)
    args = parser.parse_args(argv)
    try:
        generate_reports(args.batch_dir)
    except FileNotFoundError as exc:
        print(str(exc), file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
