"""Go-agent-compatible tool surface for the VPhone iOS bridge."""

from __future__ import annotations

import json
import math
import time
from http.server import BaseHTTPRequestHandler
from typing import Any, Callable

from .client import VPhoneSocketError
from .device import MAX_TEXT_LENGTH, unsupported_vphone_text_chars
from .protocol import encode_screenshot
from .state import NoBridgeEnvAvailableError, VPhoneBridgeState, benchmark_task_id_from_headers


MAX_ACTION_DURATION_MS = 10_000
MAX_REQUEST_BODY_BYTES = 10 * 1024 * 1024
DEFAULT_ACTION_SETTLE_SEC = 0.6
DEFAULT_DOUBLE_TAP_PAUSE_MS = 120
DEFAULT_LONG_PRESS_MS = 650
FOCUS_SETTLE_SEC = 0.3
DIRECTIONAL_SWIPE_PRESETS = {
    "": (500.0, 700),
    "default": (500.0, 700),
    "large": (700.0, 800),
    "medium": (500.0, 650),
    "small": (200.0, 420),
    "tiny": (40.0, 320),
}
RESERVED_QUICK_ACTIONS = {
    "browser_refresh",
    "browser_new_tab",
    "copy",
    "cut",
    "delete_backward",
    "find",
    "paste",
    "select_all",
    "undo",
}


