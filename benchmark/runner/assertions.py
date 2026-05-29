from __future__ import annotations
import dataclasses as dc
import re
from runner.models import HardAssertionResults, Trace
from runner.suite import HardAssertions

@dc.dataclass
class AssertionOutcome:
    all_passed: bool
    results: HardAssertionResults

@dc.dataclass
class ExpectedAnswerResult:
    passed: bool
    expected_answer: str | None
    predicted_answer: str | None
    answer_format: str

def evaluate_hard_assertions(trace: Trace, spec: HardAssertions, timed_out: bool) -> AssertionOutcome:
    results = HardAssertionResults(
        min_tool_calls=trace.total_tool_calls >= spec.min_tool_calls,
        max_tool_calls=trace.total_tool_calls <= spec.max_tool_calls,
        timeout=not timed_out,
        response_exists=bool(trace.final_response) if spec.response_required else True,
    )
    all_passed = (
        results.min_tool_calls
        and results.max_tool_calls
        and results.timeout
        and results.response_exists
    )
    return AssertionOutcome(all_passed=bool(all_passed), results=results)


def evaluate_expected_answer(
    final_response: str, expected_answer: str, answer_format: str
) -> ExpectedAnswerResult:
    if answer_format != "option_letter":
        raise ValueError(f"unsupported answer_format: {answer_format!r}")
    expected = _normalize_option_answer(expected_answer)
    predicted = _extract_option_answer(final_response)
    return ExpectedAnswerResult(
        passed=expected is not None and predicted == expected,
        expected_answer=expected,
        predicted_answer=predicted,
        answer_format=answer_format,
    )


def _normalize_option_answer(text: str) -> str | None:
    match = re.search(r"\(([a-d])\)|\b([a-d])\b", text.strip().lower())
    if not match:
        return None
    letter = match.group(1) or match.group(2)
    return f"({letter})"


def _extract_option_answer(final_response: str) -> str | None:
    text = final_response.strip()
    lower = text.lower()
    marker = "<final_answer>"
    if marker in lower:
        start = lower.rfind(marker) + len(marker)
        text = text[start:]
        end = text.lower().find("</final_answer>")
        if end != -1:
            text = text[:end]
    matches = re.findall(r"\(([a-d])\)|\b([a-d])\b", text.lower())
    answers = [a or b for a, b in matches]
    if len(set(answers)) != 1:
        return None
    return f"({answers[0]})"
