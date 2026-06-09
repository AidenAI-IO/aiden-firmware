"""Orchestrator: main optimization loop.

Ties together rollout, reflect, aggregate, patch, score, gate in the
classic SkillOpt rhythm:

  loop:
    train_rollout(current_skill) → rollouts
    reflect(rollouts) → raw_patches
    aggregate(raw_patches) → patch
    apply(patch) → candidate_skill
    selection_rollout(candidate_skill) → candidate_score
    gate(candidate_score, current_score) → accept / reject
    if no improvement for N steps → early stop
"""
from __future__ import annotations
import dataclasses as dc
import json
from datetime import datetime, timezone
from pathlib import Path

from runner.agent_client import AgentClient
from runner.judge import JudgeConfig
from runner.models import TaskResult
from runner.runtask import run_one_task
from runner.suite import Suite, TaskSpec
from runner.skillopt.aggregate import aggregate, format_rejected_context
from runner.skillopt.optimizer_client import OptimizerConfig
from runner.skillopt.patch import apply_patch, apply_patch_with_report
from runner.skillopt.reflect import run_reflect
from runner.skillopt.score import (
    aggregate_score,
    task_result_to_rollout,
    validation_gate,
    DEFAULT_MIN_DELTA,
)
from runner.skillopt.skill_override import with_skill_override
from runner.skillopt.types import Edit, OptimizationResult, RolloutResult, StepDecision


@dc.dataclass
class OptimizationConfig:
    skill_name: str
    skill_path: Path
    suite: Suite
    train_tasks: list[TaskSpec]
    selection_tasks: list[TaskSpec]
    train_suite: Suite | None = None
    selection_suite: Suite | None = None
    budget: int = 10                     # max optimization steps
    edit_budget: int = 4                 # edits per step
    min_delta: float = DEFAULT_MIN_DELTA
    optimizer_cfg: OptimizerConfig = dc.field(default_factory=OptimizerConfig)
    judge_cfg: JudgeConfig | None = None
    agent_url: str = "http://localhost:8080"
    run_id: str = dc.field(default_factory=lambda: datetime.now(timezone.utc).strftime("%Y%m%d_%H%M%S"))
    artifact_root: Path = Path("runs/skillopt")
    early_stop_patience: int = 3         # stop if no improvement for N steps


def _rollout_tasks(
    client: AgentClient,
    suite: Suite,
    tasks: list[TaskSpec],
    skill_name: str,
    run_id: str,
    phase: str,
    artifact_root: Path,
    judge_cfg: JudgeConfig | None,
) -> tuple[list[TaskResult], list[RolloutResult]]:
    """Run a batch of tasks and return (TaskResult[], RolloutResult[])."""
    task_results: list[TaskResult] = []
    judge_cache = artifact_root / "_judge_cache"
    for task in tasks:
        art_dir = artifact_root / phase / task.id
        result = run_one_task(
            client=client,
            suite=suite,
            task=task,
            attempt=1,
            artifact_dir=art_dir,
            judge_cfg=judge_cfg,
            judge_cache_dir=judge_cache,
            run_id=run_id,
        )
        task_results.append(result)
    rollouts = [task_result_to_rollout(tr) for tr in task_results]
    return task_results, rollouts


