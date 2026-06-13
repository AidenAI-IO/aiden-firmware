from __future__ import annotations
import argparse
import json
import os
import re
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from runner.agent_client import AgentClient
from runner.html_report import generate_report_html, upload_report
from runner.judge import JudgeConfig
from runner.report import git_sha, write_jsonl, write_manifest, write_summary, now_iso
from runner.recovery import recover_agent_after_timeout, wait_for_agent_ready
from runner.runtask import run_one_task, skipped_task_result
from runner.suite import load_suite

REPO_ROOT = Path(__file__).resolve().parents[2]


def wait_for_agent_clock(
    client: AgentClient,
    min_year: int | None = None,
    timeout_sec: int = 180,
    poll_sec: int = 2,
) -> bool:
    if min_year is None:
        min_year = datetime.now(timezone.utc).year
    deadline = time.monotonic() + max(0, timeout_sec)

    while True:
        try:
            result = client.invoke_tool("shell", {"command": "date +%Y", "timeout": 5})
            if not result.is_error:
                match = re.search(r"\b(19\d{2}|20\d{2})\b", result.output or "")
                if match and int(match.group(1)) >= min_year:
                    return True
        except Exception:
            pass

        now = time.monotonic()
        if now >= deadline:
            return False
        time.sleep(min(max(0, poll_sec), max(0, deadline - now)))


def cli(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="benchmark.runner")
    sub = parser.add_subparsers(dest="cmd", required=True)
    p_run = sub.add_parser("run")
    p_run.add_argument("--suite", required=True)
    p_run.add_argument("--agent-url", default=os.environ.get("AIDEN_AGENT_URL", "http://localhost:8080"))
    p_run.add_argument("--judge-model", default="claude-sonnet-4-6")
    p_run.add_argument("--no-judge", action="store_true")
    p_run.add_argument("--repeats", type=int, default=None)
    p_run.add_argument("--out", default=str(REPO_ROOT / "benchmark" / "runs"))
    p_run.add_argument("--state-file", default=os.environ.get("BENCHMARK_STATE_FILE"))
    p_run.add_argument("--skip-clock-wait", action="store_true")
    p_run.add_argument("--clock-timeout-sec", type=int, default=180)
    p_run.add_argument("--agent-ready-timeout-sec", type=int, default=120)
    p_run.add_argument("--agent-recovery-timeout-sec", type=int, default=90,
                       help="Extra wait after timeout/skipped before next task")
    p_run.add_argument("--inter-task-cooldown-sec", type=float, default=2.0)
    p_unit = sub.add_parser("unit")
    p_unit.add_argument("--suite")
    p_unit.add_argument("--suite-dir")
    p_unit.add_argument("--agent-url", default=os.environ.get("AIDEN_AGENT_URL", "http://localhost:8080"))
    p_unit.add_argument("--out", default=str(REPO_ROOT / "benchmark" / "runs"))
    p_run.add_argument("--verbose", "-v", action="store_true",
                       help="Show detailed rubric results for each task")
    p_rejudge = sub.add_parser("rejudge")
    p_rejudge.add_argument("--run-dir", required=True)
    p_rejudge.add_argument("--judge-model", default="claude-sonnet-4-6")
    p_compare = sub.add_parser("compare")
    p_compare.add_argument("--runs", nargs=2, required=True)
    args = parser.parse_args(argv)
    if args.cmd == "run":
        return _cmd_run(args)
    if args.cmd == "unit":
        from runner.unit import cmd_unit
        return cmd_unit(args)
    if args.cmd == "rejudge":
        from runner.rejudge import rejudge_run
        return rejudge_run(Path(args.run_dir), args.judge_model)
    if args.cmd == "compare":
        from runner.compare import compare_runs
        return compare_runs(Path(args.runs[0]), Path(args.runs[1]))
    return 2


