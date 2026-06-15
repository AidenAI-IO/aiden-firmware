import json
from pathlib import Path

from runner.agent_client import ChatResponse, ToolInvokeResult
from runner.unit import _check_expectation, _collect_suites, _run_suite, is_unit_suite


class FakeClient:
    def __init__(self, outputs):
        self.outputs = list(outputs)
        self.calls = []

    def invoke_tool(self, name, args, timeout=90):
        self.calls.append((name, args, timeout))
        return self.outputs.pop(0)


def test_check_expectation_validates_json_rules():
    error = _check_expectation(
        {"ok": True, "json": {"actions": {"type": "array", "min_len": 1}}},
        '{"actions":["home"]}',
        {"actions": ["home"]},
        is_error=False,
    )

    assert error == ""


def test_check_expectation_reports_missing_text():
    error = _check_expectation(
        {"ok": False, "contains": ["invalid"]},
        "unsupported platform",
        None,
        is_error=True,
    )

    assert "missing text" in error


def test_run_suite_invokes_tool_with_object_payload(tmp_path: Path):
    suite = tmp_path / "quick_action.json"
    suite.write_text(
        json.dumps({
            "kind": "unit",
            "name": "quick_action",
            "target": {"type": "tool", "name": "quick_action"},
            "tests": [{
                "id": "list_ios",
                "input": {"list": True, "platform": "ios"},
                "expect": {"ok": True, "json": {"actions": {"type": "array"}}},
            }],
        }),
        encoding="utf-8",
    )
    client = FakeClient([
        ToolInvokeResult(output='{"actions":["home"]}', is_error=False, duration_ms=12)
    ])

    results = _run_suite(client, suite)

    assert results[0].status == "passed"
    assert client.calls == [("quick_action", {"list": True, "platform": "ios"}, 10)]


def test_collect_suites_filters_unit_kind(tmp_path: Path):
    unit = tmp_path / "unit.json"
    regular = tmp_path / "regular.json"
    unit.write_text('{"kind":"unit"}', encoding="utf-8")
    regular.write_text('{"name":"regular"}', encoding="utf-8")

    class Args:
        suite = None
        suite_dir = str(tmp_path)

    assert is_unit_suite(unit) is True
    assert _collect_suites(Args()) == [unit]


class FakeAgentClient:
    def __init__(self, history):
        self.history = history
        self.messages = []
        self.clear_calls = 0

    def clear_history(self):
        self.clear_calls += 1

    def get_tool_description(self, name):
        assert name == "mouse_click"
        return "Move mouse to a position and click. Normalized coordinates use 0-1000."

    def chat(self, message, timeout_sec=None, attachments=None, skills=None):
        self.messages.append({
            "message": message,
            "timeout_sec": timeout_sec,
            "attachments": attachments,
        })
        return ChatResponse(response="", history=self.history)

    def get_history(self):
        return self.history


def test_run_suite_accepts_benchmark_suite_as_agent_unit(tmp_path: Path):
    suite = tmp_path / "perception_v1.json"
    screenshots = tmp_path / "screenshots"
    screenshots.mkdir()
    (screenshots / "task_386.jpg").write_bytes(b"jpeg")
    suite.write_text(
        json.dumps({
            "name": "perception_v1",
            "tasks": [{
                "id": "task_386",
                "category": "perception",
                "input_screenshot": "screenshots/task_386.jpg",
                "prompt": "打开右上角的聊天详情",
                "description_for_judge": "Click chat details.",
                "rubric": [
                    {
                        "id": "called_click_tool",
                        "check": "The tool trace contains at least one touch_gesture or mouse_click call.",
                    },
                    {
                        "id": "click_targets_",
                        "check": (
                            "The touch/click coordinates target the chat details area: "
                            "normalized x in [905, 967], y in [82, 97] "
                            "(0-1000 normalized space)."
                        ),
                    },
                ],
                "hard_assertions": {
                    "min_tool_calls": 1,
                    "max_tool_calls": 5,
                    "must_complete_within_sec": 120,
                    "response_required": True,
                },
            }],
        }),
        encoding="utf-8",
    )
    client = FakeAgentClient([
        {
            "type": "tool_call",
            "tool_name": "mouse_click",
            "tool_input": '{"x":940,"y":90,"coord_space":"normalized"}',
        },
        {"type": "tool_result", "tool_name": "mouse_click", "content": "{}"},
    ])

    results = _run_suite(client, suite)

    assert results[0].status == "passed"
    assert results[0].target_type == "agent"
    assert results[0].output_json["perception_first_click"]["first_click"]["x"] == 940
    assert "mouse_click:" in client.messages[0]["message"]
    assert client.messages[0]["attachments"][0]["kind"] == "image"
    assert client.clear_calls == 2


def test_agent_unit_fails_when_perception_rubric_is_unsupported(tmp_path: Path):
    suite = tmp_path / "perception_v1.json"
    screenshots = tmp_path / "screenshots"
    screenshots.mkdir()
    (screenshots / "task_386.jpg").write_bytes(b"jpeg")
    suite.write_text(
        json.dumps({
            "name": "perception_v1",
            "tasks": [{
                "id": "task_386",
                "category": "perception",
                "input_screenshot": "screenshots/task_386.jpg",
                "prompt": "打开右上角的聊天详情",
                "description_for_judge": "Click chat details.",
                "rubric": [{
                    "id": "requires_visual_semantics",
                    "check": "The target is semantically correct according to the judge.",
                }],
                "hard_assertions": {
                    "min_tool_calls": 1,
                    "max_tool_calls": 5,
                    "must_complete_within_sec": 120,
                    "response_required": True,
                },
            }],
        }),
        encoding="utf-8",
    )
    client = FakeAgentClient([
        {
            "type": "tool_call",
            "tool_name": "mouse_click",
            "tool_input": '{"x":940,"y":90,"coord_space":"normalized"}',
        },
        {"type": "tool_result", "tool_name": "mouse_click", "content": "{}"},
        {"type": "assistant", "content": "done"},
    ])

    results = _run_suite(client, suite)

    assert results[0].status == "failed"
    assert "unsupported rubric" in results[0].error
    assert results[0].output_json["perception_first_click_error"] == (
        "unsupported rubric for local first-click perception evaluation"
    )