class VPhoneToolsAPIHandler:
    def __init__(
        self,
        state: VPhoneBridgeState,
        request_timeout_sec: float = 30,
        action_settle_sec: float = DEFAULT_ACTION_SETTLE_SEC,
    ):
        self.state = state
        self.request_timeout_sec = max(0.1, float(request_timeout_sec))
        self.action_settle_sec = max(0.0, float(action_settle_sec))

    def handle_request(self, handler: BaseHTTPRequestHandler, path: str) -> None:
        if path == "/api/tools":
            if handler.command == "GET":
                self._send_json(handler, 200, {"tools": self.catalog()})
            else:
                self._send_json(handler, 405, {"error": "method_not_allowed", "output": "GET required", "is_error": True})
            return
        if path.startswith("/api/tools/"):
            if handler.command == "POST":
                self._handle_invoke(handler, path[len("/api/tools/"):])
            else:
                self._send_json(handler, 405, {"error": "method_not_allowed", "output": "POST required", "is_error": True})
            return
        self._send_json(handler, 404, {"error": "not_found", "output": "unknown endpoint", "is_error": True})

    def catalog(self) -> list[dict[str, Any]]:
        coordinate_properties = {
            "x": {"type": "number"},
            "y": {"type": "number"},
        }
        focus_schema = {
            "type": "object",
            "additionalProperties": False,
            "properties": coordinate_properties,
            "required": ["x", "y"],
        }
        tools: list[dict[str, Any]] = [
            {
                "name": "touch_gesture",
                "description": "Perform an iOS touch gesture using normalized 0-1000 coordinates.",
                "args_schema": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "type": {
                            "type": "string",
                            "enum": [
                                "tap", "double_tap", "long_press", "swipe", "drag",
                                "swipe_left", "swipe_right", "swipe_up", "swipe_down", "back", "home",
                            ],
                        },
                        "point": {
                            "type": "object", "additionalProperties": False,
                            "properties": coordinate_properties, "required": ["x", "y"],
                        },
                        "start": {
                            "type": "object", "additionalProperties": False,
                            "properties": coordinate_properties, "required": ["x", "y"],
                        },
                        "end": {
                            "type": "object", "additionalProperties": False,
                            "properties": coordinate_properties, "required": ["x", "y"],
                        },
                        "duration_ms": {"type": "integer", "minimum": 1, "maximum": MAX_ACTION_DURATION_MS},
                        "hold_ms": {"type": "integer", "minimum": 1, "maximum": MAX_ACTION_DURATION_MS},
                        "pause_ms": {"type": "integer", "minimum": 20, "maximum": 180},
                        "distance": {"type": "number"},
                        "anchor": {"type": "number"},
                        "button": {"type": "string", "enum": ["left", "right", "middle"]},
                        "strength": {"type": "string", "enum": ["large", "medium", "small", "tiny"]},
                    },
                    "required": ["type"],
                },
            },
            {
                "name": "mouse_click",
                "description": "Tap the iOS VM using normalized 0-1000 coordinates.",
                "args_schema": {
                    "type": "object", "additionalProperties": False,
                    "properties": {**coordinate_properties, "button": {"type": "string"}},
                    "required": ["x", "y"],
                },
            },
            {
                "name": "mouse_move",
                "description": "Validate a point and return a screenshot; iOS has no hover state.",
                "args_schema": {
                    "type": "object", "additionalProperties": False,
                    "properties": coordinate_properties,
                    "required": ["x", "y"],
                },
            },
            {
                "name": "mouse_scroll",
                "description": "Scroll the iOS VM using a vertical swipe.",
                "args_schema": {
                    "type": "object", "additionalProperties": False,
                    "properties": {"delta": {"type": "integer", "minimum": -127, "maximum": 127}},
                    "required": ["delta"],
                },
            },
            {
                "name": "quick_action",
                "description": "Execute iOS navigation such as home, back, app_switch, open_settings, notification_center, or control_center.",
                "args_schema": {
                    "type": "object", "additionalProperties": False,
                    "properties": {
                        "action": {"type": "string"},
                        "platform": {"type": "string", "enum": ["ios"]},
                        "list": {"type": "boolean"},
                        "alternative": {"type": "boolean"},
                        "alternative_index": {"type": "integer", "minimum": 1},
                    },
                    "required": ["platform"],
                },
            },
        ]
        capabilities = self.state.device.capabilities()
        if "keyboard" in capabilities:
            tools.extend(
                [
                    {
                        "name": "keyboard_text",
                        "description": "Type US-keyboard ASCII text into the focused iOS field.",
                        "args_schema": {
                            "type": "object", "additionalProperties": False,
                            "properties": {"text": {"type": "string", "maxLength": 1024}},
                            "required": ["text"],
                        },
                    },
                    {
                        "name": "keyboard_tap",
                        "description": "Press one iOS keyboard or navigation key.",
                        "args_schema": {
                            "type": "object", "additionalProperties": False,
                            "properties": {
                                "keys": {"type": "array", "minItems": 1, "maxItems": 6, "items": {"type": "string"}},
                            },
                            "required": ["keys"],
                        },
                    },
                    {
                        "name": "enter_text",
                        "description": "Tap a visible field and enter US-keyboard ASCII text.",
                        "args_schema": {
                            "type": "object", "additionalProperties": False,
                            "properties": {
                                "text": {"type": "string", "maxLength": 1024},
                                "focus": focus_schema,
                            },
                            "required": ["text", "focus"],
                        },
                    },
                ]
            )
        return tools

    def _handle_invoke(self, handler: BaseHTTPRequestHandler, tool_name: str) -> None:
        request_body = self._read_request_body(handler)
        if request_body is None:
            return
        raw_input = _decode_tool_input(request_body)
        try:
            tool_input = _parse_tool_input(raw_input, tool_name)
        except ValueError as exc:
            self._send_json(handler, 400, {"error": "invalid_input", "output": str(exc), "is_error": True})
            return

        task_id = benchmark_task_id_from_headers(handler.headers)
        try:
            self.state.check_task_access(task_id)
        except NoBridgeEnvAvailableError as exc:
            self._send_json(
                handler, 429,
                {"error": "no_bridge_env_available", "output": str(exc), "is_error": True},
            )
            return
        if not self.state.active_episode_id:
            self._send_json(
                handler, 409,
                {"error": "no_active_episode", "output": "no active episode; call /api/setup first", "is_error": True},
            )
            return

        started = time.monotonic()
        try:
            result = self._submit_tool_call(tool_name, tool_input)
            response: dict[str, Any] = {
                "tool": {"name": tool_name},
                "raw_input": raw_input,
                "output": result.get("output", "ok"),
                "is_error": bool(result.get("is_error", False)),
                "duration_ms": int((time.monotonic() - started) * 1000),
            }
            if result.get("error"):
                response["error"] = result["error"]
            self._send_json(handler, 200, response)
        except VPhoneSocketError as exc:
            status = 503 if exc.code in {
                "socket_not_found", "socket_refused", "socket_timeout", "socket_io",
                "display_unavailable", "guest_unavailable", "guest_ssh_failed",
                "keyboard_unavailable",
            } else 500
            self._send_json(
                handler, status,
                {
                    "output": str(exc), "is_error": True, "error": exc.code,
                    "duration_ms": int((time.monotonic() - started) * 1000),
                },
            )
        except Exception as exc:
            self._send_json(
                handler, 500,
                {
                    "output": str(exc), "is_error": True, "error": "vphone_bridge_error",
                    "duration_ms": int((time.monotonic() - started) * 1000),
                },
            )

    def _read_request_body(self, handler: BaseHTTPRequestHandler) -> dict[str, Any] | None:
        try:
            length = int(handler.headers.get("Content-Length", "0") or "0")
        except ValueError:
            self._send_json(handler, 400, {"error": "bad_header", "output": "invalid Content-Length", "is_error": True})
            return None
        if length < 0:
            self._send_json(handler, 400, {"error": "bad_header", "output": "Content-Length must be non-negative", "is_error": True})
            return None
        if length > MAX_REQUEST_BODY_BYTES:
            # Deliberately do not read the oversized body. Close instead, so the
            # unread bytes can never be parsed as the next request should this
            # handler ever be switched to HTTP/1.1 keep-alive.
            self._send_json(
                handler, 413,
                {"error": "request_too_large", "output": f"request body exceeds {MAX_REQUEST_BODY_BYTES} bytes", "is_error": True},
                close=True,
            )
            return None
        raw = handler.rfile.read(length) if length else b"{}"
        try:
            payload = json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError):
            self._send_json(handler, 400, {"error": "bad_json", "output": "invalid JSON body", "is_error": True})
            return None
        if not isinstance(payload, dict):
            self._send_json(handler, 400, {"error": "bad_json", "output": "JSON body must be an object", "is_error": True})
            return None
        return payload

    def _submit_tool_call(self, tool_name: str, tool_input: dict[str, Any]) -> dict[str, Any]:
        unknown = _unknown_coordinate_tool_fields(tool_name, tool_input)
        if unknown:
            return {"output": f"error: unknown fields: {unknown!r}", "is_error": True}
        dispatch: dict[str, Callable[[dict[str, Any]], dict[str, Any]]] = {
            "touch_gesture": self._call_touch_gesture,
            "keyboard_text": self._call_keyboard_text,
            "keyboard_tap": self._call_keyboard_tap,
            "enter_text": self._call_enter_text,
            "mouse_click": self._call_mouse_click,
            "mouse_move": self._call_mouse_move,
            "mouse_scroll": self._call_mouse_scroll,
            "quick_action": self._call_quick_action,
        }
        fn = dispatch.get(tool_name)
        if fn is None:
            return {"output": f"unknown tool: {tool_name}", "is_error": True, "error": "unknown_tool"}
        return fn(tool_input)

    def _call_touch_gesture(
        self,
        tool_input: dict[str, Any],
        *,
        log_as: tuple[str, dict[str, Any]] | None = None,
    ) -> dict[str, Any]:
        """Run a gesture. `log_as` overrides the (tool_name, tool_input) recorded
        in the action log, so callers that reuse this path (mouse_scroll) label
        their own entry while it is still written under the state lock."""
        raw_type = tool_input.get("type", "")
        if not isinstance(raw_type, str) or not raw_type.strip():
            return {"output": "error: type is required", "is_error": True}
        gesture_type = raw_type.strip().lower()
        button = str(tool_input.get("button", "left") or "left").strip().lower()
        if button not in {"", "left"}:
            return {
                "output": f"error: iOS touch gestures do not support button {button!r}",
                "is_error": True,
            }
        log_name, log_input = log_as or ("touch_gesture", tool_input)
        device = self.state.device
        try:
            if gesture_type in {"tap", "double_tap", "long_press"}:
                width, height = device.screen_size()
                point = _normalized_point_arg(tool_input)
                x, y = _to_pixels(point, width, height)
                if gesture_type == "tap":
                    return self._execute_device(
                        lambda: device.tap(x, y), log_name, log_input, f"tap {x} {y}"
                    )
                if gesture_type == "double_tap":
                    pause_ms = _duration_ms_arg(
                        tool_input.get("pause_ms"), DEFAULT_DOUBLE_TAP_PAUSE_MS, minimum=20, maximum=180
                    )
                    return self._execute_device(
                        lambda: device.double_tap(x, y, pause_ms),
                        log_name, log_input, f"double_tap {x} {y} pause={pause_ms}",
                    )
                hold_ms = _duration_ms_arg(
                    tool_input.get("duration_ms") or tool_input.get("hold_ms"), DEFAULT_LONG_PRESS_MS
                )
                return self._execute_device(
                    lambda: device.swipe(x, y, x, y, hold_ms),
                    log_name, log_input, f"long_press {x} {y} duration={hold_ms}",
                )
            if gesture_type in {"swipe", "drag"}:
                width, height = device.screen_size()
                start = _normalized_point_arg(tool_input, field="start", x_key="start_x", y_key="start_y")
                end = _normalized_point_arg(tool_input, field="end", x_key="end_x", y_key="end_y")
                x1, y1 = _to_pixels(start, width, height)
                x2, y2 = _to_pixels(end, width, height)
                duration = _duration_ms_arg(tool_input.get("duration_ms"), 300)
                return self._execute_device(
                    lambda: device.swipe(x1, y1, x2, y2, duration),
                    log_name, log_input, f"swipe {x1} {y1} {x2} {y2} duration={duration}",
                )
            if gesture_type in {"swipe_left", "swipe_right", "swipe_up", "swipe_down"}:
                payload = _directional_swipe_payload(gesture_type, tool_input)
                width, height = device.screen_size()
                x1, y1 = _to_pixels({"x": payload["start_x"], "y": payload["start_y"]}, width, height)
                x2, y2 = _to_pixels({"x": payload["end_x"], "y": payload["end_y"]}, width, height)
                duration = payload["duration_ms"]
                return self._execute_device(
                    lambda: device.swipe(x1, y1, x2, y2, duration),
                    log_name, log_input, f"{gesture_type} {x1} {y1} {x2} {y2}",
                )
            if gesture_type == "home":
                return self._execute_device(device.reset_home, log_name, log_input, "key home")
            if gesture_type == "back":
                return self._edge_back(tool_name=log_name, tool_input=log_input)
        except (TypeError, ValueError) as exc:
            return {"output": f"error: {exc}", "is_error": True}
        return {"output": f"error: unsupported gesture type: {gesture_type}", "is_error": True}

    def _call_mouse_click(self, tool_input: dict[str, Any]) -> dict[str, Any]:
        button = str(tool_input.get("button", "left") or "left").strip().lower()
        if button not in {"", "left", "right", "middle"}:
            return {"output": f"error: unsupported mouse button: {button!r}", "is_error": True}
        try:
            point = _normalized_point_arg(tool_input)
            width, height = self.state.device.screen_size()
            x, y = _to_pixels(point, width, height)
        except (TypeError, ValueError) as exc:
            return {"output": f"error: {exc}", "is_error": True}
        return self._execute_device(
            lambda: self.state.device.tap(x, y), "mouse_click", tool_input, f"tap {x} {y}"
        )

    def _call_mouse_move(self, tool_input: dict[str, Any]) -> dict[str, Any]:
        try:
            _normalized_point_arg(tool_input)
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
        gesture = "swipe_up" if delta < 0 else "swipe_down"
        # Label the log entry through log_as rather than rewriting action_log[-1]
        # afterwards: that mutation happened after _execute_device released the
        # state lock, so a concurrent tool call could have appended its own entry
        # in between and had it renamed to mouse_scroll.
        return self._call_touch_gesture(
            {"type": gesture, "strength": strength},
            log_as=("mouse_scroll", tool_input),
        )

    def _call_keyboard_text(self, tool_input: dict[str, Any]) -> dict[str, Any]:
        if "keyboard" not in self.state.device.capabilities():
            return _unsupported_keyboard_result()
        text = tool_input.get("text", "")
        if not isinstance(text, str) or not text:
            return {"output": "error: text is required", "is_error": True}
        if len(text) > MAX_TEXT_LENGTH:
            return {
                "output": f"error: text exceeds {MAX_TEXT_LENGTH} characters",
                "is_error": True,
                "error": "text_too_long",
            }
        unsupported = unsupported_vphone_text_chars(text)
        if unsupported:
            return {
                "output": f"error: keyboard_text supports only US-keyboard ASCII characters; unsupported: {unsupported!r}",
                "is_error": True,
                "error": "unsupported_text_character",
            }
        return self._execute_device(
            lambda: self.state.device.keyboard_text(text), "keyboard_text", tool_input, "keyboard_text"
        )

    def _call_keyboard_tap(self, tool_input: dict[str, Any]) -> dict[str, Any]:
        if "keyboard" not in self.state.device.capabilities():
            return _unsupported_keyboard_result()
        try:
            hold_ms = int(tool_input.get("hold_ms", 0) or 0)
        except (TypeError, ValueError):
            return {"output": "error: hold_ms must be an integer", "is_error": True}
        if hold_ms != 0:
            return {
                "output": "error: VPhone keyboard_tap hold_ms is not supported",
                "is_error": True,
                "error": "unsupported",
            }
        keys = tool_input.get("keys")
        if not isinstance(keys, list) or not keys:
            return {"output": "error: keys array is required", "is_error": True}
        normalized = [str(item).strip().lower() for item in keys if str(item).strip()]
        if any(item in {"ctrl", "alt", "shift"} for item in normalized):
            return {"output": "error: VPhone keyboard_tap does not support ctrl/alt/shift combinations", "is_error": True}
        has_meta = any(item in {"meta", "cmd", "super", "win"} for item in normalized)
        bare = [item for item in normalized if item not in {"meta", "cmd", "super", "win"}]
        aliases = {
            "return": "enter", "delete_backward": "delete", "backspace": "delete",
            "keycode_home": "home", "keycode_back": "back", "escape": "back", "esc": "back",
            "keycode_app_switch": "app_switch",
        }
        bare = [aliases.get(item, item) for item in bare]
        if has_meta and bare in ([], ["h"]):
            key = "home"
        elif len(bare) == 1:
            key = bare[0]
        else:
            return {"output": "error: keyboard_tap supports one non-modifier key at a time", "is_error": True}
        if key == "home":
            return self._execute_device(self.state.device.reset_home, "keyboard_tap", tool_input, "key home")
        if key == "back":
            return self._edge_back(tool_name="keyboard_tap", tool_input=tool_input)
        if key == "app_switch":
            return self._app_switch(tool_name="keyboard_tap", tool_input=tool_input)
        if key not in {"enter", "tab", "delete", "space"}:
            return {"output": f"error: VPhone keyboard_tap does not support key: {key!r}", "is_error": True}
        return self._execute_device(
            lambda: self.state.device.keyboard_key(key), "keyboard_tap", tool_input, f"keyboard_key {key}"
        )

    def _call_enter_text(self, tool_input: dict[str, Any]) -> dict[str, Any]:
        if "keyboard" not in self.state.device.capabilities():
            return _unsupported_keyboard_result()
        unknown = sorted(set(tool_input) - {"text", "focus"})
        if unknown:
            output = {"ok": False, "suggestion": f"Remove unsupported enter_text arguments: {unknown!r}."}
            return {"output": json.dumps(output), "is_error": False}
        text = tool_input.get("text", "")
        if not isinstance(text, str) or not text:
            return {"output": json.dumps({"ok": False, "suggestion": "Provide non-empty text, then retry enter_text."}), "is_error": False}
        if len(text) > MAX_TEXT_LENGTH:
            return {
                "output": json.dumps({"ok": False, "suggestion": f"Shorten the text to at most {MAX_TEXT_LENGTH} characters, then retry enter_text."}),
                "is_error": False,
                "error": "text_too_long",
            }
        unsupported = unsupported_vphone_text_chars(text)
        if unsupported:
            return {
                "output": json.dumps({"ok": False, "suggestion": f"VPhone text entry does not support these characters: {unsupported!r}."}, ensure_ascii=False),
                "is_error": False,
            }
        focus = tool_input.get("focus")
        if not isinstance(focus, dict):
            output = {"ok": False, "suggestion": "Provide focus as an object with x and y coordinates, then retry enter_text."}
            return {"output": json.dumps(output), "is_error": False}
        unknown_focus = sorted(set(focus) - {"x", "y"})
        if unknown_focus:
            output = {"ok": False, "suggestion": f"Remove unsupported focus arguments: {unknown_focus!r}."}
            return {"output": json.dumps(output), "is_error": False}
        point_input = {"focus": focus}
        try:
            point = _normalized_point_arg(point_input, field="focus")
            width, height = self.state.device.screen_size()
            x, y = _to_pixels(point, width, height)
        except (TypeError, ValueError) as exc:
            output = {"ok": False, "suggestion": f"Correct the focus coordinates: {exc}"}
            return {"output": json.dumps(output), "is_error": False}

        def enter() -> None:
            self.state.device.tap(x, y)
            time.sleep(FOCUS_SETTLE_SEC)
            self.state.device.keyboard_text(text)

        result = self._execute_device(enter, "enter_text", tool_input, "focus + keyboard_text")
        if result.get("is_error"):
            return result
        return {"output": json.dumps({"ok": True}), "is_error": False}

    def _call_quick_action(self, tool_input: dict[str, Any]) -> dict[str, Any]:
        platform = str(tool_input.get("platform", "") or "").strip().lower()
        if platform != "ios":
            return {"output": f"error: unsupported platform: {tool_input.get('platform')!r}; expected 'ios'", "is_error": True}
        action = _quick_action_id(tool_input)
        if bool(tool_input.get("list")) or action == "list":
            keyboard_available = "keyboard" in self.state.device.capabilities()
            return {
                "output": json.dumps(
                    {"ok": True, "platform": "ios", "actions": _quick_action_catalog(keyboard_available)}
                ),
                "is_error": False,
            }
        if bool(tool_input.get("alternative")):
            return _reserved_action(action, "alternative bindings are not defined for VPhone")
        if action == "home" or action == "quit_app":
            return self._execute_device(self.state.device.reset_home, "quick_action", tool_input, "key home")
        if action == "back":
            return self._edge_back(tool_name="quick_action", tool_input=tool_input)
        if action == "app_switch":
            return self._app_switch(tool_name="quick_action", tool_input=tool_input)
        if action == "send":
            if "keyboard" not in self.state.device.capabilities():
                return _unsupported_keyboard_result()
            return self._execute_device(
                lambda: self.state.device.keyboard_key("enter"), "quick_action", tool_input, "keyboard_key enter"
            )
        if action == "open_settings":
            return self._execute_device(
                lambda: self.state.device.launch_app("com.apple.Preferences"),
                "quick_action", tool_input, "app_launch com.apple.Preferences",
            )
        if action == "notification_center":
            return self._edge_panel(100, tool_input, "notification_center")
        if action == "control_center":
            return self._edge_panel(900, tool_input, "control_center")
        if action == "dismiss_panel":
            return self._normalized_swipe(500, 850, 500, 100, 350, "quick_action", tool_input, "dismiss_panel")
        if action == "spotlight_search":
            return self._normalized_swipe(500, 250, 500, 700, 350, "quick_action", tool_input, "spotlight_search")
        if action in RESERVED_QUICK_ACTIONS:
            return _reserved_action(action, "VPhone cannot faithfully execute this shortcut")
        return {"output": f"error: unsupported quick_action: {tool_input.get('action')!r}", "is_error": True}

    def _edge_back(self, *, tool_name: str, tool_input: dict[str, Any]) -> dict[str, Any]:
        return self._normalized_swipe(1, 500, 360, 500, 300, tool_name, tool_input, "edge back")

    def _edge_panel(self, x: int, tool_input: dict[str, Any], name: str) -> dict[str, Any]:
        return self._normalized_swipe(x, 1, x, 700, 400, "quick_action", tool_input, name)

    def _app_switch(self, *, tool_name: str, tool_input: dict[str, Any]) -> dict[str, Any]:
        return self._normalized_swipe(500, 999, 500, 420, 700, tool_name, tool_input, "app switch")

    def _normalized_swipe(
        self,
        x1: float, y1: float, x2: float, y2: float, duration_ms: int,
        tool_name: str, tool_input: dict[str, Any], summary: str,
    ) -> dict[str, Any]:
        width, height = self.state.device.screen_size()
        px1, py1 = _to_pixels({"x": x1, "y": y1}, width, height)
        px2, py2 = _to_pixels({"x": x2, "y": y2}, width, height)
        return self._execute_device(
            lambda: self.state.device.swipe(px1, py1, px2, py2, duration_ms),
            tool_name, tool_input, summary,
        )

    def _execute_device(
        self,
        fn: Callable[[], None],
        tool_name: str,
        tool_input: dict[str, Any],
        summary: str,
    ) -> dict[str, Any]:
        with self.state.lock:
            started = time.monotonic()
            try:
                fn()
            except ValueError as exc:
                return {"output": f"error: {exc}", "is_error": True}
            duration_ms = int((time.monotonic() - started) * 1000)
            if self.action_settle_sec:
                time.sleep(self.action_settle_sec)
            screenshot = self._capture_screenshot()
            # Log while still holding state.lock (an RLock, so log_action's own
            # acquisition nests safely) so a concurrent action cannot interleave
            # its own entry between this device call and its log record.
            self.state.log_action(
                tool_name=tool_name,
                tool_input=tool_input,
                summary=summary,
                duration_ms=duration_ms,
                screenshot=screenshot,
            )
        return {"output": json.dumps(_post_action_output(screenshot)), "is_error": False}

    def _call_noop_with_screenshot(self) -> dict[str, Any]:
        with self.state.lock:
            screenshot = self._capture_screenshot()
        return {"output": json.dumps(_post_action_output(screenshot)), "is_error": False}

    def _capture_screenshot(self) -> dict[str, Any]:
        payload, width, height, source_width, source_height = self.state.device.screenshot_jpeg()
        return encode_screenshot(
            payload, "image/jpeg", width, height,
            source_width=source_width, source_height=source_height,
        )

    @staticmethod
    def _send_json(
        handler: BaseHTTPRequestHandler,
        status: int,
        payload: dict[str, Any],
        *,
        close: bool = False,
    ) -> None:
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        handler.send_response(status)
        handler.send_header("Content-Type", "application/json")
        handler.send_header("Cache-Control", "no-store")
        handler.send_header("Content-Length", str(len(data)))
        if close:
            handler.close_connection = True
            handler.send_header("Connection", "close")
        handler.end_headers()
        handler.wfile.write(data)


