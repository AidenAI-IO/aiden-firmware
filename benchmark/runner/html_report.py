"""Generate a self-contained HTML benchmark report with drawer UI."""
from __future__ import annotations
import html as html_mod
import json
from pathlib import Path
from typing import Any

from runner.agent_client import AgentClient


def _esc(s: str) -> str:
    return html_mod.escape(str(s)) if s else ""


def _safe_child_path(root: Path, *parts: str) -> Path | None:
    try:
        root_resolved = root.resolve()
        candidate = root.joinpath(*parts).resolve()
        candidate.relative_to(root_resolved)
    except (OSError, ValueError):
        return None
    return candidate


def _read_excerpt(path: Path, max_chars: int = 6000) -> str:
    try:
        return path.read_text("utf-8", errors="replace")[:max_chars]
    except OSError:
        return ""


def _task_artifact_dirs(run_dir: Path, task_id: str) -> list[Path]:
    task_dir = _safe_child_path(run_dir / "tasks", task_id)
    if task_dir is None or not task_dir.is_dir():
        return []
    dirs = [task_dir]
    for path in sorted(task_dir.glob("attempt_*")):
        safe = _safe_child_path(run_dir, str(path.relative_to(run_dir)))
        if safe is not None and safe.is_dir():
            dirs.append(safe)
    return dirs


FAIL_STATUSES = {"failed", "timeout", "judge_error"}


def _task_artifact_refs(run_dir: Path, task_id: str) -> str:
    artifact_refs: list[str] = []
    for artifact_dir in _task_artifact_dirs(run_dir, task_id):
        for name in ("trace.json", "history.json", "judge.json"):
            path = artifact_dir / name
            if not path.exists():
                continue
            artifact_refs.append(str(path.relative_to(run_dir)))
        for path in sorted(artifact_dir.glob("**/*")):
            if path.suffix.lower() in {".jpg", ".jpeg", ".png", ".webp"}:
                artifact_refs.append(str(path.relative_to(run_dir)))
    return "\n".join(dict.fromkeys(artifact_refs))


def _task_error_log(
    run_dir: Path,
    task_id: str,
    status: str,
    errors: list[list[Any]],
    hard_assertions: list[list[Any]],
) -> str:
    if status not in FAIL_STATUSES and not errors and not hard_assertions:
        return ""
    parts: list[str] = []
    if hard_assertions:
        lines = [f"- {item[0]}: {item[1]}" for item in hard_assertions]
        parts.append("### Hard assertion failures\n" + "\n".join(lines))
    if errors:
        lines = [f"- {item[0]}: {item[1]}" for item in errors]
        parts.append("### Runtime errors\n" + "\n".join(lines))
    for artifact_dir in _task_artifact_dirs(run_dir, task_id):
        judge_path = artifact_dir / "judge.json"
        if judge_path.exists():
            parts.append(f"### {judge_path.relative_to(run_dir)}\n" + _read_excerpt(judge_path, 4000))
    return "\n\n".join(part for part in parts if part.strip())


def _analysis_html(run_dir: Path) -> str:
    md = run_dir / "llm_analysis.md"
    err = run_dir / "llm_analysis_error.txt"
    if md.exists():
        text = md.read_text("utf-8")[:20000]
        return f'<section class="analysis"><h2>LLM Analysis</h2><pre>{_esc(text)}</pre></section>'
    if err.exists():
        text = err.read_text("utf-8")[:4000]
        return f'<section class="analysis warning"><h2>LLM Analysis</h2><pre>{_esc(text)}</pre></section>'
    return ""


def analysis_html_for_run_dir(run_dir: Path) -> str:
    return _analysis_html(run_dir)


