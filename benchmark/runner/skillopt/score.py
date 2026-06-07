"""Score and validation gate.

Converts benchmark TaskResult into SkillOpt-style RolloutResult, computes
aggregate scores, and applies the validation gate (candidate must beat
current by min_delta to be accepted).
"""
from __future__ import annotations
import dataclasses as dc

from runner.models import TaskResult
from runner.skillopt.types import RolloutResult


# Default acceptance threshold (validation_score gain).
DEFAULT_MIN_DELTA = 0.03


def task_result_to_rollout(tr: TaskResult) -> RolloutResult:
    """Convert a TaskResult into a SkillOpt RolloutResult.

      hard = 1 if the task passed end-to-end, else 0.
      soft = rubric_pass_count / rubric_total when rubric is present,
             otherwise mirrors hard.
    """
    hard = 1 if tr.status == "passed" else 0
    if tr.rubric_total > 0:
        soft = tr.rubric_pass_count / tr.rubric_total
    else:
        soft = float(hard)

    fail_reason = ""
    if tr.status != "passed":
        if tr.status == "timeout":
            fail_reason = "timeout"
        elif tr.status == "skipped":
            fail_reason = tr.metrics.get("error", "skipped") if tr.metrics else "skipped"
        elif tr.status == "judge_error":
            fail_reason = tr.metrics.get("judge_error", "judge_error") if tr.metrics else "judge_error"
        else:
            # failed: include first 'no' rubric reason if any
            for v in tr.rubric:
                if v.verdict == "no":
                    fail_reason = f"{v.id}: {v.reason}"
                    break
            if not fail_reason:
                fail_reason = "failed"

    return RolloutResult(
        id=tr.task_id,
        hard=hard,
        soft=soft,
        n_turns=tr.metrics.get("tool_calls", 0) if tr.metrics else 0,
        fail_reason=fail_reason,
        task_description=tr.description_for_judge,
        artifact_dir=tr.artifact_dir,
    )


@dc.dataclass
class AggregateScore:
    hard: float        # mean(hard)  — fraction of tasks fully passing
    soft: float        # mean(soft)  — rubric pass-rate
    n: int             # number of tasks
    n_passed: int

    @property
    def primary(self) -> float:
        """Primary score used by the gate. Hard is more reliable on real-device runs."""
        return self.hard


def aggregate_score(results: list[TaskResult] | list[RolloutResult]) -> AggregateScore:
    """Compute (hard, soft) means over a list of results."""
    if not results:
        return AggregateScore(hard=0.0, soft=0.0, n=0, n_passed=0)
    hards: list[int] = []
    softs: list[float] = []
    for r in results:
        if isinstance(r, TaskResult):
            ro = task_result_to_rollout(r)
        else:
            ro = r
        hards.append(ro.hard)
        softs.append(ro.soft)
    n = len(hards)
    return AggregateScore(
        hard=sum(hards) / n,
        soft=sum(softs) / n,
        n=n,
        n_passed=sum(hards),
    )


@dc.dataclass
class GateDecision:
    accepted: bool
    reason: str
    candidate_score: float
    current_score: float
    delta: float


def validation_gate(
    candidate: AggregateScore,
    current: AggregateScore,
    min_delta: float = DEFAULT_MIN_DELTA,
) -> GateDecision:
    """Decide whether to accept a candidate.

    Rule:
      candidate.primary > current.primary + min_delta  -> accept
      candidate.primary == current.primary             -> reject (we don't
        compress in v1; future: accept if candidate skill is shorter)
      candidate.primary <  current.primary             -> reject (regression)
    """
    if min_delta < 0:
        raise ValueError(f"min_delta must be non-negative, got {min_delta}")

    delta = candidate.primary - current.primary
    if delta > min_delta:
        return GateDecision(
            accepted=True,
            reason=f"candidate hard {candidate.primary:.3f} > current {current.primary:.3f} (+{delta:.3f}, min_delta={min_delta})",
            candidate_score=candidate.primary,
            current_score=current.primary,
            delta=delta,
        )
    return GateDecision(
        accepted=False,
        reason=f"candidate hard {candidate.primary:.3f} not better than current {current.primary:.3f} (delta={delta:+.3f}, min_delta={min_delta})",
        candidate_score=candidate.primary,
        current_score=current.primary,
        delta=delta,
    )
