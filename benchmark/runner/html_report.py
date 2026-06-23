"""Generate a self-contained HTML benchmark report with drawer UI."""
from __future__ import annotations
import base64
import html as html_mod
import json
from pathlib import Path
from typing import Any

from runner.agent_client import AgentClient


def _esc(s: str) -> str:
    return html_mod.escape(str(s)) if s else ""


def _image_data_uri(path: Path) -> str:
    try:
        data = path.read_bytes()
    except OSError:
        return ""
    if not data:
        return ""
    if data.startswith(b"\x89PNG\r\n\x1a\n"):
        mime = "image/png"
    elif data.startswith(b"\xff\xd8"):
        mime = "image/jpeg"
    else:
        mime = "image/jpeg"
    encoded = base64.b64encode(data).decode("ascii")
    return f"data:{mime};base64,{encoded}"


def _safe_json_loads(text: Any) -> Any:
    if not isinstance(text, str):
        return None
    try:
        return json.loads(text) if text else None
    except json.JSONDecodeError:
        return None


def _compact_tool_result(raw: str) -> str:
    content = _safe_json_loads(raw)
    if isinstance(content, dict):
        content = dict(content)
        if content.get("data"):
            size = len(str(content.get("data") or ""))
            content["data"] = f"<base64 image data, {size} chars>"
        return json.dumps(content, ensure_ascii=False, indent=2)
    return str(raw or "")


def _full_trace_payload(task_dir: Path, history: list[dict[str, Any]]) -> dict[str, Any]:
    events: list[dict[str, str]] = []
    tool_step = 0
    last_tool = ""
    for index, msg in enumerate(history, start=1):
        mtype = str(msg.get("type") or "")
        if mtype == "tool_call":
            tool_step += 1
            last_tool = str(msg.get("tool_name") or "")
            tool_input = _safe_json_loads(msg.get("tool_input", "")) or msg.get("tool_input", "")
            detail = json.dumps(tool_input, ensure_ascii=False, indent=2) if not isinstance(tool_input, str) else tool_input
            events.append({
                "kind": "tool-call",
                "title": f"Tool call #{tool_step}: {last_tool}",
                "detail": detail,
                "image": "",
            })
        elif mtype == "tool_result":
            raw = str(msg.get("content") or "")
            events.append({
                "kind": "tool-result",
                "title": f"Tool result: {msg.get('tool_name') or last_tool}",
                "detail": _compact_tool_result(raw),
                "image": "",
            })
        elif mtype == "assistant":
            events.append({
                "kind": "assistant",
                "title": "Assistant",
                "detail": str(msg.get("content") or ""),
                "image": "",
            })
        elif mtype == "user":
            events.append({
                "kind": "user",
                "title": "User",
                "detail": str(msg.get("content") or ""),
                "image": "",
            })
        else:
            events.append({
                "kind": mtype or "message",
                "title": f"Message #{index}: {mtype or 'unknown'}",
                "detail": json.dumps(msg, ensure_ascii=False, indent=2),
                "image": "",
            })
    return {
        "pre_screenshot": _image_data_uri(task_dir / "pre.jpg"),
        "post_screenshot": _image_data_uri(task_dir / "post.jpg"),
        "events": events,
    }


