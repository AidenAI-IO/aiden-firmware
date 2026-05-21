from __future__ import annotations
import argparse
import os
import sys
from datetime import datetime, timezone
from pathlib import Path
from runner.agent_client import AgentClient
from runner.judge import JudgeConfig
from runner.report import git_sha, write_jsonl, write_manifest, write_summary, now_iso
from runner.runtask import run_one_task
from runner.suite import load_suite

REPO_ROOT = Path(__file__).resolve().parents[2]


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
    p_rejudge = sub.add_parser("rejudge")
    p_rejudge.add_argument("--run-dir", required=True)
    p_rejudge.add_argument("--judge-model", default="claude-sonnet-4-6")
    p_compare = sub.add_parser("compare")
    p_compare.add_argument("--runs", nargs=2, required=True)
    args = parser.parse_args(argv)
    if args.cmd == "run":
        return _cmd_run(args)
    if args.cmd == "rejudge":
        from runner.rejudge import rejudge_run
        return rejudge_run(Path(args.run_dir), args.judge_model)
    if args.cmd == "compare":
        from runner.compare import compare_runs
        return compare_runs(Path(args.runs[0]), Path(args.runs[1]))
    return 2


def _cmd_run(args: argparse.Namespace) -> int:
    suite = load_suite(Path(args.suite))
    run_id = datetime.now(timezone.utc).strftime("%Y-%m-%d_%H%M%S")
    run_dir = Path(args.out) / run_id
    client = AgentClient(base_url=args.agent_url)
    if not client.health():
        print(f"agent at {args.agent_url} is not reachable", file=sys.stderr)
        return 2
    judge_cfg = None if args.no_judge else JudgeConfig(model=args.judge_model)
    judge_cache = run_dir / "_judge_cache"
    sha, dirty = git_sha(REPO_ROOT)
    started = now_iso()
    results = []
    try:
        for task in suite.tasks:
            n = args.repeats or task.repeats
            for attempt in range(1, n + 1):
                art_dir = run_dir / "tasks" / task.id / (f"attempt_{attempt}" if n > 1 else "")
                r = run_one_task(client, suite, task, attempt, art_dir,
                                 judge_cfg, judge_cache, run_id)
                print(f"{r.status.upper():10s} {task.id} attempt={attempt} "
                      f"rubric={r.rubric_pass_count}/{r.rubric_total} "
                      f"wall={r.metrics.get('wall_ms')}ms", flush=True)
                results.append(r)
    finally:
        client.close()
    manifest = {
        "run_id": run_id, "git_sha": sha, "git_dirty": dirty,
        "suite_path": str(suite.source_path), "suite_sha256": suite.sha256,
        "agent_url": args.agent_url,
        "judge_config": {"provider": "anthropic", "model": args.judge_model} if judge_cfg else None,
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
    return 0 if manifest["totals"]["passed"] == manifest["totals"]["tasks"] else 1


if __name__ == "__main__":
    sys.exit(cli())
