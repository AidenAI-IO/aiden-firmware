import asyncio
import base64
import json
import sys
import threading
import time
import types
import urllib.error
import urllib.request
from enum import Enum

import pytest

from mobilegym.bridge.episode import BridgeEpisodeState
from mobilegym.bridge.actions import action_to_dict
from mobilegym.bridge.server import BridgeServer
from mobilegym.bridge.protocol import BridgeTokens


PNG_BYTES = b"\x89PNG\r\n\x1a\nmobilegym-png"
JPEG_BYTES = b"\xff\xd8\xff\xe0mobilegym-jpeg"


class FakeMobileGymActionType(str, Enum):
    CLICK = "CLICK"
    DOUBLE_TAP = "DOUBLE_TAP"
    LONG_PRESS = "LONG_PRESS"
    SWIPE = "SWIPE"
    DRAG = "DRAG"
    TYPE = "TYPE"
    ENTER = "ENTER"
    BACK = "BACK"
    HOME = "HOME"
    WAIT = "WAIT"


class FakeMobileGymAction:
    def __init__(self, action_type, data):
        self.action_type = action_type
        self.data = data


def install_mobilegym_action_classes(monkeypatch):
    bench_env = types.ModuleType("bench_env")
    env = types.ModuleType("bench_env.env")
    base = types.ModuleType("bench_env.env.base")
    base.Action = FakeMobileGymAction
    base.ActionType = FakeMobileGymActionType
    bench_env.env = env
    env.base = base
    monkeypatch.setitem(sys.modules, "bench_env", bench_env)
    monkeypatch.setitem(sys.modules, "bench_env.env", env)
    monkeypatch.setitem(sys.modules, "bench_env.env.base", base)


class OwnerLoop:
    def __enter__(self):
        self.loop = asyncio.new_event_loop()
        self.ready = threading.Event()
        self.thread = threading.Thread(target=self._run, name="mobilegym-owner-loop", daemon=True)
        self.thread.start()
        assert self.ready.wait(timeout=2)
        return self

    def __exit__(self, *exc):
        self.loop.call_soon_threadsafe(self.loop.stop)
        self.thread.join(timeout=2)
        self.loop.close()

    def _run(self):
        asyncio.set_event_loop(self.loop)
        self.ready.set()
        self.loop.run_forever()


class FakeObservation:
    screenshot = b"fake-screenshot"
    width = 1000
    height = 2000
    mime_type = "image/png"


class FakeObservationWithoutMime:
    def __init__(self, screenshot):
        self.screenshot = screenshot
        self.width = 1000
        self.height = 2000


class FakeStepResult:
    def __init__(self, observation):
        self.observation = observation


class FakeEnv:
    def __init__(self, owner_loop):
        self.owner_loop = owner_loop
        self.actions = []
        self.state = {"os": {}, "apps": {}}
        self.route = {"app": "launcher", "path": "/"}
        self.reset_calls = 0
        self.calls = []
        self.loop_matches = []
        self.threads = []
        self.active_steps = 0
        self.max_active_steps = 0
        self.step_delay = 0
        self.step_started = threading.Event()
        self.observation = FakeObservation()

    def _record(self, name):
        self.calls.append(name)
        self.loop_matches.append(asyncio.get_running_loop() is self.owner_loop)
        self.threads.append(threading.current_thread().name)

    async def step(self, action):
        self._record("step")
        self.active_steps += 1
        self.max_active_steps = max(self.max_active_steps, self.active_steps)
        self.step_started.set()
        if self.step_delay:
            await asyncio.sleep(self.step_delay)
        self.actions.append(action)
        self.active_steps -= 1
        return FakeStepResult(self.observation)

    async def get_observation(self):
        self._record("get_observation")
        return self.observation

    async def get_state(self, **kwargs):
        self._record("get_state")
        return self.state

    async def get_route(self):
        self._record("get_route")
        return self.route

    async def reset(self):
        self._record("reset")
        self.reset_calls += 1
        return FakeObservation()


class RunningBridge:
    def __enter__(self):
        self.owner = OwnerLoop().__enter__()
        self.env = FakeEnv(self.owner.loop)
        self.state = BridgeEpisodeState(self.env, owner_loop=self.owner.loop)
        self.server = BridgeServer(
            self.state,
            BridgeTokens(control_token="control-token", device_token="device-token"),
            host="127.0.0.1",
            port=0,
        )
        self.server.start()
        self.base_url = self.server.base_url
        return self

    def __exit__(self, *exc):
        self.server.stop()
        OwnerLoop.__exit__(self.owner, *exc)


