import pytest

from runner.agent_client import AgentRequestError, AgentTimeoutError, ChatResponse
from runner.reset import (
    ResetError,
    call_environment_release,
    call_environment_setup,
    clear_stale_adb_android_owner,
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
        return ChatResponse(response="READY", history=[])

    def clear_history(self):
        self.calls.append(("clear_history",))

    def seed_episode(self, episode, timeout=30):
        self.calls.append(("seed_episode", episode, timeout))
        return {"status": "seeded", "id": episode["id"]}

    def process_episode_memory(self, episode_id, timeout=90):
        self.calls.append(("process_episode_memory", episode_id, timeout))
        return {"episode_id": episode_id, "status": "done", "memory_ids": ["devmem-1"]}


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


def test_agent_prompt_setup_validates_expected_response():
    client = RecordingSetupClient()

    per_task_setup(
        client,
        {
            "type": "agent_prompt",
            "prompt": "prepare editor",
            "expected_response": "READY",
            "clear_history_after": False,
        },
    )


def test_agent_prompt_setup_rejects_unexpected_response():
    class FailedSetupClient(RecordingSetupClient):
        def chat(self, message, timeout_sec=None):
            return ChatResponse(response="FAILED: capture failed", history=[])

    with pytest.raises(ResetError, match="response mismatch"):
        per_task_setup(
            FailedSetupClient(),
            {
                "type": "agent_prompt",
                "prompt": "prepare editor",
                "expected_response": "READY",
                "clear_history_after": False,
            },
        )


def test_seed_episode_setup_can_leave_episode_unconsolidated():
    client = RecordingSetupClient()
    episode = {"id": "ep-1", "user_goal": "verify a device procedure"}

    per_task_setup(
        client,
        {"type": "seed_episode", "episode": episode, "consolidate": False, "timeout_sec": 45},
    )

    assert client.calls == [("seed_episode", episode, 45)]


def test_seed_episode_setup_consolidates_and_requires_memory():
    client = RecordingSetupClient()
    episode = {"id": "ep-1", "user_goal": "verify a device procedure"}

    per_task_setup(
        client,
        {"type": "seed_episode", "episode": episode, "consolidate": True, "timeout_sec": 45},
    )

    assert client.calls == [
        ("seed_episode", episode, 45),
        ("process_episode_memory", "ep-1", 45),
    ]


def test_seed_episode_setup_returns_consolidation_artifact_and_accepts_empty_expected_result():
    class UnknownClient(RecordingSetupClient):
        def process_episode_memory(self, episode_id, timeout=90):
            return {
                "episode_id": episode_id,
                "status": "done",
                "assessment": {"goal_result": "unknown", "reason": "No proof", "evidence_refs": []},
                "memory_ids": [],
            }

    result = per_task_setup(
        UnknownClient(),
        {
            "type": "seed_episode",
            "episode": {"id": "ep-unknown", "user_goal": "verify a page"},
            "consolidate": True,
            "consolidation_expectation": {
                "goal_result": "unknown",
                "allow_empty_memory": True,
            },
        },
    )

    assert result["consolidation"]["assessment"]["goal_result"] == "unknown"
    assert result["consolidation"]["memory_ids"] == []


def test_seed_episode_setup_rejects_consolidation_without_memory():
    class NoMemoryClient(RecordingSetupClient):
        def process_episode_memory(self, episode_id, timeout=90):
            return {"episode_id": episode_id, "status": "done", "memory_ids": []}

    with pytest.raises(ResetError, match="produced no device memory"):
        per_task_setup(
            NoMemoryClient(),
            {
                "type": "seed_episode",
                "episode": {"id": "ep-1", "user_goal": "verify a device procedure"},
                "consolidate": True,
            },
        )


def test_seed_episode_setup_reports_ignored_status():
    class IgnoredClient(RecordingSetupClient):
        def process_episode_memory(self, episode_id, timeout=90):
            return {"episode_id": episode_id, "status": "ignored", "memory_ids": []}

    with pytest.raises(ResetError, match="was ignored by the worker"):
        per_task_setup(
            IgnoredClient(),
            {
                "type": "seed_episode",
                "episode": {"id": "ep-1", "user_goal": "verify a device procedure"},
                "consolidate": True,
            },
        )


def test_per_task_setup_rejects_unknown_keys():
    with pytest.raises(ResetError, match="unsupported seed_episode setup keys: consolodate"):
        per_task_setup(
            RecordingSetupClient(),
            {
                "type": "seed_episode",
                "episode": {"id": "ep-1", "user_goal": "verify a device procedure"},
                "consolodate": True,
            },
        )


@pytest.mark.parametrize("setup_type", [["seed_episode"], {"name": "seed_episode"}, 1, None])
def test_per_task_setup_rejects_non_string_setup_type(setup_type):
    with pytest.raises(ResetError, match="setup type must be a string"):
        per_task_setup(RecordingSetupClient(), {"type": setup_type})


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

    result = call_environment_setup(
        "http://127.0.0.1:9090",
        timeout=12,
        app_ids=["settings"],
    )

    assert seen == {
        "url": "http://127.0.0.1:9090/api/setup",
        "method": "POST",
        "body": b'{"app_ids": ["settings"]}',
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

    assert clear_stale_adb_android_owner("http://127.0.0.1:9090") == "suite:old"
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