def _log_task_result(task_id: str, attempt: int, result, verbose: bool = False,
                     progress: str = "") -> None:
    """Print task execution result with optional detailed rubric information."""
    prefix = f"[{progress}] " if progress else ""
    status_line = (
        f"{prefix}{result.status.upper():10s} {task_id} attempt={attempt} "
        f"rubric={result.rubric_pass_count}/{result.rubric_total} "
        f"wall={result.metrics.get('wall_ms')}ms"
    )

    # Add metrics if available
    metrics_info = []
    if "tool_calls" in result.metrics:
        metrics_info.append(f"tools={result.metrics['tool_calls']}")
    if "screenshots_taken" in result.metrics:
        metrics_info.append(f"screenshots={result.metrics['screenshots_taken']}")
    if metrics_info:
        status_line += f" ({', '.join(metrics_info)})"

    print(status_line, flush=True)

    # Show detailed rubric results in verbose mode
    if verbose and result.rubric:
        print(f"  📋 Rubric Details:", flush=True)
        for i, v in enumerate(result.rubric, 1):
            verdict_symbol = "✅" if v.verdict == "yes" else "❌"
            print(f"    {verdict_symbol} [{i}/{len(result.rubric)}] {v.id}: {v.verdict.upper()}", flush=True)
            if v.reason:
                # Indent reason text for readability
                reason_lines = v.reason.strip().split('\n')
                for line in reason_lines[:3]:  # Limit to first 3 lines
                    print(f"        → {line}", flush=True)
                if len(reason_lines) > 3:
                    print(f"        → ... ({len(reason_lines) - 3} more lines)", flush=True)

    # Show hard assertion failures (verbose mode)
    if verbose and result.hard_assertions:
        ha = result.hard_assertions
        failures = []
        if ha.timeout is False:
            failures.append("timeout")
        if ha.response_exists is False:
            failures.append("no response")
        if ha.min_tool_calls is False:
            failures.append("min_tool_calls")
        if ha.max_tool_calls is False:
            failures.append("max_tool_calls")
        if ha.required_tools is False:
            failures.append("required_tools")
        if ha.forbidden_tools is False:
            failures.append("forbidden_tools")
        if ha.expected_answer is False:
            failures.append("expected_answer")
        if ha.expected_recalled_memory is False:
            failures.append("expected_recalled_memory")

        if failures:
            print(f"  ⚠️  Hard assertion failures: {', '.join(failures)}", flush=True)

    # Show error messages (verbose mode)
    if verbose and "error" in result.metrics:
        print(f"  ❌ Error: {result.metrics['error']}", flush=True)
    if verbose and "agent_error" in result.metrics:
        print(f"  ❌ Agent Error: {result.metrics['agent_error']}", flush=True)
    if verbose and "judge_error" in result.metrics:
        print(f"  ❌ Judge Error: {result.metrics['judge_error']}", flush=True)


