"""Unified /api/tools endpoint for the ADB Android environment bridge.

Mirrors the input semantics of benchmark/mobilegym/bridge/tools_api.py (the Go
agent defines the tool schemas and forwards matching calls verbatim), then
executes each tool through adb instead of a MobileGym env.

Differences from the MobileGym bridge, kept deliberately small:
- coord_space "pixel" is supported explicitly (the device has real pixels);
  "auto" still rejects out-of-range values exactly like MobileGym.
- quick_action executes Android navigation through keyevents / `cmd statusbar`
  / activity intents.
"""

from __future__ import annotations

import json
import math
import time
from http.server import BaseHTTPRequestHandler
from typing import Any, Callable

from .adb import (
    ADBCommandError,
    KEYCODE_APP_SWITCH,
    KEYCODE_DEL,
    KEYCODE_ENTER,
    KEYCODE_TAB,
    unsupported_adb_text_chars,
)
from .protocol import encode_screenshot
from .state import (
    ADBBridgeState,
    NoBridgeEnvAvailableError,
    benchmark_task_id_from_headers,
)


DEFAULT_DIRECTIONAL_SWIPE_DISTANCE = 500.0
DIRECTIONAL_SWIPE_PRESETS = {
    "": (DEFAULT_DIRECTIONAL_SWIPE_DISTANCE, 700),
    "default": (DEFAULT_DIRECTIONAL_SWIPE_DISTANCE, 700),
    "large": (700.0, 800),
    "medium": (500.0, 650),
    "small": (200.0, 420),
    "tiny": (40.0, 320),
}
HID_ABSOLUTE_MAX = 32767.0
ADB_RESERVED_QUICK_ACTIONS = {
    "browser_refresh",
    "browser_new_tab",
    "copy",
    "cut",
    "delete_backward",
    "find",
    "paste",
    "select_all",
    "undo",
    "spotlight_search",
    "search_launch_app",
}
DEFAULT_DOUBLE_TAP_PAUSE_MS = 120
DEFAULT_LONG_PRESS_MS = 500
FOCUS_SETTLE_SEC = 0.3
# Wait after an action before the post-action screenshot: adb commands (e.g.
# `am start`, keyevents) return before the UI transition finishes, so an
# immediate screencap would show the previous screen.
DEFAULT_ACTION_SETTLE_SEC = 0.6