def _parse_tool_input(raw_input: str, tool_name: str) -> dict[str, Any]:
    if raw_input.strip() in {"", "null", "{}"}:
        return {}
    try:
        parsed = json.loads(raw_input)
    except json.JSONDecodeError as exc:
        if tool_name == "keyboard_text":
            return {"text": raw_input.strip()}
        raise ValueError(f"tool input must be valid JSON: {raw_input}") from exc
    if isinstance(parsed, str) and tool_name == "keyboard_text":
        return {"text": parsed}
    if not isinstance(parsed, dict):
        raise ValueError("tool input must be a JSON object")
    return parsed


def _decode_tool_input(request_body: dict[str, Any]) -> str:
    if request_body.get("raw_input") is not None:
        return str(request_body["raw_input"])
    if "input" not in request_body or request_body["input"] in (None, ""):
        return ""
    value = request_body["input"]
    if isinstance(value, str):
        trimmed = value.strip()
        if trimmed in {"", "null"}:
            return ""
        if trimmed.startswith('"') and trimmed.endswith('"'):
            try:
                decoded = json.loads(trimmed)
                if isinstance(decoded, str):
                    return decoded
            except json.JSONDecodeError:
                pass
        return trimmed
    return json.dumps(value)


def _point_arg(
    tool_input: dict[str, Any], *, field: str = "point", x_key: str = "x", y_key: str = "y"
) -> dict[str, float]:
    point = tool_input.get(field)
    if isinstance(point, dict):
        unknown = sorted(set(point) - {"x", "y"})
        if unknown:
            raise ValueError(f"unknown {field} fields: {unknown!r}")
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
    tool_input: dict[str, Any], *, field: str = "point", x_key: str = "x", y_key: str = "y"
) -> dict[str, float]:
    point = _point_arg(tool_input, field=field, x_key=x_key, y_key=y_key)
    if not 0 <= point["x"] <= 1000 or not 0 <= point["y"] <= 1000:
        raise ValueError("coordinates must use the normalized 0-1000 scale")
    return point


