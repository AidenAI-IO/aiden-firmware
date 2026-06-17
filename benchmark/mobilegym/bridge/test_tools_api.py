"""Tests for unified /api/tools endpoint."""

import json
from http.server import HTTPServer
from pathlib import Path
from threading import Thread
from urllib.request import Request, urlopen

import pytest

from .episode import BridgeEpisodeState
from .server import BridgeServer


@pytest.fixture
def mock_env():
    """Mock MobileGym environment."""

    class MockEnv:
        def __init__(self):
            self.last_action = None
            self.step_count = 0

        async def get_observation(self):
            # Return mock screenshot
            return MockObservation()

        async def step(self, action):
            self.last_action = action
            self.step_count += 1
            return MockStepResult()

    class MockObservation:
        def __init__(self):
            self.screenshot_bytes = b"\xff\xd8\xff\xe0" + b"\x00" * 100  # Mock JPEG
            self.width = 1080
            self.height = 2400
            self.mime_type = "image/jpeg"

    class MockStepResult:
        def __init__(self):
            self.observation = MockObservation()

    return MockEnv()


@pytest.fixture
def bridge_server(mock_env):
    """Create and start bridge server."""
    import asyncio

    loop = asyncio.new_event_loop()
    thread = Thread(target=loop.run_forever, daemon=True)
    thread.start()

    state = BridgeEpisodeState(mock_env, loop)
    server = BridgeServer(state, host="127.0.0.1", port=0)
    base_url = server.start()

    yield server, base_url, state

    server.stop()
    loop.call_soon_threadsafe(loop.stop)
    thread.join(timeout=2)


def test_get_tools_catalog(bridge_server):
    """Test GET /api/tools returns tool catalog."""
    server, base_url, state = bridge_server

    req = Request(f"{base_url}/api/tools", method="GET")
    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert "tools" in data
    tools = {tool["name"]: tool for tool in data["tools"]}
    assert "screenshot" in tools
    assert "touch_gesture" in tools
    assert "keyboard_text" in tools
    assert "keyboard_tap" in tools

    # Verify tool structure
    screenshot_tool = tools["screenshot"]
    assert "description" in screenshot_tool
    assert "args_schema" in screenshot_tool


def test_invoke_screenshot_tool(bridge_server):
    """Test POST /api/tools/screenshot."""
    server, base_url, state = bridge_server

    # Start episode first
    state.active_episode_id = "test-episode-001"

    request_body = json.dumps({"input": "{}"}).encode()
    req = Request(
        f"{base_url}/api/tools/screenshot",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert "output" in data
    assert "is_error" in data
    assert data["is_error"] is False
    assert "duration_ms" in data

    # Parse screenshot output
    output = json.loads(data["output"])
    assert "data" in output
    assert "width" in output
    assert "height" in output
    assert output["width"] == 1080
    assert output["height"] == 2400


def test_invoke_touch_gesture_tap(bridge_server):
    """Test POST /api/tools/touch_gesture with tap."""
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-002"

    request_body = json.dumps({"input": {"type": "tap", "point": {"x": 500, "y": 800}}}).encode()
    req = Request(
        f"{base_url}/api/tools/touch_gesture",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert data["is_error"] is False
    output = json.loads(data["output"])
    assert "action_output" in output
    assert "data" in output  # Screenshot included


def test_invoke_keyboard_text(bridge_server):
    """Test POST /api/tools/keyboard_text."""
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-003"

    request_body = json.dumps({"input": {"text": "hello world"}}).encode()
    req = Request(
        f"{base_url}/api/tools/keyboard_text",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert data["is_error"] is False


def test_invoke_without_episode_returns_error(bridge_server):
    """Test tool invocation without active episode fails."""
    from urllib.error import HTTPError

    server, base_url, state = bridge_server

    request_body = json.dumps({"input": "{}"}).encode()
    req = Request(
        f"{base_url}/api/tools/screenshot",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    try:
        with urlopen(req, timeout=5) as resp:
            pytest.fail("Expected HTTPError 409")
    except HTTPError as e:
        assert e.code == 409
        data = json.loads(e.read().decode())
        assert data["is_error"] is True
        assert "no active episode" in data["output"].lower()


def test_invoke_without_token_still_works(bridge_server):
    """Test tool invocation without token still works (auth removed)."""
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-004"

    request_body = json.dumps({"input": "{}"}).encode()
    req = Request(
        f"{base_url}/api/tools/screenshot",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())
        assert data["is_error"] is False


def test_invoke_unknown_tool_returns_error(bridge_server):
    """Test invoking unknown tool returns error."""
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-005"

    request_body = json.dumps({"input": "{}"}).encode()
    req = Request(
        f"{base_url}/api/tools/unknown_tool",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert data["is_error"] is True
    assert "unknown tool" in data["output"].lower()


def test_decode_tool_input_formats(bridge_server):
    """Test various input formats are decoded correctly."""
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-006"

    # Test raw_input format
    request_body = json.dumps({"raw_input": '{"text": "test"}'}).encode()
    req = Request(
        f"{base_url}/api/tools/keyboard_text",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())
    assert data["is_error"] is False
    assert data["raw_input"] == '{"text": "test"}'

    # Test input as object
    request_body = json.dumps({"input": {"text": "test2"}}).encode()
    req = Request(
        f"{base_url}/api/tools/keyboard_text",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())
    assert data["is_error"] is False

    # Test input as JSON string
    request_body = json.dumps({"input": '{"text": "test3"}'}).encode()
    req = Request(
        f"{base_url}/api/tools/keyboard_text",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())
    assert data["is_error"] is False
