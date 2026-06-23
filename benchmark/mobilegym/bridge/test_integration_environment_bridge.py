"""Integration test: Go agent environment bridge mode -> MobileGym Bridge Server."""

import asyncio
import json
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path
from threading import Thread

import pytest

from mobilegym.bridge.episode import BridgeEpisodeState
from mobilegym.bridge.server import BridgeServer


@pytest.fixture
def mock_env():
    """Mock MobileGym environment."""

    class MockEnv:
        def __init__(self):
            self.actions = []

        async def get_observation(self):
            return MockObservation()

        async def step(self, action):
            self.actions.append(action)
            return MockStepResult()

    class MockObservation:
        def __init__(self):
            self.screenshot_bytes = b"\xff\xd8\xff\xe0" + b"\x00" * 100
            self.width = 1080
            self.height = 2400
            self.mime_type = "image/jpeg"

    class MockStepResult:
        def __init__(self):
            self.observation = MockObservation()

    return MockEnv()


@pytest.fixture
def bridge_server(mock_env):
    """Start Bridge Server."""
    loop = asyncio.new_event_loop()
    thread = Thread(target=loop.run_forever, daemon=True)
    thread.start()

    state = BridgeEpisodeState(mock_env, loop)
    server = BridgeServer(state, host="127.0.0.1", port=0)
    base_url = server.start()

    # Start episode
    state.active_episode_id = "integration-test"

    yield {
        "base_url": base_url,
        "state": state,
        "env": mock_env,
    }

    server.stop()
    loop.call_soon_threadsafe(loop.stop)
    thread.join(timeout=2)


def test_go_agent_environment_bridge_client(bridge_server):
    """Test that Go agent's EnvironmentBridgeClient can call Bridge Server."""
    # This test simulates what Go agent's EnvironmentBridgeClient does.
    import urllib.request

    base_url = bridge_server["base_url"]

    # 1. Get tool catalog (what Go agent does during initialization)
    req = urllib.request.Request(f"{base_url}/api/tools", method="GET")
    with urllib.request.urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        catalog = json.loads(resp.read().decode())

    assert "tools" in catalog
    tool_names = {t["name"] for t in catalog["tools"]}
    assert "screenshot" in tool_names
    assert "touch_gesture" in tool_names

    # 2. Call screenshot tool (simulating Go agent tool invocation)
    request_body = json.dumps({"input": "{}"}).encode()
    req = urllib.request.Request(
        f"{base_url}/api/tools/screenshot",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urllib.request.urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        result = json.loads(resp.read().decode())

    # Verify response format matches Go agent expectations
    assert "output" in result
    assert "is_error" in result
    assert result["is_error"] is False
    assert "duration_ms" in result
    assert "raw_input" in result

    # Parse output
    output = json.loads(result["output"])
    assert "data" in output
    assert "width" in output
    assert "height" in output

    # 3. Call touch_gesture tool
    request_body = json.dumps({"input": {"type": "tap", "point": {"x": 500, "y": 800}}}).encode()
    req = urllib.request.Request(
        f"{base_url}/api/tools/touch_gesture",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urllib.request.urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        result = json.loads(resp.read().decode())

    assert result["is_error"] is False
    output = json.loads(result["output"])
    assert "action_output" in output
    assert "data" in output  # Screenshot included


def test_bridge_server_compatible_with_hardware_board_api(bridge_server):
    """Verify Bridge Server API matches hardware board Go agent API."""
    # This test verifies that the response format from Bridge Server
    # matches what a hardware board Go agent would return via /api/tools

    base_url = bridge_server["base_url"]

    # Test screenshot tool
    request_body = json.dumps({"input": "{}"}).encode()
    req = urllib.request.Request(
        f"{base_url}/api/tools/screenshot",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urllib.request.urlopen(req, timeout=5) as resp:
        result = json.loads(resp.read().decode())

    # Verify all required fields exist (matching Go ToolInvokeResponse)
    required_fields = {"tool", "raw_input", "output", "is_error", "duration_ms"}
    assert required_fields.issubset(result.keys())

    # Verify tool descriptor format
    assert "name" in result["tool"]
    assert result["tool"]["name"] == "screenshot"

    # Verify output is a JSON string (as Go agent returns)
    assert isinstance(result["output"], str)
    output_obj = json.loads(result["output"])
    assert isinstance(output_obj, dict)


def test_multiple_tools_in_sequence(bridge_server):
    """Test calling multiple tools in sequence like a real benchmark run."""
    base_url = bridge_server["base_url"]
    env = bridge_server["env"]

    tools_to_test = [
        ("screenshot", "{}"),
        ("touch_gesture", '{"type": "tap", "point": {"x": 500, "y": 800}}'),
        ("keyboard_text", '{"text": "hello"}'),
        ("touch_gesture", '{"type": "swipe", "start": {"x": 500, "y": 1000}, "end": {"x": 500, "y": 500}}'),
        ("screenshot", "{}"),
    ]

    for tool_name, tool_input in tools_to_test:
        request_body = json.dumps({"input": tool_input}).encode()
        req = urllib.request.Request(
            f"{base_url}/api/tools/{tool_name}",
            data=request_body,
            method="POST",
            headers={"Content-Type": "application/json"},
        )

        with urllib.request.urlopen(req, timeout=5) as resp:
            assert resp.status == 200
            result = json.loads(resp.read().decode())
            assert result["is_error"] is False, f"Tool {tool_name} failed: {result}"

    # Verify env received actions
    assert len(env.actions) >= 3  # At least tap, text, swipe


def test_input_format_compatibility(bridge_server):
    """Test various input formats that Go agent might send."""
    base_url = bridge_server["base_url"]

    # Format 1: input as JSON object
    request_body = json.dumps({"input": {"text": "test1"}}).encode()
    req = urllib.request.Request(
        f"{base_url}/api/tools/keyboard_text",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        result = json.loads(resp.read().decode())
        assert result["is_error"] is False

    # Format 2: raw_input as JSON string
    request_body = json.dumps({"raw_input": '{"text": "test2"}'}).encode()
    req = urllib.request.Request(
        f"{base_url}/api/tools/keyboard_text",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        result = json.loads(resp.read().decode())
        assert result["is_error"] is False

    # Format 3: input as JSON string
    request_body = json.dumps({"input": '{"text": "test3"}'}).encode()
    req = urllib.request.Request(
        f"{base_url}/api/tools/keyboard_text",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        result = json.loads(resp.read().decode())
        assert result["is_error"] is False