def _unknown_coordinate_tool_fields(tool_name: str, tool_input: dict[str, Any]) -> list[str]:
    allowed = {
        "touch_gesture": {
            "type", "point", "start", "end", "x", "y", "start_x", "start_y",
            "end_x", "end_y", "duration_ms", "hold_before_ms", "hold_after_ms",
            "hold_ms", "pause_ms", "steps", "distance", "anchor", "button", "strength",
        },
        "mouse_click": {"x", "y", "button"},
        "mouse_move": {"x", "y"},
    }.get(tool_name)
    return [] if allowed is None else sorted(set(tool_input) - allowed)


def _to_pixels(point: dict[str, float], width: int, height: int) -> tuple[int, int]:
    x = min(width - 1, max(0, round(point["x"] / 1000 * width)))
    y = min(height - 1, max(0, round(point["y"] / 1000 * height)))
    return int(x), int(y)


def _directional_swipe_payload(gesture_type: str, tool_input: dict[str, Any]) -> dict[str, Any]:
    strength = str(tool_input.get("strength", "") or "").strip().lower()
    if strength not in DIRECTIONAL_SWIPE_PRESETS:
        raise ValueError(f"unsupported strength: {strength!r}")
    preset_distance, preset_duration = DIRECTIONAL_SWIPE_PRESETS[strength]
    distance = _positive_float(tool_input.get("distance"), preset_distance)
    distance = _clamp(distance, 1, 1000)
    anchor = _clamp(_float_or_default(tool_input.get("anchor"), 500), 0, 1000)
    half = distance / 2
    if gesture_type == "swipe_left":
        start_x, end_x, start_y, end_y = anchor + half, anchor - half, anchor, anchor
    elif gesture_type == "swipe_right":
        start_x, end_x, start_y, end_y = anchor - half, anchor + half, anchor, anchor
    elif gesture_type == "swipe_up":
        start_x, end_x, start_y, end_y = anchor, anchor, anchor + half, anchor - half
    elif gesture_type == "swipe_down":
        start_x, end_x, start_y, end_y = anchor, anchor, anchor - half, anchor + half
    else:
        raise ValueError(f"unsupported directional swipe: {gesture_type}")
    return {
        "start_x": _clamp(start_x, 0, 1000), "start_y": _clamp(start_y, 0, 1000),
        "end_x": _clamp(end_x, 0, 1000), "end_y": _clamp(end_y, 0, 1000),
        "duration_ms": _duration_ms_arg(tool_input.get("duration_ms"), preset_duration),
    }


