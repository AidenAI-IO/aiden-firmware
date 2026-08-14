"""Tests for unified /api/tools endpoint."""

import json
import time
from threading import Event, Thread
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
            self.actions = []
            self.step_count = 0

        async def get_observation(self):
            # Return mock screenshot
            return MockObservation()

        async def step(self, action):
            self.last_action = action
            self.actions.append(action)
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
    assert "screenshot" not in tools
    assert "touch_gesture" in tools
    assert "keyboard_text" in tools
    assert "keyboard_tap" in tools
    assert "enter_text" in tools
    assert "mouse_click" not in tools
    assert "mouse_move" in tools
    assert "mouse_scroll" in tools
    assert "quick_action" in tools

    assert tools["touch_gesture"]["args_schema"]["additionalProperties"] is False
    touch_props = tools["touch_gesture"]["args_schema"]["properties"]
    assert touch_props["point"]["additionalProperties"] is False
    assert touch_props["point"]["required"] == ["x", "y"]
    assert "coord_space" not in touch_props
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

    enter_text_props = tools["enter_text"]["args_schema"]["properties"]
    assert tools["enter_text"]["args_schema"]["additionalProperties"] is False
    assert "focus" in enter_text_props
    assert "send_after_commit" not in enter_text_props
    assert "segments" not in enter_text_props
    assert "max_attempts" not in enter_text_props
    assert "platform" not in enter_text_props
    assert enter_text_props["focus"]["additionalProperties"] is False
    assert "coord_space" not in enter_text_props["focus"]["properties"]

    assert tools["mouse_move"]["args_schema"]["additionalProperties"] is False
    assert tools["mouse_scroll"]["args_schema"]["additionalProperties"] is False
    assert "coord_space" not in tools["mouse_move"]["args_schema"]["properties"]
    assert tools["mouse_scroll"]["args_schema"]["properties"]["delta"]["minimum"] == -127
    assert tools["mouse_scroll"]["args_schema"]["properties"]["delta"]["maximum"] == 127

    quick_action_props = tools["quick_action"]["args_schema"]["properties"]
    assert tools["quick_action"]["args_schema"]["additionalProperties"] is False
    assert "platform" not in quick_action_props
    assert tools["quick_action"]["args_schema"]["anyOf"] == [
        {"required": ["action"]},
        {"required": ["list"], "properties": {"list": {"const": True}}},
    ]
    assert "alternative" in quick_action_props
    assert "alternative_index" in quick_action_props


