from __future__ import annotations
import json
from typing import Any
from benchmark.runner.models import ToolCall, Trace


def _safe_loads(s: str) -> Any:
    try:
        return json.loads(s) if s else {}
    except (json.JSONDecodeError, TypeError):
        return None


def extract_trace(history: list[dict[str, Any]]) -> Trace:
    tool_calls: list[ToolCall] = []
    final_response = ""
    step = 0
    pending: dict[str, Any] | None = None
    for msg in history:
        mtype = msg.get("type")
        if mtype == "tool_call":
            step += 1
            args = _safe_loads(msg.get("tool_input", "")) or {}
            if not isinstance(args, dict):
                args = {}
            pending = {"step": step, "tool": msg.get("tool_name", ""),
                       "input": args, "has_screenshot": False}
        elif mtype == "tool_result" and pending is not None:
            content = _safe_loads(msg.get("content", ""))
            if isinstance(content, dict) and content.get("data"):
                pending["has_screenshot"] = True
            tool_calls.append(ToolCall(**pending))
            pending = None
        elif mtype == "assistant":
            final_response = msg.get("content", "")
    if pending is not None:
        tool_calls.append(ToolCall(**pending))
    return Trace(
        tool_calls=tool_calls,
        final_response=final_response,
        total_tool_calls=len(tool_calls),
        total_duration_ms=0,
    )


def extract_step_screenshots(history: list[dict[str, Any]]) -> list[tuple[str, str]]:
    """Returns list of (tool_name, base64_jpeg) pairs from tool_result messages."""
    result: list[tuple[str, str]] = []
    last_tool_name = ""
    for msg in history:
        if msg.get("type") == "tool_call":
            last_tool_name = msg.get("tool_name", "")
        elif msg.get("type") == "tool_result":
            content = _safe_loads(msg.get("content", ""))
            if isinstance(content, dict):
                data = content.get("data")
                if data:
                    result.append((last_tool_name or msg.get("tool_name", ""), data))
    return result
