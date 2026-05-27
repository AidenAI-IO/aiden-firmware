import httpx
import pytest
from runner.agent_client import AgentClient, AgentTimeoutError

def make_client(handler):
    transport = httpx.MockTransport(handler)
    return AgentClient(base_url="http://test", transport=transport)

def test_clear_history_posts_correct_path():
    seen = {}
    def handler(req: httpx.Request) -> httpx.Response:
        seen["url"] = str(req.url)
        seen["method"] = req.method
        return httpx.Response(200, json={"status": "ok"})
    client = make_client(handler)
    client.clear_history()
    assert seen["method"] == "POST"
    assert seen["url"].endswith("/api/clear")

def test_chat_returns_response_and_history():
    history = [{"type": "assistant", "content": "done"}]
    def handler(req: httpx.Request) -> httpx.Response:
        assert req.method == "POST"
        assert req.url.path == "/api/chat"
        body = req.read().decode()
        assert "请打开" in body
        return httpx.Response(200, json={"response": "ok", "history": history})
    client = make_client(handler)
    resp = client.chat("请打开设置")
    assert resp.response == "ok"
    assert resp.history == history

def test_chat_timeout_raises():
    def handler(req: httpx.Request) -> httpx.Response:
        raise httpx.ReadTimeout("timeout", request=req)
    client = make_client(handler)
    with pytest.raises(AgentTimeoutError):
        client.chat("hi", timeout_sec=1)

def test_invoke_tool_returns_output():
    def handler(req: httpx.Request) -> httpx.Response:
        assert req.url.path == "/api/tools/keyboard_tap"
        body = req.read().decode()
        assert "escape" in body
        return httpx.Response(200, json={"output": "{}", "is_error": False, "duration_ms": 12})
    client = make_client(handler)
    out = client.invoke_tool("keyboard_tap", {"keys": ["escape"]})
    assert out.is_error is False
    assert out.duration_ms == 12

def test_health_returns_true_when_tools_endpoint_ok():
    def handler(req: httpx.Request) -> httpx.Response:
        assert req.url.path == "/api/tools"
        assert req.method == "GET"
        return httpx.Response(200, json={"tools": []})
    client = make_client(handler)
    assert client.health() is True
