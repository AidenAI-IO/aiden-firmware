from __future__ import annotations
import dataclasses as dc
import json
import re
from typing import Any
from runner.models import HardAssertionFailure, HardAssertionResults, Trace
from runner.suite import HardAssertions, TraceObservationSpec
from runner.trace import trace_has_skill_read

@dc.dataclass
class AssertionOutcome:
    all_passed: bool
    results: HardAssertionResults
    failures: list[HardAssertionFailure]

@dc.dataclass
class ExpectedAnswerResult:
    passed: bool
    expected_answer: str | None
    predicted_answer: str | None
    answer_format: str

@dc.dataclass
class ExpectedRecallResult:
    passed: bool
    expected_memory_ids: list[str]
    recalled_memory_ids: list[str]

@dc.dataclass
class TraceObservationResult:
    id: str
    description: str
    passed: bool
    reason: str


def evaluate_trace_observations(
    trace: Trace,
    specs: list[TraceObservationSpec],
    active_skills: list[str] | None = None,
) -> list[TraceObservationResult]:
    results: list[TraceObservationResult] = []
    active = {name.strip() for name in active_skills or [] if name.strip()}
    for spec in specs:
        passed = spec.skill_name in active or trace_has_skill_read(trace, spec.skill_name)
        if spec.skill_name in active:
            reason = f"Task requested active skill {spec.skill_name!r} via chat skills payload."
        elif passed:
            reason = f"Trace contains skill_read for {spec.skill_name!r}."
        else:
            reason = f"No skill_read call for {spec.skill_name!r} in trace."
        results.append(
            TraceObservationResult(
                id=spec.id,
                description=spec.description,
                passed=passed,
                reason=reason,
            )
        )
    return results


def evaluate_hard_assertions(trace: Trace, spec: HardAssertions, timed_out: bool) -> AssertionOutcome:
    tools_used = [tc.tool for tc in trace.tool_calls]
    unique_tools = _unique_preserve_order(tools_used)
    results = HardAssertionResults(
        min_tool_calls=trace.total_tool_calls >= spec.min_tool_calls,
        max_tool_calls=trace.total_tool_calls <= spec.max_tool_calls,
        required_tools=all(tool in tools_used for tool in spec.required_tools),
        forbidden_tools=not any(tool in tools_used for tool in spec.forbidden_tools),
        timeout=not timed_out,
        response_exists=bool(trace.final_response) if spec.response_required else True,
    )
    failures: list[HardAssertionFailure] = []
    if results.timeout is False:
        failures.append(
            HardAssertionFailure(
                id="timeout",
                label="Timeout",
                requirement=f"Task must complete within {spec.must_complete_within_sec} seconds.",
                actual="Task timed out before completion.",
            )
        )
    if results.response_exists is False:
        failures.append(
            HardAssertionFailure(
                id="response_exists",
                label="Response Exists",
                requirement="Final response is required.",
                actual="Agent final response was empty.",
            )
        )
    if results.min_tool_calls is False:
        failures.append(
            HardAssertionFailure(
                id="min_tool_calls",
                label="Min Tool Calls",
                requirement=f"Use at least {spec.min_tool_calls} tool call(s).",
                actual=f"Used {trace.total_tool_calls} tool call(s).",
            )
        )
    if results.max_tool_calls is False:
        failures.append(
            HardAssertionFailure(
                id="max_tool_calls",
                label="Max Tool Calls",
                requirement=f"Use at most {spec.max_tool_calls} tool call(s).",
                actual=f"Used {trace.total_tool_calls} tool call(s).",
            )
        )
    if results.required_tools is False:
        missing = [tool for tool in spec.required_tools if tool not in tools_used]
        failures.append(
            HardAssertionFailure(
                id="required_tools",
                label="Required Tools",
                requirement=f"Must call: {_format_list(spec.required_tools)}.",
                actual=(
                    f"Missing: {_format_list(missing)}. "
                    f"Used: {_format_list(unique_tools)}."
                ),
            )
        )
    if results.forbidden_tools is False:
        offenders = [
            f"{tc.tool} at step {tc.step}"
            for tc in trace.tool_calls
            if tc.tool in spec.forbidden_tools
        ]
        failures.append(
            HardAssertionFailure(
                id="forbidden_tools",
                label="Forbidden Tools",
                requirement=f"Must not call: {_format_list(spec.forbidden_tools)}.",
                actual=(
                    f"Forbidden calls: {_format_list(offenders)}. "
                    f"Used: {_format_list(unique_tools)}."
                ),
            )
        )
    all_passed = (
        results.min_tool_calls
        and results.max_tool_calls
        and results.required_tools
        and results.forbidden_tools
        and results.timeout
        and results.response_exists
    )
    return AssertionOutcome(all_passed=bool(all_passed), results=results, failures=failures)


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


def evaluate_expected_recalled_memory_ids(
    history: list[dict[str, Any]], expected_memory_ids: list[str]
) -> ExpectedRecallResult:
    recalled_ids: list[str] = []
    for message in history:
        if message.get("type") != "tool_result" or message.get("tool_name") != "recall_memory":
            continue
        content = message.get("content") or ""
        try:
            payload = json.loads(content)
        except (TypeError, json.JSONDecodeError):
            continue
        if not isinstance(payload, dict):
            continue
        for item in payload.get("results") or []:
            if not isinstance(item, dict):
                continue
            memory_id = item.get("id")
            if isinstance(memory_id, str) and memory_id and memory_id not in recalled_ids:
                recalled_ids.append(memory_id)
    return ExpectedRecallResult(
        passed=all(memory_id in recalled_ids for memory_id in expected_memory_ids),
        expected_memory_ids=list(expected_memory_ids),
        recalled_memory_ids=recalled_ids,
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


def _unique_preserve_order(items: list[str]) -> list[str]:
    seen: set[str] = set()
    out: list[str] = []
    for item in items:
        if item in seen:
            continue
        seen.add(item)
        out.append(item)
    return out


def _format_list(items: list[str]) -> str:
    return ", ".join(items) if items else "none"
