import json
import urllib.error
import urllib.request

import pytest

from vphone.bridge.server import VPhoneBridgeServer
from vphone.bridge.tools_api import _normalized_point_arg, _to_pixels

from tests.test_vphone_bridge import FakeVPhoneDevice


@pytest.fixture()
def bridge():
    device = FakeVPhoneDevice()
    server = VPhoneBridgeServer(device, port=0, action_settle_sec=0)
    base_url = server.start()
    server.state.acquire("vphone-ios-cli")
    try:
        yield server, device, base_url
    finally:
        server.stop()


def invoke(base_url, tool, tool_input, task_id="vphone-ios-cli"):
    raw = tool_input if isinstance(tool_input, str) else json.dumps(tool_input)
    request = urllib.request.Request(
        f"{base_url}/api/tools/{tool}",
        data=json.dumps({"input": raw}).encode(),
        headers={"Content-Type": "application/json", "benchmark-task-id": task_id},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=5) as response:
            return response.status, json.loads(response.read())
    except urllib.error.HTTPError as exc:
        return exc.code, json.loads(exc.read())


def test_coordinate_spaces_and_pixel_rejection():
    assert _to_pixels(_normalized_point_arg({"point": {"x": 500, "y": 500}}, default_space="normalized"), 1290, 2796) == (645, 1398)
    absolute = _normalized_point_arg(
        {"point": {"x": 32767, "y": 0}, "coord_space": "absolute"}, default_space="normalized"
    )
    assert round(absolute["x"]) == 1000
    with pytest.raises(ValueError, match="do not accept pixel"):
        _normalized_point_arg(
            {"point": {"x": 720, "y": 1000}, "coord_space": "pixel"}, default_space="normalized"
        )


def test_touch_gestures(bridge):
    _, device, base_url = bridge
    status, body = invoke(base_url, "touch_gesture", {"type": "tap", "point": {"x": 500, "y": 500}})
    assert status == 200 and body["is_error"] is False
    assert ("tap", 645, 1398) in device.calls
    invoke(base_url, "touch_gesture", {"type": "double_tap", "point": {"x": 500, "y": 500}})
    assert ("double_tap", 645, 1398, 120) in device.calls
    invoke(base_url, "touch_gesture", {"type": "long_press", "point": {"x": 500, "y": 500}, "hold_ms": 700})
    assert ("swipe", 645, 1398, 645, 1398, 700) in device.calls


def test_touch_duration_out_of_range_is_rejected_without_device_action(bridge):
    _, device, base_url = bridge
    status, body = invoke(
        base_url,
        "touch_gesture",
        {
            "type": "swipe",
            "start": {"x": 500, "y": 800},
            "end": {"x": 500, "y": 200},
            "duration_ms": -1,
        },
    )
    assert status == 200 and body["is_error"] is True
    assert all(call[0] != "swipe" for call in device.calls)


def test_touch_rejects_non_left_mouse_buttons(bridge):
    _, device, base_url = bridge
    status, body = invoke(
        base_url,
        "touch_gesture",
        {"type": "tap", "point": {"x": 500, "y": 500}, "button": "right"},
    )
    assert status == 200 and body["is_error"] is True
    assert all(call[0] != "tap" for call in device.calls)


def test_edge_and_directional_gestures(bridge):
    _, device, base_url = bridge
    invoke(base_url, "touch_gesture", {"type": "back"})
    back = [call for call in device.calls if call[0] == "swipe"][-1]
    assert back[1] <= 2 and back[3] > back[1]
    invoke(base_url, "touch_gesture", {"type": "swipe_up", "strength": "medium"})
    upward = [call for call in device.calls if call[0] == "swipe"][-1]
    assert upward[2] > upward[4]


def test_keyboard_text_prevalidation(bridge):
    _, device, base_url = bridge
    status, body = invoke(base_url, "keyboard_text", {"text": "hello ios"})
    assert status == 200 and body["is_error"] is False
    assert ("keyboard_text", "hello ios") in device.calls
    device.calls.clear()
    status, body = invoke(base_url, "keyboard_text", {"text": "你好"})
    assert status == 200 and body["is_error"] is True
    assert body["error"] == "unsupported_text_character"
    assert all(call[0] != "keyboard_text" for call in device.calls)