def generate_report_html(run_dir: Path) -> str:
    run_dir = run_dir.resolve()
    manifest = json.loads((run_dir / "manifest.json").read_text("utf-8"))
    results_path = run_dir / "results.jsonl"
    results: list[dict[str, Any]] = []
    if results_path.exists():
        for line in results_path.read_text("utf-8").strip().splitlines():
            results.append(json.loads(line))

    run_id = manifest.get("run_id", run_dir.name)
    suite_path = manifest.get("suite_path", "")
    suite_name = suite_path.rsplit("/", 1)[-1] if suite_path else "unknown"
    totals = manifest.get("totals", {})
    passed = totals.get("passed", 0)
    total = totals.get("tasks", len(results))
    failed = totals.get("failed", 0)
    skipped = totals.get("skipped", 0)
    timeout = totals.get("timeout", 0)
    judge_error = totals.get("judge_error", 0)
    # completed = everything that produced a result (the whole run is finished by report time)
    completed = total
    agent_url = manifest.get("agent_url", "")
    started = manifest.get("started_at", "")[:19]
    finished = manifest.get("finished_at", "")[:19]
    pass_rate = f"{passed/total*100:.1f}%" if total else "0%"
    # Progress bar segment widths (% of total)
    def _pct(n: int) -> float:
        return (n / total * 100) if total else 0.0
    pass_pct = f"{_pct(passed):.4f}"
    fail_pct = f"{_pct(failed + timeout + judge_error):.4f}"
    skip_pct = f"{_pct(skipped):.4f}"

    tasks_js_items = []
    for r in results:
        tid = r.get("task_id", "")
        task_dir = _safe_child_path(run_dir / "tasks", tid) if tid else None
        trace_path = task_dir / "trace.json" if task_dir is not None else run_dir / "__missing_trace.json"
        history_path = task_dir / "history.json" if task_dir is not None else run_dir / "__missing_history.json"
        trace_data: dict[str, Any] = {}
        prompt = ""
        if trace_path.exists():
            try:
                trace_data = json.loads(trace_path.read_text("utf-8"))
            except Exception:
                pass
        if history_path.exists():
            try:
                history = json.loads(history_path.read_text("utf-8"))
                for msg in history:
                    if msg.get("type") == "user":
                        prompt = msg.get("content", "")
                        break
            except Exception:
                pass

        tool_calls = trace_data.get("tool_calls", [])
        tool_calls_str = ""
        for tc in tool_calls:
            name = tc.get("tool", "")
            inp = json.dumps(tc.get("input", {}), ensure_ascii=False, indent=2)
            tool_calls_str += f"[{name}]\n{inp}\n\n"

        status = r.get("status", "?")
        rubric_items = r.get("rubric", [])
        rubric_spec = r.get("rubric_spec", [])

        rubric_js = []
        if rubric_items:
            for v in rubric_items:
                rubric_js.append([v.get("id",""), v.get("reason",""), v.get("verdict","")])
        elif rubric_spec:
            for s in rubric_spec:
                rubric_js.append([s.get("id",""), s.get("check",""), "\u2014"])

        # Extract hard assertions
        hard_assertions = r.get("hard_assertions", {})
        ha_failures = []
        if hard_assertions:
            if hard_assertions.get("timeout") is False:
                ha_failures.append(["Timeout", "Task did not complete within time limit", "no"])
            if hard_assertions.get("response_exists") is False:
                ha_failures.append(["Response Exists", "Agent did not produce a response", "no"])
            if hard_assertions.get("min_tool_calls") is False:
                ha_failures.append(["Min Tool Calls", "Did not meet minimum tool call requirement", "no"])
            if hard_assertions.get("max_tool_calls") is False:
                ha_failures.append(["Max Tool Calls", "Exceeded maximum tool call limit", "no"])
            if hard_assertions.get("required_tools") is False:
                ha_failures.append(["Required Tools", "Missing one or more required tool calls", "no"])
            if hard_assertions.get("forbidden_tools") is False:
                ha_failures.append(["Forbidden Tools", "Used one or more forbidden tools", "no"])
            if hard_assertions.get("expected_answer") is False:
                expected = r.get("metrics", {}).get("expected_answer", "")
                predicted = r.get("metrics", {}).get("predicted_answer", "")
                ha_failures.append(["Expected Answer", f"Expected: {expected}, Got: {predicted}", "no"])
            if hard_assertions.get("expected_recalled_memory") is False:
                ha_failures.append(["Expected Recalled Memory", "Did not recall expected memory items", "no"])

        # Extract errors
        errors = []
        metrics = r.get("metrics", {})
        if "error" in metrics:
            errors.append(["Error", metrics["error"]])
        if "agent_error" in metrics:
            errors.append(["Agent Error", metrics["agent_error"]])
        if "judge_error" in metrics:
            errors.append(["Judge Error", metrics["judge_error"]])
        artifacts_detail = _task_artifact_refs(run_dir, tid)
        error_log_detail = _task_error_log(run_dir, tid, str(status), errors, ha_failures)

        tasks_js_items.append({
            "id": tid,
            "category": r.get("category", ""),
            "status": status,
            "wall_ms": r.get("metrics", {}).get("wall_ms", 0),
            "tool_calls_count": r.get("metrics", {}).get("tool_calls", 0),
            "screenshots_taken": r.get("metrics", {}).get("screenshots_taken", 0),
            "rubric_pass": r.get("rubric_pass_count", 0),
            "rubric_total": r.get("rubric_total", 0),
            "description": r.get("description_for_judge", ""),
            "prompt": prompt,
            "response": trace_data.get("final_response", ""),
            "tool_calls_detail": tool_calls_str.strip(),
            "rubric": rubric_js,
            "hard_assertions": ha_failures,
            "errors": errors,
            "error_log_detail": error_log_detail,
            "artifacts_detail": artifacts_detail,
            "trace_observations": [
                [
                    obs.get("id", ""),
                    obs.get("reason", obs.get("description", "")),
                    "yes" if obs.get("passed") else "no",
                ]
                for obs in r.get("metrics", {}).get("trace_observations") or []
            ],
        })

    tasks_json = json.dumps(tasks_js_items, ensure_ascii=False, indent=2)
    # Escape </script> to prevent XSS script-breakout
    tasks_json = tasks_json.replace("</", r"<\/")

    rows_html = ""
    for i, t in enumerate(tasks_js_items):
        status = t["status"]
        badge_cls = "pass" if status == "passed" else "fail" if status in {"failed", "timeout", "judge_error"} else "skip"
        badge_label = "Pass" if status == "passed" else "Fail" if status == "failed" else status.title()
        rows_html += f"""<tr data-task="{i}">
  <td><span class="task-id">{_esc(t['id'])}</span></td>
  <td>{_esc(t['category'])}</td>
  <td><span class="badge {badge_cls}">{badge_label}</span></td>
  <td class="mono">{t['rubric_pass']}/{t['rubric_total']}</td>
  <td class="mono">{t['tool_calls_count']}</td>
  <td class="mono">{t['wall_ms']}ms</td>
</tr>\n"""

    return HTML_TEMPLATE.format(
        run_id=run_id, suite=suite_name, passed=passed, total=total,
        failed=failed, skipped=skipped, pass_rate=pass_rate,
        agent_url=agent_url, started=started, finished=finished,
        rows_html=rows_html, tasks_json=tasks_json,
        completed=completed, timeout=timeout, judge_error=judge_error,
        pass_pct=pass_pct, fail_pct=fail_pct, skip_pct=skip_pct,
        analysis_html=_analysis_html(run_dir),
    )


