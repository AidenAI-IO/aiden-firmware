"""CLI entry point for SkillOpt.

Usage:
    python -m runner.skillopt \\
        --skill device-operator \\
        --suite phone_control_v1 \\
        --budget 10 \\
        --edit-budget 4 \\
        --min-delta 0.03 \\
        --output /tmp/optimized.md

The suite is split into train (70%) and selection (30%) by default.
"""
from __future__ import annotations
import argparse
import dataclasses as dc
import difflib
import html
import json
import os
import re
import sys
from pathlib import Path
from urllib.parse import quote

from runner.judge import JudgeConfig
from runner.suite import load_suite
from runner.skillopt.backends import AidenDeviceBackend, SkillOptRolloutBackend
from runner.skillopt.mobilegym_backend import MobileGymBackend
from runner.skillopt.optimizer_client import OptimizerConfig
from runner.skillopt.orchestrator import optimize_skill, OptimizationConfig
from runner.skillopt.types import OptimizationResult


REPO_ROOT = Path(__file__).resolve().parents[3]
SAFE_SEGMENT = re.compile(r"^[A-Za-z0-9_.\-]+$")
DIFF_HUNK_RE = re.compile(r"^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@")


def _valid_safe_relative_label(label: str) -> bool:
    if not label or label.startswith("/"):
        return False
    return all(
        part not in {"", ".", ".."} and SAFE_SEGMENT.match(part)
        for part in label.split("/")
    )


def _validate_skill_name(skill: str) -> str | None:
    if not skill or skill in {".", ".."} or "/" in skill or "\\" in skill or not SAFE_SEGMENT.match(skill):
        return f"invalid skill name: {skill!r}"
    return None


def _validate_skillopt_suite_label(skill: str, suite_label: str) -> str | None:
    if not _valid_safe_relative_label(suite_label):
        return f"invalid suite label: {suite_label!r}"
    prefix = f"skillopt/{skill}/"
    if not suite_label.startswith(prefix):
        return f"suite {suite_label!r} must be under {prefix.rstrip('/')}"
    return None


def _validate_suite_label(skill: str, suite_label: str) -> str | None:
    if not _valid_safe_relative_label(suite_label):
        return f"invalid suite label: {suite_label!r}"
    if suite_label.startswith("skillopt/"):
        prefix = f"skillopt/{skill}/"
        if not suite_label.startswith(prefix):
            return f"suite {suite_label!r} must be under {prefix.rstrip('/')}"
    return None


def _validate_run_id(run_id: str) -> str | None:
    if not run_id or run_id in {".", ".."} or "/" in run_id or "\\" in run_id or not SAFE_SEGMENT.match(run_id):
        return f"invalid run_id: {run_id!r}"
    return None


def _resolve_skill_path(skill_name: str) -> Path:
    roots: list[Path] = []
    if env_root := os.environ.get("AIDEN_SKILLS_DIR"):
        roots.append(Path(env_root))
    roots.extend([
        REPO_ROOT / "src" / "agent" / "config" / "skills",
        REPO_ROOT / "skills",
    ])

    for root in roots:
        path = root / skill_name / "SKILL.md"
        if path.exists():
            return path
    return roots[0] / skill_name / "SKILL.md"


def _resolve_suite_path(suite_name: str) -> Path:
    rel = suite_name if suite_name.endswith(".json") else f"{suite_name}.json"
    return REPO_ROOT / "benchmark" / "suites" / rel


def _build_rollout_backend(args: argparse.Namespace, skill_path: Path) -> SkillOptRolloutBackend:
    if args.backend == "device":
        return AidenDeviceBackend(agent_url=args.agent_url)
    return MobileGymBackend(
        benchmark_root=REPO_ROOT / "benchmark",
        shared_skills_dir=skill_path.parent.parent,
        parallel=args.mobilegym_parallel,
    )


