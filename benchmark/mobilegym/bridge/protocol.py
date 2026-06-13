from __future__ import annotations

import base64
import dataclasses as dc
from collections.abc import Mapping
from typing import Any


@dc.dataclass(frozen=True)
class BridgeTokens:
    control_token: str
    device_token: str

    def require_control(self, headers: Mapping[str, str]) -> bool:
        return self._bearer_token(headers) == self.control_token and bool(self.control_token)

    def require_device(self, headers: Mapping[str, str]) -> bool:
        return self._bearer_token(headers) == self.device_token and bool(self.device_token)

    @staticmethod
    def _bearer_token(headers: Mapping[str, str]) -> str | None:
        value = _header_value(headers, "Authorization")
        if not value:
            return None
        scheme, _, token = value.partition(" ")
        if scheme.lower() != "bearer" or not token:
            return None
        return token.strip()


def bridge_ok(data: Any | None = None) -> dict[str, Any]:
    return {"ok": True, "data": {} if data is None else data}


def bridge_error(code: str, message: str, status: int = 400) -> dict[str, Any]:
    return {"ok": False, "error": {"code": code, "message": message}, "status": status}


def encode_screenshot(
    payload: bytes,
    mime_type: str = "application/octet-stream",
    width: int | None = None,
    height: int | None = None,
) -> dict[str, Any]:
    if width is None or height is None:
        inferred = _image_dimensions(payload)
        if inferred is not None:
            width = inferred[0] if width is None else width
            height = inferred[1] if height is None else height
    if width is None or height is None:
        raise ValueError("screenshot width and height are required")
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


def _header_value(headers: Mapping[str, str], name: str) -> str | None:
    if hasattr(headers, "get"):
        value = headers.get(name)  # type: ignore[arg-type]
        if value:
            return str(value)
    lowered = name.lower()
    for key, value in headers.items():
        if key.lower() == lowered:
            return str(value)
    return None


def _image_dimensions(payload: bytes) -> tuple[int, int] | None:
    if payload.startswith(b"\x89PNG\r\n\x1a\n") and len(payload) >= 24:
        return int.from_bytes(payload[16:20], "big"), int.from_bytes(payload[20:24], "big")
    if not payload.startswith(b"\xff\xd8"):
        return None

    offset = 2
    while offset + 9 < len(payload):
        if payload[offset] != 0xFF:
            offset += 1
            continue
        marker = payload[offset + 1]
        offset += 2
        while marker == 0xFF and offset < len(payload):
            marker = payload[offset]
            offset += 1
        if marker in {0x01, *range(0xD0, 0xD9)}:
            continue
        if offset + 2 > len(payload):
            return None
        segment_length = int.from_bytes(payload[offset : offset + 2], "big")
        if segment_length < 2 or offset + segment_length > len(payload):
            return None
        if marker in {0xC0, 0xC1, 0xC2, 0xC3, 0xC5, 0xC6, 0xC7, 0xC9, 0xCA, 0xCB, 0xCD, 0xCE, 0xCF}:
            return int.from_bytes(payload[offset + 5 : offset + 7], "big"), int.from_bytes(
                payload[offset + 3 : offset + 5], "big"
            )
        offset += segment_length
    return None