def upload_report(client: AgentClient, html: str, run_dir: Path | None = None) -> bool:
    """Upload report HTML to the board via the agent's shell tool."""
    import base64
    try:
        # Use base64 to avoid heredoc delimiter collision
        encoded = base64.b64encode(html.encode("utf-8")).decode("ascii")
        cmd = ("mkdir -p /userdata/agent/benchmark && "
               f"printf '%s' '{encoded}' | base64 -d > /userdata/agent/benchmark/report.html")
        if run_dir is not None:
            run_id = run_dir.name
            manifest = run_dir / "manifest.json"
            manifest_text = manifest.read_text("utf-8")
            json.loads(manifest_text)
            manifest_encoded = base64.b64encode(manifest_text.encode("utf-8")).decode("ascii")
            board_run_dir = f"/userdata/agent/benchmark/runs/{run_id}"
            cmd = (
                f"mkdir -p {board_run_dir} /userdata/agent/benchmark && "
                f"printf '%s' '{encoded}' | base64 -d > {board_run_dir}/report.html && "
                f"printf '%s' '{manifest_encoded}' | base64 -d > {board_run_dir}/manifest.json && "
                f"cp {board_run_dir}/report.html /userdata/agent/benchmark/report.html"
            )
            analysis_parts = []
            for name in ("llm_analysis.md", "llm_analysis.json", "llm_analysis_error.txt"):
                path = run_dir / name
                if not path.exists():
                    continue
                text = path.read_text("utf-8")
                encoded_part = base64.b64encode(text.encode("utf-8")).decode("ascii")
                analysis_parts.append(
                    f"printf '%s' '{encoded_part}' | base64 -d > {board_run_dir}/{name}"
                )
            if analysis_parts:
                cmd += " && " + " && ".join(analysis_parts)
        result = client.invoke_tool("shell", {"command": cmd})
        return not result.is_error
    except Exception:
        return False


