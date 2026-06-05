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
import sys
from pathlib import Path

from runner.judge import JudgeConfig
from runner.suite import load_suite
from runner.skillopt.optimizer_client import OptimizerConfig
from runner.skillopt.orchestrator import optimize_skill, OptimizationConfig


REPO_ROOT = Path(__file__).resolve().parents[3]


def cli(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="python -m runner.skillopt",
        description="SkillOpt: optimize an Aiden skill through rollout reflection.",
    )
    parser.add_argument("--skill", required=True, help="Skill name (e.g. device-operator)")
    parser.add_argument("--suite", required=True, help="Suite name (e.g. phone_control_v1)")
    parser.add_argument("--budget", type=int, default=10, help="Max optimization steps")
    parser.add_argument("--edit-budget", type=int, default=4, help="Edits per step")
    parser.add_argument("--min-delta", type=float, default=0.03, help="Validation gate threshold")
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

    args = parser.parse_args(argv)

    # Resolve skill path
    skill_dir = REPO_ROOT / "src" / "agent" / "config" / "skills" / args.skill
    skill_path = skill_dir / "SKILL.md"
    if not skill_path.exists():
        print(f"Error: skill not found: {skill_path}", file=sys.stderr)
        return 2

    # Load suite
    suite_path = REPO_ROOT / "benchmark" / "suites" / f"{args.suite}.json"
    if not suite_path.exists():
        print(f"Error: suite not found: {suite_path}", file=sys.stderr)
        return 2
    suite = load_suite(suite_path)

    # Split train/selection (70/30)
    n_train = int(len(suite.tasks) * 0.7)
    if n_train == 0:
        n_train = max(1, len(suite.tasks) - 1)
    train_tasks = suite.tasks[:n_train]
    selection_tasks = suite.tasks[n_train:]
    if not selection_tasks:
        print("Error: suite too small to split train/selection", file=sys.stderr)
        return 2

    print(f"Skill: {args.skill}")
    print(f"Suite: {args.suite} ({len(train_tasks)} train, {len(selection_tasks)} selection)")
    print(f"Budget: {args.budget} steps, {args.edit_budget} edits/step, min_delta={args.min_delta}")
    print(f"Optimizer: {args.optimizer_model}")
    print(f"Judge: {args.judge_model if not args.no_judge else 'disabled'}")
    print(f"Agent: {args.agent_url}")
    print()

    optimizer_cfg = OptimizerConfig(model=args.optimizer_model)
    judge_cfg = None if args.no_judge else JudgeConfig(model=args.judge_model)

    cfg = OptimizationConfig(
        skill_name=args.skill,
        skill_path=skill_path,
        suite=suite,
        train_tasks=train_tasks,
        selection_tasks=selection_tasks,
        budget=args.budget,
        edit_budget=args.edit_budget,
        min_delta=args.min_delta,
        optimizer_cfg=optimizer_cfg,
        judge_cfg=judge_cfg,
        agent_url=args.agent_url,
        artifact_root=Path(args.artifact_root),
    )

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
    if args.dry_run:
        import difflib
        original = skill_path.read_text()
        diff = difflib.unified_diff(
            original.splitlines(keepends=True),
            result.best_skill.splitlines(keepends=True),
            fromfile=str(skill_path),
            tofile="best_skill",
        )
        print("\n".join(diff))
    else:
        output_path.write_text(result.best_skill, encoding="utf-8")
        print(f"Best skill written to: {output_path}")

    return 0 if result.best_score > result.initial_score else 1


if __name__ == "__main__":
    sys.exit(cli())
