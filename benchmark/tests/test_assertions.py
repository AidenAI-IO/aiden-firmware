import runner.assertions as assertions
from runner.assertions import evaluate_hard_assertions, AssertionOutcome
from runner.suite import HardAssertions, RequiredToolCallSpec
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
    assert out.failures[0].id == "min_tool_calls"
    assert out.failures[0].requirement == "Use at least 2 tool call(s)."
    assert out.failures[0].actual == "Used 1 tool call(s)."

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

def test_required_tools_pass_when_all_present():
    trace = Trace(
        tool_calls=[
            ToolCall(step=1, tool="enter_plan_mode", input={}),
            ToolCall(step=2, tool="commit_plan", input={}),
            ToolCall(step=3, tool="shell", input={"command": "printf 1"}),
        ],
        final_response="ok",
        total_tool_calls=3,
        total_duration_ms=0,
    )
    spec = HardAssertions(required_tools=["enter_plan_mode", "commit_plan"])

    out = evaluate_hard_assertions(trace, spec, timed_out=False)

    assert out.all_passed is True
    assert out.results.required_tools is True

def test_required_tools_fail_when_missing():
    spec = HardAssertions(required_tools=["enter_plan_mode"])

    out = evaluate_hard_assertions(make_trace(1), spec, timed_out=False)

    assert out.all_passed is False
    assert out.results.required_tools is False
    assert out.failures[0].id == "required_tools"
    assert out.failures[0].requirement == "Must call: enter_plan_mode."
    assert out.failures[0].actual == "Missing: enter_plan_mode. Used: x."


def test_forbidden_tools_fail_when_present():
    trace = Trace(
        tool_calls=[ToolCall(step=1, tool="screenshot", input={})],
        final_response="ok",
        total_tool_calls=1,
        total_duration_ms=0,
    )
    spec = HardAssertions(forbidden_tools=["screenshot"])

    out = evaluate_hard_assertions(trace, spec, timed_out=False)

    assert out.all_passed is False
    assert out.results.forbidden_tools is False
    assert out.failures[0].id == "forbidden_tools"
    assert out.failures[0].requirement == "Must not call: screenshot."
    assert out.failures[0].actual == "Forbidden calls: screenshot at step 1. Used: screenshot."


def test_required_tool_calls_match_nested_input_subset():
    trace = Trace(
        tool_calls=[
            ToolCall(
                step=1,
                tool="enter_text",
                input={
                    "text": "+1 202-555-0147",
                    "platform": "ios",
                    "focus": {"x": 500, "y": 360, "coord_space": "normalized"},
                },
            )
        ],
        final_response="ok",
        total_tool_calls=1,
        total_duration_ms=0,
    )
    spec = HardAssertions(
        required_tool_calls=[
            RequiredToolCallSpec(
                tool="enter_text",
                input_contains={"text": "+1 202-555-0147", "focus": {"x": 500}},
            )
        ]
    )

    out = evaluate_hard_assertions(trace, spec, timed_out=False)

    assert out.all_passed is True
    assert out.results.required_tool_calls is True


def test_required_tool_calls_support_string_contains_matcher():
    trace = Trace(
        tool_calls=[
            ToolCall(
                step=1,
                tool="enter_text",
                input={"text": "Biden: +1 202-555-0147", "platform": "ios"},
            )
        ],
        final_response="ok",
        total_tool_calls=1,
        total_duration_ms=0,
    )
    spec = HardAssertions(
        required_tool_calls=[
            RequiredToolCallSpec(
                tool="enter_text",
                input_contains={
                    "text": {"$contains": "+1 202-555-0147"},
                    "platform": "ios",
                },
            )
        ]
    )

    out = evaluate_hard_assertions(trace, spec, timed_out=False)

    assert out.all_passed is True
    assert out.results.required_tool_calls is True


def test_required_tool_calls_report_missing_input_match():
    trace = Trace(
        tool_calls=[ToolCall(step=1, tool="bridge_contacts", input={"action": "query", "query": "Alice"})],
        final_response="ok",
        total_tool_calls=1,
        total_duration_ms=0,
    )
    spec = HardAssertions(
        required_tool_calls=[
            RequiredToolCallSpec(
                tool="bridge_contacts",
                input_contains={"action": "query", "query": "Biden"},
            )
        ]
    )

    out = evaluate_hard_assertions(trace, spec, timed_out=False)

    assert out.all_passed is False
    assert out.results.required_tool_calls is False
    assert out.failures[0].id == "required_tool_calls"

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

def test_expected_option_answer_rejects_ambiguous_predicted_answer():
    result = assertions.evaluate_expected_answer("I think (a), but maybe (c).", "(c)", "option_letter")

    assert result.passed is False
    assert result.predicted_answer is None

def test_expected_recalled_memory_ids_pass_when_tool_result_contains_id():
    history = [
        {"type": "tool_call", "tool_name": "recall_memory", "tool_input": "{}"},
        {
            "type": "tool_result",
            "tool_name": "recall_memory",
            "content": '{"results":[{"id":"personamem_solo_travel"}]}',
        },
    ]

    result = assertions.evaluate_expected_recalled_memory_ids(history, ["personamem_solo_travel"])

    assert result.passed is True
    assert result.recalled_memory_ids == ["personamem_solo_travel"]


def test_expected_recalled_memory_ids_use_structured_metadata_when_content_is_omitted():
    history = [
        {
            "type": "tool_result",
            "tool_name": "recall_memory",
            "content": "[Large tool result omitted from public history (12000 chars)]",
            "recalled_memory_ids": [
                "personamem_campfire_storytelling",
                "personamem_solo_travel",
            ],
        },
    ]

    result = assertions.evaluate_expected_recalled_memory_ids(history, ["personamem_solo_travel"])

    assert result.passed is True
    assert result.recalled_memory_ids == [
        "personamem_campfire_storytelling",
        "personamem_solo_travel",
    ]

def test_expected_recalled_memory_ids_fail_when_expected_id_absent():
    history = [
        {
            "type": "tool_result",
            "tool_name": "recall_memory",
            "content": '{"results":[{"id":"personamem_campfire_storytelling"}]}',
        },
    ]

    result = assertions.evaluate_expected_recalled_memory_ids(history, ["personamem_solo_travel"])

    assert result.passed is False
    assert result.recalled_memory_ids == ["personamem_campfire_storytelling"]

def test_expected_recalled_memory_ids_ignores_non_object_json_payload():
    history = [
        {
            "type": "tool_result",
            "tool_name": "recall_memory",
            "content": "[]",
        },
    ]

    result = assertions.evaluate_expected_recalled_memory_ids(history, ["personamem_solo_travel"])

    assert result.passed is False
    assert result.recalled_memory_ids == []
