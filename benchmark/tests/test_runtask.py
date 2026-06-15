import base64
import json
from pathlib import Path

from runner.agent_client import (
    AgentRequestError,
    AgentTimeoutError,
    ChatResponse,
    ToolInvokeResult,
)
from runner.runtask import run_one_task
from runner.suite import HardAssertions, RubricItem, Suite, TaskSpec


class FakeClient:
    def __init__(self, response="ok"):
        self.response = response
        self.messages = []

    def health(self):
        return True

    def clear_history(self, timeout=30):
        pass

    def recover_after_timeout(self, timeout_sec=90, poll_sec=3.0):
        return True

    def invoke_tool(self, name, args):
        assert name == "screenshot"
        payload = {
            "width": 1,
            "height": 1,
            "format": "jpeg",
            "data": base64.b64encode(b"x").decode("ascii"),
        }
        return ToolInvokeResult(output=json.dumps(payload), is_error=False, duration_ms=1)

    def chat(self, message, timeout_sec=None, attachments=None, skills=None):
        self.messages.append(message)
        return ChatResponse(
            response=self.response,
            history=[{"type": "assistant", "content": self.response}],
        )


def test_run_one_task_passes_without_judge_when_hard_assertions_pass(tmp_path: Path):
    suite = Suite(
        name="smoke",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
    )
    task = TaskSpec(
        id="chat_smoke",
        category="diagnostic",
        description_for_judge="Smoke chat task.",
        prompt="ping",
        rubric=[RubricItem(id="responds", check="Agent responds.")],
        hard_assertions=HardAssertions(min_tool_calls=0, max_tool_calls=0),
    )

    result = run_one_task(
        FakeClient(), suite, task, 1, tmp_path / "artifacts", None, None, "run-1"
    )

    assert result.status == "passed"
    assert result.hard_assertions.response_exists is True


def test_run_one_task_fails_without_judge_when_expected_answer_is_wrong(tmp_path: Path):
    suite = Suite(
        name="persona",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
    )
    task = TaskSpec(
        id="personamem_case",
        category="memory",
        description_for_judge="Choose the personalized option.",
        prompt="Choose one option.",
        rubric=[RubricItem(id="chooses_correct", check="Agent chooses correct option.")],
        hard_assertions=HardAssertions(min_tool_calls=0, max_tool_calls=0),
        expected_answer="(c)",
        answer_format="option_letter",
    )

    result = run_one_task(
        FakeClient("I choose <final_answer>(b)</final_answer>"),
        suite,
        task,
        1,
        tmp_path / "artifacts",
        None,
        None,
        "run-1",
    )

    assert result.status == "failed"
    assert result.metrics["expected_answer_match"] is False
    assert result.metrics["predicted_answer"] == "(b)"


def test_run_one_task_applies_suite_prompt_prefix(tmp_path: Path):
    suite = Suite(
        name="persona",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
        prompt_prefix="You must call recall_memory before answering.",
    )
    task = TaskSpec(
        id="personamem_case",
        category="memory",
        description_for_judge="Choose the personalized option.",
        prompt="Choose one option.",
        rubric=[RubricItem(id="chooses_correct", check="Agent chooses correct option.")],
        hard_assertions=HardAssertions(min_tool_calls=0, max_tool_calls=0),
    )
    client = FakeClient("ok")

    result = run_one_task(
        client, suite, task, 1, tmp_path / "artifacts", None, None, "run-1"
    )

    assert result.status == "passed"
    assert client.messages == [
        "You must call recall_memory before answering.\n\nChoose one option."
    ]


class TimeoutClient(FakeClient):
    def __init__(self):
        super().__init__()
        self.history = [
            {"type": "tool_call", "tool_name": "screenshot", "tool_input": "{}"},
            {"type": "tool_result", "content": "{}"},
        ]

    def chat(self, message, timeout_sec=None, attachments=None, skills=None):
        raise AgentTimeoutError("deadline exceeded")

    def get_history(self):
        return self.history


