"""Unit tests for score.py."""
import pytest

from runner.models import HardAssertionResults, RubricVerdict, TaskResult
from skillopt.score import (
    aggregate_score,
    task_result_to_rollout,
    validation_gate,
)


def _mk_task_result(task_id: str, status: str, rubric_pass: int = 0, rubric_total: int = 0):
    return TaskResult(
        suite="s", run_id="r", task_id=task_id, category="c", attempt=1,
        status=status,
        rubric=[],
        rubric_pass_count=rubric_pass,
        rubric_total=rubric_total,
        hard_assertions=HardAssertionResults(),
        metrics={"tool_calls": 3},
        artifact_dir="/tmp/t",
        description_for_judge="desc",
    )


def test_task_result_to_rollout_passed():
    tr = _mk_task_result("t1", "passed", 2, 2)
    ro = task_result_to_rollout(tr)
    assert ro.id == "t1"
    assert ro.hard == 1
    assert ro.soft == 1.0


def test_task_result_to_rollout_failed_partial_rubric():
    tr = _mk_task_result("t2", "failed", 1, 2)
    ro = task_result_to_rollout(tr)
    assert ro.hard == 0
    assert ro.soft == 0.5


def test_task_result_to_rollout_timeout():
    tr = _mk_task_result("t3", "timeout")
    ro = task_result_to_rollout(tr)
    assert ro.hard == 0
    assert ro.fail_reason == "timeout"


def test_aggregate_score_mixed():
    results = [
        _mk_task_result("a", "passed", 2, 2),
        _mk_task_result("b", "failed", 1, 2),
        _mk_task_result("c", "passed", 2, 2),
    ]
    agg = aggregate_score(results)
    assert agg.n == 3
    assert agg.n_passed == 2
    # hard = 2/3
    assert abs(agg.hard - 2/3) < 1e-6
    # soft = (1.0 + 0.5 + 1.0) / 3
    assert abs(agg.soft - 2.5/3) < 1e-6


def test_aggregate_score_empty():
    agg = aggregate_score([])
    assert agg.n == 0
    assert agg.hard == 0.0
    assert agg.soft == 0.0


def test_validation_gate_accept():
    cur = aggregate_score([_mk_task_result("a", "failed"), _mk_task_result("b", "passed")])
    cand = aggregate_score([_mk_task_result("a", "passed"), _mk_task_result("b", "passed")])
    decision = validation_gate(cand, cur, min_delta=0.03)
    assert decision.accepted
    assert decision.delta > 0


def test_validation_gate_reject_regression():
    cur = aggregate_score([_mk_task_result("a", "passed"), _mk_task_result("b", "passed")])
    cand = aggregate_score([_mk_task_result("a", "failed"), _mk_task_result("b", "passed")])
    decision = validation_gate(cand, cur, min_delta=0.03)
    assert not decision.accepted
    assert decision.delta < 0


def test_validation_gate_reject_no_change():
    cur = aggregate_score([_mk_task_result("a", "passed")])
    cand = aggregate_score([_mk_task_result("a", "passed")])
    decision = validation_gate(cand, cur, min_delta=0.03)
    assert not decision.accepted
    assert decision.delta == 0


def test_validation_gate_reject_below_min_delta():
    # 4/5 vs 3/5 = +0.20 (clearly above 0.03), but with 100/100 vs 99/100, +0.01 < 0.03
    cur_results = [_mk_task_result(f"t{i}", "passed") for i in range(99)] + [_mk_task_result("t99", "failed")]
    cand_results = [_mk_task_result(f"t{i}", "passed") for i in range(100)]
    cur = aggregate_score(cur_results)
    cand = aggregate_score(cand_results)
    decision = validation_gate(cand, cur, min_delta=0.03)
    # delta = 0.01, below min_delta
    assert not decision.accepted


def test_validation_gate_rejects_negative_min_delta():
    cur = aggregate_score([_mk_task_result("a", "passed")])
    cand = aggregate_score([_mk_task_result("a", "failed")])

    with pytest.raises(ValueError, match="min_delta must be non-negative"):
        validation_gate(cand, cur, min_delta=-0.5)