def cli(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="python -m runner.skillopt",
        description="SkillOpt: optimize an Aiden skill through rollout reflection.",
    )
    parser.add_argument("--skill", required=True, help="Skill name (e.g. device-operator)")
    parser.add_argument(
        "--backend",
        choices=["device", "mobilegym"],
        default="device",
        help="Rollout backend: device uses the current Aiden daemon; mobilegym uses MobileGym execution.",
    )
    parser.add_argument("--suite", help="Suite name to split 70/30 (e.g. phone_control_v1)")
    parser.add_argument("--train-suite", help="Explicit train suite name (e.g. skillopt/device-operator/device_operator_train)")
    parser.add_argument(
        "--selection-suite",
        "--validation-suite",
        dest="selection_suite",
        help="Explicit selection/validation suite name",
    )
    parser.add_argument("--budget", type=int, default=10, help="Max optimization steps")
    parser.add_argument("--edit-budget", type=int, default=4, help="Edits per step")
    parser.add_argument("--min-delta", type=float, default=0.03, help="Validation gate threshold")
    parser.add_argument("--mobilegym-parallel", type=int, default=1, help="MobileGym isolated worker count")
    parser.add_argument(
        "--optimizer-model",
        default="anthropic/claude-opus-4-7",
        help="OpenRouter model ID for optimizer",
    )
    parser.add_argument(
        "--judge-model",
        default="anthropic/claude-sonnet-4-6",
        help="OpenRouter model ID for judge (rubric eval)",
    )
    parser.add_argument(
        "--no-judge",
        action="store_true",
        help="Skip judge (use hard assertions only)",
    )
    parser.add_argument(
        "--agent-url",
        default="http://localhost:8080",
        help="Agent base URL",
    )
    parser.add_argument(
        "--output",
        help="Write best skill to this path (default: overwrite original)",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print diff but don't write files",
    )
    parser.add_argument(
        "--artifact-root",
        default=str(REPO_ROOT / "benchmark" / "runs" / "skillopt"),
        help="Root dir for run artifacts",
    )
    parser.add_argument(
        "--run-id",
        help="Run id for artifact directory (default: UTC timestamp)",
    )

    args = parser.parse_args(argv)
    if args.suite and (args.train_suite or args.selection_suite):
        parser.error("--suite cannot be combined with --train-suite/--selection-suite")
    if bool(args.train_suite) != bool(args.selection_suite):
        parser.error("--train-suite and --selection-suite must be provided together")
    if not args.suite and not args.train_suite:
        parser.error("either --suite or --train-suite/--selection-suite is required")
    if err := _validate_skill_name(args.skill):
        print(f"Error: {err}", file=sys.stderr)
        return 2
    if args.mobilegym_parallel <= 0:
        print("Error: mobilegym_parallel must be positive", file=sys.stderr)
        return 2
    if args.run_id:
        if err := _validate_run_id(args.run_id):
            print(f"Error: {err}", file=sys.stderr)
            return 2
    if args.train_suite:
        for label in (args.train_suite, args.selection_suite):
            if err := _validate_skillopt_suite_label(args.skill, label):
                print(f"Error: {err}", file=sys.stderr)
                return 2
    elif args.suite:
        if err := _validate_suite_label(args.skill, args.suite):
            print(f"Error: {err}", file=sys.stderr)
            return 2

    # Resolve skill path
    skill_path = _resolve_skill_path(args.skill)
    if not skill_path.exists():
        print(f"Error: skill not found: {skill_path}", file=sys.stderr)
        return 2

    train_suite = None
    selection_suite = None
    if args.train_suite:
        train_suite_path = _resolve_suite_path(args.train_suite)
        selection_suite_path = _resolve_suite_path(args.selection_suite)
        if not train_suite_path.exists():
            print(f"Error: train suite not found: {train_suite_path}", file=sys.stderr)
            return 2
        if not selection_suite_path.exists():
            print(f"Error: selection suite not found: {selection_suite_path}", file=sys.stderr)
            return 2
        train_suite = load_suite(train_suite_path)
        selection_suite = load_suite(selection_suite_path)
        suite = train_suite
        train_tasks = train_suite.tasks
        selection_tasks = selection_suite.tasks
        if not train_tasks:
            print("Error: train suite has no tasks", file=sys.stderr)
            return 2
        if not selection_tasks:
            print("Error: selection suite has no tasks", file=sys.stderr)
            return 2
    else:
        # Load suite and split train/selection (70/30)
        suite_path = _resolve_suite_path(args.suite)
        if not suite_path.exists():
            print(f"Error: suite not found: {suite_path}", file=sys.stderr)
            return 2
        suite = load_suite(suite_path)

        n_train = int(len(suite.tasks) * 0.7)
        if n_train == 0:
            n_train = max(1, len(suite.tasks) - 1)
        train_tasks = suite.tasks[:n_train]
        selection_tasks = suite.tasks[n_train:]
        if not selection_tasks:
            print("Error: suite too small to split train/selection", file=sys.stderr)
            return 2

    print(f"Skill: {args.skill}")
    if args.train_suite:
        print(
            f"Suites: train={args.train_suite} ({len(train_tasks)} tasks), "
            f"selection={args.selection_suite} ({len(selection_tasks)} tasks)"
        )
    else:
        print(f"Suite: {args.suite} ({len(train_tasks)} train, {len(selection_tasks)} selection)")
    print(f"Budget: {args.budget} steps, {args.edit_budget} edits/step, min_delta={args.min_delta}")
    print(f"Optimizer: {args.optimizer_model}")
    print(f"Judge: {args.judge_model if not args.no_judge else 'disabled'}")
    print(f"Backend: {args.backend}")
    print(f"Agent: {args.agent_url}")
    print()

    optimizer_cfg = OptimizerConfig(model=args.optimizer_model)
    judge_cfg = None if args.no_judge else JudgeConfig(model=args.judge_model)

    cfg_kwargs = dict(
        skill_name=args.skill,
        skill_path=skill_path,
        suite=suite,
        train_tasks=train_tasks,
        selection_tasks=selection_tasks,
        train_suite=train_suite,
        selection_suite=selection_suite,
        budget=args.budget,
        edit_budget=args.edit_budget,
        min_delta=args.min_delta,
        optimizer_cfg=optimizer_cfg,
        judge_cfg=judge_cfg,
        agent_url=args.agent_url,
        artifact_root=Path(args.artifact_root),
        rollout_backend=_build_rollout_backend(args, skill_path),
    )
    if args.run_id:
        cfg_kwargs["run_id"] = args.run_id
    cfg = OptimizationConfig(**cfg_kwargs)

    original_skill = skill_path.read_text(encoding="utf-8")
    result = optimize_skill(cfg)

    print()
    print("=" * 60)
    print(f"Optimization complete: {result.skill_name}")
    print(f"  Initial score: {result.initial_score:.3f}")
    print(f"  Best score:    {result.best_score:.3f}  (delta={result.best_score - result.initial_score:+.3f})")
    print(f"  Steps:         {len(result.steps)} ({result.accepted_count} accepted, {result.rejected_count} rejected)")
    print("=" * 60)

    # Write output
    output_path = Path(args.output) if args.output else skill_path
    diff_text = _skill_diff(original_skill, result.best_skill, str(skill_path), "best_skill")
    if args.dry_run:
        print(diff_text)
    else:
        output_path.parent.mkdir(parents=True, exist_ok=True)
        output_path.write_text(result.best_skill, encoding="utf-8")
        print(f"Best skill written to: {output_path}")

    if not args.dry_run:
        _write_web_artifacts(
            cfg=cfg,
            result=result,
            original_skill=original_skill,
            diff_text=diff_text,
            optimizer_model=args.optimizer_model,
            judge_model=None if args.no_judge else args.judge_model,
            train_suite_label=args.train_suite or args.suite or "",
            selection_suite_label=args.selection_suite or args.suite or "",
            backend=args.backend,
        )

    return 0 if result.best_score > result.initial_score else 1


