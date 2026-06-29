"""Tests for unified /api/tools endpoint."""

import json
from threading import Thread
from urllib.request import Request, urlopen

import pytest

from .episode import BridgeEpisodeState, BridgeTaskRouter
from .actions import action_to_dict
from .server import BridgeServer


@pytest.fixture
def mock_env():
    """Mock MobileGym environment."""
    return mock_env_factory()


def mock_env_factory():
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
    loop.close()


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
    assert "enter_text_in_field" in tools
    assert "enter_text_via_bridge" in tools
    assert "mouse_click" in tools
    assert "mouse_move" in tools
    assert "mouse_scroll" in tools
    assert "quick_action" in tools

    # Verify tool structure
    screenshot_tool = tools["screenshot"]
    assert "description" in screenshot_tool
    assert "args_schema" in screenshot_tool
    assert screenshot_tool["args_schema"]["additionalProperties"] is False
    assert tools["touch_gesture"]["args_schema"]["additionalProperties"] is False
    touch_props = tools["touch_gesture"]["args_schema"]["properties"]
    assert touch_props["point"]["additionalProperties"] is False
    assert touch_props["point"]["required"] == ["x", "y"]
    assert touch_props["coord_space"]["enum"] == ["auto", "pixel", "normalized", "absolute"]
    assert touch_props["button"]["enum"] == ["left", "right", "middle"]
    assert touch_props["strength"]["enum"] == ["large", "medium", "small", "tiny"]
    assert "hold_before_ms" in touch_props
    assert "hold_after_ms" in touch_props
    assert "hold_ms" in touch_props
    assert "pause_ms" in touch_props
    assert "steps" in touch_props

    keyboard_tap_props = tools["keyboard_tap"]["args_schema"]["properties"]
    assert tools["keyboard_text"]["args_schema"]["additionalProperties"] is False
    assert tools["keyboard_tap"]["args_schema"]["additionalProperties"] is False
    assert "hold_ms" in keyboard_tap_props

    enter_text_props = tools["enter_text_in_field"]["args_schema"]["properties"]
    assert tools["enter_text_in_field"]["args_schema"]["additionalProperties"] is False
    assert tools["enter_text_via_bridge"]["args_schema"]["additionalProperties"] is False
    assert enter_text_props["platform"]["enum"] == ["ios", "android", "mac"]
    assert enter_text_props["mode"]["enum"] == ["form", "search"]
    assert enter_text_props["focus"]["additionalProperties"] is False
    assert enter_text_props["focus"]["properties"]["coord_space"]["enum"] == ["auto", "normalized", "absolute"]

    mouse_click_props = tools["mouse_click"]["args_schema"]["properties"]
    assert tools["mouse_click"]["args_schema"]["additionalProperties"] is False
    assert tools["mouse_move"]["args_schema"]["additionalProperties"] is False
    assert tools["mouse_scroll"]["args_schema"]["additionalProperties"] is False
    assert mouse_click_props["button"]["enum"] == ["left", "right", "middle"]
    assert mouse_click_props["coord_space"]["enum"] == ["auto", "pixel", "normalized", "absolute"]
    assert tools["mouse_move"]["args_schema"]["properties"]["coord_space"]["enum"] == [
        "auto",
        "pixel",
        "normalized",
        "absolute",
    ]
    assert tools["mouse_scroll"]["args_schema"]["properties"]["delta"]["minimum"] == -127
    assert tools["mouse_scroll"]["args_schema"]["properties"]["delta"]["maximum"] == 127

    quick_action_props = tools["quick_action"]["args_schema"]["properties"]
    assert tools["quick_action"]["args_schema"]["additionalProperties"] is False
    assert quick_action_props["platform"]["enum"] == ["ios", "android", "mac"]
    assert "alternative" in quick_action_props
    assert "alternative_index" in quick_action_props


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
    assert action_to_dict(state.env.last_action) == {
        "action_type": "CLICK",
        "data": {"point": [500.0, 800.0]},
    }


def test_invoke_touch_gesture_tap_accepts_top_level_string_coordinates(bridge_server):
    """Regression test for Go environment bridge payloads that include top-level x/y."""
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-002b"

    request_body = json.dumps({"input": {"type": "tap", "coord_space": "normalized", "x": "135", "y": "705"}}).encode()
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
    assert action_to_dict(state.env.last_action) == {
        "action_type": "CLICK",
        "data": {"point": [135.0, 705.0]},
    }


