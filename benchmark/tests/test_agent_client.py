import json
import socket
import time
from unittest.mock import patch

import pytest

from runner.agent_client import AgentClient, AgentTimeoutError


class FakeResponse:
    def __init__(self, status: int, body: dict):
        self.status = status
        self._body = json.dumps(body).encode("utf-8")

    def read(self):
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


def _captured(seen, status=200, body=None):
    """Return a urlopen replacement that records the request and returns body."""
    def fake_urlopen(req, timeout=None):
        seen["url"] = req.full_url
        seen["method"] = req.get_method()
        seen["headers"] = {k.lower(): v for k, v in req.header_items()}
        try:
            seen["body"] = req.data.decode("utf-8") if req.data else ""
        except Exception:
            seen["body"] = ""
        seen["timeout"] = timeout
        return FakeResponse(status, body or {})
    return fake_urlopen


def test_clear_history_posts_correct_path():
    seen = {}
    client = AgentClient(base_url="http://test")
    with patch("urllib.request.urlopen", _captured(seen, body={"status": "ok"})):
        client.clear_history()
    assert seen["method"] == "POST"
    assert seen["url"].endswith("/api/clear")


def test_chat_returns_response_and_history():
    history = [{"type": "assistant", "content": "done"}]
    seen = {}
    client = AgentClient(base_url="http://test")
    with patch("urllib.request.urlopen",
               _captured(seen, body={"response": "ok", "history": history})):
        resp = client.chat("请打开设置")
    assert seen["method"] == "POST"
    assert seen["url"].endswith("/api/chat")
    assert "请打开" in seen["body"]
    assert resp.response == "ok"
    assert resp.history == history


def test_chat_long_polls_async_result_until_complete():
    history = [
        {"type": "user", "content": "请打开设置"},
        {"type": "assistant", "content": "done"},
    ]
    seen = []
    responses = [
        FakeResponse(200, {"request_id": "req-1"}),
        FakeResponse(200, {"status": "complete", "response": "done", "history": history}),
    ]

    def fake_urlopen(req, timeout=None):
        seen.append((req.full_url, req.get_method(), timeout))
        return responses.pop(0)

    client = AgentClient(base_url="http://test")
    with patch("urllib.request.urlopen", fake_urlopen):
        resp = client.chat("请打开设置", timeout_sec=30)

    assert resp.response == "done"
    assert resp.history == history
    assert seen[0][0].endswith("/api/chat")
    assert seen[1][0].endswith("/api/chat/result?request_id=req-1&wait=true")
    assert seen[1][2] == 30


def test_get_history_returns_current_history():
    history = [{"type": "tool_call", "tool_name": "screenshot"}]
    seen = {}
    client = AgentClient(base_url="http://test")
    with patch("urllib.request.urlopen", _captured(seen, body=history)):
        result = client.get_history()
    assert seen["method"] == "GET"
    assert seen["url"].endswith("/api/history")
    assert result == history


def test_device_type_reads_runtime_phone_bridge_status():
    seen = {}
    client = AgentClient(base_url="http://test")
    with patch(
        "urllib.request.urlopen",
        _captured(seen, body={"device_type": "Android"}),
    ):
        result = client.device_type()

    assert seen["method"] == "GET"
    assert seen["url"].endswith("/api/phone-bridge/status")
    assert result == "Android"


def test_chat_includes_skills_when_provided():
    seen = {}
    client = AgentClient(base_url="http://test")
    with patch("urllib.request.urlopen",
               _captured(seen, body={"response": "ok", "history": []})):
        client.chat("请打开设置", skills=["device-operator"])
    body = json.loads(seen["body"])
    assert body["skills"] == ["device-operator"]


def test_recover_after_timeout_waits_until_clear_succeeds(monkeypatch):
    sleeps = []
    monkeypatch.setattr(time, "sleep", lambda seconds: sleeps.append(seconds))

    class RecoverClient:
        def __init__(self):
            self.attempts = 0

        def health(self) -> bool:
            return True

        def clear_history(self, timeout: int = 30) -> None:
            self.attempts += 1
            if self.attempts < 2:
                raise AgentTimeoutError("busy")

    client = RecoverClient()
    assert AgentClient.recover_after_timeout(client, timeout_sec=10, poll_sec=1) is True
    assert client.attempts == 2
    assert sleeps == [1]


def test_chat_timeout_raises():
    def fake_urlopen(req, timeout=None):
        raise socket.timeout("read timed out")
    client = AgentClient(base_url="http://test")
    with patch("urllib.request.urlopen", fake_urlopen), \
         pytest.raises(AgentTimeoutError):
        client.chat("hi", timeout_sec=1)


def test_invoke_tool_returns_output():
    seen = {}
    client = AgentClient(base_url="http://test")
    with patch("urllib.request.urlopen",
               _captured(seen, body={"output": "{}", "is_error": False, "duration_ms": 12})):
        out = client.invoke_tool("keyboard_tap", {"keys": ["escape"]})
    assert seen["url"].endswith("/api/tools/keyboard_tap")
    assert "escape" in seen["body"]
    assert seen["timeout"] == 90
    assert out.is_error is False
    assert out.duration_ms == 12


def test_invoke_tool_can_send_benchmark_task_id_header():
    seen = {}
    client = AgentClient(base_url="http://test")
    with patch("urllib.request.urlopen",
               _captured(seen, body={"output": "{}", "is_error": False, "duration_ms": 12})):
        client.invoke_tool("screenshot", {}, benchmark_task_id="suite.json:t1")

    assert seen["headers"]["benchmark-task-id"] == "suite.json:t1"


def test_seed_memory_sends_benchmark_token_header():
    seen = {}
    client = AgentClient(base_url="http://test", benchmark_token="seed-token")
    with patch("urllib.request.urlopen", _captured(seen, body={"status": "seeded", "id": "mem-1"})):
        result = client.seed_memory({"id": "mem-1", "content": "remember this"})

    assert seen["url"].endswith("/api/benchmark/seed_memory")
    assert seen["headers"]["authorization"] == "Bearer seed-token"
    assert result == {"status": "seeded", "id": "mem-1"}


def test_set_phone_bridge_state_sends_benchmark_token_header():
    seen = {}
    client = AgentClient(base_url="http://test", benchmark_token="state-token")
    state = {
        "platform": "ios",
        "app_state": "background",
        "pip_bridge_enabled": True,
    }
    with patch(
        "urllib.request.urlopen",
        _captured(seen, body={"ok": True, "status": state}),
    ):
        result = client.set_phone_bridge_state(state)

    assert seen["url"].endswith("/api/benchmark/phone_bridge_state")
    assert seen["headers"]["authorization"] == "Bearer state-token"
    assert json.loads(seen["body"])["pip_bridge_enabled"] is True
    assert result["ok"] is True


def test_health_returns_true_when_tools_endpoint_ok():
    seen = {}
    client = AgentClient(base_url="http://test")
    with patch("urllib.request.urlopen", _captured(seen, body={"tools": []})):
        assert client.health() is True
    assert seen["url"].endswith("/api/tools")
    assert seen["method"] == "GET"
