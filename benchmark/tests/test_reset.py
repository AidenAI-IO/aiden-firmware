import pytest

from runner.agent_client import AgentRequestError
from runner.reset import ResetError, per_task_setup


class FailingChatClient:
    def chat(self, message, timeout_sec=None):
        raise AgentRequestError("HTTP 500: missing auth")


class FailingToolClient:
    def invoke_tool(self, name, args):
        raise AgentRequestError("HTTP 404: unknown tool")


class RecordingSetupClient:
    def __init__(self):
        self.calls = []

    def chat(self, message, timeout_sec=None):
        self.calls.append(("chat", message, timeout_sec))

    def clear_history(self):
        self.calls.append(("clear_history",))


def test_agent_prompt_setup_wraps_chat_errors_as_reset_error():
    setup = {"type": "agent_prompt", "prompt": "remember this", "timeout_sec": 5}

    with pytest.raises(ResetError, match="setup agent_prompt failed"):
        per_task_setup(FailingChatClient(), setup)


def test_tool_sequence_wraps_tool_errors_as_reset_error():
    setup = {"tool_sequence": [{"tool": "missing_tool", "args": {}}]}

    with pytest.raises(ResetError, match="tool missing_tool failed"):
        per_task_setup(FailingToolClient(), setup)


def test_agent_prompt_setup_clears_history_by_default():
    client = RecordingSetupClient()

    per_task_setup(client, {"type": "agent_prompt", "prompt": "remember this", "timeout_sec": 5})

    assert client.calls == [
        ("chat", "remember this", 5),
        ("clear_history",),
    ]


def test_agent_prompt_setup_can_make_history_clear_explicit():
    client = RecordingSetupClient()

    per_task_setup(
        client,
        {
            "type": "agent_prompt",
            "prompt": "remember this",
            "timeout_sec": 5,
            "clear_history_after": True,
        },
    )

    assert client.calls[-1] == ("clear_history",)
