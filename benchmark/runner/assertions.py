from __future__ import annotations
import dataclasses as dc
import json
import re
from typing import Any
from runner.models import HardAssertionFailure, HardAssertionResults, Trace
from runner.matching import dict_contains
from runner.suite import HardAssertions, TraceObservationSpec
from runner.trace import trace_has_skill_read


PROHIBITED_ACTION_PATTERNS = {
    "send": re.compile(r"\bsend\b", re.IGNORECASE),
    "post": re.compile(r"\bpost\b", re.IGNORECASE),
    "purchase": re.compile(r"\b(buy|purchase)\b", re.IGNORECASE),
    "checkout": re.compile(r"\bcheck(?:\s|-)?out\b", re.IGNORECASE),
    "payment": re.compile(r"\b(pay|payment)\b", re.IGNORECASE),
    "order_submission": re.compile(r"\b(order submission|submit order|place order)\b", re.IGNORECASE),
    "transfer": re.compile(r"\btransfer\b", re.IGNORECASE),
    "cancel": re.compile(r"\bcancel\b", re.IGNORECASE),
}

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
        if spec.skill_name:
            passed = spec.skill_name in active or trace_has_skill_read(trace, spec.skill_name)
            if spec.skill_name in active:
                reason = f"Task requested active skill {spec.skill_name!r} via chat skills payload."
            elif passed:
                reason = f"Trace contains skill_read for {spec.skill_name!r}."
            else:
                reason = f"No skill_read call for {spec.skill_name!r} in trace."
        else:
            passed = _trace_has_tool_call(trace, spec.tool_name, spec.input_contains)
            input_desc = f" with input containing {spec.input_contains!r}" if spec.input_contains else ""
            reason = (
                f"Trace contains {spec.tool_name!r}{input_desc}."
                if passed
                else f"No {spec.tool_name!r}{input_desc} call in trace."
            )
        results.append(
            TraceObservationResult(
                id=spec.id,
                description=spec.description,
                passed=passed,
                reason=reason,
            )
        )
    return results


def _trace_has_tool_call(trace: Trace, tool_name: str, input_contains: dict[str, Any]) -> bool:
    target = tool_name.strip()
    if not target:
        return False
    for tc in trace.tool_calls:
        if tc.tool != target:
            continue
        if dict_contains(tc.input if isinstance(tc.input, dict) else {}, input_contains):
            return True
    return False


def evaluate_hard_assertions(trace: Trace, spec: HardAssertions, timed_out: bool) -> AssertionOutcome:
    tools_used = [tc.tool for tc in trace.tool_calls]
    unique_tools = _unique_preserve_order(tools_used)
    prohibited_action_offenders = _prohibited_action_offenders(trace, spec.prohibited_actions)
    missing_tool_calls = [
        requirement
        for requirement in spec.required_tool_calls
        if not _trace_has_tool_call(trace, requirement.tool, requirement.input_contains)
    ]
    results = HardAssertionResults(
        min_tool_calls=trace.total_tool_calls >= spec.min_tool_calls,
        max_tool_calls=trace.total_tool_calls <= spec.max_tool_calls,
        required_tools=all(tool in tools_used for tool in spec.required_tools),
        forbidden_tools=not any(tool in tools_used for tool in spec.forbidden_tools),
        prohibited_actions=not prohibited_action_offenders if spec.prohibited_actions else None,
        required_tool_calls=not missing_tool_calls if spec.required_tool_calls else None,
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
    if results.prohibited_actions is False:
        failures.append(
            HardAssertionFailure(
                id="prohibited_actions",
                label="Prohibited Actions",
                requirement=f"Must not execute semantic action(s): {_format_list(spec.prohibited_actions)}.",
                actual=f"Prohibited semantic action calls: {_format_list(prohibited_action_offenders)}.",
            )
        )
    if results.required_tool_calls is False:
        expected = [
            _required_tool_call_description(item.tool, item.input_contains)
            for item in missing_tool_calls
        ]
        failures.append(
            HardAssertionFailure(
                id="required_tool_calls",
                label="Required Tool Calls",
                requirement=f"Trace must contain: {_format_list(expected)}.",
                actual=f"Used: {_format_list(unique_tools)}.",
            )
        )
    all_passed = (
        results.min_tool_calls
        and results.max_tool_calls
        and results.required_tools
        and results.forbidden_tools
        and (results.prohibited_actions is not False)
        and (results.required_tool_calls is not False)
        and results.timeout
        and results.response_exists
    )
    return AssertionOutcome(all_passed=bool(all_passed), results=results, failures=failures)


def _required_tool_call_description(tool: str, input_contains: dict[str, Any]) -> str:
    if not input_contains:
        return tool
    return f"{tool} input contains {json.dumps(input_contains, ensure_ascii=False, sort_keys=True)}"


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
        for memory_id in message.get("recalled_memory_ids") or []:
            if isinstance(memory_id, str) and memory_id and memory_id not in recalled_ids:
                recalled_ids.append(memory_id)
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


def _prohibited_action_offenders(trace: Trace, prohibited_actions: list[str]) -> list[str]:
    if not prohibited_actions:
        return []
    patterns = [
        (action, PROHIBITED_ACTION_PATTERNS.get(action, re.compile(rf"\b{re.escape(action)}\b", re.IGNORECASE)))
        for action in prohibited_actions
    ]
    offenders: list[str] = []
    for tc in trace.tool_calls:
        for label, value in _semantic_trace_values(tc.tool, tc.input):
            text = str(value)
            for _, pattern in patterns:
                if pattern.search(text):
                    offenders.append(f"{tc.tool} {label}={text} at step {tc.step}")
                    break
    return offenders


def _semantic_trace_values(tool: str, tool_input: dict[str, Any]) -> list[tuple[str, str]]:
    values: list[tuple[str, str]] = [("tool", tool)]
    if not isinstance(tool_input, dict):
        return values
    for key in ("action", "command", "intent", "label", "target", "button", "name"):
        value = tool_input.get(key)
        if isinstance(value, str) and value.strip():
            descriptor = key if key == "action" else f"input.{key}"
            values.append((descriptor, value))
    nested = tool_input.get("input")
    if isinstance(nested, dict):
        for key in ("action", "command", "intent", "label", "target", "button", "name"):
            value = nested.get(key)
            if isinstance(value, str) and value.strip():
                values.append((f"input.{key}", value))
    return values


def _format_list(items: list[str]) -> str:
    return ", ".join(items) if items else "none"
