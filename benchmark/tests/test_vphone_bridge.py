import base64
import concurrent.futures
import http.client
import json
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

import pytest

from vphone.bridge.client import VPhoneSocketError
from vphone.bridge.server import VPhoneBridgeServer
from vphone.bridge.tools_api import MAX_REQUEST_BODY_BYTES


class FakeVPhoneDevice:
    def __init__(self, *, healthy=True, capabilities=None, vphoned_connected=True):
        self.healthy = healthy
        self.vphoned_connected = vphoned_connected
        self.width = 1290
        self.height = 2796
        self.calls = []
        self._capabilities = set(capabilities or {"screenshot", "touch", "hid", "keyboard", "apps", "url", "clipboard", "gesture_completion"})

    def check_device(self):
        self.calls.append(("check_device",))
        if not self.healthy:
            raise VPhoneSocketError("socket_refused", "VM offline")
        return {
            "screen_width": self.width,
            "screen_height": self.height,
            "display_ready": True,
            "vphoned_connected": self.vphoned_connected,
            "capabilities": sorted(self._capabilities),
        }

    def capabilities(self):
        return set(self._capabilities)

    def screen_size(self):
        return self.width, self.height

    def screenshot_jpeg(self):
        self.calls.append(("screenshot_jpeg",))
        return b"vphone-jpeg", 720, 1561, self.width, self.height

    def tap(self, x, y):
        self.calls.append(("tap", x, y))

    def double_tap(self, x, y, pause_ms):
        self.calls.append(("double_tap", x, y, pause_ms))

    def swipe(self, x1, y1, x2, y2, duration_ms):
        self.calls.append(("swipe", x1, y1, x2, y2, duration_ms))

    def hardware_key(self, name):
        self.calls.append(("hardware_key", name))

    def keyboard_key(self, name):
        self.calls.append(("keyboard_key", name))

    def keyboard_text(self, text):
        self.calls.append(("keyboard_text", text))

    def clipboard_set(self, text):
        self.calls.append(("clipboard_set", text))

    def launch_app(self, bundle_id):
        self.calls.append(("launch_app", bundle_id))

    def reset_home(self):
        self.calls.append(("reset_home",))


@pytest.fixture()
def bridge():
    device = FakeVPhoneDevice()
    server = VPhoneBridgeServer(device, port=0, action_settle_sec=0)
    base_url = server.start()
    try:
        yield server, device, base_url
    finally:
        server.stop()


def request(base_url, path, *, method="GET", payload=None, task_id=None):
    headers = {"Content-Type": "application/json"}
    if task_id:
        headers["benchmark-task-id"] = task_id
    data = json.dumps(payload or {}).encode() if method == "POST" else None
    req = urllib.request.Request(base_url + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=5) as response:
            return response.status, json.loads(response.read())
    except urllib.error.HTTPError as exc:
        return exc.code, json.loads(exc.read())


def test_health_and_concurrency(bridge):
    _, _, base_url = bridge
    status, body = request(base_url, "/health")
    assert status == 200
    assert body["data"]["bridge_type"] == "vphone_ios"
    assert body["data"]["platform"] == "ios"
    assert body["data"]["screen_width"] == 1290
    status, body = request(base_url, "/api/concurrent")
    assert status == 200 and body["data"]["concurrent"] == 1


def test_health_reports_unavailable():
    server = VPhoneBridgeServer(FakeVPhoneDevice(healthy=False), port=0)
    base_url = server.start()
    try:
        status, body = request(base_url, "/health")
    finally:
        server.stop()
    assert status == 503
    assert body["error"]["code"] == "socket_refused"


def test_health_requires_vphoned_connection():
    from vphone.bridge.device import VPhoneDevice

    class StatusClient:
        def request(self, payload):
            del payload
            return {
                "ok": True,
                "screen_width": 1290,
                "screen_height": 2796,
                "display_ready": True,
                "vphoned_connected": False,
                "capabilities": ["screenshot", "touch"],
            }

    device = VPhoneDevice("/tmp/unused-vphone.sock")
    device.client = StatusClient()
    server = VPhoneBridgeServer(device, port=0)
    base_url = server.start()
    try:
        status, body = request(base_url, "/health")
    finally:
        server.stop()
        device.close()
    assert status == 503
    assert body["error"]["code"] == "guest_unavailable"


def test_setup_ownership_release_and_rollback(bridge):
    server, device, base_url = bridge
    status, body = request(base_url, "/api/setup", method="POST", task_id="vphone-ios-cli")
    assert status == 200 and body["data"]["episode_id"] == "vphone-ios-cli"
    assert ("reset_home",) in device.calls
    status, body = request(base_url, "/api/setup", method="POST", task_id="vphone-ios-cli")
    assert status == 200 and body["data"]["episode_id"] == "vphone-ios-cli"
    status, body = request(base_url, "/api/setup", method="POST", task_id="other")
    assert status == 429 and body["error"]["code"] == "no_bridge_env_available"
    status, body = request(base_url, "/api/release", method="POST", task_id="other")
    assert status == 200 and body["data"]["released"] is False
    status, body = request(base_url, "/api/release", method="POST", task_id="vphone-ios-cli")
    assert body["data"]["released"] is True
    assert server.state.active_task_id == ""


