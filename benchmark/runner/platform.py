from __future__ import annotations

import json
import urllib.request
from enum import Enum
from typing import Any

from runner.environment_endpoint import EnvironmentEndpoint


class TargetPlatform(str, Enum):
    IOS = "ios"
    ANDROID = "android"
    MAC = "mac"


VALID_TARGET_PLATFORMS = {platform.value for platform in TargetPlatform}


def normalize_target_platform(
    platform: Any,
    *,
    field: str = "target platform",
) -> TargetPlatform:
    if isinstance(platform, TargetPlatform):
        return platform
    normalized = str(platform or "").strip().lower()
    if normalized in {"macos", "darwin"}:
        normalized = TargetPlatform.MAC.value
    try:
        return TargetPlatform(normalized)
    except ValueError as exc:
        raise ValueError(f"unsupported {field}: {platform!r}") from exc


def _legacy_platform_from_environment_health(
    health: dict[str, Any],
) -> TargetPlatform | None:
    device_platform = str(health.get("device_platform") or "").strip()
    if device_platform:
        try:
            return normalize_target_platform(
                device_platform, field="environment device platform"
            )
        except ValueError:
            pass

    bridge_type = str(health.get("bridge_type") or "").strip().lower()
    if bridge_type in {"adb_android", "mobilegym"}:
        return TargetPlatform.ANDROID
    if bridge_type == "vphone_ios":
        return TargetPlatform.IOS
    return None


def _validate_platform_constraint(
    platform: TargetPlatform,
    constraint: str | TargetPlatform | None,
) -> None:
    raw_constraint = (
        constraint.value
        if isinstance(constraint, TargetPlatform)
        else str(constraint or "").strip().lower()
    )
    if not raw_constraint or raw_constraint == "auto":
        return
    expected = normalize_target_platform(constraint)
    if platform is not expected:
        raise ValueError(
            "target platform constraint does not match source: "
            f"expected {expected.value}, reported {platform.value}"
        )


def resolve_environment_platform(
    health: dict[str, Any],
    *,
    constraint: str | TargetPlatform | None = None,
) -> TargetPlatform:
    if "platform" in health:
        platform = normalize_target_platform(
            health.get("platform"), field="environment platform"
        )
    else:
        platform = _legacy_platform_from_environment_health(health)
        if platform is None:
            raise ValueError(
                "environment bridge health does not report a supported platform"
            )
    _validate_platform_constraint(platform, constraint)
    return platform


def resolve_mock_platform(
    platform: Any,
    *,
    constraint: str | TargetPlatform | None = None,
) -> TargetPlatform:
    resolved = normalize_target_platform(platform, field="mock environment platform")
    _validate_platform_constraint(resolved, constraint)
    return resolved


def resolve_daemon_platform(
    platform: Any,
    *,
    constraint: str | TargetPlatform | None = None,
) -> TargetPlatform:
    resolved = normalize_target_platform(platform, field="daemon target platform")
    _validate_platform_constraint(resolved, constraint)
    return resolved


def platform_from_environment_health(health: dict[str, Any]) -> str:
    """Return the canonical health platform for legacy string-based callers."""
    try:
        return resolve_environment_platform(health).value
    except ValueError:
        if "platform" in health:
            raise
        return ""


def read_environment_health(environment_url: str, *, timeout: float = 5.0) -> dict[str, Any]:
    try:
        url = EnvironmentEndpoint(environment_url).health
    except ValueError:
        return {}
    with urllib.request.urlopen(url, timeout=timeout) as response:
        payload = json.loads(response.read().decode("utf-8"))
    if isinstance(payload, dict) and isinstance(payload.get("data"), dict):
        return payload["data"]
    return payload if isinstance(payload, dict) else {}