def test_invoke_provider_screenshot(bridge_server):
    """Test POST /api/providers/screenshot."""
    server, base_url, state = bridge_server

    request_body = json.dumps({"format": "jpeg", "quality": 80}).encode()
    req = Request(
        f"{base_url}/api/providers/screenshot",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert data["ok"] is True
    output = data["data"]
    assert "image" in output
    assert output["meta"]["width"] == 1080
    assert output["meta"]["height"] == 2400


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

    request_body = json.dumps({"input": {"type": "tap", "x": "135", "y": "705"}}).encode()
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


def test_invoke_enter_text_maps_to_mobilegym_type_action(bridge_server):
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-enter-text"

    request_body = json.dumps(
        {
            "input": {
                "text": "微信读书",
                "focus": {"x": 500, "y": 120},
            }
        }
    ).encode()
    req = Request(
        f"{base_url}/api/tools/enter_text",
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
    assert action_to_dict(state.env.last_action) == {
        "action_type": "TYPE",
        "data": {"value": "微信读书", "point": [500.0, 120.0]},
    }


def test_invoke_enter_text_supports_ascii(bridge_server):
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-enter-text-bridge"

    request_body = json.dumps(
        {
            "input": {
                "text": "Trip report",
                "focus": {"x": 250, "y": 800},
            }
        }
    ).encode()
    req = Request(
        f"{base_url}/api/tools/enter_text",
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
    assert action_to_dict(state.env.last_action) == {
        "action_type": "TYPE",
        "data": {"value": "Trip report", "point": [250.0, 800.0]},
    }


def test_invoke_enter_text_preserves_exact_whitespace(bridge_server):
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-enter-text-whitespace"

    req = Request(
        f"{base_url}/api/tools/enter_text",
        data=json.dumps({"input": {"text": "  padded  ", "focus": {"x": 500, "y": 120}}}).encode(),
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert data["is_error"] is False
    output = json.loads(data["output"])
    assert output == {"ok": True}
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
            data=json.dumps({"input": {"action": action}}).encode(),
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
            data=json.dumps({"input": {"action": action}}).encode(),
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


def test_invoke_quick_action_rejects_legacy_platform_argument(bridge_server):
    _server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-legacy-platform"

    req = Request(
        f"{base_url}/api/tools/quick_action",
        data=json.dumps({"input": {"action": "home", "platform": "ios"}}).encode(),
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert data["is_error"] is True
    assert "unknown fields" in data["output"]


def test_invoke_keyboard_tap_keycode_app_switch_uses_swipe(bridge_server):
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-keyboard-app-switch"

    req = Request(
        f"{base_url}/api/tools/keyboard_tap",
        data=json.dumps({"input": {"keys": ["KEYCODE_APP_SWITCH"]}}).encode(),
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert data["is_error"] is False
    assert action_to_dict(state.env.last_action) == {
        "action_type": "SWIPE",
        "data": {"point1": [500.0, 1000.0], "point2": [500.0, 500.0], "duration": 900.0},
    }


@pytest.mark.parametrize(
    ("key_name", "action_type"),
    [("KEYCODE_BACK", "BACK"), ("KEYCODE_HOME", "HOME")],
)
def test_invoke_keyboard_tap_keycode_aliases_preserve_actions(bridge_server, key_name, action_type):
    server, base_url, state = bridge_server
    state.active_episode_id = f"test-episode-keyboard-{key_name.lower()}"

    req = Request(
        f"{base_url}/api/tools/keyboard_tap",
        data=json.dumps({"input": {"keys": [key_name]}}).encode(),
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert data["is_error"] is False
    assert action_to_dict(state.env.last_action) == {"action_type": action_type, "data": {}}


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


def test_invoke_enter_text_focuses_and_types_unicode(bridge_server):
    _server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-enter-text"

    request_body = json.dumps(
        {
            "input": {
                "text": "隐私",
                "focus": {"x": 500, "y": 80},
            }
        }
    ).encode()
    req = Request(
        f"{base_url}/api/tools/enter_text",
        data=request_body,
        method="POST",
        headers={"Content-Type": "application/json"},
    )

    with urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        data = json.loads(resp.read().decode())

    assert data["is_error"] is False
    output = json.loads(data["output"])
    assert output == {"ok": True}
    assert action_to_dict(state.env.last_action) == {
        "action_type": "TYPE",
        "data": {"value": "隐私", "point": [500.0, 80.0]},
    }


def test_invoke_enter_text_rejects_removed_or_missing_arguments(bridge_server):
    _server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-enter-text-invalid"

    for tool_input in (
        {"text": "hello"},
        {"text": "hello", "focus": {"x": 500, "y": 80}, "platform": "ios"},
        {"text": "hello", "focus": {"x": 500, "y": 80, "extra": True}},
    ):
        request_body = json.dumps({"input": tool_input}).encode()
        req = Request(
            f"{base_url}/api/tools/enter_text",
            data=request_body,
            method="POST",
            headers={"Content-Type": "application/json"},
        )
        with urlopen(req, timeout=5) as resp:
            data = json.loads(resp.read().decode())
        assert data["is_error"] is False
        output = json.loads(data["output"])
        assert output["ok"] is False
        assert "suggestion" in output


def test_invoke_rejects_non_object_json_body(bridge_server):
    from urllib.error import HTTPError

    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-bad-body"

    req = Request(
        f"{base_url}/api/tools/touch_gesture",
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

    request_body = json.dumps({"input": {"type": "home"}}).encode()
    req = Request(
        f"{base_url}/api/tools/touch_gesture",
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
    request_body = json.dumps({"input": {"type": "home"}}).encode()
    req = Request(
        f"{base_url}/api/tools/touch_gesture",
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

    request_body = json.dumps({"input": {"type": "home"}}).encode()
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


def test_invoke_unknown_tool_returns_error(bridge_server):
    """Test invoking unknown tool returns error."""
    server, base_url, state = bridge_server
    state.active_episode_id = "test-episode-005"

    request_body = json.dumps({"input": {"type": "home"}}).encode()
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
        request_body = json.dumps({"input": {"type": "home"}}).encode()
        req = Request(
            f"{base_url}/api/tools/touch_gesture",
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


def test_setup_token_deduplicates_concurrent_and_completed_requests():
    import asyncio

    class BlockingResetEnv:
        def __init__(self):
            self.reset_calls = 0
            self.reset_entered = Event()
            self.allow_reset = Event()

        def reset(self):
            self.reset_calls += 1
            self.reset_entered.set()
            self.allow_reset.wait(timeout=5)

    loop = asyncio.new_event_loop()
    loop_thread = Thread(target=loop.run_forever, daemon=True)
    loop_thread.start()
    env = BlockingResetEnv()
    state = BridgeEpisodeState(env, loop)
    server = BridgeServer(state, host="127.0.0.1", port=0)
    base_url = server.start()
    responses = []

    def setup(payload):
        request = Request(
            f"{base_url}/api/setup",
            data=json.dumps(payload).encode(),
            method="POST",
            headers={
                "Content-Type": "application/json",
                "benchmark-task-id": "task.setup-token",
            },
        )
        with urlopen(request, timeout=5) as response:
            responses.append((response.status, json.loads(response.read().decode())))

    first = Thread(target=setup, args=({"episode_id": "episode-1", "setup_token": "token-1"},))
    second = Thread(target=setup, args=({"episode_id": "episode-1", "setup_token": "token-1"},))
    try:
        first.start()
        assert env.reset_entered.wait(timeout=2)
        second.start()
        time.sleep(0.1)
        assert env.reset_calls == 1
        env.allow_reset.set()
        first.join(timeout=5)
        second.join(timeout=5)
        assert not first.is_alive()
        assert not second.is_alive()
        assert [status for status, _ in responses] == [200, 200]
        assert env.reset_calls == 1

        setup({"episode_id": "episode-1", "setup_token": "token-1"})
        assert env.reset_calls == 1

        setup({"episode_id": "episode-1", "setup_token": "token-2"})
        assert env.reset_calls == 2

        release = Request(
            f"{base_url}/api/release",
            data=b"{}",
            method="POST",
            headers={
                "Content-Type": "application/json",
                "benchmark-task-id": "task.setup-token",
            },
        )
        with urlopen(release, timeout=5) as response:
            assert response.status == 200
        setup({"episode_id": "episode-1", "setup_token": "token-2"})
        assert env.reset_calls == 3

        setup({"episode_id": "episode-1"})
        setup({"episode_id": "episode-1"})
        assert env.reset_calls == 5
    finally:
        env.allow_reset.set()
        server.stop()
        loop.call_soon_threadsafe(loop.stop)
        loop_thread.join(timeout=2)
        loop.close()


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
            request_body = json.dumps({"input": {"type": "tap", "point": point}}).encode()
            req = Request(
                f"{base_url}/api/tools/touch_gesture",
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
            request_body = json.dumps({"input": {"type": "home"}}).encode()
            req = Request(
                f"{base_url}/api/tools/touch_gesture",
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

        request_body = json.dumps({"input": {"type": "home"}}).encode()
        req = Request(
            f"{base_url}/api/tools/touch_gesture",
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