def test_failed_setup_does_not_leak_ownership(bridge):
    server, device, base_url = bridge
    device.healthy = False
    status, _ = request(base_url, "/api/setup", method="POST", task_id="broken")
    assert status == 503
    assert server.state.active_task_id == ""


def test_provider_screenshot_contains_scaled_and_source_dimensions(bridge):
    _, _, base_url = bridge
    status, body = request(
        base_url,
        "/api/providers/screenshot",
        method="POST",
        payload={"format": "jpeg", "quality": 80},
    )
    assert status == 200
    meta = body["data"]["meta"]
    assert (meta["width"], meta["height"]) == (720, 1561)
    assert (meta["source_width"], meta["source_height"]) == (1290, 2796)
    assert base64.b64decode(body["data"]["image"]) == b"vphone-jpeg"


def test_provider_screenshot_returns_frame_metadata(bridge):
    _, device, base_url = bridge
    status, body = request(
        base_url,
        "/api/providers/screenshot",
        method="POST",
        payload={"format": "jpeg", "quality": 80},
    )
    assert status == 200
    assert body["ok"] is True
    data = body["data"]
    assert (data["meta"]["width"], data["meta"]["height"]) == (720, 1561)
    assert (data["meta"]["source_width"], data["meta"]["source_height"]) == (1290, 2796)
    assert data["meta"]["pixel_format"] == "jpeg"
    assert data["capture_info"]["capture_backend"] == "vphone"
    assert base64.b64decode(data["image"]) == b"vphone-jpeg"
    assert ("screenshot_jpeg",) in device.calls


def test_provider_screenshot_rejects_conflicting_task_id(bridge):
    _, _, base_url = bridge
    request(base_url, "/api/setup", method="POST", task_id="owner")
    status, body = request(
        base_url,
        "/api/providers/screenshot",
        method="POST",
        payload={"format": "jpeg", "quality": 80},
        task_id="other",
    )
    assert status == 429
    assert body["error"]["code"] == "no_bridge_env_available"


def test_tool_requires_setup_and_enforces_task_id(bridge):
    _, _, base_url = bridge
    status, body = request(base_url, "/api/tools/touch_gesture", method="POST", payload={"input": {"type": "home"}})
    assert status == 409 and body["error"] == "no_active_episode"
    request(base_url, "/api/setup", method="POST", task_id="owner")
    status, body = request(
        base_url, "/api/tools/touch_gesture", method="POST", payload={"input": {"type": "home"}}, task_id="other"
    )
    assert status == 429 and body["error"] == "no_bridge_env_available"


def test_anonymous_request_cannot_reset_or_use_owned_vm(bridge):
    server, device, base_url = bridge
    status, _ = request(base_url, "/api/setup", method="POST", task_id="owner")
    assert status == 200 and server.state.active_episode_id == "owner"
    # Setup without a benchmark-task-id header must not reset an owned VM.
    status, body = request(base_url, "/api/setup", method="POST")
    assert status == 429 and body["error"]["code"] == "no_bridge_env_available"
    assert server.state.active_episode_id == "owner"
    # Tool calls without a task id are rejected against an owned VM.
    status, body = request(base_url, "/api/tools/touch_gesture", method="POST", payload={"input": {"type": "home"}})
    assert status == 429 and body["error"] == "no_bridge_env_available"
    # The owner is unaffected.
    status, body = request(
        base_url, "/api/tools/touch_gesture", method="POST", payload={"input": {"type": "home"}}, task_id="owner"
    )
    assert status == 200 and body["is_error"] is False


def test_anonymous_setup_allowed_when_vm_unowned(bridge):
    server, _, base_url = bridge
    status, body = request(base_url, "/api/setup", method="POST")
    assert status == 200 and body["data"]["reset"] is True
    assert server.state.active_task_id == ""
    assert server.state.active_episode_id.startswith("reset-")


def test_request_body_limit_is_enforced_before_reading_body(bridge):
    _, _, base_url = bridge
    parsed = urllib.parse.urlsplit(base_url)
    connection = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=2)
    connection.putrequest("POST", "/api/setup")
    connection.putheader("Content-Type", "application/json")
    connection.putheader("Content-Length", str(MAX_REQUEST_BODY_BYTES + 1))
    connection.endheaders()
    response = connection.getresponse()
    body = json.loads(response.read())
    connection_header = response.getheader("Connection")
    connection.close()
    assert response.status == 413
    assert body["error"]["code"] == "request_too_large"
    # The oversized body is never read, so the connection must not be reused:
    # leftover bytes would otherwise be parsed as the next request.
    assert connection_header == "close"