def generate_report_html(run_dir: Path) -> str:
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
        task_dir = run_dir / "tasks" / tid
        trace_path = task_dir / "trace.json"
        history_path = task_dir / "history.json"
        trace_data: dict[str, Any] = {}
        history: list[dict[str, Any]] = []
        prompt = ""
        if trace_path.exists():
            try:
                trace_data = json.loads(trace_path.read_text("utf-8"))
            except Exception:
                pass
        if history_path.exists():
            try:
                loaded_history = json.loads(history_path.read_text("utf-8"))
                history = [msg for msg in loaded_history if isinstance(msg, dict)] if isinstance(loaded_history, list) else []
                for msg in history:
                    if msg.get("type") == "user":
                        prompt = msg.get("content", "")
                        break
            except Exception:
                history = []
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

        hard_assertion_failures = [
            {
                "id": str(item.get("id") or ""),
                "label": str(item.get("label") or item.get("id") or ""),
                "requirement": str(item.get("requirement") or ""),
                "actual": str(item.get("actual") or ""),
            }
            for item in r.get("hard_assertion_failures") or []
            if isinstance(item, dict)
        ]

        # Extract errors
        errors = []
        metrics = r.get("metrics", {})
        if "error" in metrics:
            errors.append(["Error", metrics["error"]])
        if "agent_error" in metrics:
            errors.append(["Agent Error", metrics["agent_error"]])
        if "judge_error" in metrics:
            errors.append(["Judge Error", metrics["judge_error"]])

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
            "hard_assertion_failures": hard_assertion_failures,
            "errors": errors,
            "full_trace": _full_trace_payload(task_dir, history),
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
.assertion-row {{ display: grid; grid-template-columns: 1fr 54px; gap: 8px; padding: 10px 0; border-bottom: 1px solid var(--border); font-size: 12px }}
.assertion-row:last-child {{ border-bottom: 0 }}
.assertion-row .rid {{ font-weight: 650; margin-bottom: 6px }}
.assertion-detail {{ display: grid; grid-template-columns: 92px 1fr; gap: 4px 8px; color: var(--muted) }}
.assertion-detail strong {{ color: var(--text); font-weight: 650 }}
.assertion-row b {{ text-align: right; color: oklch(48% 0.16 28); font-variant-numeric: tabular-nums }}
.error-block {{ border-color: color-mix(in oklch, oklch(60% 0.18 28) 28%, var(--border)) }}
.error-block .block-head {{ background: color-mix(in oklch, oklch(60% 0.18 28) 7%, white); border-bottom-color: color-mix(in oklch, oklch(60% 0.18 28) 20%, var(--border)) }}
.error-item {{ padding: 8px 0; border-bottom: 1px solid var(--border) }}
.error-item:last-child {{ border-bottom: 0 }}
.error-type {{ font-weight: 650; font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; color: oklch(48% 0.16 28); margin-bottom: 4px }}
.error-msg {{ font-family: var(--font-mono); font-size: 12px; color: var(--muted); line-height: 1.5 }}
.warning-block {{ border-color: color-mix(in oklch, oklch(70% 0.14 75) 34%, var(--border)) }}
.warning-block .block-head {{ background: color-mix(in oklch, oklch(70% 0.14 75) 10%, white); border-bottom-color: color-mix(in oklch, oklch(70% 0.14 75) 25%, var(--border)) }}
.trace-actions {{ display: flex; justify-content: flex-start; margin-bottom: 12px }}
.trace-toggle {{ height: 34px; border: 1px solid var(--border); border-radius: 8px; background: var(--fg); color: white; padding: 0 12px; font: inherit; font-size: 12px; font-weight: 650; cursor: pointer }}
.trace-toggle:hover {{ background: color-mix(in oklch, var(--fg) 88%, var(--accent)) }}
.full-trace {{ display: grid; gap: 12px; margin-bottom: 12px }}
.full-trace[hidden] {{ display: none }}
.trace-shots {{ display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px }}
.trace-shot {{ border: 1px solid var(--border); border-radius: 12px; overflow: hidden; background: color-mix(in oklch, var(--bg) 72%, white) }}
.trace-shot figcaption {{ padding: 8px 10px; border-bottom: 1px solid var(--border); color: var(--muted); font-size: 11px; font-weight: 700; letter-spacing: 0.04em; text-transform: uppercase }}
.trace-shot img {{ display: block; width: 100%; height: auto; max-height: 520px; object-fit: contain; background: #050505 }}
.trace-event {{ border: 1px solid var(--border); border-radius: 12px; overflow: hidden; background: var(--surface) }}
.trace-event-head {{ display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 9px 11px; border-bottom: 1px solid var(--border); background: color-mix(in oklch, var(--bg) 72%, white) }}
.trace-event-head strong {{ font-size: 12px }}
.trace-kind {{ color: var(--muted); font-family: var(--font-mono); font-size: 10px; text-transform: uppercase }}
.trace-event pre {{ margin: 0; padding: 10px 11px; white-space: pre-wrap; word-break: break-word; font-family: var(--font-mono); font-size: 11px; line-height: 1.55; color: var(--muted) }}
.trace-event img {{ display: block; width: 100%; max-height: 620px; object-fit: contain; background: #050505; border-top: 1px solid var(--border) }}
.pager {{ padding: 12px 16px; color: var(--muted); font-size: 12px; border-top: 1px solid var(--border) }}
@media (max-width: 768px) {{
  .summary {{ grid-template-columns: repeat(2, 1fr) }}
  .trace-shots {{ grid-template-columns: 1fr }}
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
function token(s) {{ return String(s || "").toLowerCase().replace(/[^a-z0-9_-]/g, "-"); }}
function hasFullTrace(t) {{
  var ft = t.full_trace || {{}};
  return !!(ft.pre_screenshot || ft.post_screenshot || (ft.events && ft.events.length));
}}
function renderTraceShot(label, src) {{
  if (!src) return "";
  return '<figure class="trace-shot"><figcaption>' + esc(label) + '</figcaption><img src="' + esc(src) + '" alt="' + esc(label) + ' screenshot"></figure>';
}}
function renderTraceEvent(event, index) {{
  var kind = token(event.kind || "message");
  var detail = event.detail ? '<pre>' + esc(event.detail) + '</pre>' : '';
  return '<section class="trace-event ' + kind + '">' +
    '<div class="trace-event-head"><strong>' + esc(event.title || ("Trace event " + index)) + '</strong><span class="trace-kind">' + esc(event.kind || "message") + '</span></div>' +
    detail + '</section>';
}}
function renderFullTrace(t) {{
  var ft = t.full_trace || {{}};
  var shots = renderTraceShot("Pre screenshot", ft.pre_screenshot) + renderTraceShot("Post screenshot", ft.post_screenshot);
  var body = shots ? '<div class="trace-shots">' + shots + '</div>' : '';
  var events = ft.events || [];
  if (events.length) body += events.map(function(event, index) {{ return renderTraceEvent(event, index + 1); }}).join("");
  if (!body) body = '<div class="block"><div class="block-body">No trace artifacts were captured for this task.</div></div>';
  return body;
}}
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
  if (hasFullTrace(t)) {{
    body += '<div class="trace-actions"><button id="traceToggle" class="trace-toggle" type="button">View full trace</button></div>';
    body += '<div id="fullTrace" class="full-trace" hidden>' + renderFullTrace(t) + '</div>';
  }}
  if (t.tool_calls_detail) {{
    body += '<div class="block"><div class="block-head"><strong>Tool Calls</strong><span>' + t.tool_calls_count + ' calls</span></div><pre class="block-body">' + esc(t.tool_calls_detail) + '</pre></div>';
  }}
  if (t.artifacts_detail) {{
    body += '<div class="block"><div class="block-head"><strong>Artifacts</strong><span>files</span></div><pre class="block-body">' + esc(t.artifacts_detail) + '</pre></div>';
  }}
  body += '<div class="block"><div class="block-head"><strong>Agent Response</strong><span>final reply</span></div><pre class="block-body">' + esc(t.response) + '</pre></div>';
  if (t.errors && t.errors.length) {{
    var err = t.errors.map(function(e) {{
      return '<div class="error-item"><div class="error-type">' + esc(e[0]) + '</div><div class="error-msg">' + esc(e[1]) + '</div></div>';
    }}).join("");
    body += '<div class="block error-block"><div class="block-head"><strong>❌ Errors</strong><span>' + t.errors.length + ' error(s)</span></div><div class="block-body">' + err + '</div></div>';
  }}
  if (t.hard_assertion_failures && t.hard_assertion_failures.length) {{
    var ha = t.hard_assertion_failures.map(function(r) {{
      return '<div class="assertion-row"><div><div class="rid">' + esc(r.label || r.id) + '</div>' +
        '<div class="assertion-detail"><strong>Requirement</strong><span>' + esc(r.requirement) + '</span>' +
        '<strong>Actual</strong><span>' + esc(r.actual) + '</span></div></div><b>no</b></div>';
    }}).join("");
    body += '<div class="block warning-block"><div class="block-head"><strong>⚠️ Hard Assertion Failures</strong><span>' + t.hard_assertion_failures.length + ' failure(s)</span></div><div class="block-body">' + ha + '</div></div>';
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
  var traceToggle = document.getElementById("traceToggle");
  if (traceToggle) {{
    traceToggle.addEventListener("click", function() {{
      var fullTrace = document.getElementById("fullTrace");
      if (!fullTrace) return;
      var willOpen = fullTrace.hasAttribute("hidden");
      if (willOpen) fullTrace.removeAttribute("hidden"); else fullTrace.setAttribute("hidden", "");
      traceToggle.textContent = willOpen ? "Hide full trace" : "View full trace";
    }});
  }}
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
