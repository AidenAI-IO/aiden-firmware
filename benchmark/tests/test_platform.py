import json

import pytest

from runner.platform import platform_from_environment_health, read_environment_health


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


def test_read_environment_health_normalizes_bridge_api_endpoint(monkeypatch):
    seen = {}

    def fake_urlopen(url, timeout=None):
        seen["url"] = url
        return FakeResponse({"data": {"platform": "ios"}})

    monkeypatch.setattr("runner.platform.urllib.request.urlopen", fake_urlopen)

    assert read_environment_health("https://example.com/bridge/api/screen?task=1") == {
        "platform": "ios"
    }
    assert seen["url"] == "https://example.com/bridge/health"


def test_platform_from_environment_health_rejects_invalid_canonical_platform():
    with pytest.raises(ValueError, match="unsupported environment platform"):
        platform_from_environment_health(
            {"platform": "windows", "bridge_type": "mobilegym"}
        )


def test_platform_from_environment_health_uses_legacy_fields_when_platform_is_absent():
    assert platform_from_environment_health({"device_platform": "macos"}) == "mac"
    assert platform_from_environment_health({"bridge_type": "mobilegym"}) == "android"
