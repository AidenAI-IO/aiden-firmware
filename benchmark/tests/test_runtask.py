import base64
import json
from pathlib import Path

from runner.agent_client import ChatResponse, ToolInvokeResult
from runner.runtask import run_one_task
from runner.suite import HardAssertions, RubricItem, Suite, TaskSpec


class FakeClient:
    def clear_history(self):
        pass

    def invoke_tool(self, name, args):
        assert name == "screenshot"
        payload = {
            "width": 1,
            "height": 1,
            "format": "jpeg",
            "data": base64.b64encode(b"x").decode("ascii"),
        }
        return ToolInvokeResult(output=json.dumps(payload), is_error=False, duration_ms=1)

    def chat(self, message, timeout_sec=None, attachments=None):
        return ChatResponse(
            response="ok",
            history=[{"type": "assistant", "content": "ok"}],
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
