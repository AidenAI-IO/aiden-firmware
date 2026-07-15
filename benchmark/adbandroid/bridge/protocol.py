"""Response envelope helpers for the ADB Android environment bridge.

Mirrors benchmark/mobilegym/bridge/protocol.py so runner-side consumers
(capture.py, reset.py, read_environment_bridge_concurrency) see the same
shapes from both bridges.
"""

from __future__ import annotations

import base64
from typing import Any


def bridge_ok(data: Any | None = None) -> dict[str, Any]:
    return {"ok": True, "data": {} if data is None else data}


def bridge_error(code: str, message: str, status: int = 400) -> dict[str, Any]:
    return {"ok": False, "error": {"code": code, "message": message}, "status": status}


def encode_screenshot(payload: bytes, mime_type: str, width: int, height: int) -> dict[str, Any]:
    fmt = mime_type.split("/", 1)[-1] if "/" in mime_type else mime_type
    if fmt == "jpg":
        fmt = "jpeg"
    return {
        "width": int(width),
        "height": int(height),
        "format": fmt,
        "size": len(payload),
        "data": base64.b64encode(payload).decode("ascii"),
    }