def test_run_one_task_preserves_history_after_timeout(tmp_path: Path):
    suite = Suite(
        name="phone",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
    )
    task = TaskSpec(
        id="slow_task",
        category="phone",
        description_for_judge="Times out after using a tool.",
        prompt="do something slow",
        rubric=[RubricItem(id="done", check="Done.")],
        hard_assertions=HardAssertions(min_tool_calls=1, max_tool_calls=4),
    )

    result = run_one_task(
        TimeoutClient(), suite, task, 1, tmp_path / "artifacts", None, None, "run-1"
    )

    assert result.status == "timeout"
    assert result.metrics["tool_calls"] == 1
    history = json.loads((tmp_path / "artifacts" / "history.json").read_text())
    assert history[0]["tool_name"] == "screenshot"


class PerceptionClient(FakeClient):
    def __init__(self, history, fail_chat=False):
        super().__init__()
        self.history = history
        self.fail_chat = fail_chat
        self.attachments = []

    def get_tool_description(self, name):
        assert name == "mouse_click"
        return "Move mouse to a position and click. Normalized coordinates use 0-1000."

    def chat(self, message, timeout_sec=None, attachments=None, skills=None):
        self.messages.append(message)
        self.attachments.append(attachments)
        if self.fail_chat:
            raise AgentRequestError("HTTP 500: 413 Request Entity Too Large")
        return ChatResponse(response="", history=self.history)

    def get_history(self):
        return self.history


def _perception_suite(tmp_path: Path) -> Suite:
    suite_path = tmp_path / "perception_v1.json"
    screenshots = tmp_path / "screenshots"
    screenshots.mkdir()
    (screenshots / "task_386.jpg").write_bytes(b"jpeg")
    suite_path.write_text("{}", encoding="utf-8")
    return Suite(
        name="perception_v1",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=suite_path,
    )


def _task_386_spec() -> TaskSpec:
    return TaskSpec(
        id="task_386",
        category="perception",
        description_for_judge="Click chat details.",
        prompt="打开右上角的聊天详情",
        input_screenshot="screenshots/task_386.jpg",
        rubric=[
            RubricItem(
                id="called_click_tool",
                check="The tool trace contains at least one touch_gesture or mouse_click call.",
            ),
            RubricItem(
                id="click_targets_",
                check=(
                    "The touch/click coordinates target the chat details area: "
                    "normalized x in [905, 967], y in [82, 97] "
                    "(0-1000 normalized space, where 500 is center)."
                ),
            ),
        ],
        hard_assertions=HardAssertions(
            min_tool_calls=1,
            max_tool_calls=5,
            must_complete_within_sec=120,
            response_required=True,
        ),
    )


def test_perception_task_injects_mouse_click_description_and_scores_first_click(tmp_path: Path):
    history = [
        {"type": "tool_call", "tool_name": "mouse_click",
         "tool_input": '{"x":"940","y":"90","coord_space":"normalized"}'},
        {"type": "tool_result", "tool_name": "mouse_click", "content": "{}"},
        {"type": "tool_call", "tool_name": "mouse_click",
         "tool_input": '{"x":"0.935","y":"0.083","coord_space":"normalized"}'},
        {"type": "tool_result", "tool_name": "mouse_click", "content": "{}"},
    ]
    client = PerceptionClient(history)

    result = run_one_task(
        client,
        _perception_suite(tmp_path),
        _task_386_spec(),
        1,
        tmp_path / "artifacts",
        None,
        None,
        "run-1",
    )

    assert result.status == "passed"
    assert result.rubric_pass_count == 2
    assert result.metrics["perception_first_click"]["first_click"]["x"] == 940
    assert "mouse_click:" in client.messages[0]
    assert "0-1000" in client.messages[0]
    assert client.attachments[0][0]["kind"] == "image"


def test_perception_task_can_pass_from_history_after_agent_413(tmp_path: Path):
    history = [
        {"type": "tool_call", "tool_name": "mouse_click",
         "tool_input": '{"x":920,"y":90,"coord_space":"normalized"}'},
        {"type": "tool_result", "tool_name": "mouse_click", "content": "{}"},
    ]
    client = PerceptionClient(history, fail_chat=True)

    result = run_one_task(
        client,
        _perception_suite(tmp_path),
        _task_386_spec(),
        1,
        tmp_path / "artifacts",
        None,
        None,
        "run-1",
    )

    assert result.status == "passed"
    assert result.metrics["agent_error"].startswith("HTTP 500")
    assert result.metrics["response_required_satisfied_by_first_click"] is True
    assert result.hard_assertions.response_exists is True
