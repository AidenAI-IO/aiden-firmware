from __future__ import annotations

import json
import urllib.parse
import urllib.request
from typing import Any


VALID_TARGET_PLATFORMS = {"ios", "android", "mac"}


def platform_from_environment_health(health: dict[str, Any]) -> str:
    platform = str(health.get("platform") or health.get("device_platform") or "").strip().lower()
    if platform in {"macos", "darwin"}:
        platform = "mac"
    if platform in VALID_TARGET_PLATFORMS:
        return platform

    bridge_type = str(health.get("bridge_type") or "").strip().lower()
    if bridge_type in {"adb_android", "mobilegym"}:
        return "android"
    if bridge_type == "vphone_ios":
        return "ios"
    return ""


def read_environment_health(environment_url: str, *, timeout: float = 0.5) -> dict[str, Any]:
    parsed = urllib.parse.urlsplit(str(environment_url).strip())
    if not parsed.scheme or not parsed.netloc:
        return {}
    url = urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, "/health", "", ""))
    with urllib.request.urlopen(url, timeout=timeout) as response:
        payload = json.loads(response.read().decode("utf-8"))
    if isinstance(payload, dict) and isinstance(payload.get("data"), dict):
        return payload["data"]
    return payload if isinstance(payload, dict) else {}


def read_environment_platform(environment_url: str, *, timeout: float = 0.5) -> str:
    try:
        return platform_from_environment_health(read_environment_health(environment_url, timeout=timeout))
    except Exception:
        return ""
