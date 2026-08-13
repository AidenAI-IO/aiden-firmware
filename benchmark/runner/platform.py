from __future__ import annotations

import json
import urllib.parse
import urllib.request
from typing import Any

VALID_TARGET_PLATFORMS = {"ios", "android", "mac"}
DEVICE_TYPE_BY_TARGET_PLATFORM = {
    "ios": "iOS",
    "android": "Android",
    "mac": "macOS",
}
ENVIRONMENT_API_SUFFIXES = (
    "/api/setup",
    "/api/release",
    "/api/screen",
    "/api/concurrent",
)


def platform_from_environment_health(health: dict[str, Any]) -> str:
    if "platform" in health:
        platform = str(health.get("platform") or "").strip().lower()
        if platform in {"macos", "darwin"}:
            platform = "mac"
        if platform in VALID_TARGET_PLATFORMS:
            return platform
        raise ValueError(f"unsupported environment platform: {health.get('platform')!r}")

    platform = str(health.get("device_platform") or "").strip().lower()
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


def device_type_from_target_platform(platform: str) -> str:
    return DEVICE_TYPE_BY_TARGET_PLATFORM.get(str(platform or "").strip().lower(), "")


def target_platform_from_device_type(device_type: str) -> str:
    normalized = str(device_type or "").strip().lower()
    if normalized == "ios":
        return "ios"
    if normalized == "android":
        return "android"
    if normalized in {"mac", "macos"}:
        return "mac"
    return ""


def environment_health_endpoint(environment_url: str) -> str:
    raw = str(environment_url or "").strip()
    parsed = urllib.parse.urlsplit(raw)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ValueError(f"invalid environment endpoint: {environment_url!r}")

    path = parsed.path.rstrip("/")
    for suffix in ENVIRONMENT_API_SUFFIXES:
        if path == suffix or path.endswith(suffix):
            path = path[: -len(suffix)]
            break
    if path != "/health" and not path.endswith("/health"):
        path = f"{path}/health" if path else "/health"
    return urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, path, "", ""))


def read_environment_health(environment_url: str, *, timeout: float = 0.5) -> dict[str, Any]:
    try:
        url = environment_health_endpoint(environment_url)
    except ValueError:
        return {}
    with urllib.request.urlopen(url, timeout=timeout) as response:
        payload = json.loads(response.read().decode("utf-8"))
    if isinstance(payload, dict) and isinstance(payload.get("data"), dict):
        return payload["data"]
    return payload if isinstance(payload, dict) else {}
