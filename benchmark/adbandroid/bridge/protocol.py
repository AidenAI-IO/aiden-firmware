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


def encode_provider_frame(
    payload: bytes,
    *,
    width: int,
    height: int,
    pixel_format: str = "jpeg",
    backend: str = "adb",
    seq: int = 1,
    source_width: int | None = None,
    source_height: int | None = None,
    adb_device: dict[str, Any] | None = None,
) -> dict[str, Any]:
    source_width = int(source_width or width)
    source_height = int(source_height or height)
    capture_info: dict[str, Any] = {"capture_backend": backend}
    if adb_device:
        capture_info["adb_device"] = adb_device
    return {
        "meta": {
            "seq": int(seq),
            "width": int(width),
            "height": int(height),
            "source_width": source_width,
            "source_height": source_height,
            "crop_x": 0,
            "crop_y": 0,
            "crop_width": int(width),
            "crop_height": int(height),
            "pixel_format": pixel_format,
            "stride": 0,
            "bytes": len(payload),
            "stale": False,
        },
        "capture_info": capture_info,
        "image": base64.b64encode(payload).decode("ascii"),
    }