def request_json(base_url, method, path, payload=None, token=None, timeout=2):
    data = None
    headers = {}
    if method != "GET":
        data = json.dumps(payload or {}).encode("utf-8")
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(f"{base_url}{path}", data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        return exc.code, json.loads(exc.read().decode("utf-8"))


def start_episode(bridge, episode_id="ep1"):
    return request_json(
        bridge.base_url,
        "POST",
        "/episode/start",
        {"episode_id": episode_id},
        token="control-token",
    )


def expected_screenshot(payload=b"fake-screenshot", fmt="png"):
    return {
        "width": 1000,
        "height": 2000,
        "format": fmt,
        "size": len(payload),
        "data": base64.b64encode(payload).decode("ascii"),
    }


def test_health_and_runner_endpoints_require_control_token_before_env_mutation():
    with RunningBridge() as bridge:
        status, body = request_json(bridge.base_url, "GET", "/health")
        assert status == 200
        assert body["data"]["status"] == "ok"

        status, _ = request_json(bridge.base_url, "POST", "/episode/start", {"episode_id": "ep1"})
        assert status == 401
        status, _ = request_json(
            bridge.base_url, "POST", "/episode/start", {"episode_id": "ep1"}, token="device-token"
        )
        assert status == 401

        status, _ = start_episode(bridge)
        assert status == 200

        status, _ = request_json(bridge.base_url, "POST", "/reset", {}, token="wrong-token")
        assert status == 401
        assert bridge.env.reset_calls == 0

        status, _ = request_json(bridge.base_url, "POST", "/reset", {}, token="control-token")
        assert status == 200
        assert bridge.env.reset_calls == 1

        status, body = request_json(bridge.base_url, "POST", "/state", {}, token="control-token")
        assert status == 200
        assert body["data"] == bridge.env.state

        status, body = request_json(bridge.base_url, "POST", "/route", {}, token="control-token")
        assert status == 200
        assert body["data"] == bridge.env.route

        status, _ = request_json(
            bridge.base_url, "POST", "/episode/end", {"episode_id": "ep1"}, token="device-token"
        )
        assert status == 401
        status, _ = request_json(
            bridge.base_url, "POST", "/episode/end", {"episode_id": "ep1"}, token="control-token"
        )
        assert status == 200

        assert bridge.env.loop_matches and all(bridge.env.loop_matches)
        assert bridge.env.threads and set(bridge.env.threads) == {"mobilegym-owner-loop"}


def test_device_endpoints_require_active_episode_and_device_token():
    with RunningBridge() as bridge:
        assert start_episode(bridge)[0] == 200

        status, _ = request_json(
            bridge.base_url,
            "POST",
            "/tap",
            {"episode_id": "ep1", "x": 0.5, "y": 0.25},
            token="wrong-token",
        )
        assert status == 401
        assert bridge.env.actions == []

        status, _ = request_json(
            bridge.base_url,
            "POST",
            "/tap",
            {"episode_id": "old", "x": 0.5, "y": 0.25},
            token="device-token",
        )
        assert status == 409
        assert bridge.env.actions == []

        status, body = request_json(
            bridge.base_url,
            "POST",
            "/screenshot",
            {"episode_id": "ep1"},
            token="device-token",
        )
        assert status == 200
        assert body == expected_screenshot()

        action_requests = [
            ("/tap", {"episode_id": "ep1", "x": 0.1, "y": 0.2}, "CLICK"),
            ("/tap", {"episode_id": "ep1", "x": 0.1, "y": 0.2, "count": 2}, "DOUBLE_TAP"),
            (
                "/tap",
                {"episode_id": "ep1", "x": 0.1, "y": 0.2, "kind": "long_press", "duration_ms": 777},
                "LONG_PRESS",
            ),
            (
                "/swipe",
                {
                    "episode_id": "ep1",
                    "start_x": 100,
                    "start_y": 200,
                    "end_x": 800,
                    "end_y": 900,
                    "duration_ms": 300,
                },
                "SWIPE",
            ),
            (
                "/drag",
                {"episode_id": "ep1", "start_x": 200, "start_y": 300, "end_x": 700, "end_y": 600},
                "DRAG",
            ),
            ("/type_text", {"episode_id": "ep1", "text": "hello"}, "TYPE"),
            ("/key", {"episode_id": "ep1", "key": "enter"}, "ENTER"),
            ("/back", {"episode_id": "ep1"}, "BACK"),
            ("/home", {"episode_id": "ep1"}, "HOME"),
            ("/wait", {"episode_id": "ep1", "duration_ms": 10}, "WAIT"),
        ]
        for index, (path, payload, _) in enumerate(action_requests, start=1):
            status, body = request_json(bridge.base_url, "POST", path, payload, token="device-token")
            assert status == 200
            assert body == {
                "ok": True,
                "message": "ok",
                "action_id": f"ep1:{index:04d}",
                "screenshot": expected_screenshot(),
            }

        assert len(bridge.env.actions) == len(action_requests)
        assert [action_to_dict(action)["action_type"] for action in bridge.env.actions] == [
            expected_type for _, _, expected_type in action_requests
        ]
        assert all(bridge.env.loop_matches)
        assert set(bridge.env.threads) == {"mobilegym-owner-loop"}


def test_key_endpoint_supports_mobilegym_action_type_without_key_and_rejects_unsupported_before_step(monkeypatch):
    install_mobilegym_action_classes(monkeypatch)

    with RunningBridge() as bridge:
        assert start_episode(bridge)[0] == 200

        status, body = request_json(
            bridge.base_url,
            "POST",
            "/key",
            {"episode_id": "ep1", "key": "enter"},
            token="device-token",
        )
        assert status == 200
        assert body["ok"] is True
        assert len(bridge.env.actions) == 1
        assert action_to_dict(bridge.env.actions[0]) == {"action_type": "ENTER", "data": {}}

        status, body = request_json(
            bridge.base_url,
            "POST",
            "/key",
            {"episode_id": "ep1", "key": "volume_up"},
            token="device-token",
        )
        assert status == 400
        assert body["ok"] is False
        assert body["error"]["code"] == "bad_request"
        assert body["error"]["message"] == "unsupported key: volume_up"
        assert len(bridge.env.actions) == 1


@pytest.mark.parametrize(("payload", "fmt"), [(PNG_BYTES, "png"), (JPEG_BYTES, "jpeg")])
def test_screenshot_infers_format_from_bytes_when_mime_type_is_absent(payload, fmt):
    with RunningBridge() as bridge:
        bridge.env.observation = FakeObservationWithoutMime(payload)
        assert start_episode(bridge)[0] == 200

        status, body = request_json(
            bridge.base_url,
            "POST",
            "/screenshot",
            {"episode_id": "ep1"},
            token="device-token",
        )

        assert status == 200
        assert body == expected_screenshot(payload, fmt)


def test_episode_end_returns_action_log_entries_with_screenshots():
    with RunningBridge() as bridge:
        assert start_episode(bridge)[0] == 200

        status, action_body = request_json(
            bridge.base_url,
            "POST",
            "/tap",
            {"episode_id": "ep1", "x": 0.1, "y": 0.2},
            token="device-token",
        )
        assert status == 200

        status, end_body = request_json(
            bridge.base_url,
            "POST",
            "/episode/end",
            {"episode_id": "ep1"},
            token="control-token",
        )

        assert status == 200
        assert end_body["data"]["action_log"][0]["screenshot"] == action_body["screenshot"]


def test_episode_end_waits_for_in_flight_env_work_before_returning():
    with RunningBridge() as bridge:
        bridge.env.step_delay = 0.2
        assert start_episode(bridge)[0] == 200
        action_done = threading.Event()
        end_done = threading.Event()

        def call_wait():
            request_json(
                bridge.base_url,
                "POST",
                "/wait",
                {"episode_id": "ep1", "duration_ms": 1},
                token="device-token",
                timeout=3,
            )
            action_done.set()

        def call_end():
            status, _ = request_json(
                bridge.base_url,
                "POST",
                "/episode/end",
                {"episode_id": "ep1"},
                token="control-token",
                timeout=3,
            )
            assert status == 200
            end_done.set()

        action_thread = threading.Thread(target=call_wait, daemon=True)
        action_thread.start()
        assert bridge.env.step_started.wait(timeout=1)

        end_thread = threading.Thread(target=call_end, daemon=True)
        end_thread.start()
        time.sleep(0.05)
        assert not end_done.is_set()

        action_thread.join(timeout=3)
        end_thread.join(timeout=3)
        assert action_done.is_set()
        assert end_done.is_set()
        assert len(bridge.env.actions) == 1


def test_env_work_is_serialized_and_bridge_serves_screenshot_while_chat_is_blocked():
    with RunningBridge() as bridge:
        bridge.env.step_delay = 0.05
        assert start_episode(bridge)[0] == 200

        def call_wait():
            status, _ = request_json(
                bridge.base_url,
                "POST",
                "/wait",
                {"episode_id": "ep1", "duration_ms": 1},
                token="device-token",
                timeout=3,
            )
            assert status == 200

        first = threading.Thread(target=call_wait, daemon=True)
        second = threading.Thread(target=call_wait, daemon=True)
        first.start()
        second.start()
        first.join(timeout=3)
        second.join(timeout=3)
        assert bridge.env.max_active_steps == 1

        chat_blocked = threading.Event()
        release_chat = threading.Event()

        def fake_blocked_chat():
            chat_blocked.set()
            release_chat.wait(timeout=2)

        chat_thread = threading.Thread(target=fake_blocked_chat, daemon=True)
        chat_thread.start()
        assert chat_blocked.wait(timeout=1)

        status, body = request_json(
            bridge.base_url,
            "POST",
            "/screenshot",
            {"episode_id": "ep1"},
            token="device-token",
            timeout=2,
        )
        assert status == 200
        assert body == expected_screenshot()

        release_chat.set()
        chat_thread.join(timeout=1)
        assert not chat_thread.is_alive()