def _write_state(path: str | None, payload: dict) -> None:
    if not path:
        return
    try:
        p = Path(path)
        p.parent.mkdir(parents=True, exist_ok=True)
        tmp = p.with_suffix(p.suffix + ".tmp")
        tmp.write_text(json.dumps(payload, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")
        tmp.replace(p)
    except Exception as e:
        print(f"warning: failed to write benchmark state: {e}", file=sys.stderr, flush=True)


def _cmd_run(args: argparse.Namespace) -> int:
    suite = load_suite(Path(args.suite))
    run_id = datetime.now(timezone.utc).strftime("%Y-%m-%d_%H%M%S")
    run_dir = Path(args.out) / run_id
    client = AgentClient(base_url=args.agent_url)
    if not client.health():
        print(f"agent at {args.agent_url} is not reachable", file=sys.stderr)
        return 2
    if not args.skip_clock_wait and not wait_for_agent_clock(client, timeout_sec=args.clock_timeout_sec):
        print("agent board clock did not sync before benchmark start", file=sys.stderr)
        client.close()
        return 2
    judge_cfg = None if args.no_judge else JudgeConfig(model=args.judge_model)
    judge_cache = run_dir / "_judge_cache"
    sha, dirty = git_sha(REPO_ROOT)
    started = now_iso()
    results = []
    # Validate --repeats
    if args.repeats is not None and args.repeats <= 0:
        print(f"Error: --repeats must be positive, got {args.repeats}", file=sys.stderr)
        return 2
    # Compute total number of executions (accounting for repeats) for progress display
    total_runs = 0
    for task in suite.tasks:
        n = args.repeats if args.repeats is not None else task.repeats
        total_runs += n if n > 0 else 1
    completed = 0
    base_state = {
        "status": "running",
        "suite": str(suite.source_path),
        "run_id": run_id,
        "total": total_runs,
        "completed": 0,
        "started_at": started,
    }
    _write_state(args.state_file, base_state)
    try:
        for task in suite.tasks:
            n = args.repeats if args.repeats is not None else task.repeats
            if n <= 0:
                n = 1
            for attempt in range(1, n + 1):
                current_index = completed + 1
                progress = f"{current_index}/{total_runs}"
                _write_state(args.state_file, {
                    **base_state,
                    "completed": completed,
                    "current": current_index,
                    "current_task": task.id,
                    "current_attempt": attempt,
                })
                print(f"[{progress}] RUNNING    {task.id} attempt={attempt}", flush=True)
                if not wait_for_agent_ready(
                    client, timeout_sec=args.agent_ready_timeout_sec
                ):
                    print(
                        f"[{progress}] SKIPPED    {task.id} attempt={attempt} "
                        f"rubric=0/{len(task.rubric)} wall=Nonems "
                        f"(agent not ready)",
                        flush=True,
                    )
                    results.append(
                        skipped_task_result(
                            suite, task, attempt,
                            run_dir / "tasks" / task.id
                            / (f"attempt_{attempt}" if n > 1 else ""),
                            run_id,
                            f"agent not ready within {args.agent_ready_timeout_sec}s",
                        )
                    )
                    completed += 1
                    _write_state(args.state_file, {
                        **base_state,
                        "completed": completed,
                        "current": current_index,
                        "current_task": task.id,
                        "current_attempt": attempt,
                        "last_result": "skipped",
                    })
                    continue

                art_dir = run_dir / "tasks" / task.id / (f"attempt_{attempt}" if n > 1 else "")
                try:
                    r = run_one_task(client, suite, task, attempt, art_dir,
                                     judge_cfg, judge_cache, run_id)
                except Exception as e:
                    print(f"[{progress}] ERROR      {task.id} attempt={attempt} — {e}", flush=True)
                    r = skipped_task_result(suite, task, attempt, art_dir, run_id, str(e))
                _log_task_result(task.id, attempt, r, verbose=args.verbose, progress=progress)
                results.append(r)
                completed += 1
                _write_state(args.state_file, {
                    **base_state,
                    "completed": completed,
                    "current": current_index,
                    "current_task": task.id,
                    "current_attempt": attempt,
                    "last_result": r.status,
                })

                if r.status in {"timeout", "skipped", "judge_error", "failed"}:
                    if not recover_agent_after_timeout(
                        client, timeout_sec=args.agent_recovery_timeout_sec
                    ):
                        wait_for_agent_ready(
                            client, timeout_sec=args.agent_recovery_timeout_sec
                        )
                if args.inter_task_cooldown_sec > 0:
                    time.sleep(args.inter_task_cooldown_sec)
    finally:
        client.close()
    manifest = {
        "run_id": run_id, "git_sha": sha, "git_dirty": dirty,
        "suite_path": str(suite.source_path), "suite_sha256": suite.sha256,
        "agent_url": args.agent_url,
        "judge_config": {"provider": "openrouter", "model": args.judge_model} if judge_cfg else None,
        "judge_prompt_version": "v1",
        "started_at": started, "finished_at": now_iso(),
        "totals": {"tasks": len(results),
                   "passed": sum(1 for r in results if r.status == "passed"),
                   "failed": sum(1 for r in results if r.status == "failed"),
                   "skipped": sum(1 for r in results if r.status == "skipped"),
                   "judge_error": sum(1 for r in results if r.status == "judge_error"),
                   "timeout": sum(1 for r in results if r.status == "timeout")},
    }
    write_manifest(run_dir / "manifest.json", manifest)
    write_jsonl(run_dir / "results.jsonl", results)
    write_summary(run_dir / "summary.md", suite.name, manifest, results)
    html = generate_report_html(run_dir)
    (run_dir / "report.html").write_text(html, encoding="utf-8")

    # Print final summary
    print("\n" + "="*60, flush=True)
    print(f"📊 Benchmark Summary - {suite.name}", flush=True)
    print("="*60, flush=True)
    print(f"Total Tasks:   {manifest['totals']['tasks']}", flush=True)
    print(f"✅ Passed:     {manifest['totals']['passed']}", flush=True)
    print(f"❌ Failed:     {manifest['totals']['failed']}", flush=True)
    print(f"⏭️  Skipped:    {manifest['totals']['skipped']}", flush=True)
    if manifest['totals']['timeout'] > 0:
        print(f"⏱️  Timeout:    {manifest['totals']['timeout']}", flush=True)
    if manifest['totals']['judge_error'] > 0:
        print(f"⚠️  Judge Error: {manifest['totals']['judge_error']}", flush=True)
    print(f"\n📁 Results saved to: {run_dir}", flush=True)
    print("="*60 + "\n", flush=True)

    upload_client = AgentClient(base_url=args.agent_url)
    if upload_report(upload_client, html, run_dir):
        print(f"Report uploaded → http://{args.agent_url.split('//')[1].split(':')[0]}:80/benchmark")
    else:
        print("Warning: failed to upload report to board")
    upload_client.close()
    return 0 if manifest["totals"]["passed"] == manifest["totals"]["tasks"] else 1


if __name__ == "__main__":
    sys.exit(cli())