def test_mouse_scroll_labels_its_own_action_log_entry(bridge):
    server, _, base_url = bridge
    request(base_url, "/api/setup", method="POST", task_id="owner")
    status, body = request(
        base_url,
        "/api/tools/mouse_scroll",
        method="POST",
        payload={"input": {"delta": 5}},
        task_id="owner",
    )
    assert status == 200 and body["is_error"] is False
    entries = list(server.state.action_log)
    assert len(entries) == 1
    assert entries[0]["tool"] == "mouse_scroll"
    assert entries[0]["input"] == {"delta": 5}
    assert entries[0]["vphone"].startswith("swipe_down")


def test_concurrent_scroll_and_tap_keep_their_own_log_labels():
    """mouse_scroll used to relabel action_log[-1] after _execute_device released
    the state lock, so a concurrent call's entry could be renamed. Each entry's
    tool label must match its own recorded gesture."""

    class SlowDevice(FakeVPhoneDevice):
        def swipe(self, x1, y1, x2, y2, duration_ms):
            time.sleep(0.01)
            super().swipe(x1, y1, x2, y2, duration_ms)

        def tap(self, x, y):
            time.sleep(0.01)
            super().tap(x, y)

    device = SlowDevice()
    server = VPhoneBridgeServer(device, port=0, action_settle_sec=0)
    base_url = server.start()
    server.state.acquire("owner")
    try:

        def call(index):
            if index % 2 == 0:
                return request(
                    base_url, "/api/tools/mouse_scroll", method="POST",
                    payload={"input": {"delta": 3}}, task_id="owner",
                )
            return request(
                base_url, "/api/tools/touch_gesture", method="POST",
                payload={"input": {"type": "tap", "point": {"x": 500, "y": 500}}}, task_id="owner",
            )

        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as executor:
            futures = [executor.submit(call, i) for i in range(8)]
            for future in futures:
                assert future.result(timeout=10)[0] == 200
    finally:
        server.stop()

    entries = list(server.state.action_log)
    assert len(entries) == 8
    for entry in entries:
        if entry["tool"] == "mouse_scroll":
            assert entry["input"] == {"delta": 3}, entry
            assert entry["vphone"].startswith("swipe_"), entry
        else:
            assert entry["tool"] == "touch_gesture", entry
            assert entry["input"] == {"type": "tap", "point": {"x": 500, "y": 500}}, entry
            assert entry["vphone"].startswith("tap "), entry


def test_catalog_omits_keyboard_when_host_does_not_support_it():
    server = VPhoneBridgeServer(FakeVPhoneDevice(capabilities={"screenshot", "touch", "hid"}), port=0)
    base_url = server.start()
    try:
        status, body = request(base_url, "/api/tools")
    finally:
        server.stop()
    assert status == 200
    names = {tool["name"] for tool in body["tools"]}
    assert "keyboard_text" not in names
    assert "keyboard_tap" not in names
    assert "enter_text_via_bridge" not in names
    quick_action = next(tool for tool in body["tools"] if tool["name"] == "quick_action")
    assert quick_action["args_schema"]["additionalProperties"] is False
    assert "platform" not in quick_action["args_schema"]["properties"]
    assert quick_action["args_schema"]["anyOf"] == [
        {"required": ["action"]},
        {"required": ["list"], "properties": {"list": {"const": True}}},
    ]
    assert "url" not in quick_action["args_schema"]["properties"]


def test_parallel_tool_requests_are_serialized_by_device_lock():
    class SerializedDevice(FakeVPhoneDevice):
        def __init__(self):
            super().__init__()
            self.active = 0
            self.max_active = 0
            self.counter_lock = threading.Lock()

        def screenshot_jpeg(self):
            with self.counter_lock:
                self.active += 1
                self.max_active = max(self.max_active, self.active)
            try:
                time.sleep(0.05)
                return super().screenshot_jpeg()
            finally:
                with self.counter_lock:
                    self.active -= 1

    device = SerializedDevice()
    server = VPhoneBridgeServer(device, port=0, action_settle_sec=0)
    base_url = server.start()
    server.state.acquire("owner")
    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:
            futures = [
                executor.submit(
                    request,
                    base_url,
                    "/api/providers/screenshot",
                    method="POST",
                    payload={"format": "jpeg", "quality": 80},
                    task_id="owner",
                )
                for _ in range(2)
            ]
            responses = [future.result(timeout=2) for future in futures]
    finally:
        server.stop()
    assert all(status == 200 and body.get("ok") is True for status, body in responses)
    assert device.max_active == 1
