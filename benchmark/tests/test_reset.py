import pytest

from runner.agent_client import AgentRequestError, AgentTimeoutError
from runner.reset import ResetError, call_environment_reset, environment_reset_endpoint, per_task_setup


class FailingChatClient:
    def chat(self, message, timeout_sec=None):
        raise AgentRequestError("HTTP 500: missing auth")


class FailingToolClient:
    def invoke_tool(self, name, args):
        raise AgentRequestError("HTTP 404: unknown tool")


class TimeoutToolClient:
    def invoke_tool(self, name, args):
        raise AgentTimeoutError("timed out")


class FailingClearHistoryClient:
    def chat(self, message, timeout_sec=None):
        return None

    def clear_history(self):
        raise AgentRequestError("HTTP 500: clear failed")


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


def test_tool_sequence_wraps_tool_timeouts_as_reset_error():
    setup = {"tool_sequence": [{"tool": "touch_gesture", "args": {"type": "home"}}]}

    with pytest.raises(ResetError, match="tool touch_gesture timed out"):
        per_task_setup(TimeoutToolClient(), setup)


def test_agent_prompt_setup_wraps_clear_history_errors_as_reset_error():
    setup = {"type": "agent_prompt", "prompt": "remember this", "timeout_sec": 5}

    with pytest.raises(ResetError, match="setup agent_prompt clear_history failed"):
        per_task_setup(FailingClearHistoryClient(), setup)


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


def test_environment_reset_endpoint_is_derived_from_environment_endpoint():
    assert environment_reset_endpoint("http://127.0.0.1:9090") == "http://127.0.0.1:9090/api/reset"
    assert environment_reset_endpoint("http://127.0.0.1:9090/api/reset") == "http://127.0.0.1:9090/api/reset"


def test_call_environment_reset_posts_to_api_reset(monkeypatch):
    seen = {}

    class FakeResponse:
        status = 200

        def read(self):
            return b'{"ok": true, "data": {"episode_id": "reset-1", "reset": true}}'

        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

    def fake_urlopen(req, timeout=None):
        seen["url"] = req.full_url
        seen["method"] = req.get_method()
        seen["body"] = req.data
        seen["timeout"] = timeout
        return FakeResponse()

    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    result = call_environment_reset("http://127.0.0.1:9090", timeout=12)

    assert seen == {
        "url": "http://127.0.0.1:9090/api/reset",
        "method": "POST",
        "body": b"{}",
        "timeout": 12,
    }
    assert result["data"]["episode_id"] == "reset-1"
