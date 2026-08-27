from __future__ import annotations

import base64
import json
import threading
import urllib.error
import urllib.request

import pytest

from desktop.bridge.device import DesktopDevice
from desktop.bridge.server import DesktopBridgeServer


class FakeDevice:
    platform = "linux"
    backend = "fake"

    def __init__(self):
        self.calls = []

    def check_device(self):
        return {"state": "online", "width": 1000, "height": 500, "backend": self.backend}

    def screenshot_jpeg(self, quality=85):
        # Minimal JPEG header is sufficient for protocol tests; dimensions come
        # from the fake device rather than image decoding.
        return b"\xff\xd8\xff\xd9", 1000, 500

    def click(self, *args, **kwargs): self.calls.append(("click", args, kwargs))
    def long_press(self, *args, **kwargs): self.calls.append(("long_press", args, kwargs))
    def drag(self, *args, **kwargs): self.calls.append(("drag", args, kwargs))
    def move(self, *args, **kwargs): self.calls.append(("move", args, kwargs))
    def scroll(self, *args, **kwargs): self.calls.append(("scroll", args, kwargs))
    def write(self, *args, **kwargs): self.calls.append(("write", args, kwargs))
    def press(self, *args, **kwargs): self.calls.append(("press", args, kwargs))
    def quick_action(self, *args, **kwargs): self.calls.append(("quick_action", args, kwargs))


@pytest.fixture()
def bridge():
    server = DesktopBridgeServer(device=FakeDevice(), host="127.0.0.1", port=0, action_settle_sec=0)
    server.start()
    try:
        yield server
    finally:
        server.stop()


def request(bridge, path, payload=None, method=None, headers=None):
    body = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(bridge.base_url + path, data=body, method=method or ("POST" if body else "GET"), headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=3) as response:
            return response.status, json.loads(response.read())
    except urllib.error.HTTPError as exc:
        return exc.code, json.loads(exc.read())


def test_health_concurrent_and_screenshot(bridge):
    status, health = request(bridge, "/health")
    assert status == 200
    assert health["data"]["platform"] == "linux"
    assert request(bridge, "/api/concurrent")[1]["data"]["concurrent"] == 1
    status, payload = request(bridge, "/api/providers/screenshot", {})
    assert status == 200
    frame = payload["data"]
    assert frame["meta"]["width"] == 1000
    assert base64.b64decode(frame["image"]) == b"\xff\xd8\xff\xd9"


def test_task_routing_and_mnk(bridge):
    owner = {"benchmark-task-id": "one"}
    assert request(bridge, "/api/setup", {}, headers=owner)[0] == 200
    assert request(bridge, "/api/providers/mnk", {"operation": "click", "click": {"x": 100, "y": 200}}, headers=owner)[0] == 200
    assert request(bridge, "/api/providers/screenshot", {}, headers={"benchmark-task-id": "two"})[0] == 429
    assert request(bridge, "/api/release", {}, headers=owner)[1]["data"]["released"] is True


def test_tools_catalog_and_keyboard_modifier(bridge):
    status, catalog = request(bridge, "/api/tools")
    assert status == 200
    assert {tool["name"] for tool in catalog["tools"]} >= {"touch_gesture", "keyboard_text", "keyboard_tap", "quick_action"}
    status, result = request(bridge, "/api/tools/keyboard_tap", {"input": {"keys": ["ctrl", "l"]}})
    assert status == 200
    assert result["is_error"] is False
    assert bridge.state.device.calls[-1][0] == "press"


def test_desktop_device_coordinate_validation_without_backend():
    device = DesktopDevice.__new__(DesktopDevice)
    device.system = "linux"
    device._pyautogui = None
    with pytest.raises(ValueError):
        device._pixels(-1, 500)


@pytest.mark.parametrize(
    ("system", "expected"),
    [("darwin", "Screen Recording"), ("windows", "桌面应用"), ("linux", "图形桌面")],
)
def test_permission_hint_is_platform_specific(system, expected):
    device = DesktopDevice.__new__(DesktopDevice)
    device.system = system
    assert expected in device.permission_hint
    assert "重启终端" in device.permission_hint