HTML_TEMPLATE = """<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Benchmark: {suite} \u2014 {run_id}</title>
<style>
:root {{
  --bg: oklch(99% 0.002 240);
  --surface: oklch(100% 0 0);
  --fg: oklch(18% 0.012 250);
  --muted: oklch(54% 0.012 250);
  --border: oklch(92% 0.005 250);
  --accent: oklch(58% 0.18 255);
  --font-body: -apple-system, BlinkMacSystemFont, system-ui, sans-serif;
  --font-mono: ui-monospace, "SF Mono", Menlo, Monaco, Consolas, monospace;
}}
* {{ box-sizing: border-box; margin: 0; padding: 0 }}
html {{ background: var(--bg); color: var(--fg); font-family: var(--font-body); -webkit-font-smoothing: antialiased }}
body {{ min-height: 100vh; background: linear-gradient(to bottom, var(--surface), var(--bg) 360px) }}
.page {{ width: min(1200px, calc(100vw - 48px)); margin: 0 auto; padding: 24px 0 56px }}
.topbar {{ display: flex; align-items: center; justify-content: space-between; padding: 14px 0; border-bottom: 1px solid var(--border); margin-bottom: 24px }}
.brand {{ display: flex; align-items: center; gap: 10px }}
.mark {{ width: 24px; height: 24px; border-radius: 6px; background: var(--fg); position: relative }}
.mark::before,.mark::after {{ content: ""; position: absolute; left: 6px; right: 6px; height: 2px; border-radius: 999px; background: white }}
.mark::before {{ top: 7px }} .mark::after {{ bottom: 7px }}
.brand strong {{ font-size: 14px; letter-spacing: -0.01em }}
.meta-info {{ color: var(--muted); font-size: 12px }}
.summary {{ display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; margin-bottom: 20px }}
.summary-card {{ border: 1px solid var(--border); border-radius: 14px; background: var(--surface); padding: 14px }}
.summary-card .label {{ color: var(--muted); font-size: 11px; font-weight: 700; letter-spacing: 0.04em; text-transform: uppercase }}
.summary-card .value {{ margin-top: 10px; font-size: 28px; font-weight: 700; letter-spacing: -0.03em }}
.progress {{ border: 1px solid var(--border); border-radius: 14px; background: var(--surface); padding: 14px 16px; margin-bottom: 20px }}
.progress-head {{ display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 10px }}
.progress-head .label {{ color: var(--muted); font-size: 11px; font-weight: 700; letter-spacing: 0.04em; text-transform: uppercase }}
.progress-head .count {{ font-family: var(--font-mono); font-size: 14px; font-weight: 700; font-variant-numeric: tabular-nums }}
.progress-bar {{ display: flex; height: 10px; border-radius: 999px; overflow: hidden; background: color-mix(in oklch, var(--bg) 60%, var(--border)) }}
.progress-bar .seg {{ height: 100% }}
.progress-bar .seg.pass {{ background: oklch(58% 0.16 145) }}
.progress-bar .seg.fail {{ background: oklch(60% 0.18 28) }}
.progress-bar .seg.skip {{ background: oklch(70% 0.14 75) }}
.progress-legend {{ display: flex; flex-wrap: wrap; gap: 14px; margin-top: 10px; font-size: 12px; color: var(--muted) }}
.progress-legend span {{ display: inline-flex; align-items: center; gap: 5px }}
.progress-legend i {{ width: 9px; height: 9px; border-radius: 3px; display: inline-block }}
.progress-legend i.pass {{ background: oklch(58% 0.16 145) }}
.progress-legend i.fail {{ background: oklch(60% 0.18 28) }}
.progress-legend i.skip {{ background: oklch(70% 0.14 75) }}
.analysis {{ border: 1px solid var(--border); border-radius: 16px; background: var(--surface); padding: 16px; margin-bottom: 20px }}
.analysis h2 {{ font-size: 14px; margin-bottom: 10px }}
.analysis pre {{ white-space: pre-wrap; font-family: var(--font-mono); font-size: 12px; line-height: 1.5; color: var(--fg) }}
.analysis.warning {{ border-color: color-mix(in oklch, oklch(60% 0.18 28) 35%, var(--border)) }}
.panel {{ border: 1px solid var(--border); border-radius: 16px; background: var(--surface); overflow: hidden }}
.panel-header {{ display: flex; justify-content: space-between; align-items: center; padding: 14px 16px; border-bottom: 1px solid var(--border) }}
.panel-title {{ font-size: 14px; font-weight: 650 }}
.task-table {{ width: 100%; border-collapse: collapse; font-size: 13px }}
.task-table th {{ padding: 10px 14px; text-align: left; color: var(--muted); font-size: 11px; font-weight: 700; letter-spacing: 0.04em; text-transform: uppercase; background: color-mix(in oklch, var(--bg) 80%, white); border-bottom: 1px solid var(--border); position: sticky; top: 0 }}
.task-table td {{ padding: 12px 14px; border-bottom: 1px solid var(--border) }}
.task-table tr {{ cursor: pointer }}
.task-table tbody tr:hover {{ background: color-mix(in oklch, var(--accent) 4%, white) }}
.task-table tbody tr.active {{ background: color-mix(in oklch, var(--accent) 7%, white) }}
.task-id {{ font-family: var(--font-mono); font-size: 12px; color: var(--muted) }}
.mono {{ font-family: var(--font-mono); font-variant-numeric: tabular-nums }}
.badge {{ display: inline-flex; align-items: center; height: 22px; padding: 0 8px; border-radius: 999px; border: 1px solid var(--border); font-size: 11px; font-weight: 700 }}
.badge.pass {{ color: oklch(42% 0.13 150); border-color: color-mix(in oklch, oklch(58% 0.16 145) 32%, var(--border)); background: color-mix(in oklch, oklch(58% 0.16 145) 8%, white) }}
.badge.fail {{ color: oklch(48% 0.16 28); border-color: color-mix(in oklch, oklch(60% 0.18 28) 28%, var(--border)); background: color-mix(in oklch, oklch(60% 0.18 28) 7%, white) }}
.badge.skip {{ color: oklch(48% 0.12 75); border-color: color-mix(in oklch, oklch(70% 0.14 75) 34%, var(--border)); background: color-mix(in oklch, oklch(70% 0.14 75) 10%, white) }}
.drawer-backdrop {{ position: fixed; inset: 0; z-index: 40; background: color-mix(in oklch, var(--fg) 18%, transparent); opacity: 0; pointer-events: none; transition: opacity 180ms ease }}
.drawer {{ position: fixed; top: 0; right: 0; bottom: 0; z-index: 50; width: min(720px, 100vw); background: var(--surface); border-left: 1px solid var(--border); box-shadow: -24px 0 80px color-mix(in oklch, var(--fg) 10%, transparent); transform: translateX(100%); transition: transform 220ms ease; display: flex; flex-direction: column }}
body.open .drawer-backdrop {{ opacity: 1; pointer-events: auto }}
body.open .drawer {{ transform: translateX(0) }}
.drawer-top {{ padding: 18px; border-bottom: 1px solid var(--border); background: linear-gradient(to bottom, color-mix(in oklch, var(--bg) 72%, white), var(--surface)); flex-shrink: 0 }}
.drawer-top h2 {{ font-size: 18px; letter-spacing: -0.02em; margin-bottom: 8px }}
.drawer-chips {{ display: flex; flex-wrap: wrap; gap: 6px }}
.chip {{ display: inline-flex; align-items: center; height: 24px; padding: 0 8px; border-radius: 999px; border: 1px solid var(--border); background: var(--surface); color: var(--muted); font-size: 11px }}
.close-btn {{ position: absolute; top: 16px; right: 16px; width: 30px; height: 30px; border: 1px solid var(--border); border-radius: 8px; background: var(--surface); color: var(--fg); font-size: 16px; cursor: pointer; display: flex; align-items: center; justify-content: center }}
.drawer-body {{ flex: 1; min-height: 0; overflow-y: scroll; -webkit-overflow-scrolling: touch; padding: 16px }}
.drawer-body > .block {{ margin-bottom: 12px }}
.drawer-body > .block:last-child {{ margin-bottom: 0 }}
.block {{ border: 1px solid var(--border); border-radius: 12px; overflow: hidden; background: var(--surface) }}
.block-head {{ display: flex; justify-content: space-between; align-items: center; padding: 10px 12px; border-bottom: 1px solid var(--border); background: color-mix(in oklch, var(--bg) 72%, white) }}
.block-head strong {{ font-size: 11px; letter-spacing: 0.04em; text-transform: uppercase }}
.block-head span {{ color: var(--muted); font-family: var(--font-mono); font-size: 10px }}
.block-body {{ padding: 12px 14px; font-size: 13px; line-height: 1.6 }}
pre.block-body {{ margin: 0; white-space: pre-wrap; word-break: break-word; font-family: var(--font-mono); font-size: 12px; line-height: 1.65 }}
.rubric-row {{ display: grid; grid-template-columns: 1fr 54px; gap: 8px; padding: 8px 0; border-bottom: 1px solid var(--border); font-size: 12px }}
.rubric-row:last-child {{ border-bottom: 0 }}
.rubric-row .rid {{ font-weight: 650; margin-bottom: 2px }}
.rubric-row .reason {{ color: var(--muted) }}
.rubric-row b {{ text-align: right; font-variant-numeric: tabular-nums }}
.rubric-row b.yes {{ color: oklch(42% 0.13 150) }}
.rubric-row b.no {{ color: oklch(48% 0.16 28) }}
.error-block {{ border-color: color-mix(in oklch, oklch(60% 0.18 28) 28%, var(--border)) }}
.error-block .block-head {{ background: color-mix(in oklch, oklch(60% 0.18 28) 7%, white); border-bottom-color: color-mix(in oklch, oklch(60% 0.18 28) 20%, var(--border)) }}
.error-item {{ padding: 8px 0; border-bottom: 1px solid var(--border) }}
.error-item:last-child {{ border-bottom: 0 }}
.error-type {{ font-weight: 650; font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: oklch(48% 0.16 28); margin-bottom: 4px }}
.error-msg {{ font-family: var(--font-mono); font-size: 12px; color: var(--muted); line-height: 1.5 }}
.warning-block {{ border-color: color-mix(in oklch, oklch(70% 0.14 75) 34%, var(--border)) }}
.warning-block .block-head {{ background: color-mix(in oklch, oklch(70% 0.14 75) 10%, white); border-bottom-color: color-mix(in oklch, oklch(70% 0.14 75) 25%, var(--border)) }}
.pager {{ padding: 12px 16px; color: var(--muted); font-size: 12px; border-top: 1px solid var(--border) }}
@media (max-width: 768px) {{
  .summary {{ grid-template-columns: repeat(2, 1fr) }}
  .drawer {{ width: 100vw }}
  .page {{ width: calc(100vw - 24px) }}
}}
</style>
</head>
<body>
<main class="page">
<header class="topbar">
  <div class="brand"><div class="mark"></div><strong>Agent Benchmark</strong></div>
  <div class="meta-info">{agent_url} &middot; {started} ~ {finished}</div>
</header>
<section class="summary">
  <article class="summary-card"><div class="label">Total</div><div class="value">{total}</div></article>
  <article class="summary-card"><div class="label">Pass Rate</div><div class="value">{pass_rate}</div></article>
  <article class="summary-card"><div class="label">Failed</div><div class="value">{failed}</div></article>
  <article class="summary-card"><div class="label">Skipped</div><div class="value">{skipped}</div></article>
</section>
<section class="progress">
  <div class="progress-head">
    <span class="label">Execution Progress</span>
    <span class="count">{completed}/{total}</span>
  </div>
  <div class="progress-bar">
    <div class="seg pass" style="width:{pass_pct}%"></div>
    <div class="seg fail" style="width:{fail_pct}%"></div>
    <div class="seg skip" style="width:{skip_pct}%"></div>
  </div>
  <div class="progress-legend">
    <span><i class="pass"></i>Passed {passed}</span>
    <span><i class="fail"></i>Failed {failed}</span>
    <span><i class="skip"></i>Skipped {skipped}</span>
    <span><i class="fail"></i>Timeout {timeout}</span>
    <span><i class="fail"></i>Judge Error {judge_error}</span>
  </div>
</section>
{analysis_html}
<section class="panel">
  <div class="panel-header">
    <h2 class="panel-title">Task Records</h2>
    <span style="color:var(--muted);font-size:12px">click a row to inspect</span>
  </div>
  <div style="overflow:auto">
    <table class="task-table">
      <thead><tr><th>Task ID</th><th>Category</th><th>Status</th><th>Rubric</th><th>Tools</th><th>Latency</th></tr></thead>
      <tbody>{rows_html}</tbody>
    </table>
  </div>
  <div class="pager">Showing {total} tasks &middot; {suite} &middot; run {run_id}</div>
</section>
</main>
<div class="drawer-backdrop" id="backdrop"></div>
<aside class="drawer" id="drawer">
  <div class="drawer-top" style="position:relative">
    <h2 id="dTitle">\u2014</h2>
    <div class="drawer-chips" id="dChips"></div>
    <button class="close-btn" id="closeBtn">&times;</button>
  </div>
  <div class="drawer-body" id="dBody"></div>
</aside>
<script>
const TASKS = {tasks_json};
const rows = document.querySelectorAll("tbody tr[data-task]");
const backdrop = document.getElementById("backdrop");
const closeBtn = document.getElementById("closeBtn");
function esc(s) {{ var d = document.createElement("div"); d.textContent = s || ""; return d.innerHTML; }}
function openDrawer(i) {{
  var t = TASKS[i]; if (!t) return;
  document.getElementById("dTitle").textContent = t.id;
  document.getElementById("dChips").innerHTML =
    '<span class="chip">' + esc(t.category) + '</span>' +
    '<span class="chip">' + esc(t.status) + '</span>' +
    '<span class="chip">' + esc(String(t.tool_calls_count)) + ' tools</span>' +
    '<span class="chip">' + esc(String(t.wall_ms)) + 'ms</span>' +
    (t.screenshots_taken ? '<span class="chip">' + esc(String(t.screenshots_taken)) + ' screenshots</span>' : '');
  var body = "";
  body += '<div class="block"><div class="block-head"><strong>Prompt</strong><span>user input</span></div><pre class="block-body">' + esc(t.prompt) + '</pre></div>';
  body += '<div class="block"><div class="block-head"><strong>Task Description</strong><span>for judge</span></div><div class="block-body">' + esc(t.description) + '</div></div>';
  if (t.tool_calls_detail) {{
    body += '<div class="block"><div class="block-head"><strong>Tool Calls</strong><span>' + t.tool_calls_count + ' calls</span></div><pre class="block-body">' + esc(t.tool_calls_detail) + '</pre></div>';
  }}
  if (t.artifacts_detail) {{
    body += '<div class="block"><div class="block-head"><strong>Artifacts</strong><span>files</span></div><pre class="block-body">' + esc(t.artifacts_detail) + '</pre></div>';
  }}
  if (t.error_log_detail) {{
    body += '<div class="block error-block"><div class="block-head"><strong>Error Log</strong><span>task failure details</span></div><pre class="block-body">' + esc(t.error_log_detail) + '</pre></div>';
  }}
  body += '<div class="block"><div class="block-head"><strong>Agent Response</strong><span>final reply</span></div><pre class="block-body">' + esc(t.response) + '</pre></div>';
  if (t.errors && t.errors.length) {{
    var err = t.errors.map(function(e) {{
      return '<div class="error-item"><div class="error-type">' + esc(e[0]) + '</div><div class="error-msg">' + esc(e[1]) + '</div></div>';
    }}).join("");
    body += '<div class="block error-block"><div class="block-head"><strong>❌ Errors</strong><span>' + t.errors.length + ' error(s)</span></div><div class="block-body">' + err + '</div></div>';
  }}
  if (t.hard_assertions && t.hard_assertions.length) {{
    var ha = t.hard_assertions.map(function(r) {{
      return '<div class="rubric-row"><div><div class="rid">' + esc(r[0]) + '</div><div class="reason">' + esc(r[1]) + '</div></div><b class="no">' + r[2] + '</b></div>';
    }}).join("");
    body += '<div class="block warning-block"><div class="block-head"><strong>⚠️ Hard Assertion Failures</strong><span>' + t.hard_assertions.length + ' failure(s)</span></div><div class="block-body">' + ha + '</div></div>';
  }}
  if (t.rubric && t.rubric.length) {{
    var rb = t.rubric.map(function(r) {{
      var cls = r[2] === "yes" ? "yes" : r[2] === "no" ? "no" : "";
      return '<div class="rubric-row"><div><div class="rid">' + esc(r[0]) + '</div><div class="reason">' + esc(r[1]) + '</div></div><b class="' + cls + '">' + r[2] + '</b></div>';
    }}).join("");
    body += '<div class="block"><div class="block-head"><strong>Rubric</strong><span>' + t.rubric_pass + '/' + t.rubric_total + '</span></div><div class="block-body">' + rb + '</div></div>';
  }}
  if (t.trace_observations && t.trace_observations.length) {{
    var ob = t.trace_observations.map(function(r) {{
      var cls = r[2] === "yes" ? "yes" : "no";
      return '<div class="rubric-row"><div><div class="rid">' + esc(r[0]) + '</div><div class="reason">' + esc(r[1]) + '</div></div><b class="' + cls + '">' + r[2] + '</b></div>';
    }}).join("");
    body += '<div class="block"><div class="block-head"><strong>Trace observations</strong><span>informational</span></div><div class="block-body">' + ob + '</div></div>';
  }}
  document.getElementById("dBody").innerHTML = body;
  rows.forEach(function(r) {{ r.classList.toggle("active", Number(r.dataset.task) === i); }});
  document.body.classList.add("open");
}}
function closeDrawer() {{ document.body.classList.remove("open"); }}
rows.forEach(function(r) {{ r.addEventListener("click", function() {{ openDrawer(Number(r.dataset.task)); }}); }});
backdrop.addEventListener("click", closeDrawer);
closeBtn.addEventListener("click", closeDrawer);
window.addEventListener("keydown", function(e) {{ if (e.key === "Escape") closeDrawer(); }});
</script>
</body>
</html>"""
