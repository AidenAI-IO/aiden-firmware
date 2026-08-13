import pytest

from runner.agent_client import AgentRequestError, AgentTimeoutError, ChatResponse
from runner.reset import (
    ResetError,
    call_environment_release,
    call_environment_setup,
    clear_stale_adb_android_owner,
    environment_health_endpoint,
    environment_release_endpoint,
    environment_setup_endpoint,
    per_task_setup,
)


class FailingChatClient:
    def chat(self, message, timeout_sec=None):
        raise AgentRequestError("HTTP 500: missing auth")


class FailingClearHistoryClient:
    def chat(self, message, timeout_sec=None):
        return ChatResponse(response="done", history=[])

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
        return ChatResponse(response="done", history=[])

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


def test_agent_prompt_setup_includes_prompt_prefix():
    client = RecordingSetupClient()

    per_task_setup(
        client,
        {"type": "agent_prompt", "prompt": "prepare editor", "timeout_sec": 5},
        prompt_prefix="ADB benchmark rules",
    )

    assert client.calls[0] == ("chat", "ADB benchmark rules\n\nprepare editor", 5)


def test_agent_prompt_setup_rejects_explicit_operation_blocker():
    class BlockedSetupClient(RecordingSetupClient):
        def chat(self, message, timeout_sec=None):
            self.calls.append(("chat", message, timeout_sec))
            return ChatResponse(
                response="当前 app_connected: false，我无法直接获取截图或执行触控操作。",
                history=[],
            )

    client = BlockedSetupClient()

    with pytest.raises(ResetError, match="setup agent_prompt did not establish a usable state"):
        per_task_setup(client, {"type": "agent_prompt", "prompt": "prepare editor"})

    assert all(call[0] != "clear_history" for call in client.calls)


@pytest.mark.parametrize(
    "response",
    [
        "I'm stopping here because the task is not making measurable progress. Stop reason: no_progress.",
        "I'm stopping here because the task is not making measurable progress. Stop reason: budget_exceeded.",
    ],
)
def test_agent_prompt_setup_rejects_agent_termination_without_completion(response):
    class StoppedSetupClient(RecordingSetupClient):
        def chat(self, message, timeout_sec=None):
            self.calls.append(("chat", message, timeout_sec))
            return ChatResponse(response=response, history=[])

    client = StoppedSetupClient()

    with pytest.raises(ResetError, match="setup agent_prompt did not establish a usable state"):
        per_task_setup(client, {"type": "agent_prompt", "prompt": "prepare editor"})

    assert all(call[0] != "clear_history" for call in client.calls)


def test_agent_prompt_setup_accepts_successful_tool_activity():
    class SuccessfulSetupClient(RecordingSetupClient):
        def chat(self, message, timeout_sec=None):
            self.calls.append(("chat", message, timeout_sec))
            return ChatResponse(
                response="准备完成。",
                history=[
                    {"type": "tool_call", "tool_name": "open_app", "tool_input": "{}"},
                    {"type": "tool_result", "content": "ok"},
                ],
            )

    client = SuccessfulSetupClient()

    per_task_setup(client, {"type": "agent_prompt", "prompt": "prepare editor"})

    assert client.calls[-1] == ("clear_history",)


def test_environment_setup_endpoint_is_derived_from_environment_endpoint():
    assert environment_setup_endpoint("http://127.0.0.1:9090") == "http://127.0.0.1:9090/api/setup"
    assert environment_setup_endpoint("http://127.0.0.1:9090/api/setup") == "http://127.0.0.1:9090/api/setup"
    assert environment_setup_endpoint("http://127.0.0.1:9090/api/release") == "http://127.0.0.1:9090/api/setup"
    assert environment_setup_endpoint("http://127.0.0.1:9090/api/screen") == "http://127.0.0.1:9090/api/setup"


def test_environment_release_endpoint_is_derived_from_environment_endpoint():
    assert environment_release_endpoint("http://127.0.0.1:9090") == "http://127.0.0.1:9090/api/release"
    assert environment_release_endpoint("http://127.0.0.1:9090/api/setup") == "http://127.0.0.1:9090/api/release"
    assert environment_release_endpoint("http://127.0.0.1:9090/api/release") == "http://127.0.0.1:9090/api/release"
    assert environment_release_endpoint("http://127.0.0.1:9090/api/screen") == "http://127.0.0.1:9090/api/release"


def test_environment_health_endpoint_is_derived_from_environment_endpoint():
    assert environment_health_endpoint("http://127.0.0.1:9090") == "http://127.0.0.1:9090/health"
    assert environment_health_endpoint("http://127.0.0.1:9090/api/setup") == "http://127.0.0.1:9090/health"


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


def test_clear_stale_adb_android_owner_releases_expired_owner(monkeypatch):
    calls = []

    class FakeResponse:
        def __init__(self, body):
            self.body = body

        def read(self):
            return self.body

        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

    def fake_urlopen(req, timeout=None):
        calls.append((req.full_url, req.get_method(), req.headers.get("Benchmark-task-id"), timeout))
        if req.full_url.endswith("/health"):
            return FakeResponse(
                b'{"ok": true, "data": {"bridge_type": "adb_android", "active_task_id": "suite:old", "active_task_lease_state": "expired"}}'
            )
        return FakeResponse(b'{"ok": true, "data": {"released": true}}')

    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    assert clear_stale_adb_android_owner("http://127.0.0.1:9090/api/setup") == "suite:old"
    assert calls == [
        ("http://127.0.0.1:9090/health", "GET", None, 2.0),
        ("http://127.0.0.1:9090/api/release", "POST", "suite:old", 2),
    ]


def test_clear_stale_adb_android_owner_preserves_active_owner(monkeypatch):
    calls = []

    class FakeResponse:
        def read(self):
            return (
                b'{"ok": true, "data": {"bridge_type": "adb_android", '
                b'"active_task_id": "suite:running", "active_task_lease_state": "active"}}'
            )

        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

    def fake_urlopen(req, timeout=None):
        calls.append((req.full_url, req.get_method(), req.headers.get("Benchmark-task-id"), timeout))
        return FakeResponse()

    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    assert clear_stale_adb_android_owner("http://127.0.0.1:9090") == ""
    assert calls == [("http://127.0.0.1:9090/health", "GET", None, 2.0)]


def test_clear_stale_adb_android_owner_preserves_owner_without_lease_state(monkeypatch):
    calls = []

    class FakeResponse:
        def read(self):
            return b'{"ok": true, "data": {"bridge_type": "adb_android", "active_task_id": "suite:running"}}'

        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

    def fake_urlopen(req, timeout=None):
        calls.append((req.full_url, req.get_method(), req.headers.get("Benchmark-task-id"), timeout))
        return FakeResponse()

    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    assert clear_stale_adb_android_owner("http://127.0.0.1:9090") == ""
    assert calls == [("http://127.0.0.1:9090/health", "GET", None, 2.0)]


def test_clear_stale_adb_android_owner_ignores_non_adb_bridge(monkeypatch):
    calls = []

    class FakeResponse:
        def read(self):
            return b'{"ok": true, "data": {"bridge_type": "mobilegym", "active_task_id": "suite:old"}}'

        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

    def fake_urlopen(req, timeout=None):
        calls.append((req.full_url, req.get_method()))
        return FakeResponse()

    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    assert clear_stale_adb_android_owner("http://127.0.0.1:9090") == ""
    assert calls == [("http://127.0.0.1:9090/health", "GET")]