def test_invoke_touch_gesture_tap_accepts_list_point(bridge_server):
    """Regression test for agent traces that emit point as [x, y]."""
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-002c"

    request_body = json.dumps({"input": {"type": "tap", "point": [498, 828]}}).encode()
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
    assert action_to_dict(state.env.last_action) == {
        "action_type": "CLICK",
        "data": {"point": [498.0, 828.0]},
    }


def test_invoke_mouse_click_maps_to_tap(bridge_server):
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-mouse"

    request_body = json.dumps({"input": {"x": 321, "y": 654, "button": "left"}}).encode()
    req = Request(
        f"{base_url}/api/tools/mouse_click",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert data["is_error"] is False
    assert action_to_dict(state.env.last_action) == {
        "action_type": "CLICK",
        "data": {"point": [321.0, 654.0]},
    }


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
    assert action_to_dict(state.env.last_action) == {
        "action_type": "TYPE",
        "data": {"value": "hello world"},
    }


def test_invoke_enter_text_in_field_maps_to_mobilegym_type_action(bridge_server):
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-enter-text"

    request_body = json.dumps(
        {
            "input": {
                "text": "微信读书",
                "platform": "android",
                "mode": "search",
                "focus": {"x": 500, "y": 120, "coord_space": "normalized"},
                "segments": ["wei", "xin", "du", "shu"],
            }
        }
    ).encode()
    req = Request(
        f"{base_url}/api/tools/enter_text_in_field",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert data["is_error"] is False
    output = json.loads(data["output"])
    assert output["ok"] is True
    assert output["committed"] is True
    assert output["target_text"] == "微信读书"
    assert output["required_mode"] == "composition"
    assert output["mode"] == "search"
    assert action_to_dict(state.env.last_action) == {
        "action_type": "TYPE",
        "data": {"value": "微信读书", "point": [500.0, 120.0]},
    }


def test_invoke_enter_text_via_bridge_aliases_mobilegym_text_entry(bridge_server):
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-enter-text-bridge"

    request_body = json.dumps(
        {
            "input": {
                "text": "Trip report",
                "platform": "android",
                "focus": {"x": 250, "y": 800, "coord_space": "normalized"},
            }
        }
    ).encode()
    req = Request(
        f"{base_url}/api/tools/enter_text_via_bridge",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert data["is_error"] is False
    output = json.loads(data["output"])
    assert output["ok"] is True
    assert output["committed"] is True
    assert output["target_text"] == "Trip report"
    assert output["required_mode"] == "ascii"
    assert action_to_dict(state.env.last_action) == {
        "action_type": "TYPE",
        "data": {"value": "Trip report", "point": [250.0, 800.0]},
    }


def test_invoke_enter_text_preserves_exact_whitespace(bridge_server):
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-enter-text-whitespace"

    req = Request(
        f"{base_url}/api/tools/enter_text_in_field",
        data=json.dumps({"input": {"text": "  padded  ", "focus": {"x": 500, "y": 120}}}).encode(),
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert data["is_error"] is False
    output = json.loads(data["output"])
    assert output["target_text"] == "  padded  "
    assert action_to_dict(state.env.last_action) == {
        "action_type": "TYPE",
        "data": {"value": "  padded  ", "point": [500.0, 120.0]},
    }


def test_invoke_quick_action_handles_mobilegym_common_actions(bridge_server):
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-quick-actions"

    for action, expected_type in [
        ("spotlight_search", "SWIPE"),
        ("search", "SWIPE"),
        ("search_launch_app", "SWIPE"),
        ("app_switch", "SWIPE"),
        ("send", "ENTER"),
    ]:
        req = Request(
            f"{base_url}/api/tools/quick_action",
            data=json.dumps({"input": {"action": action, "platform": "android"}}).encode(),
            method="POST",
            headers={"Content-Type": "application/json"},
        )

        with urlopen(req, timeout=5) as resp:
            assert resp.status == 200
            data = json.loads(resp.read().decode())

        assert data["is_error"] is False
        assert "unsupported quick_action" not in data["output"]
        assert action_to_dict(state.env.last_action)["action_type"] == expected_type

    no_action_count = state.env.step_count
    for action in ["select_all", "delete_backward", "copy", "paste", "undo", "find", "cut", "browser_refresh", "browser_new_tab"]:
        req = Request(
            f"{base_url}/api/tools/quick_action",
            data=json.dumps({"input": {"action": action, "platform": "android"}}).encode(),
            method="POST",
            headers={"Content-Type": "application/json"},
        )

        with urlopen(req, timeout=5) as resp:
            assert resp.status == 200
            data = json.loads(resp.read().decode())

        assert data["is_error"] is False
        output = json.loads(data["output"])
        assert output["ok"] is False
        assert output["status"] == "reserved"
        assert "unsupported quick_action" not in data["output"]
        assert state.env.step_count == no_action_count


def test_invoke_keyboard_text_accepts_plain_text_fallback(bridge_server):
    """Matches the Go keyboard_text fallback for bare plain text input."""
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-003b"

    request_body = json.dumps({"raw_input": "plain text"}).encode()
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
    assert action_to_dict(state.env.last_action) == {
        "action_type": "TYPE",
        "data": {"value": "plain text"},
    }


def test_invoke_rejects_non_object_json_body(bridge_server):
    from urllib.error import HTTPError

    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-bad-body"

    req = Request(
        f"{base_url}/api/tools/screenshot",
        data=json.dumps([]).encode(),
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with pytest.raises(HTTPError) as exc_info:
        urlopen(req, timeout=5)
    assert exc_info.value.code == 400
    data = json.loads(exc_info.value.read().decode())
    assert data["is_error"] is True
    assert data["error"] == "bad_json"


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
        with urlopen(req, timeout=5):
            pytest.fail("Expected HTTPError 409")
    except HTTPError as e:
        assert e.code == 409
        data = json.loads(e.read().decode())
        assert data["is_error"] is True
        assert "no active episode" in data["output"].lower()


def test_invoke_stale_episode_returns_conflict(bridge_server, monkeypatch):
    from urllib.error import HTTPError

    from .episode import StaleEpisodeError

    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-stale"

    def fail_require_active(episode_id):
        raise StaleEpisodeError("stale episode_id")

    monkeypatch.setattr(state, "require_active", fail_require_active)
    request_body = json.dumps({"input": "{}"}).encode()
    req = Request(
        f"{base_url}/api/tools/screenshot",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with pytest.raises(HTTPError) as exc_info:
        urlopen(req, timeout=5)
    assert exc_info.value.code == 409
    data = json.loads(exc_info.value.read().decode())
    assert data["error"] == "stale_episode"


def test_touch_gesture_rejects_non_string_type(bridge_server):
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-bad-gesture"

    request_body = json.dumps({"input": {"type": {"kind": "tap"}, "point": {"x": 500, "y": 800}}}).encode()
    req = Request(
        f"{base_url}/api/tools/touch_gesture",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())
    assert data["is_error"] is True
    assert "type must be a string" in data["output"]


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


def test_multi_env_tools_require_benchmark_task_id_header():
    import asyncio
    from urllib.error import HTTPError

    loop = asyncio.new_event_loop()
    thread = Thread(target=loop.run_forever, daemon=True)
    thread.start()

    envs = [mock_env_factory(), mock_env_factory()]
    states = [BridgeEpisodeState(env, loop) for env in envs]
    for index, state in enumerate(states):
        state.active_episode_id = f"ep-{index}"
    server = BridgeServer(BridgeTaskRouter(states), host="127.0.0.1", port=0)
    base_url = server.start()
    try:
        request_body = json.dumps({"input": "{}"}).encode()
        req = Request(
            f"{base_url}/api/tools/screenshot",
            data=request_body,
            method="POST",
            headers={"Content-Type": "application/json"},
        )
        with pytest.raises(HTTPError) as exc_info:
            urlopen(req, timeout=5)
        assert exc_info.value.code == 400
        data = json.loads(exc_info.value.read().decode())
        assert data["error"] == "missing_benchmark_task_id"
    finally:
        server.stop()
        loop.call_soon_threadsafe(loop.stop)
        thread.join(timeout=2)
        loop.close()


def test_reset_episode_restarts_env_before_reset_when_supported():
    import asyncio

    class RestartableEnv:
        def __init__(self):
            self.calls = []
            self.restarted = False

        async def close(self):
            self.calls.append("close")

        async def start(self):
            self.calls.append("start")
            self.restarted = True
            return self

        async def reset(self):
            self.calls.append("reset")
            if not self.restarted:
                raise RuntimeError("second reset would hang without page restart")

    async def run():
        env = RestartableEnv()
        state = BridgeEpisodeState(env, asyncio.get_running_loop())

        result = await state.reset_episode("episode-1")

        assert result == {"episode_id": "episode-1", "reset": True}
        assert env.calls == ["close", "start", "reset"]

    asyncio.run(run())


def test_reset_episode_retries_after_reset_timeout(monkeypatch):
    import asyncio
    from . import episode as episode_mod

    monkeypatch.setattr(episode_mod, "EPISODE_RESET_TIMEOUT_SEC", 0.01)

    class TimeoutThenSuccessEnv:
        def __init__(self):
            self.calls = []
            self.reset_calls = 0

        async def close(self):
            self.calls.append("close")

        async def start(self):
            self.calls.append("start")
            return self

        async def reset(self):
            self.calls.append("reset")
            self.reset_calls += 1
            if self.reset_calls == 1:
                await asyncio.sleep(10)

    async def run():
        env = TimeoutThenSuccessEnv()
        state = BridgeEpisodeState(env, asyncio.get_running_loop())

        result = await state.reset_episode("episode-1")

        assert result == {"episode_id": "episode-1", "reset": True}
        assert env.calls == ["close", "start", "reset", "close", "start", "reset"]

    asyncio.run(run())


def test_multi_env_tools_route_by_benchmark_task_id_header():
    import asyncio

    loop = asyncio.new_event_loop()
    thread = Thread(target=loop.run_forever, daemon=True)
    thread.start()

    envs = [mock_env_factory(), mock_env_factory()]
    states = [BridgeEpisodeState(env, loop) for env in envs]
    for index, state in enumerate(states):
        state.active_episode_id = f"ep-{index}"
    server = BridgeServer(BridgeTaskRouter(states), host="127.0.0.1", port=0)
    base_url = server.start()
    try:
        for task_id, point in [
            ("task.alpha", {"x": 111, "y": 222}),
            ("task.beta", {"x": 333, "y": 444}),
        ]:
            request_body = json.dumps({"input": {"x": point["x"], "y": point["y"]}}).encode()
            req = Request(
                f"{base_url}/api/tools/mouse_click",
                data=request_body,
                method="POST",
                headers={"Content-Type": "application/json", "benchmark-task-id": task_id},
            )
            with urlopen(req, timeout=5) as resp:
                assert resp.status == 200
                data = json.loads(resp.read().decode())
                assert data["is_error"] is False

        assert action_to_dict(envs[0].last_action) == {
            "action_type": "CLICK",
            "data": {"point": [111.0, 222.0]},
        }
        assert action_to_dict(envs[1].last_action) == {
            "action_type": "CLICK",
            "data": {"point": [333.0, 444.0]},
        }
    finally:
        server.stop()
        loop.call_soon_threadsafe(loop.stop)
        thread.join(timeout=2)
        loop.close()


def test_multi_env_tools_return_capacity_error_until_task_released():
    import asyncio
    from urllib.error import HTTPError

    loop = asyncio.new_event_loop()
    thread = Thread(target=loop.run_forever, daemon=True)
    thread.start()

    envs = [mock_env_factory()]
    states = [BridgeEpisodeState(envs[0], loop)]
    states[0].active_episode_id = "ep-0"
    server = BridgeServer(BridgeTaskRouter(states), host="127.0.0.1", port=0)
    base_url = server.start()
    try:
        for task_id in ("task.alpha", "task.beta"):
            request_body = json.dumps({"input": "{}"}).encode()
            req = Request(
                f"{base_url}/api/tools/screenshot",
                data=request_body,
                method="POST",
                headers={"Content-Type": "application/json", "benchmark-task-id": task_id},
            )
            if task_id == "task.alpha":
                with urlopen(req, timeout=5) as resp:
                    assert resp.status == 200
            else:
                with pytest.raises(HTTPError) as exc_info:
                    urlopen(req, timeout=5)
                assert exc_info.value.code == 429
                data = json.loads(exc_info.value.read().decode())
                assert data["error"] == "no_bridge_env_available"

        release_req = Request(
            f"{base_url}/api/release",
            data=b"{}",
            method="POST",
            headers={"Content-Type": "application/json", "benchmark-task-id": "task.alpha"},
        )
        with urlopen(release_req, timeout=5) as resp:
            assert resp.status == 200

        request_body = json.dumps({"input": "{}"}).encode()
        req = Request(
            f"{base_url}/api/tools/screenshot",
            data=request_body,
            method="POST",
            headers={"Content-Type": "application/json", "benchmark-task-id": "task.beta"},
        )
        with urlopen(req, timeout=5) as resp:
            assert resp.status == 200
    finally:
        server.stop()
        loop.call_soon_threadsafe(loop.stop)
        thread.join(timeout=2)
        loop.close()
