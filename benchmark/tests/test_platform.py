import json
import urllib.error

import pytest

from runner.platform import (
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


def _http_error(url, code):
    return urllib.error.HTTPError(url, code, "error", {}, None)


def test_read_environment_health_falls_back_to_device_status_on_404(monkeypatch):
    seen = []

    def fake_urlopen(url, timeout=None):
        seen.append(url)
        if url.endswith("/health"):
            raise _http_error(url, 404)
        return FakeResponse({"connected": True, "device_type": "macOS"})

    monkeypatch.setattr("runner.platform.urllib.request.urlopen", fake_urlopen)

    health = read_environment_health("http://device.local:8080")

    assert health == {
        "status": "ok",
        "bridge_type": "go-agent",
        "device_platform": "macOS",
        "concurrent": 1,
    }
    assert resolve_environment_platform(health) is TargetPlatform.MAC
    assert seen == [
        "http://device.local:8080/health",
        "http://device.local:8080/api/phone-bridge/status",
    ]


def test_read_environment_health_fallback_requires_a_device_type(monkeypatch):
    def fake_urlopen(url, timeout=None):
        if url.endswith("/health"):
            raise _http_error(url, 404)
        return FakeResponse({"connected": False})

    monkeypatch.setattr("runner.platform.urllib.request.urlopen", fake_urlopen)

    with pytest.raises(ValueError, match="exposes neither /health"):
        read_environment_health("http://device.local:8080")


def test_read_environment_health_propagates_non_404_errors(monkeypatch):
    def fake_urlopen(url, timeout=None):
        raise _http_error(url, 503)

    monkeypatch.setattr("runner.platform.urllib.request.urlopen", fake_urlopen)

    with pytest.raises(urllib.error.HTTPError):
        read_environment_health("http://device.local:8080")


def test_platform_from_environment_health_supports_windows_and_linux():
    assert platform_from_environment_health({"platform": "Windows"}) == "windows"
    assert platform_from_environment_health({"platform": "LINUX"}) == "linux"


def test_platform_from_environment_health_rejects_invalid_canonical_platform():
    with pytest.raises(ValueError, match="unsupported environment platform"):
        platform_from_environment_health(
            {"platform": "chromeos", "bridge_type": "mobilegym"}
        )


def test_platform_from_environment_health_uses_legacy_fields_when_platform_is_absent():
    assert platform_from_environment_health({"device_platform": "macos"}) == "mac"
    assert platform_from_environment_health({"bridge_type": "mobilegym"}) == "android"


def test_resolve_environment_platform_returns_canonical_resolution():
    assert resolve_environment_platform({"platform": "Darwin"}) is TargetPlatform.MAC


def test_resolve_environment_platform_supports_legacy_health():
    assert resolve_environment_platform({"bridge_type": "vphone_ios"}) is TargetPlatform.IOS


def test_resolve_environment_platform_rejects_missing_platform():
    with pytest.raises(ValueError, match="does not report a supported platform"):
        resolve_environment_platform({"bridge_type": "unknown"})


def test_resolve_environment_platform_treats_cli_platform_as_constraint():
    assert resolve_environment_platform(
        {"platform": "ANDROID"}, constraint="android"
    ) is TargetPlatform.ANDROID

    with pytest.raises(ValueError, match="expected ios.*reported android"):
        resolve_environment_platform({"platform": "android"}, constraint="ios")


def test_resolve_mock_platform_validates_constraint():
    assert resolve_mock_platform("iOS", constraint="auto") is TargetPlatform.IOS

    with pytest.raises(ValueError, match="expected android.*reported ios"):
        resolve_mock_platform("ios", constraint="android")


def test_resolve_daemon_platform_validates_constraint():
    assert resolve_daemon_platform("macOS", constraint="mac") is TargetPlatform.MAC
    assert resolve_daemon_platform("Windows", constraint="windows") is TargetPlatform.WINDOWS
    assert resolve_daemon_platform("LINUX", constraint="linux") is TargetPlatform.LINUX

    with pytest.raises(ValueError, match="expected ios.*reported android"):
        resolve_daemon_platform("android", constraint="ios")
