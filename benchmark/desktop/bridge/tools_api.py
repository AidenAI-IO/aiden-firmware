from __future__ import annotations

import json
import time
from http.server import BaseHTTPRequestHandler
from typing import Any

from .device import DesktopDevice, DesktopDeviceError
from .state import DesktopBridgeState, benchmark_task_id_from_headers


MAX_REQUEST_BODY_BYTES = 10 * 1024 * 1024


class DesktopToolsAPIHandler:
    def __init__(self, state: DesktopBridgeState, action_settle_sec: float = 0.2):
        self.state = state
        self.action_settle_sec = max(0.0, float(action_settle_sec))

    def catalog(self) -> list[dict[str, Any]]:
        point = {"type": "object", "additionalProperties": False, "properties": {"x": {"type": "number"}, "y": {"type": "number"}}, "required": ["x", "y"]}
        return [
            {"name": "touch_gesture", "description": "Perform a mouse gesture on the desktop using normalized 0-1000 coordinates.", "args_schema": {"type": "object", "properties": {"type": {"type": "string", "enum": ["tap", "double_tap", "long_press", "swipe", "drag", "swipe_left", "swipe_right", "swipe_up", "swipe_down", "back", "home"]}, "point": point, "start": point, "end": point, "duration_ms": {"type": "integer", "minimum": 0, "maximum": 10000}, "button": {"type": "string", "enum": ["left", "right", "middle"]}}, "required": ["type"]}},
            {"name": "keyboard_text", "description": "Type text into the focused desktop application.", "args_schema": {"type": "object", "properties": {"text": {"type": "string", "maxLength": 4096}}, "required": ["text"]}},
            {"name": "keyboard_tap", "description": "Press desktop keyboard keys; modifier keys may be combined.", "args_schema": {"type": "object", "properties": {"keys": {"type": "array", "minItems": 1, "maxItems": 6, "items": {"type": "string"}}}, "required": ["keys"]}},
            {"name": "enter_text", "description": "Type text into the currently focused desktop field.", "args_schema": {"type": "object", "properties": {"text": {"type": "string", "maxLength": 4096}, "focus": point}, "required": ["text"]}},
            {"name": "mouse_move", "description": "Move the desktop pointer to a normalized coordinate.", "args_schema": {"type": "object", "properties": {"x": {"type": "number"}, "y": {"type": "number"}}, "required": ["x", "y"]}},
            {"name": "mouse_scroll", "description": "Scroll the desktop pointer vertically.", "args_schema": {"type": "object", "properties": {"delta": {"type": "integer", "minimum": -127, "maximum": 127}}, "required": ["delta"]}},
            {"name": "quick_action", "description": "Execute common desktop actions such as back, home/show desktop, app switching, or settings.", "args_schema": {"type": "object", "properties": {"action": {"type": "string"}, "list": {"type": "boolean"}, "alternative": {"type": "boolean"}, "alternative_index": {"type": "integer", "minimum": 1}}, "anyOf": [{"required": ["action"]}, {"required": ["list"], "properties": {"list": {"const": True}}}]}},
        ]

    def handle_request(self, handler: BaseHTTPRequestHandler, path: str) -> None:
        if path == "/api/tools":
            if handler.command != "GET":
                self._send(handler, 405, {"error": "method_not_allowed", "output": "GET required", "is_error": True})
            else:
                self._send(handler, 200, {"tools": self.catalog()})
            return
        if path.startswith("/api/tools/"):
            if handler.command != "POST":
                self._send(handler, 405, {"error": "method_not_allowed", "output": "POST required", "is_error": True})
                return
            self._invoke(handler, path[len("/api/tools/"):])
            return
        self._send(handler, 404, {"error": "not_found", "output": "unknown endpoint", "is_error": True})

    def invoke(self, tool_name: str, tool_input: dict[str, Any]) -> dict[str, Any]:
        started = time.monotonic()
        try:
            result = self._submit_tool_call(tool_name, tool_input)
            return {"output": json.dumps(result, ensure_ascii=False), "is_error": False, "duration_ms": round((time.monotonic() - started) * 1000)}
        except (ValueError, DesktopDeviceError) as exc:
            return {"error": "desktop_tool_error", "output": str(exc), "is_error": True, "duration_ms": round((time.monotonic() - started) * 1000)}

    def _submit_tool_call(self, tool_name: str, data: dict[str, Any]) -> dict[str, Any]:
        device: DesktopDevice = self.state.device
        if tool_name == "touch_gesture":
            gesture = str(data.get("type") or "").strip().lower()
            button = str(data.get("button") or "left").strip().lower()
            if button not in {"left", "right", "middle"}:
                raise ValueError("button must be left, right, or middle")
            if gesture in {"back", "home"}:
                device.quick_action(gesture)
            elif gesture in {"tap", "double_tap", "long_press"}:
                point = _point(data.get("point", data))
                if gesture == "tap":
                    device.click(*point, button=button)
                elif gesture == "double_tap":
                    device.click(*point, button=button, clicks=2)
                else:
                    hold_ms = _duration(data.get("hold_ms", data.get("duration_ms", 500)))
                    device.long_press(*point, hold_sec=hold_ms / 1000, button=button)
            elif gesture in {"swipe", "drag"}:
                start_value = data.get("start", {"x": data.get("start_x"), "y": data.get("start_y")})
                end_value = data.get("end", {"x": data.get("end_x"), "y": data.get("end_y")})
                start, end = _point(start_value), _point(end_value)
                device.drag(start, end, duration=_duration(data.get("duration_ms", 500)) / 1000, button=button)
            elif gesture in {"swipe_left", "swipe_right", "swipe_up", "swipe_down"}:
                anchor = float(data.get("anchor", 500))
                distance = float(data.get("distance", 500))
                if gesture == "swipe_left": start, end = (anchor + distance / 2, anchor), (anchor - distance / 2, anchor)
                elif gesture == "swipe_right": start, end = (anchor - distance / 2, anchor), (anchor + distance / 2, anchor)
                elif gesture == "swipe_up": start, end = (anchor, anchor + distance / 2), (anchor, anchor - distance / 2)
                else: start, end = (anchor, anchor - distance / 2), (anchor, anchor + distance / 2)
                device.drag(start, end, duration=_duration(data.get("duration_ms", 500)) / 1000)
            else:
                raise ValueError(f"unsupported desktop gesture: {gesture!r}")
        elif tool_name == "keyboard_text":
            text = data.get("text")
            if not isinstance(text, str): raise ValueError("text is required")
            device.write(text)
        elif tool_name == "keyboard_tap":
            keys = data.get("keys")
            if not isinstance(keys, list) or not keys or not all(isinstance(k, str) for k in keys): raise ValueError("keys must be a non-empty string array")
            device.press(keys)
        elif tool_name == "enter_text":
            text = data.get("text")
            if not isinstance(text, str): raise ValueError("text is required")
            focus = data.get("focus")
            if focus is not None: device.click(*_point(focus))
            device.write(text)
        elif tool_name == "mouse_move":
            device.move(*_point(data))
        elif tool_name == "mouse_scroll":
            delta = data.get("delta")
            if isinstance(delta, bool) or not isinstance(delta, int): raise ValueError("delta must be an integer")
            device.scroll(max(-127, min(127, delta)))
        elif tool_name == "quick_action":
            if data.get("list"):
                return {"actions": ["back", "home", "show_desktop", "app_switch", "close_window", "open_settings"]}
            device.quick_action(str(data.get("action") or ""))
        else:
            raise ValueError(f"unknown desktop tool: {tool_name}")
        if self.action_settle_sec: time.sleep(self.action_settle_sec)
        return {"ok": True}

    def _invoke(self, handler: BaseHTTPRequestHandler, tool_name: str) -> None:
        try:
            self.state.check_task_access(benchmark_task_id_from_headers(handler.headers))
            raw = _read_body(handler)
            if "raw_input" in raw and raw["raw_input"] is not None: value = raw["raw_input"]
            else: value = raw.get("input", {})
            if isinstance(value, str): value = json.loads(value) if value.strip() else {}
            if not isinstance(value, dict): raise ValueError("tool input must be a JSON object")
            result = self.invoke(tool_name, value)
            self._send(handler, 200, {"tool": {"name": tool_name}, "raw_input": json.dumps(value, ensure_ascii=False), **result})
        except Exception as exc:
            self._send(handler, 400, {"error": str(exc), "output": str(exc), "is_error": True})

    @staticmethod
    def _send(handler: BaseHTTPRequestHandler, status: int, payload: dict[str, Any]) -> None:
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        handler.send_response(status); handler.send_header("Content-Type", "application/json"); handler.send_header("Content-Length", str(len(data))); handler.end_headers(); handler.wfile.write(data)


def _read_body(handler: BaseHTTPRequestHandler) -> dict[str, Any]:
    try: length = int(handler.headers.get("Content-Length", "0") or "0")
    except ValueError as exc: raise ValueError("invalid Content-Length") from exc
    if length > MAX_REQUEST_BODY_BYTES: raise ValueError("request body too large")
    raw = handler.rfile.read(length) if length else b"{}"
    value = json.loads(raw.decode("utf-8"))
    if not isinstance(value, dict): raise ValueError("JSON body must be an object")
    return value


def _point(value: Any) -> tuple[float, float]:
    if isinstance(value, (list, tuple)) and len(value) == 2:
        x, y = value
    elif isinstance(value, dict):
        x, y = value.get("x"), value.get("y")
    else:
        raise ValueError("point with x and y is required")
    if isinstance(x, bool) or isinstance(y, bool) or not isinstance(x, (int, float)) or not isinstance(y, (int, float)) or not (0 <= float(x) <= 1000 and 0 <= float(y) <= 1000): raise ValueError("x and y must be numbers in range [0, 1000]")
    return float(x), float(y)


def _duration(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, (int, float)): raise ValueError("duration must be a number")
    return max(0, min(10000, int(value)))
