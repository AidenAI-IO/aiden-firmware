import json

import pytest

from runner.platform import (
    PlatformResolution,
    PlatformSource,
    TargetPlatform,
    platform_from_environment_health,
    read_environment_health,
    resolve_daemon_platform,
    resolve_environment_platform,
    resolve_mock_platform,
)


class FakeResponse:
    def __init__(self, payload):
        self._body = json.dumps(payload).encode("utf-8")

    def read(self):
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


def test_read_environment_health_preserves_endpoint_path_prefix(monkeypatch):
    seen = {}

    def fake_urlopen(url, timeout=None):
        seen["url"] = url
        seen["timeout"] = timeout
        return FakeResponse({"platform": "android"})

    monkeypatch.setattr("runner.platform.urllib.request.urlopen", fake_urlopen)

    assert read_environment_health("https://example.com/bridge", timeout=1.5) == {
        "platform": "android"
    }
    assert seen == {"url": "https://example.com/bridge/health", "timeout": 1.5}


def test_read_environment_health_treats_input_as_base_url(monkeypatch):
    seen = {}

    def fake_urlopen(url, timeout=None):
        seen["url"] = url
        return FakeResponse({"data": {"platform": "ios"}})

    monkeypatch.setattr("runner.platform.urllib.request.urlopen", fake_urlopen)

    assert read_environment_health("https://example.com/bridge/api") == {
        "platform": "ios"
    }
    assert seen["url"] == "https://example.com/bridge/api/health"


def test_platform_from_environment_health_rejects_invalid_canonical_platform():
    with pytest.raises(ValueError, match="unsupported environment platform"):
        platform_from_environment_health(
            {"platform": "windows", "bridge_type": "mobilegym"}
        )


def test_platform_from_environment_health_uses_legacy_fields_when_platform_is_absent():
    assert platform_from_environment_health({"device_platform": "macos"}) == "mac"
    assert platform_from_environment_health({"bridge_type": "mobilegym"}) == "android"


def test_resolve_environment_platform_returns_canonical_resolution():
    assert resolve_environment_platform({"platform": "Darwin"}) == PlatformResolution(
        platform=TargetPlatform.MAC,
        source=PlatformSource.ENVIRONMENT_HEALTH,
    )


def test_cli_constraint_platform_source_is_canonical():
    assert PlatformSource.CLI_CONSTRAINT.value == "cli_constraint"


def test_resolve_environment_platform_marks_legacy_health_source():
    assert resolve_environment_platform({"bridge_type": "vphone_ios"}) == PlatformResolution(
        platform=TargetPlatform.IOS,
        source=PlatformSource.LEGACY_ENVIRONMENT_HEALTH,
    )


def test_resolve_environment_platform_rejects_missing_platform():
    with pytest.raises(ValueError, match="does not report a supported platform"):
        resolve_environment_platform({"bridge_type": "unknown"})


def test_resolve_environment_platform_treats_cli_platform_as_constraint():
    assert resolve_environment_platform(
        {"platform": "ANDROID"}, constraint="android"
    ).platform is TargetPlatform.ANDROID

    with pytest.raises(ValueError, match="expected ios.*reported android"):
        resolve_environment_platform({"platform": "android"}, constraint="ios")


def test_resolve_mock_platform_has_its_own_source_and_constraint_validation():
    assert resolve_mock_platform("iOS", constraint="auto") == PlatformResolution(
        platform=TargetPlatform.IOS,
        source=PlatformSource.MOCK_ENVIRONMENT,
    )

    with pytest.raises(ValueError, match="expected android.*reported ios"):
        resolve_mock_platform("ios", constraint="android")


def test_resolve_daemon_platform_has_its_own_source_and_constraint_validation():
    assert resolve_daemon_platform("macOS", constraint="mac") == PlatformResolution(
        platform=TargetPlatform.MAC,
        source=PlatformSource.DAEMON_STATUS,
    )

    with pytest.raises(ValueError, match="expected ios.*reported android"):
        resolve_daemon_platform("android", constraint="ios")
