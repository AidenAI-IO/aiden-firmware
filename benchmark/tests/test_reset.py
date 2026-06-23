import pytest

from runner.agent_client import AgentRequestError, AgentTimeoutError
from runner.reset import (
    ResetError,
    call_environment_release,
    call_environment_setup,
    environment_release_endpoint,
    environment_setup_endpoint,
    per_task_setup,
)


class FailingChatClient:
    def chat(self, message, timeout_sec=None):
        raise AgentRequestError("HTTP 500: missing auth")


class FailingClearHistoryClient:
    def chat(self, message, timeout_sec=None):
        return None

    def clear_history(self):
        raise AgentRequestError("HTTP 500: clear failed")


class TimeoutChatClient:
    def chat(self, message, timeout_sec=None):
        raise AgentTimeoutError("timed out")


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


def test_agent_prompt_setup_wraps_timeouts_as_reset_error():
    setup = {"type": "agent_prompt", "prompt": "remember this", "timeout_sec": 5}

    with pytest.raises(ResetError, match="setup agent_prompt timed out"):
        per_task_setup(TimeoutChatClient(), setup)


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

def test_environment_setup_endpoint_is_derived_from_environment_endpoint():
    assert environment_setup_endpoint("http://127.0.0.1:9090") == "http://127.0.0.1:9090/api/setup"
    assert environment_setup_endpoint("http://127.0.0.1:9090/api/setup") == "http://127.0.0.1:9090/api/setup"


def test_environment_release_endpoint_is_derived_from_environment_endpoint():
    assert environment_release_endpoint("http://127.0.0.1:9090") == "http://127.0.0.1:9090/api/release"
    assert environment_release_endpoint("http://127.0.0.1:9090/api/setup") == "http://127.0.0.1:9090/api/release"


def test_call_environment_setup_posts_to_api_setup(monkeypatch):
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
        seen["task_id"] = req.headers.get("Benchmark-task-id")
        seen["timeout"] = timeout
        return FakeResponse()

    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    result = call_environment_setup("http://127.0.0.1:9090", timeout=12)

    assert seen == {
        "url": "http://127.0.0.1:9090/api/setup",
        "method": "POST",
        "body": b"{}",
        "task_id": None,
        "timeout": 12,
    }
    assert result["data"]["episode_id"] == "reset-1"


def test_call_environment_setup_sends_benchmark_task_id_header(monkeypatch):
    seen = {}

    class FakeResponse:
        status = 200

        def read(self):
            return b'{"ok": true}'

        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

    def fake_urlopen(req, timeout=None):
        seen["task_id"] = req.headers.get("Benchmark-task-id")
        return FakeResponse()

    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    call_environment_setup("http://127.0.0.1:9090", task_id="clock.CountAlarms")

    assert seen["task_id"] == "clock.CountAlarms"


def test_call_environment_release_sends_benchmark_task_id_header(monkeypatch):
    seen = {}

    class FakeResponse:
        status = 200

        def read(self):
            return b'{"ok": true, "data": {"released": true}}'

        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

    def fake_urlopen(req, timeout=None):
        seen["url"] = req.full_url
        seen["method"] = req.get_method()
        seen["body"] = req.data
        seen["task_id"] = req.headers.get("Benchmark-task-id")
        return FakeResponse()

    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    call_environment_release("http://127.0.0.1:9090", task_id="clock.CountAlarms")

    assert seen == {
        "url": "http://127.0.0.1:9090/api/release",
        "method": "POST",
        "body": b"{}",
        "task_id": "clock.CountAlarms",
    }