def _duration_ms_arg(value: Any, default: int, *, minimum: int = 1, maximum: int = MAX_ACTION_DURATION_MS) -> int:
    if value in (None, ""):
        return int(default)
    try:
        parsed = int(value)
    except (TypeError, ValueError) as exc:
        raise ValueError("duration must be an integer") from exc
    if parsed < minimum or parsed > maximum:
        raise ValueError(f"duration must be between {minimum} and {maximum} milliseconds")
    return parsed


def _finite_float(value: Any, name: str) -> float:
    try:
        parsed = float(value)
    except (TypeError, ValueError) as exc:
        raise ValueError(f"{name} must be a number") from exc
    if not math.isfinite(parsed):
        raise ValueError(f"{name} must be finite")
    return parsed


def _positive_float(value: Any, default: float) -> float:
    if value in (None, ""):
        return default
    parsed = float(value)
    return parsed if parsed > 0 else default


def _float_or_default(value: Any, default: float) -> float:
    return default if value in (None, "") else float(value)


def _clamp(value: float, minimum: float, maximum: float) -> float:
    return max(minimum, min(maximum, value))


def _post_action_output(screenshot: dict[str, Any]) -> dict[str, Any]:
    return {
        "action_output": "ok",
        "data": screenshot["data"],
        "width": screenshot["width"],
        "height": screenshot["height"],
        "format": screenshot.get("format", "jpeg"),
        "source_width": screenshot.get("source_width"),
        "source_height": screenshot.get("source_height"),
    }