def test_keyboard_tools_and_send_are_unsupported_without_host_capability():
    device = FakeVPhoneDevice(capabilities={"screenshot", "touch", "hid"})
    server = VPhoneBridgeServer(device, port=0, action_settle_sec=0)
    base_url = server.start()
    server.state.acquire("vphone-ios-cli")
    try:
        _, body = invoke(base_url, "keyboard_text", {"text": "hello"})
        assert body["is_error"] is True and body["error"] == "unsupported"
        _, body = invoke(base_url, "quick_action", {"platform": "ios", "action": "send"})
        assert body["is_error"] is True and body["error"] == "unsupported"
        _, body = invoke(base_url, "quick_action", {"platform": "ios", "list": True})
        actions = {item["id"]: item for item in json.loads(body["output"])["actions"]}
        assert actions["send"]["status"] == "unsupported"
    finally:
        server.stop()


def test_keyboard_hold_and_unverified_text_modes_are_not_silently_ignored(bridge):
    _, device, base_url = bridge
    _, body = invoke(base_url, "keyboard_tap", {"keys": ["enter"], "hold_ms": 100})
    assert body["is_error"] is True and body["error"] == "unsupported"
    _, body = invoke(
        base_url,
        "enter_text_in_field",
        {
            "text": "hello",
            "platform": "ios",
            "focus": {"x": 500, "y": 100},
            "segments": [],
            "send_after_commit": True,
        },
    )
    assert body["is_error"] is True and body["error"] == "unsupported"
    _, body = invoke(
        base_url,
        "enter_text_in_field",
        {
            "text": "hello",
            "platform": "ios",
            "focus": {"x": 500, "y": 100},
            "segments": ["ni"],
        },
    )
    assert body["is_error"] is True and body["error"] == "unsupported"
    assert all(call[0] not in {"keyboard_key", "keyboard_text"} for call in device.calls)


def test_enter_text_in_field_and_clipboard_path(bridge):
    _, device, base_url = bridge
    status, body = invoke(
        base_url,
        "enter_text_in_field",
        {"text": "hello ios", "platform": "ios", "focus": {"x": 500, "y": 100}},
    )
    output = json.loads(body["output"])
    assert status == 200 and output["committed"] is True
    assert device.calls.index(("tap", 645, 280)) < device.calls.index(("keyboard_text", "hello ios"))
    status, body = invoke(
        base_url,
        "enter_text_via_bridge",
        {"text": "你好", "platform": "ios", "focus": {"x": 500, "y": 100}},
    )
    output = json.loads(body["output"])
    assert output["committed"] is False
    assert output["required_mode"] == "composition"


def test_quick_actions(bridge):
    _, device, base_url = bridge
    invoke(base_url, "quick_action", {"platform": "ios", "action": "home"})
    invoke(base_url, "quick_action", {"platform": "ios", "action": "open_settings"})
    invoke(base_url, "quick_action", {"platform": "ios", "action": "send"})
    assert ("reset_home",) in device.calls
    assert ("launch_app", "com.apple.Preferences") in device.calls
    assert ("keyboard_key", "enter") in device.calls


def test_quick_action_panels_and_app_switcher(bridge):
    _, device, base_url = bridge
    for action in ("notification_center", "control_center", "app_switch", "dismiss_panel"):
        status, body = invoke(base_url, "quick_action", {"platform": "ios", "action": action})
        assert status == 200 and body["is_error"] is False
    swipes = [call for call in device.calls if call[0] == "swipe"]
    assert len(swipes) == 4


def test_tool_response_contains_post_action_screenshot(bridge):
    _, _, base_url = bridge
    _, body = invoke(base_url, "touch_gesture", {"type": "home"})
    output = json.loads(body["output"])
    assert output["action_output"] == "ok"
    assert output["width"] == 720
    assert output["source_width"] == 1290
