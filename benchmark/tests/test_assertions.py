import runner.assertions as assertions
from runner.assertions import evaluate_hard_assertions, AssertionOutcome
from runner.suite import HardAssertions
from runner.models import Trace, ToolCall

def make_trace(n: int, response: str = "ok") -> Trace:
    return Trace(
        tool_calls=[ToolCall(step=i+1, tool="x", input={}) for i in range(n)],
        final_response=response, total_tool_calls=n, total_duration_ms=0,
    )

def test_within_bounds_passes():
    spec = HardAssertions(min_tool_calls=1, max_tool_calls=10)
    out = evaluate_hard_assertions(make_trace(3), spec, timed_out=False)
    assert out.all_passed is True

def test_below_min_tool_calls_fails():
    spec = HardAssertions(min_tool_calls=2, max_tool_calls=10)
    out = evaluate_hard_assertions(make_trace(1), spec, timed_out=False)
    assert out.all_passed is False
    assert out.results.min_tool_calls is False

def test_above_max_tool_calls_fails():
    spec = HardAssertions(min_tool_calls=1, max_tool_calls=2)
    out = evaluate_hard_assertions(make_trace(5), spec, timed_out=False)
    assert out.all_passed is False
    assert out.results.max_tool_calls is False

def test_timeout_fails():
    spec = HardAssertions(min_tool_calls=0, max_tool_calls=10)
    out = evaluate_hard_assertions(make_trace(3), spec, timed_out=True)
    assert out.all_passed is False
    assert out.results.timeout is False

def test_missing_response_fails_when_required():
    spec = HardAssertions(min_tool_calls=0, max_tool_calls=10, response_required=True)
    out = evaluate_hard_assertions(make_trace(1, response=""), spec, timed_out=False)
    assert out.all_passed is False
    assert out.results.response_exists is False

def test_expected_option_answer_matches_tagged_final_answer():
    result = assertions.evaluate_expected_answer(
        "Reasoning text. <final_answer>(c)</final_answer>", "(C)", "option_letter"
    )

    assert result.passed is True
    assert result.predicted_answer == "(c)"

def test_expected_option_answer_fails_for_wrong_answer():
    result = assertions.evaluate_expected_answer("I choose (b).", "(c)", "option_letter")

    assert result.passed is False
    assert result.predicted_answer == "(b)"

def test_expected_option_answer_rejects_invalid_expected_answer():
    result = assertions.evaluate_expected_answer("No option selected.", "z", "option_letter")

    assert result.passed is False
    assert result.expected_answer is None