def _quick_action_id(tool_input: dict[str, Any]) -> str:
    action = str(tool_input.get("action", "") or "").strip().lower().replace("-", "_")
    aliases = {
        "返回": "back", "go_back": "back", "navigate_back": "back",
        "recents": "app_switch", "switch_app": "app_switch", "task_switcher": "app_switch",
        "主屏": "home", "go_home": "home", "home_screen": "home",
        "spotlight": "spotlight_search", "global_search": "spotlight_search", "search": "spotlight_search",
        "notifications": "notification_center", "notification_shade": "notification_center", "通知": "notification_center",
        "quick_settings": "control_center", "quick_setting": "control_center", "快捷设置": "control_center",
        "close_panel": "dismiss_panel", "enter": "send", "press_enter": "send", "submit": "send",
        "settings": "open_settings", "system_settings": "open_settings", "设置": "open_settings",
    }
    return aliases.get(action, action)


def _quick_action_catalog(keyboard_available: bool) -> list[dict[str, str]]:
    actions = [
        {"id": "back", "status": "active", "tool": "quick_action"},
        {"id": "home", "status": "active", "tool": "quick_action"},
        {"id": "app_switch", "status": "active", "tool": "quick_action"},
        {"id": "notification_center", "status": "active", "tool": "quick_action"},
        {"id": "control_center", "status": "active", "tool": "quick_action"},
        {"id": "dismiss_panel", "status": "active", "tool": "quick_action"},
        {"id": "open_settings", "status": "active", "tool": "quick_action"},
        {"id": "spotlight_search", "status": "active", "tool": "quick_action"},
        {"id": "quit_app", "status": "active", "tool": "quick_action"},
        {"id": "copy", "status": "reserved", "tool": "keyboard_tap"},
        {"id": "paste", "status": "reserved", "tool": "keyboard_tap"},
    ]
    actions.append(
        {
            "id": "send",
            "status": "active" if keyboard_available else "unsupported",
            "tool": "keyboard_tap",
        }
    )
    return actions


def _unsupported_keyboard_result() -> dict[str, Any]:
    return {
        "output": "VPhone host-control keyboard capability is unavailable",
        "is_error": True,
        "error": "unsupported",
    }


def _reserved_action(action: str, reason: str) -> dict[str, Any]:
    return {
        "output": json.dumps({"ok": False, "action": action, "platform": "ios", "status": "reserved", "reason": reason}),
        "is_error": False,
    }
