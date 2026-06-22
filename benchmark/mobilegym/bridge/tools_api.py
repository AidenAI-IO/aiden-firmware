"""Unified /api/tools endpoint compatible with Go agent tool proxy.

This module implements a tool catalog and invocation API that matches the Go agent's
/api/tools interface, making the MobileGym bridge server compatible as a tool-proxy-endpoint.
"""

from __future__ import annotations

import asyncio
import json
import math
import time
from http.server import BaseHTTPRequestHandler
from typing import Any

from .actions import action_to_dict, build_action
from .episode import BridgeEpisodeState, BridgeTaskRouter, MissingBenchmarkTaskIDError, NoBridgeEnvAvailableError
from .protocol import encode_screenshot


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
US_KEYBOARD_TEXT_CHARS = set(
    "abcdefghijklmnopqrstuvwxyz"
    "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
    "0123456789"
    " \n\r\t"
    "-=[]\\;'`,./"
    "!@#$%^&*()_+{}|:\"~<>?"
)


class ToolsAPIHandler:
    """Handler for /api/tools endpoint compatible with Go agent tool proxy."""

    def __init__(self, state: BridgeEpisodeState | BridgeTaskRouter, request_timeout_sec: float = 30):
        self.router = BridgeTaskRouter.from_state(state)
        self.state = self.router.default_state
        self.request_timeout_sec = request_timeout_sec

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
        tools = [
            {
                "name": "screenshot",
                "description": "Capture a screenshot from the MobileGym simulator. No input required (pass empty JSON {} or \"\"). Returns a JSON object with width, height, and base64-encoded JPEG image data.",
                "args_schema": {
                    "type": "object",
                    "properties": {},
                    "additionalProperties": False,
                },
            },
            {
                "name": "touch_gesture",
                "description": "Perform touch gestures on the MobileGym simulator (tap, swipe, drag, long_press, etc.).",
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
                "description": "Type text into the MobileGym simulator.",
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
                "description": "Press keyboard keys in the MobileGym simulator (e.g., enter, back, home).",
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
                "name": "mouse_click",
                "description": "Click/tap a coordinate in the MobileGym simulator. Coordinates use normalized 0-1000 space.",
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
                "description": "Move the pointer. MobileGym has no hover state, so this is accepted as a no-op and returns a screenshot.",
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
                "description": "Scroll using a MobileGym swipe approximation.",
                "args_schema": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {"delta": {"type": "integer", "minimum": -127, "maximum": 127}},
                    "required": ["delta"],
                },
            },
            {
                "name": "quick_action",
                "description": "Execute common platform navigation actions such as back or home.",
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
        # Read request body
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

        # Decode input (matches Go agent's decodeToolInvokeInput)
        raw_input = self._decode_tool_input(request_body)

        # Parse input as JSON (tools expect JSON object input). keyboard_text
        # intentionally mirrors the Go tool's plain-text compatibility fallback.
        try:
            if raw_input.strip() in ("", "null", "{}"):
                tool_input = {}
            else:
                parsed_input = json.loads(raw_input)
                if tool_name == "keyboard_text" and isinstance(parsed_input, str):
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
            if tool_name == "keyboard_text":
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

        # Check active episode
        try:
            state = self.router.state_for_headers(handler.headers)
        except MissingBenchmarkTaskIDError as exc:
            self._send_json(
                handler,
                400,
                {"error": "missing_benchmark_task_id", "output": str(exc), "is_error": True},
            )
            return
        except NoBridgeEnvAvailableError as exc:
            self._send_json(
                handler,
                429,
                {"error": "no_bridge_env_available", "output": str(exc), "is_error": True},
            )
            return
        session, ok = self._get_active_session(state, tool_input)
        if not ok:
            self._send_json(
                handler,
                409,
                {"error": "no_active_episode", "output": "no active episode; call episode/start first", "is_error": True},
            )
            return

        # Execute tool
        started_at = time.time()
        try:
            result = self._submit_tool_call(state, tool_name, tool_input)
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

        except TimeoutError:
            duration_ms = int((time.time() - started_at) * 1000)
            self._send_json(
                handler,
                504,
                {
                    "output": "tool execution timed out",
                    "is_error": True,
                    "error": "timeout",
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

    def _decode_tool_input(self, request_body: dict[str, Any]) -> str:
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

    def _get_active_session(self, state: BridgeEpisodeState, tool_input: dict[str, Any]) -> tuple[dict[str, str], bool]:
        """Get active episode session."""
        episode_id = state.active_episode_id
        if not episode_id:
            return {}, False
        return {"episode_id": episode_id}, True

    def _submit_tool_call(self, state: BridgeEpisodeState, tool_name: str, tool_input: dict[str, Any]) -> dict[str, Any]:
        """Submit tool call to MobileGym environment."""
        episode_id = state.active_episode_id

        if tool_name == "screenshot":
            return self._call_screenshot(state, episode_id)
        elif tool_name == "touch_gesture":
            return self._call_touch_gesture(state, tool_input, episode_id)
        elif tool_name == "keyboard_text":
            return self._call_keyboard_text(state, tool_input, episode_id)
        elif tool_name == "keyboard_tap":
            return self._call_keyboard_tap(state, tool_input, episode_id)
        elif tool_name == "mouse_click":
            return self._call_mouse_click(state, tool_input, episode_id)
        elif tool_name == "mouse_move":
            return self._call_mouse_move(state, tool_input, episode_id)
        elif tool_name == "mouse_scroll":
            return self._call_mouse_scroll(state, tool_input, episode_id)
        elif tool_name == "quick_action":
            return self._call_quick_action(state, tool_input, episode_id)
        else:
            return {"output": f"unknown tool: {tool_name}", "is_error": True, "error": "unknown_tool"}

    def _call_screenshot(self, state: BridgeEpisodeState, episode_id: str) -> dict[str, Any]:
        """Execute screenshot tool."""

        async def get_screenshot(env: Any) -> dict[str, Any]:
            state.require_active(episode_id)
            observation = await _maybe_await(env.get_observation())
            screenshot = _encode_observation_screenshot(observation)
            return {"output": json.dumps(screenshot), "is_error": False}

        future = asyncio.run_coroutine_threadsafe(state.run_env(get_screenshot), state.owner_loop)
        return future.result(timeout=self.request_timeout_sec)

    def _call_touch_gesture(self, state: BridgeEpisodeState, tool_input: dict[str, Any], episode_id: str) -> dict[str, Any]:
        """Execute touch_gesture tool."""
        gesture_type = tool_input.get("type", "").strip().lower()
        if not gesture_type:
            return {"output": "error: type is required", "is_error": True}

        # Map gesture type to bridge action
        try:
            if gesture_type == "tap":
                point = _normalized_point_arg(tool_input, default_space="normalized")
                action = build_action("tap", point)
            elif gesture_type == "double_tap":
                point = _normalized_point_arg(tool_input, default_space="normalized")
                action = build_action("tap", {**point, "count": 2})
            elif gesture_type == "long_press":
                point = _normalized_point_arg(tool_input, default_space="normalized")
                action = build_action(
                    "tap",
                    {
                        **point,
                        "kind": "long_press",
                        "duration_ms": tool_input.get("duration_ms", tool_input.get("hold_ms", 500)),
                    },
                )
            elif gesture_type in ("swipe", "drag"):
                start = _normalized_point_arg(
                    tool_input,
                    field="start",
                    x_key="start_x",
                    y_key="start_y",
                    default_space="normalized",
                )
                end = _normalized_point_arg(
                    tool_input,
                    field="end",
                    x_key="end_x",
                    y_key="end_y",
                    default_space="normalized",
                )
                action = build_action(
                    gesture_type,
                    {
                        "start_x": start["x"],
                        "start_y": start["y"],
                        "end_x": end["x"],
                        "end_y": end["y"],
                        "duration_ms": tool_input.get("duration_ms", 300),
                    },
                )
            elif gesture_type in ("swipe_left", "swipe_right", "swipe_up", "swipe_down"):
                action = build_action("swipe", _directional_swipe_payload(gesture_type, tool_input))
            elif gesture_type == "back":
                action = build_action("back", {})
            elif gesture_type == "home":
                action = build_action("home", {})
            else:
                return {"output": f"error: unsupported gesture type: {gesture_type}", "is_error": True}
        except (TypeError, ValueError) as exc:
            return {"output": f"error: {exc}", "is_error": True}

        return self._execute_action(state, action, episode_id)

    def _call_mouse_click(self, state: BridgeEpisodeState, tool_input: dict[str, Any], episode_id: str) -> dict[str, Any]:
        """Execute mouse_click as a MobileGym tap."""
        button = str(tool_input.get("button", "left") or "left").strip().lower()
        if button not in ("", "left", "right", "middle"):
            return {"output": f"error: unsupported mouse button: {button!r}", "is_error": True}
        try:
            point = _normalized_point_arg(tool_input, default_space="auto")
        except (TypeError, ValueError) as exc:
            return {"output": f"error: {exc}", "is_error": True}
        action = build_action("tap", point)
        return self._execute_action(state, action, episode_id)

    def _call_mouse_move(self, state: BridgeEpisodeState, tool_input: dict[str, Any], episode_id: str) -> dict[str, Any]:
        """Validate mouse_move input and return a screenshot.

        MobileGym has no hover/pointer state, but accepting the tool keeps the
        proxy contract aligned with the local HID tools.
        """
        try:
            _normalized_point_arg(tool_input, default_space="auto")
        except (TypeError, ValueError) as exc:
            return {"output": f"error: {exc}", "is_error": True}
        return self._call_noop_with_screenshot(state, episode_id)

    def _call_mouse_scroll(self, state: BridgeEpisodeState, tool_input: dict[str, Any], episode_id: str) -> dict[str, Any]:
        """Execute mouse_scroll as an approximate vertical swipe."""
        try:
            delta = int(tool_input.get("delta", 0))
        except (TypeError, ValueError) as exc:
            return {"output": f"error: invalid delta: {exc}", "is_error": True}
        if delta == 0:
            return self._call_noop_with_screenshot(state, episode_id)
        strength = "medium" if abs(delta) >= 3 else "small"
        gesture_type = "swipe_up" if delta < 0 else "swipe_down"
        return self._call_touch_gesture(state, {"type": gesture_type, "strength": strength}, episode_id)

    def _call_quick_action(self, state: BridgeEpisodeState, tool_input: dict[str, Any], episode_id: str) -> dict[str, Any]:
        """Execute a small MobileGym-compatible quick_action subset."""
        platform = str(tool_input.get("platform", "") or "").strip().lower()
        if platform not in ("ios", "android", "mac"):
            return {"output": f"error: unsupported platform: {tool_input.get('platform')!r}", "is_error": True}

        action = _quick_action_id(tool_input)
        if bool(tool_input.get("list")) or action == "list":
            output = {
                "ok": True,
                "platform": platform,
                "actions": [
                    {"id": "back", "status": "active", "tool": "touch_gesture"},
                    {"id": "home", "status": "active", "tool": "touch_gesture"},
                    {"id": "notification_center", "status": "active", "tool": "touch_gesture"},
                    {"id": "quick_settings", "status": "active", "tool": "touch_gesture"},
                ],
            }
            return {"output": json.dumps(output), "is_error": False}

        if bool(tool_input.get("alternative")):
            return {"output": "error: mobilegym quick_action does not define alternative bindings", "is_error": True}

        if action == "back":
            return self._call_touch_gesture(state, {"type": "back"}, episode_id)
        if action == "home":
            return self._call_touch_gesture(state, {"type": "home"}, episode_id)
        if action == "notification_center":
            return self._call_touch_gesture(state, {"type": "swipe_down", "strength": "medium", "anchor": 500}, episode_id)
        if action == "quick_settings":
            action = build_action(
                "swipe",
                {"start_x": 850, "start_y": 0, "end_x": 850, "end_y": 700, "duration_ms": 500},
            )
            return self._execute_action(state, action, episode_id)

        return {"output": f"error: unsupported quick_action: {tool_input.get('action')!r}", "is_error": True}

    def _call_keyboard_text(self, state: BridgeEpisodeState, tool_input: dict[str, Any], episode_id: str) -> dict[str, Any]:
        """Execute keyboard_text tool."""
        text = tool_input.get("text", "")
        if not isinstance(text, str):
            return {"output": "error: text must be a string", "is_error": True}
        if text == "":
            return {"output": "error: text is required", "is_error": True}
        unsupported = _unsupported_keyboard_text_chars(text)
        if unsupported:
            return {
                "output": f"error: keyboard_text supports only US-keyboard ASCII characters; unsupported characters: {unsupported!r}",
                "is_error": True,
            }
        action = build_action("type_text", {"text": text})
        return self._execute_action(state, action, episode_id)

    def _call_keyboard_tap(self, state: BridgeEpisodeState, tool_input: dict[str, Any], episode_id: str) -> dict[str, Any]:
        """Execute keyboard_tap tool."""
        keys = tool_input.get("keys", [])
        if not keys:
            return {"output": "error: keys array is required", "is_error": True}
        if not isinstance(keys, list):
            return {"output": "error: keys must be an array", "is_error": True}

        # Handle meta+h -> home
        normalized_keys = [str(k).strip().lower() for k in keys if str(k).strip()]
        if not normalized_keys:
            return {"output": "error: at least one key or modifier is required", "is_error": True}
        has_meta = any(k in ("meta", "cmd", "super", "win") for k in normalized_keys)
        non_modifiers = [k for k in normalized_keys if k not in ("meta", "cmd", "super", "win", "ctrl", "alt", "shift")]

        try:
            if has_meta and len(non_modifiers) == 0:
                action = build_action("home", {})
            elif len(non_modifiers) == 0:
                return self._call_noop_with_screenshot(state, episode_id)
            elif has_meta and len(non_modifiers) == 1 and non_modifiers[0] == "h":
                action = build_action("home", {})
            elif len(non_modifiers) == 1:
                key = non_modifiers[0]
                if key in ("enter", "return"):
                    action = build_action("key", {"key": "enter"})
                elif key in ("home",):
                    action = build_action("key", {"key": "home"})
                elif key in ("back", "escape", "esc"):
                    action = build_action("key", {"key": "back"})
                else:
                    return {"output": f"error: mobilegym keyboard_tap does not support key: {key!r}", "is_error": True}
            else:
                return {"output": "error: mobilegym keyboard_tap supports one non-modifier key at a time", "is_error": True}
        except (TypeError, ValueError) as exc:
            return {"output": f"error: {exc}", "is_error": True}

        return self._execute_action(state, action, episode_id)

    def _execute_action(self, state: BridgeEpisodeState, action: Any, episode_id: str) -> dict[str, Any]:
        """Execute a MobileGym action and return result with screenshot."""
        action_payload = action_to_dict(action)

        async def step_env(env: Any) -> dict[str, Any]:
            state.require_active(episode_id)
            started = time.time()
            step_result = await _maybe_await(env.step(action))
            duration_ms = int((time.time() - started) * 1000)

            observation = _observation_value(step_result, "observation")
            if observation is None:
                observation = await _maybe_await(env.get_observation())

            screenshot = _encode_observation_screenshot(observation)

            # Format output as post-action screenshot result
            output_data = {
                "action_output": "ok",
                "data": screenshot["data"],
                "width": screenshot["width"],
                "height": screenshot["height"],
                "format": screenshot.get("format", "jpeg"),
            }

            return {"output": json.dumps(output_data), "is_error": False}

        future = asyncio.run_coroutine_threadsafe(state.run_env(step_env), state.owner_loop)
        return future.result(timeout=self.request_timeout_sec)

    def _call_noop_with_screenshot(self, state: BridgeEpisodeState, episode_id: str) -> dict[str, Any]:
        async def get_screenshot(env: Any) -> dict[str, Any]:
            state.require_active(episode_id)
            observation = await _maybe_await(env.get_observation())
            screenshot = _encode_observation_screenshot(observation)
            output_data = {
                "action_output": "ok",
                "data": screenshot["data"],
                "width": screenshot["width"],
                "height": screenshot["height"],
                "format": screenshot.get("format", "jpeg"),
            }
            return {"output": json.dumps(output_data), "is_error": False}

        future = asyncio.run_coroutine_threadsafe(state.run_env(get_screenshot), state.owner_loop)
        return future.result(timeout=self.request_timeout_sec)

    def _send_json(self, handler: BaseHTTPRequestHandler, status: int, payload: dict[str, Any]) -> None:
        """Send JSON response."""
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        handler.send_response(status)
        handler.send_header("Content-Type", "application/json")
        handler.send_header("Content-Length", str(len(data)))
        handler.end_headers()
        handler.wfile.write(data)


async def _maybe_await(value: Any) -> Any:
    if asyncio.iscoroutine(value):
        return await value
    return value


def _encode_observation_screenshot(observation: Any) -> dict[str, Any]:
    payload = _observation_value(observation, "screenshot")
    if payload is None:
        payload = _observation_value(observation, "screenshot_bytes")
    if isinstance(payload, str):
        payload = payload.encode("utf-8")
    if not isinstance(payload, bytes):
        raise ValueError("observation does not contain screenshot bytes")
    mime_type = _observation_value(observation, "mime_type") or _infer_screenshot_mime_type(payload)
    width = _observation_int(observation, "width") or _observation_int(observation, "screenshot_width")
    height = _observation_int(observation, "height") or _observation_int(observation, "screenshot_height")
    return encode_screenshot(payload, mime_type=str(mime_type), width=width, height=height)


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
) -> dict[str, float]:
    point = _point_arg(tool_input, field=field, x_key=x_key, y_key=y_key)
    coord_space = str(tool_input.get("coord_space", "") or "").strip().lower() or default_space
    if coord_space == "normalized":
        return {"x": _clamp(point["x"], 0.0, 1000.0), "y": _clamp(point["y"], 0.0, 1000.0)}
    if coord_space == "auto":
        if 0.0 <= point["x"] <= 1000.0 and 0.0 <= point["y"] <= 1000.0:
            return point
        raise ValueError("mobilegym coord_space auto only supports 0-1000 normalized coordinates")
    if coord_space == "absolute":
        return {
            "x": _clamp(point["x"], 0.0, HID_ABSOLUTE_MAX) / HID_ABSOLUTE_MAX * 1000.0,
            "y": _clamp(point["y"], 0.0, HID_ABSOLUTE_MAX) / HID_ABSOLUTE_MAX * 1000.0,
        }
    if coord_space == "pixel":
        raise ValueError("mobilegym coord_space pixel is not supported; use normalized coordinates")
    raise ValueError(f"unsupported coord_space: {coord_space!r}")


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


def _unsupported_keyboard_text_chars(text: str) -> str:
    return "".join(ch for ch in text if ch not in US_KEYBOARD_TEXT_CHARS)


def _quick_action_id(tool_input: dict[str, Any]) -> str:
    action = str(tool_input.get("action", "") or "").strip().lower().replace("-", "_")
    aliases = {
        "返回": "back",
        "go_back": "back",
        "navigate_back": "back",
        "主屏": "home",
        "go_home": "home",
        "home_screen": "home",
        "notifications": "notification_center",
        "notification_shade": "notification_center",
        "通知": "notification_center",
        "control_center": "quick_settings",
        "quick_setting": "quick_settings",
        "快捷设置": "quick_settings",
    }
    return aliases.get(action, action)


def _infer_screenshot_mime_type(payload: bytes) -> str:
    if payload.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    if payload.startswith(b"\xff\xd8"):
        return "image/jpeg"
    return "application/octet-stream"


def _observation_int(observation: Any, name: str) -> int | None:
    value = _observation_value(observation, name)
    if value is None:
        return None
    return int(value)


def _observation_value(observation: Any, name: str) -> Any:
    if isinstance(observation, dict):
        value = observation.get(name)
    else:
        value = getattr(observation, name, None)
    if callable(value):
        return value()
    return value