def _skill_diff(original: str, best: str, fromfile: str, tofile: str) -> str:
    return "".join(difflib.unified_diff(
        original.splitlines(keepends=True),
        best.splitlines(keepends=True),
        fromfile=fromfile,
        tofile=tofile,
    ))


def _passed_count(score: float, total: int) -> int:
    if total <= 0:
        return 0
    passed = int(round(score * total))
    return max(0, min(total, passed))


def _write_web_artifacts(
    cfg: OptimizationConfig,
    result: OptimizationResult,
    original_skill: str,
    diff_text: str,
    optimizer_model: str,
    judge_model: str | None,
    train_suite_label: str,
    selection_suite_label: str,
    backend: str,
) -> None:
    run_dir = cfg.artifact_root / cfg.run_id
    run_dir.mkdir(parents=True, exist_ok=True)
    score_summary = _score_summary(result)
    linked_reports = _linked_reports(cfg.run_id, result, backend)
    best_verification = score_summary.get("best_verification") or {}
    validation_total = int(best_verification.get("n") or len(cfg.selection_tasks))
    passed = int(best_verification.get("n_passed") or _passed_count(result.best_score, validation_total))
    totals = {"tasks": validation_total, "passed": passed, "failed": max(0, validation_total - passed)}

    manifest = {
        "run_id": cfg.run_id,
        "mode": "skillopt",
        "backend": backend,
        "skill": cfg.skill_name,
        "suite_path": f"skillopt:{cfg.skill_name}",
        "train_suite": train_suite_label,
        "validation_suite": selection_suite_label,
        "agent_url": cfg.agent_url,
        "model": os.environ.get("AIDEN_MODEL", ""),
        "optimizer_config": {"provider": cfg.optimizer_cfg.provider, "model": optimizer_model},
        "judge_config": {"provider": "openrouter", "model": judge_model} if judge_model else None,
        "scores": {"initial": result.initial_score, "best": result.best_score},
        "score_summary": score_summary,
        "linked_reports": linked_reports,
        "artifacts": {"best_skill": "best_skill.md", "diff": "diff.patch", "result": "result.json"},
        "totals": totals,
    }
    (run_dir / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
    (run_dir / "result.json").write_text(json.dumps(dc.asdict(result), ensure_ascii=False, indent=2), encoding="utf-8")
    (run_dir / "diff.patch").write_text(diff_text, encoding="utf-8")
    if not (run_dir / "best_skill.md").exists():
        (run_dir / "best_skill.md").write_text(result.best_skill, encoding="utf-8")
    (run_dir / "report.html").write_text(_render_report_html(manifest, result, original_skill, diff_text), encoding="utf-8")


def _score_to_dict(score) -> dict:
    if score is None:
        return {}
    return {
        "hard": score.hard,
        "soft": score.soft,
        "n": score.n,
        "n_passed": score.n_passed,
    }


def _score_summary(result: OptimizationResult) -> dict:
    baseline = next((p.score for p in result.phase_summaries if p.phase == "baseline_selection"), None)
    train_scores = [p.score for p in result.phase_summaries if p.kind == "train"]
    verification_scores = [p.score for p in result.phase_summaries if p.kind == "verification"]
    best_verification = max(verification_scores, key=lambda score: score.hard, default=None)
    return {
        "baseline_verification": _score_to_dict(baseline),
        "latest_train": _score_to_dict(train_scores[-1] if train_scores else None),
        "best_verification": _score_to_dict(best_verification),
    }


def _linked_reports(run_id: str, result: OptimizationResult, backend: str) -> dict[str, str]:
    if backend != "mobilegym":
        return {}
    return {
        summary.phase: f"/benchmark/report/{run_id}-{summary.phase}"
        for summary in result.phase_summaries
    }


def _render_report_html(manifest: dict, result: OptimizationResult, original_skill: str, diff_text: str) -> str:
    step_rows = _render_step_rows(result)
    skill = html.escape(manifest["skill"])
    train_suite = html.escape(manifest.get("train_suite", ""))
    validation_suite = html.escape(manifest.get("validation_suite", ""))
    diff = _render_diff_html(diff_text)
    original_len = len(original_skill.splitlines())
    best_len = len(result.best_skill.splitlines())
    score_rows = _render_score_rows(result, manifest.get("linked_reports", {}))
    edit_rows = _render_edit_rows(result)
    artifact_rows = _render_artifact_rows(manifest)
    stop_reason = html.escape(result.stop_reason or "completed budget or accepted best candidate")
    return f"""<!doctype html>
<html><head><meta charset="utf-8"><title>SkillOpt Report</title>
<style>
:root{{--bg:#f6f7fb;--surface:#fbfcff;--fg:#273142;--muted:#475569;--border:#e6eaf2;--accent:#2563eb;--font-mono:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,monospace}}
body{{font-family:system-ui,-apple-system,sans-serif;background:#f6f7fb;color:#273142;margin:0;padding:28px;max-width:1100px}}
.card{{background:#fbfcff;border:1px solid #e6eaf2;border-radius:14px;padding:18px;margin:0 0 16px}}
h1{{font-size:24px;margin:0 0 8px}} p{{color:#475569}} table{{width:100%;border-collapse:collapse;font-size:13px}} td,th{{border-bottom:1px solid #edf0f6;padding:8px;text-align:left;vertical-align:top}} pre{{background:#111827;color:#d1fae5;border-radius:12px;padding:14px;overflow:auto;font-size:12px;line-height:1.5}} code{{background:#eef2ff;border-radius:6px;padding:2px 6px}} a{{color:#2563eb}}
.artifact-link{{font-weight:650;text-decoration:none}}
.artifact-link:hover{{text-decoration:underline}}
.detail-btn{{border:0;background:transparent;color:var(--accent);cursor:pointer;padding:0;font:inherit;font-weight:650}}
.detail-btn:hover{{text-decoration:underline}}
.drawer-backdrop{{position:fixed;inset:0;z-index:40;background:color-mix(in oklch,var(--fg) 18%,transparent);opacity:0;pointer-events:none;transition:opacity 180ms ease}}
.drawer{{position:fixed;top:0;right:0;bottom:0;z-index:50;width:min(720px,100vw);background:var(--surface);border-left:1px solid var(--border);box-shadow:-24px 0 80px color-mix(in oklch,var(--fg) 10%,transparent);transform:translateX(100%);transition:transform 220ms ease;display:flex;flex-direction:column}}
body.open .drawer-backdrop{{opacity:1;pointer-events:auto}}
body.open .drawer{{transform:translateX(0)}}
.drawer-top{{padding:18px;border-bottom:1px solid var(--border);background:linear-gradient(to bottom,color-mix(in oklch,var(--bg) 72%,white),var(--surface));flex-shrink:0}}
.drawer-top h2{{font-size:18px;letter-spacing:-0.02em;margin:0 42px 8px 0}}
.drawer-chips{{display:flex;flex-wrap:wrap;gap:6px}}
.chip{{display:inline-flex;align-items:center;height:24px;padding:0 8px;border-radius:999px;border:1px solid var(--border);background:var(--surface);color:var(--muted);font-size:11px}}
.close-btn{{position:absolute;top:16px;right:16px;width:30px;height:30px;border:1px solid var(--border);border-radius:8px;background:var(--surface);color:var(--fg);font-size:16px;cursor:pointer;display:flex;align-items:center;justify-content:center}}
.drawer-body{{flex:1;min-height:0;overflow-y:scroll;-webkit-overflow-scrolling:touch;padding:16px}}
.drawer-body>.block{{margin-bottom:12px}}
.drawer-body>.block:last-child{{margin-bottom:0}}
.block{{border:1px solid var(--border);border-radius:12px;overflow:hidden;background:var(--surface)}}
.block-head{{display:flex;justify-content:space-between;align-items:center;padding:10px 12px;border-bottom:1px solid var(--border);background:color-mix(in oklch,var(--bg) 72%,white)}}
.block-head strong{{font-size:11px;letter-spacing:0.04em;text-transform:uppercase}}
.block-head span{{color:var(--muted);font-family:var(--font-mono);font-size:10px}}
.block-body{{padding:12px 14px;font-size:13px;line-height:1.6}}
pre.block-body{{margin:0;white-space:pre-wrap;word-break:break-word;font-family:var(--font-mono);font-size:12px;line-height:1.65;background:var(--surface);color:var(--fg);border-radius:0}}
.edit-summary{{display:flex;flex-wrap:wrap;gap:6px;align-items:center}}
.edit-pill{{display:inline-flex;align-items:center;height:24px;border-radius:999px;border:1px solid var(--border);background:color-mix(in oklch,var(--bg) 65%,white);padding:0 8px;color:var(--muted);font-size:11px}}
.diff-summary{{display:flex;gap:8px;flex-wrap:wrap;margin:0 0 12px;color:var(--muted);font-size:12px}}
.diff-pill{{display:inline-flex;align-items:center;gap:5px;border:1px solid var(--border);border-radius:999px;background:color-mix(in oklch,var(--bg) 60%,white);padding:4px 8px}}
.diff-pill.add{{color:oklch(42% 0.13 154);border-color:color-mix(in oklch,oklch(64% 0.15 154) 34%,var(--border));background:color-mix(in oklch,oklch(92% 0.06 154) 72%,white)}}
.diff-pill.del{{color:oklch(45% 0.16 28);border-color:color-mix(in oklch,oklch(65% 0.17 28) 30%,var(--border));background:color-mix(in oklch,oklch(93% 0.05 28) 72%,white)}}
.diff-viewer{{border:1px solid var(--border);border-radius:12px;overflow:auto;background:oklch(98% 0.006 255);font-family:var(--font-mono);font-size:12px;line-height:1.55}}
.diff-line{{display:grid;grid-template-columns:4.5ch 4.5ch minmax(42rem,1fr);min-height:22px}}
.diff-line span{{white-space:pre}}
.diff-old,.diff-new{{padding:2px 7px;text-align:right;color:color-mix(in oklch,var(--muted) 68%,transparent);border-right:1px solid color-mix(in oklch,var(--border) 70%,transparent);user-select:none}}
.diff-code{{padding:2px 10px}}
.diff-file .diff-code{{font-weight:700;color:oklch(37% 0.08 255);background:color-mix(in oklch,var(--bg) 82%,white)}}
.diff-hunk .diff-code{{color:oklch(42% 0.13 270);background:color-mix(in oklch,oklch(92% 0.05 270) 78%,white)}}
.diff-add{{background:color-mix(in oklch,oklch(94% 0.06 154) 64%,white)}}
.diff-add .diff-code{{color:oklch(35% 0.11 154)}}
.diff-del{{background:color-mix(in oklch,oklch(94% 0.06 28) 66%,white)}}
.diff-del .diff-code{{color:oklch(40% 0.14 28)}}
.diff-context .diff-code{{color:var(--fg)}}
.diff-note .diff-code{{color:var(--muted);font-style:italic}}
.diff-empty{{border:1px dashed var(--border);border-radius:12px;padding:14px;color:var(--muted);background:color-mix(in oklch,var(--bg) 66%,white)}}
.error-block{{border-color:color-mix(in oklch,oklch(60% 0.18 28) 28%,var(--border))}}
.error-block .block-head{{background:color-mix(in oklch,oklch(60% 0.18 28) 7%,white);border-bottom-color:color-mix(in oklch,oklch(60% 0.18 28) 20%,var(--border))}}
@media (max-width:768px){{.drawer{{width:100vw}}body{{padding:18px}}}}
</style></head><body>
<h1>SkillOpt Report</h1>
<div class="card"><p><strong>Skill:</strong> {skill}</p><p><strong>Train:</strong> {train_suite}</p><p><strong>Verification:</strong> {validation_suite}</p><p><strong>Initial score:</strong> {result.initial_score:.3f} <strong>Best score:</strong> {result.best_score:.3f}</p><p><strong>Stop reason:</strong> {stop_reason}</p><p><strong>Skill size:</strong> {original_len} lines to {best_len} lines</p></div>
<div class="card"><h2>Scores</h2><p><strong>SkillOpt gate</strong> is the only score used to accept or reject edits. Open a phase report when task-level details are needed.</p><table><thead><tr><th>Phase</th><th>Kind</th><th>Suite</th><th>SkillOpt gate</th><th>Gate hard rate</th><th>Report</th></tr></thead><tbody>{score_rows}</tbody></table></div>
<div class="card"><h2>Steps</h2><table><thead><tr><th>Step</th><th>Decision</th><th>Current</th><th>Candidate</th><th>Reason</th></tr></thead><tbody>{step_rows}</tbody></table></div>
<div class="card"><h2>Edits</h2><table><thead><tr><th>Step</th><th>Status</th><th>Summary</th><th>Details</th></tr></thead><tbody>{edit_rows}</tbody></table></div>
<div class="card"><h2>Artifacts</h2><table><thead><tr><th>Name</th><th>Path</th></tr></thead><tbody>{artifact_rows}</tbody></table></div>
<div class="card"><h2>Diff</h2>{diff}</div>
{_artifact_drawer_markup(manifest, result)}
</body></html>"""


def _render_diff_html(diff_text: str) -> str:
    if not diff_text.strip():
        return '<div class="diff-empty">No skill text changes were produced.</div>'

    old_line: int | None = None
    new_line: int | None = None
    added = 0
    removed = 0
    rows = []

    for line in diff_text.splitlines():
        old_cell = ""
        new_cell = ""
        line_class = "diff-context"

        if line.startswith("@@"):
            match = DIFF_HUNK_RE.match(line)
            if match:
                old_line = int(match.group(1))
                new_line = int(match.group(2))
            line_class = "diff-hunk"
        elif line.startswith("--- ") or line.startswith("+++ "):
            line_class = "diff-file"
        elif line.startswith("+") and not line.startswith("+++"):
            line_class = "diff-add"
            new_cell = str(new_line) if new_line is not None else ""
            if new_line is not None:
                new_line += 1
            added += 1
        elif line.startswith("-") and not line.startswith("---"):
            line_class = "diff-del"
            old_cell = str(old_line) if old_line is not None else ""
            if old_line is not None:
                old_line += 1
            removed += 1
        elif line.startswith("\\"):
            line_class = "diff-note"
        else:
            old_cell = str(old_line) if old_line is not None else ""
            new_cell = str(new_line) if new_line is not None else ""
            if old_line is not None:
                old_line += 1
            if new_line is not None:
                new_line += 1

        rows.append(_render_diff_line(line_class, old_cell, new_cell, line))

    summary = (
        '<div class="diff-summary">'
        f'<span class="diff-pill add">+{added} added</span>'
        f'<span class="diff-pill del">-{removed} removed</span>'
        '<span class="diff-pill">line numbers show original/new skill</span>'
        '</div>'
    )
    return summary + '<div class="diff-viewer">' + "".join(rows) + "</div>"


def _render_diff_line(line_class: str, old_cell: str, new_cell: str, code: str) -> str:
    return (
        f'<div class="diff-line {line_class}">'
        f'<span class="diff-old">{html.escape(old_cell)}</span>'
        f'<span class="diff-new">{html.escape(new_cell)}</span>'
        f'<span class="diff-code">{html.escape(code)}</span>'
        "</div>"
    )


def _render_step_rows(result: OptimizationResult) -> str:
    rows = []
    for step in result.steps:
        rows.append(
            "<tr>"
            f"<td>{step.step}</td>"
            f"<td>{'accepted' if step.accepted else 'rejected'}</td>"
            f"<td>{step.current_score:.3f}</td>"
            f"<td>{step.candidate_score:.3f}</td>"
            f"<td>{html.escape(step.reason)}</td>"
            "</tr>"
        )
    if rows:
        return "".join(rows)
    stop = result.stop_reason or "No optimization steps produced edits."
    step_label = _step_label_from_stop_reason(stop)
    return (
        "<tr>"
        f"<td>{html.escape(step_label)}</td>"
        "<td>stopped before candidate verification</td>"
        "<td>-</td><td>-</td>"
        f"<td>{html.escape(stop)}</td>"
        "</tr>"
    )


def _step_label_from_stop_reason(reason: str) -> str:
    match = re.search(r"step\s+(\d+)", reason, flags=re.IGNORECASE)
    return f"Step {match.group(1)}" if match else "-"


def _render_score_rows(result: OptimizationResult, linked_reports: dict[str, str]) -> str:
    rows = []
    for summary in result.phase_summaries:
        report = linked_reports.get(summary.phase, "")
        report_cell = f'<a href="{html.escape(report)}">open report</a>' if report else ""
        rows.append(
            "<tr>"
            f"<td>{html.escape(summary.phase)}</td>"
            f"<td>{html.escape(summary.kind)}</td>"
            f"<td>{html.escape(summary.suite_name)}</td>"
            f"<td>{summary.score.n_passed}/{summary.score.n}</td>"
            f"<td>{summary.score.hard:.3f}</td>"
            f"<td>{report_cell}</td>"
            "</tr>"
        )
    return "".join(rows) or "<tr><td colspan=\"6\">No rollout phases recorded.</td></tr>"


def _json_for_script(value) -> str:
    return json.dumps(value, ensure_ascii=False).replace("</", "<\\/")


def _render_edit_rows(result: OptimizationResult) -> str:
    rows = []
    for step in result.steps:
        edits = step.edits_applied if step.accepted else step.edits_rejected
        count = len(edits)
        count_label = f"{count} edit" if count == 1 else f"{count} edits"
        ops = []
        for edit in edits:
            if edit.op not in ops:
                ops.append(edit.op)
        op_html = "".join(f'<span class="edit-pill">{html.escape(op)}</span>' for op in ops[:4])
        summary = f'<div class="edit-summary"><span>{html.escape(count_label)}</span>{op_html}</div>'
        rows.append(
            "<tr>"
            f"<td>{step.step}</td>"
            f"<td>{'applied' if step.accepted else 'rejected'}</td>"
            f"<td>{summary}</td>"
            f"<td><button class=\"detail-btn\" type=\"button\" data-edit-step=\"{step.step}\">View edits</button></td>"
            "</tr>"
        )
    if rows:
        return "".join(rows)
    reason = result.stop_reason or "No optimization steps produced edits."
    return f"<tr><td colspan=\"4\">{html.escape(reason)}</td></tr>"


def _format_edit(edit) -> str:
    parts = [f"op={edit.op}"]
    if edit.target:
        parts.append(f"target={edit.target}")
    if edit.support_count is not None:
        parts.append(f"support={edit.support_count}")
    if edit.source_type:
        parts.append(f"source={edit.source_type}")
    if edit.content:
        parts.append(f"content:\n{edit.content}")
    return "\n".join(parts)


def _edit_detail_drawer_data(result: OptimizationResult | None) -> list[dict]:
    if result is None:
        return []
    out = []
    for step in result.steps:
        edits = step.edits_applied if step.accepted else step.edits_rejected
        out.append({
            "step": step.step,
            "status": "applied" if step.accepted else "rejected",
            "decision_reason": step.reason,
            "patch_reasoning": step.patch_reasoning,
            "edits": [
                {
                    "op": edit.op,
                    "target": edit.target,
                    "content": edit.content,
                    "support_count": edit.support_count,
                    "source_type": edit.source_type,
                    "merge_level": edit.merge_level,
                    "formatted": _format_edit(edit),
                }
                for edit in edits
            ],
        })
    return out


def _render_artifact_rows(manifest: dict) -> str:
    rows = []
    run_id = str(manifest.get("run_id") or "")
    for name, path in (manifest.get("artifacts") or {}).items():
        href = _report_artifact_href(run_id, str(path))
        name_html = html.escape(str(name))
        path_html = html.escape(str(path))
        href_html = html.escape(href)
        cell = (
            f'<a class="artifact-link" href="{href_html}" data-artifact-name="{name_html}" '
            f'data-artifact-url="{href_html}">{path_html}</a>'
            if href
            else f"<code>{path_html}</code>"
        )
        rows.append(
            "<tr>"
            f"<td>{name_html}</td>"
            f"<td>{cell}</td>"
            "</tr>"
        )
    for phase, report in (manifest.get("linked_reports") or {}).items():
        rows.append(
            "<tr>"
            f"<td>{html.escape(str(phase))}</td>"
            f"<td><a href=\"{html.escape(str(report))}\">{html.escape(str(report))}</a></td>"
            "</tr>"
        )
    return "".join(rows) or "<tr><td colspan=\"2\">No artifacts recorded.</td></tr>"


def _report_artifact_href(run_id: str, path: str) -> str:
    parts = path.split("/")
    if _validate_run_id(run_id) or not _valid_safe_relative_label(path):
        return ""
    return "/benchmark/report/" + quote(run_id, safe="") + "/" + "/".join(quote(part, safe="") for part in parts)


def _artifact_drawer_markup(manifest: dict | None = None, result: OptimizationResult | None = None) -> str:
    edit_detail_data = _json_for_script(_edit_detail_drawer_data(result))
    markup = """
<div class="drawer-backdrop" id="artifactBackdrop"></div>
<aside class="drawer" id="artifactDrawer" aria-label="Artifact preview">
  <div class="drawer-top" style="position:relative">
    <h2 id="artifactTitle">Artifact</h2>
    <div class="drawer-chips" id="artifactChips"></div>
    <button class="close-btn" id="artifactCloseBtn" type="button" aria-label="Close artifact preview">&times;</button>
  </div>
  <div class="drawer-body" id="artifactBody"></div>
</aside>
<script>
const artifactBackdrop = document.getElementById("artifactBackdrop");
const artifactCloseBtn = document.getElementById("artifactCloseBtn");
const artifactBody = document.getElementById("artifactBody");
const artifactTitle = document.getElementById("artifactTitle");
const artifactChips = document.getElementById("artifactChips");
const editDetailData = __EDIT_DETAIL_DATA__;
function esc(s) { var d = document.createElement("div"); d.textContent = s || ""; return d.innerHTML; }
function closeArtifactDrawer() { document.body.classList.remove("open"); }
function attachArtifactLinks(root) {
  root.querySelectorAll("[data-artifact-url]").forEach(function(link) {
    link.addEventListener("click", openArtifactDrawer);
  });
}
function setArtifactDrawer(name, url, content, isError) {
  artifactTitle.textContent = name;
  artifactChips.innerHTML = '<span class="chip">artifact</span><span class="chip">' + esc(url.split('/').pop()) + '</span>';
  var blockClass = isError ? 'block error-block' : 'block';
  var label = isError ? 'load failed' : 'content';
  artifactBody.innerHTML = '<div class="' + blockClass + '"><div class="block-head"><strong>' + esc(name) + '</strong><span>' + label + '</span></div><pre class="block-body">' + esc(content) + '</pre></div>' +
    '<div class="block"><div class="block-head"><strong>Raw file</strong><span>fallback</span></div><div class="block-body"><a href="' + esc(url) + '">Open raw file</a></div></div>';
}
async function openArtifactDrawer(event) {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
  event.preventDefault();
  var link = event.currentTarget;
  var url = link.dataset.artifactUrl || link.href;
  var name = link.dataset.artifactName || link.textContent || 'artifact';
  artifactTitle.textContent = name;
  artifactChips.innerHTML = '<span class="chip">artifact</span><span class="chip">loading</span>';
  artifactBody.innerHTML = '<div class="block"><div class="block-head"><strong>Loading</strong><span>fetch</span></div><div class="block-body">Loading artifact content...</div></div>';
  document.body.classList.add("open");
  try {
    var response = await fetch(url);
    if (!response.ok) throw new Error('HTTP ' + response.status);
    setArtifactDrawer(name, url, await response.text(), false);
  } catch (err) {
    setArtifactDrawer(name, url, 'Could not load artifact: ' + (err && err.message ? err.message : String(err)), true);
  }
}
function renderEditBlock(edit, index) {
  var meta = [];
  if (edit.target) meta.push('target');
  if (edit.support_count !== null && edit.support_count !== undefined) meta.push('support=' + edit.support_count);
  if (edit.source_type) meta.push(edit.source_type);
  var label = meta.join(' | ') || 'edit ' + String(index + 1);
  return '<div class="block"><div class="block-head"><strong>' + esc(edit.op || 'edit') + '</strong><span>' + esc(label) + '</span></div><pre class="block-body">' + esc(edit.formatted || '') + '</pre></div>';
}
function openEditDrawer(event) {
  event.preventDefault();
  var step = Number(event.currentTarget.dataset.editStep || 0);
  var detail = editDetailData.find(function(item) { return Number(item.step) === step; });
  if (!detail) return;
  artifactTitle.textContent = 'Step ' + String(step) + ' edits';
  var editCount = (detail.edits || []).length;
  var editLabel = editCount === 1 ? '1 edit' : String(editCount) + ' edits';
  artifactChips.innerHTML = '<span class="chip">' + esc(detail.status || 'step') + '</span><span class="chip">' + esc(editLabel) + '</span>';
  var decision = detail.decision_reason ? '<div><strong>Decision:</strong> ' + esc(detail.decision_reason) + '</div>' : '';
  var reasoning = detail.patch_reasoning ? '<div class="block"><div class="block-head"><strong>Reasoning</strong><span>optimizer</span></div><pre class="block-body">' + esc(detail.patch_reasoning) + '</pre></div>' : '';
  var edits = (detail.edits || []).map(renderEditBlock).join('') || '<div class="block"><div class="block-body">No edits recorded.</div></div>';
  artifactBody.innerHTML = '<div class="block"><div class="block-head"><strong>Step summary</strong><span>decision</span></div><div class="block-body">' + decision + '</div></div>' + reasoning + edits;
  document.body.classList.add("open");
}
attachArtifactLinks(document);
document.querySelectorAll("[data-edit-step]").forEach(function(button) {
  button.addEventListener("click", openEditDrawer);
});
artifactBackdrop.addEventListener("click", closeArtifactDrawer);
artifactCloseBtn.addEventListener("click", closeArtifactDrawer);
window.addEventListener("keydown", function(event) { if (event.key === "Escape") closeArtifactDrawer(); });
</script>
"""
    return markup.replace("__EDIT_DETAIL_DATA__", edit_detail_data)


if __name__ == "__main__":
    sys.exit(cli())