class ADBToolsAPIHandler:
    """Handler for the ADB bridge /api/tools endpoint."""

    def __init__(
        self,
        state: ADBBridgeState,
        request_timeout_sec: float = 30,
        action_settle_sec: float = DEFAULT_ACTION_SETTLE_SEC,
    ):
        self.state = state
        self.request_timeout_sec = request_timeout_sec
        self.action_settle_sec = max(0.0, action_settle_sec)

    def handle_request(self, handler: BaseHTTPRequestHandler, path: str) -> None:
        """Route /api/tools requests to catalog or invocation."""
        if path == "/api/tools":
            if handler.command == "GET":
                self._handle_catalog(handler)
            else:
                handler.send_error(405, "Method not allowed")
        elif path.startswith("/api/tools/"):
            if handler.command == "POST":
                tool_name = path[len("/api/tools/"):]
                self._handle_invoke(handler, tool_name)
            else:
                handler.send_error(405, "Method not allowed")
        else:
            handler.send_error(404, "Not found")

    def _handle_catalog(self, handler: BaseHTTPRequestHandler) -> None:
        """GET /api/tools - return tool catalog."""
        enter_text_focus_schema = {
            "type": "object",
            "additionalProperties": False,
            "properties": {
                "x": {"type": "number"},
                "y": {"type": "number"},
                "coord_space": {"type": "string", "enum": ["auto", "normalized", "absolute", "pixel"]},
            },
            "required": ["x", "y"],
            "description": "Input field coordinates. Prefer normalized 0-1000 coordinates.",
        }
        tools = [
            {
                "name": "screenshot",
                "description": "Capture a screenshot from the Android device via adb. No input required (pass empty JSON {} or \"\"). Returns a JSON object with width, height, and base64-encoded JPEG image data.",
                "args_schema": {
                    "type": "object",
                    "properties": {},
                    "additionalProperties": False,
                },
            },
            {
                "name": "touch_gesture",
                "description": "Perform touch gestures on the Android device (tap, swipe, drag, long_press, etc.).",
                "args_schema": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "type": {
                            "type": "string",
                            "enum": ["tap", "double_tap", "long_press", "swipe", "drag", "swipe_left", "swipe_right", "swipe_up", "swipe_down", "back", "home"],
                            "description": "Type of gesture to perform",
                        },
                        "point": {
                            "type": "object",
                            "additionalProperties": False,
                            "properties": {"x": {"type": "number"}, "y": {"type": "number"}},
                            "required": ["x", "y"],
                            "description": "Point for tap/double_tap/long_press (normalized 0-1000)",
                        },
                        "start": {
                            "type": "object",
                            "additionalProperties": False,
                            "properties": {"x": {"type": "number"}, "y": {"type": "number"}},
                            "required": ["x", "y"],
                            "description": "Start point for swipe/drag",
                        },
                        "end": {
                            "type": "object",
                            "additionalProperties": False,
                            "properties": {"x": {"type": "number"}, "y": {"type": "number"}},
                            "required": ["x", "y"],
                            "description": "End point for swipe/drag",
                        },
                        "duration_ms": {"type": "integer", "minimum": 0, "description": "Duration in milliseconds"},
                        "hold_before_ms": {"type": "integer", "minimum": 0, "description": "Optional dwell after pressing before a swipe begins."},
                        "hold_after_ms": {"type": "integer", "minimum": 0, "description": "Optional dwell at the destination before release."},
                        "hold_ms": {"type": "integer", "minimum": 0, "description": "Tap or long-press hold duration in milliseconds."},
                        "pause_ms": {"type": "integer", "minimum": 0, "description": "Pause between taps for double_tap."},
                        "steps": {"type": "integer", "minimum": 1, "description": "Number of movement steps for swipe or drag."},
                        "distance": {"type": "number", "description": "Directional swipe travel in normalized 0-1000 units"},
                        "anchor": {"type": "number", "description": "Directional swipe fixed-axis coordinate in normalized 0-1000 units"},
                        "coord_space": {"type": "string", "enum": ["auto", "pixel", "normalized", "absolute"], "description": "Coordinate space; normalized uses 0-1000 screen coordinates."},
                        "button": {"type": "string", "enum": ["left", "right", "middle"]},
                        "strength": {
                            "type": "string",
                            "enum": ["large", "medium", "small", "tiny"],
                            "description": "Directional swipe preset",
                        },
                    },
                    "required": ["type"],
                },
            },
            {
                "name": "keyboard_text",
                "description": "Type text on the Android device via adb input text (US-keyboard ASCII only).",
                "args_schema": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "text": {"type": "string", "description": "Text to type"},
                    },
                    "required": ["text"],
                },
            },
            {
                "name": "keyboard_tap",
                "description": "Press keyboard keys on the Android device (e.g., enter, back, home).",
                "args_schema": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "keys": {
                            "type": "array",
                            "minItems": 1,
                            "maxItems": 6,
                            "items": {"type": "string"},
                            "description": "Keys to press (e.g., ['enter'], ['meta', 'h'])",
                        },
                        "hold_ms": {"type": "integer", "minimum": 0, "description": "Optional press duration before release."},
                    },
                    "required": ["keys"],
                },
            },
            {
                "name": "enter_text_in_field",
                "description": "Enter target text into an input field on the Android device: taps the focus point, then types via adb input text. Accepts the Go agent text-entry contract.",
                "args_schema": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "text": {"type": "string", "description": "Exact text that must appear in the field."},
                        "platform": {"type": "string", "enum": ["ios", "android", "mac"]},
                        "mode": {"type": "string", "enum": ["form", "search"]},
                        "focus": enter_text_focus_schema,
                        "segments": {"type": "array", "items": {"type": "string"}},
                        "max_attempts": {"type": "integer", "minimum": 1},
                    },
                    "required": ["text", "focus"],
                },
            },
            {
                "name": "enter_text_via_bridge",
                "description": "ADB-compatible alias for text entry. Types via adb input text instead of the physical phone bridge or HID clipboard path.",
                "args_schema": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "text": {"type": "string", "description": "Exact text that must appear in the field."},
                        "platform": {"type": "string", "enum": ["ios", "android", "mac"]},
                        "focus": enter_text_focus_schema,
                    },
                    "required": ["text", "focus"],
                },
            },
            {
                "name": "mouse_click",
                "description": "Click/tap a coordinate on the Android device. Coordinates use normalized 0-1000 space.",
                "args_schema": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "x": {"type": "number"},
                        "y": {"type": "number"},
                        "button": {"type": "string", "enum": ["left", "right", "middle"]},
                        "coord_space": {"type": "string", "enum": ["auto", "pixel", "normalized", "absolute"]},
                    },
                    "required": ["x", "y"],
                },
            },
            {
                "name": "mouse_move",
                "description": "Move the pointer. Android has no hover state, so this is accepted as a no-op and returns a screenshot.",
                "args_schema": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "x": {"type": "number"},
                        "y": {"type": "number"},
                        "coord_space": {"type": "string", "enum": ["auto", "pixel", "normalized", "absolute"]},
                    },
                    "required": ["x", "y"],
                },
            },
            {
                "name": "mouse_scroll",
                "description": "Scroll using an adb swipe approximation.",
                "args_schema": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {"delta": {"type": "integer", "minimum": -127, "maximum": 127}},
                    "required": ["delta"],
                },
            },
            {
                "name": "quick_action",
                "description": "Execute common platform navigation actions such as back, home, or open_settings.",
                "args_schema": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "action": {"type": "string"},
                        "platform": {"type": "string", "enum": ["ios", "android", "mac"]},
                        "list": {"type": "boolean"},
                        "alternative": {"type": "boolean"},
                        "alternative_index": {"type": "integer", "minimum": 1},
                    },
                    "required": ["platform"],
                },
            },
        ]

        response = {"tools": tools}
        self._send_json(handler, 200, response)

    def _handle_invoke(self, handler: BaseHTTPRequestHandler, tool_name: str) -> None:
        """POST /api/tools/{tool_name} - invoke a tool."""
        try:
            content_length = int(handler.headers.get("Content-Length", "0") or "0")
        except ValueError:
            self._send_json(handler, 400, {"error": "bad_header", "output": "invalid Content-Length", "is_error": True})
            return

        raw_body = handler.rfile.read(content_length) if content_length else b"{}"
        try:
            request_body = json.loads(raw_body.decode("utf-8"))
        except json.JSONDecodeError:
            self._send_json(handler, 400, {"error": "bad_json", "output": "invalid JSON body", "is_error": True})
            return
        if not isinstance(request_body, dict):
            self._send_json(handler, 400, {"error": "bad_json", "output": "JSON body must be an object", "is_error": True})
            return

        # Decode input (matches Go agent's decodeToolInvokeInput)
        raw_input = _decode_tool_input(request_body)

        # Parse input as JSON (tools expect JSON object input). keyboard_text
        # intentionally mirrors the Go tool's plain-text compatibility fallback.
        try:
            if raw_input.strip() in ("", "null", "{}"):
                tool_input = {}
            else:
                parsed_input = json.loads(raw_input)
                if tool_name in ("keyboard_text", "enter_text_in_field", "enter_text_via_bridge") and isinstance(parsed_input, str):
                    tool_input = {"text": parsed_input}
                elif isinstance(parsed_input, dict):
                    tool_input = parsed_input
                else:
                    self._send_json(
                        handler,
                        400,
                        {"error": "invalid_input", "output": "tool input must be a JSON object", "is_error": True},
                    )
                    return
        except json.JSONDecodeError:
            if tool_name in ("keyboard_text", "enter_text_in_field", "enter_text_via_bridge"):
                tool_input = {"text": raw_input.strip()}
            else:
                self._send_json(
                    handler,
                    400,
                    {"error": "invalid_input", "output": f"tool input must be valid JSON: {raw_input}", "is_error": True},
                )
                return

        if not isinstance(tool_input, dict):
            self._send_json(
                handler,
                400,
                {"error": "invalid_input", "output": "tool input must be a JSON object", "is_error": True},
            )
            return

        # Single-device routing: empty task id falls through to the one state;
        # a different owning task id gets 429 (mirrors the MobileGym router).
        task_id = benchmark_task_id_from_headers(handler.headers)
        try:
            self.state.check_task_access(task_id)
        except NoBridgeEnvAvailableError as exc:
            self._send_json(
                handler,
                429,
                {"error": "no_bridge_env_available", "output": str(exc), "is_error": True},
            )
            return
        if not self.state.active_episode_id:
            self._send_json(
                handler,
                409,
                {"error": "no_active_episode", "output": "no active episode; call /api/setup first", "is_error": True},
            )
            return

        # Execute tool
        started_at = time.time()
        try:
            result = self._submit_tool_call(tool_name, tool_input)
            duration_ms = int((time.time() - started_at) * 1000)

            response = {
                "tool": {"name": tool_name},
                "raw_input": raw_input,
                "output": result.get("output", "ok"),
                "is_error": result.get("is_error", False),
                "duration_ms": duration_ms,
            }
            if result.get("error"):
                response["error"] = result["error"]

            self._send_json(handler, 200, response)

        except ADBCommandError as exc:
            duration_ms = int((time.time() - started_at) * 1000)
            self._send_json(
                handler,
                500,
                {
                    "output": str(exc),
                    "is_error": True,
                    "error": "adb_error",
                    "duration_ms": duration_ms,
                },
            )
        except Exception as exc:
            duration_ms = int((time.time() - started_at) * 1000)
            self._send_json(
                handler,
                500,
                {
                    "output": str(exc),
                    "is_error": True,
                    "error": str(exc),
                    "duration_ms": duration_ms,
                },
            )

    def _submit_tool_call(self, tool_name: str, tool_input: dict[str, Any]) -> dict[str, Any]:
        """Dispatch tool call to the ADB device."""
        if tool_name == "screenshot":
            return self._call_screenshot()
        elif tool_name == "touch_gesture":
            return self._call_touch_gesture(tool_input)
        elif tool_name == "keyboard_text":
            return self._call_keyboard_text(tool_input)
        elif tool_name == "keyboard_tap":
            return self._call_keyboard_tap(tool_input)
        elif tool_name == "enter_text_in_field":
            return self._call_enter_text(tool_input, tool_name="enter_text_in_field")
        elif tool_name == "enter_text_via_bridge":
            return self._call_enter_text(tool_input, tool_name="enter_text_via_bridge")
        elif tool_name == "mouse_click":
            return self._call_mouse_click(tool_input)
        elif tool_name == "mouse_move":
            return self._call_mouse_move(tool_input)
        elif tool_name == "mouse_scroll":
            return self._call_mouse_scroll(tool_input)
        elif tool_name == "quick_action":
            return self._call_quick_action(tool_input)
        else:
            return {"output": f"unknown tool: {tool_name}", "is_error": True, "error": "unknown_tool"}

    # ---- tool implementations ---------------------------------------------

    def _call_screenshot(self) -> dict[str, Any]:
        with self.state.lock:
            jpeg, width, height = self.state.device.screenshot_jpeg()
        screenshot = encode_screenshot(jpeg, "image/jpeg", width, height)
        return {"output": json.dumps(screenshot), "is_error": False}

    def _call_touch_gesture(
        self,
        tool_input: dict[str, Any],
        *,
        log_tool_name: str = "touch_gesture",
        log_tool_input: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        raw_gesture_type = tool_input.get("type", "")
        if not isinstance(raw_gesture_type, str):
            return {"output": "error: type must be a string", "is_error": True}
        gesture_type = raw_gesture_type.strip().lower()
        if not gesture_type:
            return {"output": "error: type is required", "is_error": True}

        device = self.state.device
        log_input = tool_input if log_tool_input is None else log_tool_input

        try:
            if gesture_type in ("tap", "double_tap", "long_press"):
                width, height = device.screen_size()
                point = _normalized_point_arg(
                    tool_input, default_space="normalized", screen_size=(width, height)
                )
                x, y = _to_pixels(point, width, height)
                if gesture_type == "tap":
                    return self._execute_device(
                        lambda: device.tap(x, y),
                        tool_name=log_tool_name,
                        tool_input=log_input,
                        adb_summary=f"input tap {x} {y}",
                    )
                if gesture_type == "double_tap":
                    pause_ms = int(tool_input.get("pause_ms") or DEFAULT_DOUBLE_TAP_PAUSE_MS)

                    def double_tap() -> None:
                        device.tap(x, y)
                        time.sleep(max(0, pause_ms) / 1000)
                        device.tap(x, y)

                    return self._execute_device(
                        double_tap,
                        tool_name=log_tool_name,
                        tool_input=log_input,
                        adb_summary=f"input tap {x} {y} (x2)",
                    )
                hold_ms = int(
                    tool_input.get("duration_ms") or tool_input.get("hold_ms") or DEFAULT_LONG_PRESS_MS
                )
                return self._execute_device(
                    lambda: device.swipe(x, y, x, y, hold_ms),
                    tool_name=log_tool_name,
                    tool_input=log_input,
                    adb_summary=f"input swipe {x} {y} {x} {y} {hold_ms} (long_press)",
                )
            elif gesture_type in ("swipe", "drag"):
                width, height = device.screen_size()
                start = _normalized_point_arg(
                    tool_input,
                    field="start",
                    x_key="start_x",
                    y_key="start_y",
                    default_space="normalized",
                    screen_size=(width, height),
                )
                end = _normalized_point_arg(
                    tool_input,
                    field="end",
                    x_key="end_x",
                    y_key="end_y",
                    default_space="normalized",
                    screen_size=(width, height),
                )
                duration_ms = int(tool_input.get("duration_ms") or 300)
                x1, y1 = _to_pixels(start, width, height)
                x2, y2 = _to_pixels(end, width, height)
                return self._execute_device(
                    lambda: device.swipe(x1, y1, x2, y2, duration_ms),
                    tool_name=log_tool_name,
                    tool_input=log_input,
                    adb_summary=f"input swipe {x1} {y1} {x2} {y2} {duration_ms}",
                )
            elif gesture_type in ("swipe_left", "swipe_right", "swipe_up", "swipe_down"):
                payload = _directional_swipe_payload(gesture_type, tool_input)
                width, height = device.screen_size()
                x1, y1 = _to_pixels({"x": payload["start_x"], "y": payload["start_y"]}, width, height)
                x2, y2 = _to_pixels({"x": payload["end_x"], "y": payload["end_y"]}, width, height)
                duration_ms = payload["duration_ms"]
                return self._execute_device(
                    lambda: device.swipe(x1, y1, x2, y2, duration_ms),
                    tool_name=log_tool_name,
                    tool_input=log_input,
                    adb_summary=f"input swipe {x1} {y1} {x2} {y2} {duration_ms} ({gesture_type})",
                )
            elif gesture_type == "back":
                return self._execute_device(
                    lambda: device.keyevent("KEYCODE_BACK"),
                    tool_name=log_tool_name,
                    tool_input=log_input,
                    adb_summary="input keyevent KEYCODE_BACK",
                )
            elif gesture_type == "home":
                return self._execute_device(
                    lambda: device.keyevent("KEYCODE_HOME"),
                    tool_name=log_tool_name,
                    tool_input=log_input,
                    adb_summary="input keyevent KEYCODE_HOME",
                )
            else:
                return {"output": f"error: unsupported gesture type: {gesture_type}", "is_error": True}
        except (TypeError, ValueError) as exc:
            return {"output": f"error: {exc}", "is_error": True}

    def _call_mouse_click(self, tool_input: dict[str, Any]) -> dict[str, Any]:
        button = str(tool_input.get("button", "left") or "left").strip().lower()
        if button not in ("", "left", "right", "middle"):
            return {"output": f"error: unsupported mouse button: {button!r}", "is_error": True}
        device = self.state.device
        try:
            width, height = device.screen_size()
            point = _normalized_point_arg(tool_input, default_space="auto", screen_size=(width, height))
        except (TypeError, ValueError) as exc:
            return {"output": f"error: {exc}", "is_error": True}
        x, y = _to_pixels(point, width, height)
        return self._execute_device(
            lambda: device.tap(x, y),
            tool_name="mouse_click",
            tool_input=tool_input,
            adb_summary=f"input tap {x} {y}",
        )

    def _call_mouse_move(self, tool_input: dict[str, Any]) -> dict[str, Any]:
        """Validate mouse_move input and return a screenshot (no adb action)."""
        try:
            width, height = self.state.device.screen_size()
            _normalized_point_arg(tool_input, default_space="auto", screen_size=(width, height))
        except (TypeError, ValueError) as exc:
            return {"output": f"error: {exc}", "is_error": True}
        return self._call_noop_with_screenshot()

    def _call_mouse_scroll(self, tool_input: dict[str, Any]) -> dict[str, Any]:
        try:
            delta = int(tool_input.get("delta", 0))
        except (TypeError, ValueError) as exc:
            return {"output": f"error: invalid delta: {exc}", "is_error": True}
        if delta == 0:
            return self._call_noop_with_screenshot()
        strength = "medium" if abs(delta) >= 3 else "small"
        gesture_type = "swipe_up" if delta < 0 else "swipe_down"
        return self._call_touch_gesture(
            {"type": gesture_type, "strength": strength},
            log_tool_name="mouse_scroll",
            log_tool_input=tool_input,
        )

    def _call_quick_action(self, tool_input: dict[str, Any]) -> dict[str, Any]:
        platform = str(tool_input.get("platform", "") or "").strip().lower()
        if platform not in ("ios", "android", "mac"):
            return {"output": f"error: unsupported platform: {tool_input.get('platform')!r}", "is_error": True}

        action = _quick_action_id(tool_input)
        if bool(tool_input.get("list")) or action == "list":
            output = {"ok": True, "platform": platform, "actions": _adb_quick_action_catalog()}
            return {"output": json.dumps(output), "is_error": False}

        if bool(tool_input.get("alternative")):
            return _adb_reserved_quick_action(action, platform, "adb bridge does not define alternative bindings")

        device = self.state.device
        if action == "back":
            return self._execute_device(
                lambda: device.keyevent("KEYCODE_BACK"),
                tool_name="quick_action",
                tool_input=tool_input,
                adb_summary="input keyevent KEYCODE_BACK",
            )
        if action == "home":
            return self._execute_device(
                lambda: device.keyevent("KEYCODE_HOME"),
                tool_name="quick_action",
                tool_input=tool_input,
                adb_summary="input keyevent KEYCODE_HOME",
            )
        if action == "app_switch":
            return self._execute_device(
                lambda: device.keyevent(KEYCODE_APP_SWITCH),
                tool_name="quick_action",
                tool_input=tool_input,
                adb_summary=f"input keyevent {KEYCODE_APP_SWITCH}",
            )
        if action == "send":
            return self._execute_device(
                lambda: device.keyevent(KEYCODE_ENTER),
                tool_name="quick_action",
                tool_input=tool_input,
                adb_summary=f"input keyevent {KEYCODE_ENTER}",
            )
        if action == "open_settings":
            return self._execute_device(
                device.start_settings,
                tool_name="quick_action",
                tool_input=tool_input,
                adb_summary="am start -a android.settings.SETTINGS",
            )
        if action == "notification_center":
            return self._execute_device(
                lambda: _statusbar_with_fallback(device, device.expand_notifications, _top_swipe(device)),
                tool_name="quick_action",
                tool_input=tool_input,
                adb_summary="cmd statusbar expand-notifications",
            )
        if action == "control_center":
            def expand_quick_settings() -> None:
                _statusbar_with_fallback(device, device.expand_settings, _top_swipe(device, repeat=2))

            return self._execute_device(
                expand_quick_settings,
                tool_name="quick_action",
                tool_input=tool_input,
                adb_summary="cmd statusbar expand-settings",
            )
        if action == "dismiss_panel":
            return self._execute_device(
                lambda: _statusbar_with_fallback(
                    device, device.collapse_statusbar, lambda: device.keyevent("KEYCODE_BACK")
                ),
                tool_name="quick_action",
                tool_input=tool_input,
                adb_summary="cmd statusbar collapse",
            )
        if action == "quit_app":
            return self._execute_device(
                lambda: device.keyevent("KEYCODE_HOME"),
                tool_name="quick_action",
                tool_input=tool_input,
                adb_summary="input keyevent KEYCODE_HOME",
            )
        if action in ADB_RESERVED_QUICK_ACTIONS:
            return _adb_reserved_quick_action(
                action,
                platform,
                "adb bridge cannot faithfully execute this shortcut; use direct touch or text-entry tools",
            )

        return {"output": f"error: unsupported quick_action: {tool_input.get('action')!r}", "is_error": True}

    def _call_enter_text(self, tool_input: dict[str, Any], *, tool_name: str) -> dict[str, Any]:
        """Execute Go-agent text-entry tools through adb input text."""
        text = tool_input.get("text", "")
        if not isinstance(text, str):
            return {"output": "error: text must be a string", "is_error": True}
        if text == "":
            output = {
                "ok": False,
                "committed": False,
                "target_text": "",
                "required_mode": "ascii",
                "attempts": 1,
                "ime_switches": 0,
                "vlm_calls": 0,
                "reason": "text is required",
            }
            return {"output": json.dumps(output), "is_error": False}
        unsupported = unsupported_adb_text_chars(text)
        if unsupported:
            output = {
                "ok": False,
                "committed": False,
                "target_text": text,
                "required_mode": _required_text_input_mode(text),
                "attempts": 1,
                "ime_switches": 0,
                "vlm_calls": 0,
                "reason": f"adb input text supports only US-keyboard ASCII characters; unsupported: {unsupported!r}",
            }
            return {"output": json.dumps(output, ensure_ascii=False), "is_error": False}

        device = self.state.device
        steps: list[str] = []
        focus = tool_input.get("focus")
        focus_pixels: tuple[int, int] | None = None
        if focus is not None:
            if not isinstance(focus, dict):
                return {"output": "error: focus must be an object", "is_error": True}
            point_input: dict[str, Any] = {"focus": focus}
            if "coord_space" in focus and "coord_space" not in tool_input:
                point_input["coord_space"] = focus["coord_space"]
            try:
                width, height = device.screen_size()
                point = _normalized_point_arg(
                    point_input, field="focus", default_space="normalized", screen_size=(width, height)
                )
            except (TypeError, ValueError) as exc:
                return {"output": f"error: {exc}", "is_error": True}
            focus_pixels = _to_pixels(point, width, height)

        def enter_text() -> None:
            if focus_pixels is not None:
                device.tap(*focus_pixels)
                steps.append(f"tap focus {focus_pixels[0]} {focus_pixels[1]}")
                time.sleep(FOCUS_SETTLE_SEC)
            device.input_text(text)
            steps.append("adb input text")

        result = self._execute_device(
            enter_text,
            tool_name=tool_name,
            tool_input=tool_input,
            adb_summary="input text",
        )
        if result.get("is_error"):
            return result

        mode = _text_input_mode(tool_input.get("mode"))
        output = {
            "ok": True,
            "committed": True,
            "target_text": text,
            "field_text": text,
            "required_mode": _required_text_input_mode(text),
            "mode": mode,
            "attempts": 1,
            "ime_switches": 0,
            "vlm_calls": 0,
            "reason": "adb input text sent",
            "steps": steps,
        }
        return {"output": json.dumps(output, ensure_ascii=False), "is_error": False}

    def _call_keyboard_text(self, tool_input: dict[str, Any]) -> dict[str, Any]:
        text = tool_input.get("text", "")
        if not isinstance(text, str):
            return {"output": "error: text must be a string", "is_error": True}
        if text == "":
            return {"output": "error: text is required", "is_error": True}
        unsupported = unsupported_adb_text_chars(text)
        if unsupported:
            return {
                "output": f"error: keyboard_text supports only US-keyboard ASCII characters; unsupported characters: {unsupported!r}",
                "is_error": True,
            }
        device = self.state.device
        return self._execute_device(
            lambda: device.input_text(text),
            tool_name="keyboard_text",
            tool_input=tool_input,
            adb_summary="input text",
        )

    def _call_keyboard_tap(self, tool_input: dict[str, Any]) -> dict[str, Any]:
        keys = tool_input.get("keys", [])
        if not keys:
            return {"output": "error: keys array is required", "is_error": True}
        if not isinstance(keys, list):
            return {"output": "error: keys must be an array", "is_error": True}

        normalized_keys = [str(k).strip().lower() for k in keys if str(k).strip()]
        if not normalized_keys:
            return {"output": "error: at least one key or modifier is required", "is_error": True}
        alias_map = {
            "keycode_back": "back",
            "keycode_home": "home",
            "keycode_app_switch": "app_switch",
            "return": "enter",
            "escape": "back",
            "esc": "back",
            "delete_backward": "backspace",
        }
        normalized_keys = [alias_map.get(k, k) for k in normalized_keys]
        has_meta = any(k in ("meta", "cmd", "super", "win") for k in normalized_keys)
        non_modifiers = [k for k in normalized_keys if k not in ("meta", "cmd", "super", "win", "ctrl", "alt", "shift")]

        keycode_map: dict[str, str | int] = {
            "enter": KEYCODE_ENTER,
            "home": "KEYCODE_HOME",
            "back": "KEYCODE_BACK",
            "app_switch": KEYCODE_APP_SWITCH,
            "backspace": KEYCODE_DEL,
            "tab": KEYCODE_TAB,
        }

        if has_meta and len(non_modifiers) == 0:
            keycode: str | int = "KEYCODE_HOME"
            summary_key = "home"
        elif len(non_modifiers) == 0:
            return self._call_noop_with_screenshot()
        elif has_meta and len(non_modifiers) == 1 and non_modifiers[0] == "h":
            keycode = "KEYCODE_HOME"
            summary_key = "home"
        elif len(non_modifiers) == 1:
            key = non_modifiers[0]
            if key not in keycode_map:
                return {"output": f"error: adb keyboard_tap does not support key: {key!r}", "is_error": True}
            keycode = keycode_map[key]
            summary_key = key
        else:
            return {"output": "error: adb keyboard_tap supports one non-modifier key at a time", "is_error": True}

        device = self.state.device
        return self._execute_device(
            lambda: device.keyevent(keycode),
            tool_name="keyboard_tap",
            tool_input=tool_input,
            adb_summary=f"input keyevent {keycode} ({summary_key})",
        )

    # ---- execution helpers --------------------------------------------------

    def _execute_device(
        self,
        fn: Callable[[], None],
        *,
        tool_name: str,
        tool_input: dict[str, Any],
        adb_summary: str,
    ) -> dict[str, Any]:
        """Run a device action, then capture a post-action screenshot."""
        with self.state.lock:
            started = time.monotonic()
            try:
                fn()
            except ValueError as exc:
                return {"output": f"error: {exc}", "is_error": True}
            duration_ms = int((time.monotonic() - started) * 1000)
            if self.action_settle_sec > 0:
                time.sleep(self.action_settle_sec)
            jpeg, width, height = self.state.device.screenshot_jpeg()
        screenshot = encode_screenshot(jpeg, "image/jpeg", width, height)
        self.state.log_action(
            tool_name=tool_name,
            tool_input=tool_input,
            adb_summary=adb_summary,
            duration_ms=duration_ms,
            screenshot=screenshot,
        )
        output_data = {
            "action_output": "ok",
            "data": screenshot["data"],
            "width": screenshot["width"],
            "height": screenshot["height"],
            "format": screenshot.get("format", "jpeg"),
        }
        return {"output": json.dumps(output_data), "is_error": False}

    def _call_noop_with_screenshot(self) -> dict[str, Any]:
        with self.state.lock:
            jpeg, width, height = self.state.device.screenshot_jpeg()
        screenshot = encode_screenshot(jpeg, "image/jpeg", width, height)
        output_data = {
            "action_output": "ok",
            "data": screenshot["data"],
            "width": screenshot["width"],
            "height": screenshot["height"],
            "format": screenshot.get("format", "jpeg"),
        }
        return {"output": json.dumps(output_data), "is_error": False}

    def _send_json(self, handler: BaseHTTPRequestHandler, status: int, payload: dict[str, Any]) -> None:
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        handler.send_response(status)
        handler.send_header("Content-Type", "application/json")
        handler.send_header("Content-Length", str(len(data)))
        handler.end_headers()
        handler.wfile.write(data)


# ---- shared helpers (mirroring mobilegym/bridge/tools_api.py) ---------------


def _decode_tool_input(request_body: dict[str, Any]) -> str:
    """Decode tool input from request body (matches Go decodeToolInvokeInput)."""
    if "raw_input" in request_body and request_body["raw_input"] is not None:
        return str(request_body["raw_input"])

    if "input" in request_body:
        input_value = request_body["input"]
        if input_value is None or input_value == "":
            return ""
        if isinstance(input_value, str):
            trimmed = input_value.strip()
            if trimmed in ("", "null"):
                return ""
            # If it's a quoted string, unwrap it
            if trimmed.startswith('"') and trimmed.endswith('"'):
                try:
                    return json.loads(trimmed)
                except json.JSONDecodeError:
                    pass
            return trimmed
        # input is already a dict/list, re-serialize it
        return json.dumps(input_value)

    return ""


def _statusbar_with_fallback(device: Any, primary: Callable[[], None], fallback: Callable[[], None]) -> None:
    try:
        primary()
    except ADBCommandError:
        fallback()


def _top_swipe(device: Any, *, repeat: int = 1) -> Callable[[], None]:
    """Return a fallback that swipes down from the top edge of the screen."""

    def swipe_from_top() -> None:
        width, height = device.screen_size()
        for index in range(max(1, repeat)):
            device.swipe(width // 2, 1, width // 2, int(height * 0.7), 300)
            if index + 1 < repeat:
                time.sleep(0.3)

    return swipe_from_top


def _directional_swipe_payload(gesture_type: str, tool_input: dict[str, Any]) -> dict[str, Any]:
    strength = str(tool_input.get("strength", "")).strip().lower()
    if strength not in DIRECTIONAL_SWIPE_PRESETS:
        raise ValueError(f"unsupported strength: {strength!r}")
    preset_distance, preset_duration = DIRECTIONAL_SWIPE_PRESETS[strength]
    distance = _positive_float(tool_input.get("distance"), preset_distance)
    distance = max(1.0, min(1000.0, distance))
    anchor = _float_or_default(tool_input.get("anchor"), 500.0)
    anchor = max(0.0, min(1000.0, anchor))
    half = distance / 2.0

    if gesture_type == "swipe_left":
        start_x, end_x = anchor + half, anchor - half
        start_y = end_y = anchor
    elif gesture_type == "swipe_right":
        start_x, end_x = anchor - half, anchor + half
        start_y = end_y = anchor
    elif gesture_type == "swipe_up":
        start_y, end_y = anchor + half, anchor - half
        start_x = end_x = anchor
    elif gesture_type == "swipe_down":
        start_y, end_y = anchor - half, anchor + half
        start_x = end_x = anchor
    else:
        raise ValueError(f"unsupported directional swipe: {gesture_type}")

    return {
        "start_x": max(0.0, min(1000.0, start_x)),
        "start_y": max(0.0, min(1000.0, start_y)),
        "end_x": max(0.0, min(1000.0, end_x)),
        "end_y": max(0.0, min(1000.0, end_y)),
        "duration_ms": int(tool_input.get("duration_ms") or preset_duration),
    }


def _positive_float(value: Any, default: float) -> float:
    if value in (None, ""):
        return default
    parsed = float(value)
    return parsed if parsed > 0 else default


def _float_or_default(value: Any, default: float) -> float:
    if value in (None, ""):
        return default
    return float(value)


def _point_arg(
    tool_input: dict[str, Any],
    *,
    field: str = "point",
    x_key: str = "x",
    y_key: str = "y",
) -> dict[str, float]:
    point = tool_input.get(field)
    if isinstance(point, dict):
        if "x" not in point or "y" not in point:
            raise ValueError(f"{field}.x and {field}.y are required")
        return {"x": _finite_float(point["x"], f"{field}.x"), "y": _finite_float(point["y"], f"{field}.y")}
    if isinstance(point, (list, tuple)):
        if len(point) != 2:
            raise ValueError(f"{field} must contain [x, y]")
        return {"x": _finite_float(point[0], f"{field}[0]"), "y": _finite_float(point[1], f"{field}[1]")}

    if x_key in tool_input and y_key in tool_input:
        return {"x": _finite_float(tool_input[x_key], x_key), "y": _finite_float(tool_input[y_key], y_key)}

    raise ValueError(f"{field} is required")


def _normalized_point_arg(
    tool_input: dict[str, Any],
    *,
    field: str = "point",
    x_key: str = "x",
    y_key: str = "y",
    default_space: str,
    screen_size: tuple[int, int] | None = None,
) -> dict[str, float]:
    """Return a normalized 0-1000 point from tool input.

    Mirrors the MobileGym helper, plus explicit "pixel" support (requires
    screen_size). "auto" stays strict: values outside 0-1000 are rejected so
    bad LLM output produces a deterministic error rather than a mis-tap.
    """
    point = _point_arg(tool_input, field=field, x_key=x_key, y_key=y_key)
    coord_space = str(tool_input.get("coord_space", "") or "").strip().lower() or default_space
    if coord_space == "normalized":
        return {"x": _clamp(point["x"], 0.0, 1000.0), "y": _clamp(point["y"], 0.0, 1000.0)}
    if coord_space == "auto":
        if 0.0 <= point["x"] <= 1000.0 and 0.0 <= point["y"] <= 1000.0:
            return point
        raise ValueError("adb bridge coord_space auto only supports 0-1000 normalized coordinates")
    if coord_space == "absolute":
        return {
            "x": _clamp(point["x"], 0.0, HID_ABSOLUTE_MAX) / HID_ABSOLUTE_MAX * 1000.0,
            "y": _clamp(point["y"], 0.0, HID_ABSOLUTE_MAX) / HID_ABSOLUTE_MAX * 1000.0,
        }
    if coord_space == "pixel":
        if screen_size is None:
            raise ValueError("coord_space pixel requires a known screen size")
        width, height = screen_size
        px = _clamp(point["x"], 0.0, max(0.0, width - 1.0))
        py = _clamp(point["y"], 0.0, max(0.0, height - 1.0))
        return {"x": px / width * 1000.0, "y": py / height * 1000.0}
    raise ValueError(f"unsupported coord_space: {coord_space!r}")


def _to_pixels(point: dict[str, float], width: int, height: int) -> tuple[int, int]:
    """Convert a normalized 0-1000 point to device pixel coordinates."""
    x = min(width - 1, max(0, round(point["x"] / 1000.0 * width)))
    y = min(height - 1, max(0, round(point["y"] / 1000.0 * height)))
    return int(x), int(y)


def _finite_float(value: Any, name: str) -> float:
    try:
        parsed = float(value)
    except (TypeError, ValueError) as exc:
        raise ValueError(f"{name} must be a number") from exc
    if math.isnan(parsed) or math.isinf(parsed):
        raise ValueError(f"{name} must be a finite number")
    return parsed


def _clamp(value: float, minimum: float, maximum: float) -> float:
    return max(minimum, min(maximum, value))


def _required_text_input_mode(text: str) -> str:
    if any(ord(ch) > 127 for ch in text):
        return "composition"
    return "ascii"


def _text_input_mode(value: Any) -> str:
    mode = str(value or "").strip().lower()
    if mode == "search":
        return "search"
    return "form"


def _quick_action_id(tool_input: dict[str, Any]) -> str:
    action = str(tool_input.get("action", "") or "").strip().lower().replace("-", "_")
    aliases = {
        "返回": "back",
        "go_back": "back",
        "navigate_back": "back",
        "recents": "app_switch",
        "switch_app": "app_switch",
        "task_switcher": "app_switch",
        "主屏": "home",
        "go_home": "home",
        "home_screen": "home",
        "spotlight": "spotlight_search",
        "global_search": "spotlight_search",
        "search": "spotlight_search",
        "search_launch_app": "spotlight_search",
        "搜索": "spotlight_search",
        "notifications": "notification_center",
        "notification_shade": "notification_center",
        "通知": "notification_center",
        "quick_settings": "control_center",
        "quick_setting": "control_center",
        "快捷设置": "control_center",
        "close_panel": "dismiss_panel",
        "enter": "send",
        "press_enter": "send",
        "send_message": "send",
        "submit": "send",
        "refresh": "browser_refresh",
        "reload": "browser_refresh",
        "backspace": "delete_backward",
        "backward_delete": "delete_backward",
        "退格": "delete_backward",
        "settings": "open_settings",
        "system_settings": "open_settings",
        "设置": "open_settings",
    }
    return aliases.get(action, action)


def _adb_quick_action_catalog() -> list[dict[str, str]]:
    return [
        {"id": "back", "status": "active", "tool": "keyboard_tap"},
        {"id": "home", "status": "active", "tool": "keyboard_tap"},
        {"id": "app_switch", "status": "active", "tool": "keyboard_tap"},
        {"id": "notification_center", "status": "active", "tool": "quick_action"},
        {"id": "control_center", "status": "active", "tool": "quick_action"},
        {"id": "dismiss_panel", "status": "active", "tool": "quick_action"},
        {"id": "send", "status": "active", "tool": "keyboard_tap"},
        {"id": "open_settings", "status": "active", "tool": "quick_action"},
        {"id": "quit_app", "status": "active", "tool": "quick_action"},
        {"id": "browser_refresh", "status": "reserved", "tool": "keyboard_tap"},
        {"id": "copy", "status": "reserved", "tool": "keyboard_tap"},
        {"id": "paste", "status": "reserved", "tool": "keyboard_tap"},
    ]


def _adb_reserved_quick_action(action: str, platform: str, reason: str) -> dict[str, Any]:
    output = {"ok": False, "action": action, "platform": platform, "status": "reserved", "reason": reason}
    return {"output": json.dumps(output), "is_error": False}
