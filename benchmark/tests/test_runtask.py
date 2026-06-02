import base64
import json
from pathlib import Path

from runner.agent_client import AgentTimeoutError, ChatResponse, ToolInvokeResult
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