def optimize_skill(cfg: OptimizationConfig) -> OptimizationResult:
    """Run the full optimization loop. Returns best skill + stats."""
    skill_path = cfg.skill_path
    if not skill_path.exists():
        raise FileNotFoundError(f"skill not found: {skill_path}")

    original = skill_path.read_text(encoding="utf-8")
    current = original
    client = AgentClient(base_url=cfg.agent_url)
    run_root = cfg.artifact_root / cfg.run_id
    run_root.mkdir(parents=True, exist_ok=True)
    train_suite = cfg.train_suite or cfg.suite
    selection_suite = cfg.selection_suite or cfg.suite
    try:
        # Baseline eval on selection split
        print(f"[baseline] Evaluating original skill on {len(cfg.selection_tasks)} selection tasks...")
        _, sel_rollouts = _rollout_tasks(
            client, selection_suite, cfg.selection_tasks, cfg.skill_name,
            cfg.run_id, "baseline_selection", run_root, cfg.judge_cfg,
        )
        current_score = aggregate_score(sel_rollouts)
        print(f"[baseline] hard={current_score.hard:.3f} soft={current_score.soft:.3f} ({current_score.n_passed}/{current_score.n})")

        best = current
        best_score = current_score
        steps: list[StepDecision] = []
        rejected: list[Edit] = []
        no_improvement_count = 0

        for step_idx in range(1, cfg.budget + 1):
            print(f"\n[step {step_idx}] Train rollout on {len(cfg.train_tasks)} tasks...")
            _, train_rollouts = _rollout_tasks(
                client, train_suite, cfg.train_tasks, cfg.skill_name,
                cfg.run_id, f"step_{step_idx:02d}_train", run_root, cfg.judge_cfg,
            )
            train_score = aggregate_score(train_rollouts)
            print(f"[step {step_idx}] train: hard={train_score.hard:.3f} soft={train_score.soft:.3f}")

            # Reflect
            print(f"[step {step_idx}] Reflect (calling optimizer LLM)...")
            rejected_ctx = format_rejected_context(rejected)
            raw_patches = run_reflect(
                cfg.optimizer_cfg,
                current,
                train_rollouts,
                edit_budget=cfg.edit_budget,
                rejected_context=rejected_ctx,
            )
            if not raw_patches:
                print(f"[step {step_idx}] no patches produced by reflect; stopping.")
                break

            # Aggregate
            patch = aggregate(raw_patches, edit_budget=cfg.edit_budget)
            if not patch.edits:
                print(f"[step {step_idx}] aggregate produced 0 edits after dedup; stopping.")
                break
            print(f"[step {step_idx}] {len(patch.edits)} edits: {[e.op for e in patch.edits]}")

            # Apply
            candidate, reports = apply_patch_with_report(current, patch)
            step_artifact = run_root / f"step_{step_idx:02d}"
            step_artifact.mkdir(parents=True, exist_ok=True)
            (step_artifact / "candidate.md").write_text(candidate, encoding="utf-8")
            (step_artifact / "patch.json").write_text(
                json.dumps(patch.to_dict(), ensure_ascii=False, indent=2), encoding="utf-8"
            )
            (step_artifact / "patch_reports.json").write_text(
                json.dumps(reports, ensure_ascii=False, indent=2), encoding="utf-8"
            )

            # Selection eval with candidate
            print(f"[step {step_idx}] Selection eval with candidate...")
            with with_skill_override(client, skill_path, candidate):
                _, cand_rollouts = _rollout_tasks(
                    client, selection_suite, cfg.selection_tasks, cfg.skill_name,
                    cfg.run_id, f"step_{step_idx:02d}_selection", run_root, cfg.judge_cfg,
                )
            candidate_score_agg = aggregate_score(cand_rollouts)
            print(f"[step {step_idx}] candidate: hard={candidate_score_agg.hard:.3f} soft={candidate_score_agg.soft:.3f}")

            # Gate
            decision = validation_gate(candidate_score_agg, current_score, cfg.min_delta)
            print(f"[step {step_idx}] gate: {decision.reason}")
            (step_artifact / "decision.json").write_text(
                json.dumps(dc.asdict(decision), ensure_ascii=False, indent=2), encoding="utf-8"
            )

            if decision.accepted:
                current = candidate
                current_score = candidate_score_agg
                if current_score.primary > best_score.primary:
                    best = current
                    best_score = current_score
                    (run_root / "best_skill.md").write_text(best, encoding="utf-8")
                no_improvement_count = 0
                steps.append(StepDecision(
                    step=step_idx,
                    candidate_score=decision.candidate_score,
                    current_score=decision.current_score,
                    accepted=True,
                    reason=decision.reason,
                    edits_applied=patch.edits,
                ))
            else:
                rejected.extend(patch.edits)
                no_improvement_count += 1
                steps.append(StepDecision(
                    step=step_idx,
                    candidate_score=decision.candidate_score,
                    current_score=decision.current_score,
                    accepted=False,
                    reason=decision.reason,
                    edits_rejected=patch.edits,
                ))

            # Early stop
            if no_improvement_count >= cfg.early_stop_patience:
                print(f"[step {step_idx}] no improvement for {cfg.early_stop_patience} steps; stopping.")
                break

        return OptimizationResult(
            skill_name=cfg.skill_name,
            initial_score=aggregate_score(sel_rollouts).primary,
            best_score=best_score.primary,
            best_skill=best,
            steps=steps,
            accepted_count=sum(1 for s in steps if s.accepted),
            rejected_count=sum(1 for s in steps if not s.accepted),
        )
    finally:
        client.close()
