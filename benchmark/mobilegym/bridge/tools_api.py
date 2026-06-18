"""Unified /api/tools endpoint compatible with Go agent tool proxy.

This module implements a tool catalog and invocation API that matches the Go agent's
/api/tools interface, making the MobileGym bridge server compatible as a tool-proxy-endpoint.
"""

from __future__ import annotations

import asyncio
import json
import time
from http.server import BaseHTTPRequestHandler
from typing import Any

from .actions import action_to_dict, build_action
from .episode import BridgeEpisodeState
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


class ToolsAPIHandler:
    """Handler for /api/tools endpoint compatible with Go agent tool proxy."""

    def __init__(self, state: BridgeEpisodeState, request_timeout_sec: float = 30):
        self.state = state
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
                    "properties": {
                        "type": {
                            "type": "string",
                            "enum": ["tap", "double_tap", "long_press", "swipe", "drag", "swipe_left", "swipe_right", "swipe_up", "swipe_down", "back", "home"],
                            "description": "Type of gesture to perform",
                        },
                        "point": {
                            "type": "object",
                            "properties": {"x": {"type": "number"}, "y": {"type": "number"}},
                            "description": "Point for tap/double_tap/long_press (normalized 0-1000)",
                        },
                        "start": {
                            "type": "object",
                            "properties": {"x": {"type": "number"}, "y": {"type": "number"}},
                            "description": "Start point for swipe/drag",
                        },
                        "end": {
                            "type": "object",
                            "properties": {"x": {"type": "number"}, "y": {"type": "number"}},
                            "description": "End point for swipe/drag",
                        },
                        "duration_ms": {"type": "integer", "description": "Duration in milliseconds"},
                        "distance": {"type": "number", "description": "Directional swipe travel in normalized 0-1000 units"},
                        "anchor": {"type": "number", "description": "Directional swipe fixed-axis coordinate in normalized 0-1000 units"},
                        "strength": {
                            "type": "string",
                            "enum": ["large", "medium", "small", "tiny", "default"],
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
                    "properties": {
                        "keys": {
                            "type": "array",
                            "items": {"type": "string"},
                            "description": "Keys to press (e.g., ['enter'], ['meta', 'h'])",
                        },
                    },
                    "required": ["keys"],
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

        # Parse input as JSON (tools expect JSON object input)
        try:
            if raw_input.strip() in ("", "null", "{}"):
                tool_input = {}
            else:
                tool_input = json.loads(raw_input)
        except json.JSONDecodeError:
            self._send_json(
                handler,
                400,
                {"error": "invalid_input", "output": f"tool input must be valid JSON: {raw_input}", "is_error": True},
            )
            return

        # Check active episode
        session, ok = self._get_active_session(tool_input)
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

    def _get_active_session(self, tool_input: dict[str, Any]) -> tuple[dict[str, str], bool]:
        """Get active episode session."""
        episode_id = self.state.active_episode_id
        if not episode_id:
            return {}, False
        return {"episode_id": episode_id}, True

    def _submit_tool_call(self, tool_name: str, tool_input: dict[str, Any]) -> dict[str, Any]:
        """Submit tool call to MobileGym environment."""
        episode_id = self.state.active_episode_id

        if tool_name == "screenshot":
            return self._call_screenshot(episode_id)
        elif tool_name == "touch_gesture":
            return self._call_touch_gesture(tool_input, episode_id)
        elif tool_name == "keyboard_text":
            return self._call_keyboard_text(tool_input, episode_id)
        elif tool_name == "keyboard_tap":
            return self._call_keyboard_tap(tool_input, episode_id)
        else:
            return {"output": f"unknown tool: {tool_name}", "is_error": True, "error": "unknown_tool"}

    def _call_screenshot(self, episode_id: str) -> dict[str, Any]:
        """Execute screenshot tool."""

        async def get_screenshot(env: Any) -> dict[str, Any]:
            self.state.require_active(episode_id)
            observation = await _maybe_await(env.get_observation())
            screenshot = _encode_observation_screenshot(observation)
            return {"output": json.dumps(screenshot), "is_error": False}

        future = asyncio.run_coroutine_threadsafe(self.state.run_env(get_screenshot), self.state.owner_loop)
        return future.result(timeout=self.request_timeout_sec)

    def _call_touch_gesture(self, tool_input: dict[str, Any], episode_id: str) -> dict[str, Any]:
        """Execute touch_gesture tool."""
        gesture_type = tool_input.get("type", "").strip().lower()
        if not gesture_type:
            return {"output": "error: type is required", "is_error": True}

        # Map gesture type to bridge action
        if gesture_type == "tap":
            point = tool_input.get("point", {})
            action = build_action("tap", {"x": point.get("x", 0), "y": point.get("y", 0)})
        elif gesture_type == "double_tap":
            point = tool_input.get("point", {})
            action = build_action("tap", {"x": point.get("x", 0), "y": point.get("y", 0), "count": 2})
        elif gesture_type == "long_press":
            point = tool_input.get("point", {})
            action = build_action(
                "tap",
                {
                    "x": point.get("x", 0),
                    "y": point.get("y", 0),
                    "kind": "long_press",
                    "duration_ms": tool_input.get("duration_ms", 500),
                },
            )
        elif gesture_type in ("swipe", "drag"):
            start = tool_input.get("start", {})
            end = tool_input.get("end", {})
            action = build_action(
                gesture_type,
                {
                    "start_x": start.get("x", 0),
                    "start_y": start.get("y", 0),
                    "end_x": end.get("x", 0),
                    "end_y": end.get("y", 0),
                    "duration_ms": tool_input.get("duration_ms", 300),
                },
            )
        elif gesture_type in ("swipe_left", "swipe_right", "swipe_up", "swipe_down"):
            try:
                action = build_action("swipe", _directional_swipe_payload(gesture_type, tool_input))
            except (TypeError, ValueError) as exc:
                return {"output": f"error: {exc}", "is_error": True}
        elif gesture_type == "back":
            action = build_action("back", {})
        elif gesture_type == "home":
            action = build_action("home", {})
        else:
            return {"output": f"error: unsupported gesture type: {gesture_type}", "is_error": True}

        return self._execute_action(action, episode_id)

    def _call_keyboard_text(self, tool_input: dict[str, Any], episode_id: str) -> dict[str, Any]:
        """Execute keyboard_text tool."""
        text = tool_input.get("text", "")
        action = build_action("type_text", {"text": text})
        return self._execute_action(action, episode_id)

    def _call_keyboard_tap(self, tool_input: dict[str, Any], episode_id: str) -> dict[str, Any]:
        """Execute keyboard_tap tool."""
        keys = tool_input.get("keys", [])
        if not keys:
            return {"output": "error: keys array is required", "is_error": True}

        # Handle meta+h -> home
        has_meta = any(k.lower() in ("meta", "cmd", "super", "win") for k in keys)
        non_modifiers = [k for k in keys if k.lower() not in ("meta", "cmd", "super", "win", "ctrl", "alt", "shift")]

        if has_meta and len(non_modifiers) == 1 and non_modifiers[0].lower() == "h":
            action = build_action("home", {})
        elif len(non_modifiers) == 1:
            key = non_modifiers[0].lower()
            if key in ("enter", "return"):
                action = build_action("key", {"key": "enter"})
            elif key in ("back", "escape", "esc"):
                action = build_action("key", {"key": "back"})
            else:
                action = build_action("key", {"key": key})
        else:
            return {"output": "error: mobilegym keyboard_tap supports one non-modifier key at a time", "is_error": True}

        return self._execute_action(action, episode_id)

    def _execute_action(self, action: Any, episode_id: str) -> dict[str, Any]:
        """Execute a MobileGym action and return result with screenshot."""
        action_payload = action_to_dict(action)

        async def step_env(env: Any) -> dict[str, Any]:
            self.state.require_active(episode_id)
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

        future = asyncio.run_coroutine_threadsafe(self.state.run_env(step_env), self.state.owner_loop)
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
