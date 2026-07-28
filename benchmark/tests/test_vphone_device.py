from pathlib import Path
from types import SimpleNamespace

import pytest
from PIL import Image

from vphone.bridge.client import VPhoneSocketError
from vphone.bridge.device import (
    MAX_TEXT_LENGTH,
    GuestSSHConfig,
    VPhoneDevice,
    unsupported_vphone_text_chars,
)


class RecordingClient:
    def __init__(self):
        self.calls = []

    def request(self, payload, *, timeout_sec=None):
        self.calls.append((payload, timeout_sec))
        return {"ok": True}


@pytest.fixture()
def device():
    value = VPhoneDevice("/tmp/unused-vphone.sock", timeout_sec=0.1)
    value.client = RecordingClient()
    try:
        yield value
    finally:
        value.close()


def test_keyboard_charset_and_dynamic_timeout(device):
    accepted = "Az09 -=[]\\;'`,./!@#$%^&*()_+{}|:\"~<>?\t\n\r"
    assert unsupported_vphone_text_chars(accepted) == ""
    assert unsupported_vphone_text_chars("hello你好🙂") == "你好🙂"

    for length in (1, 100, MAX_TEXT_LENGTH):
        text = "a" * length
        device.keyboard_text(text)
        payload, timeout = device.client.calls[-1]
        assert payload == {"t": "keyboard_text", "text": text, "screen": False}
        assert timeout == 5.0 + length * 0.02


def test_keyboard_rejects_unsupported_or_oversized_text_before_socket(device):
    with pytest.raises(ValueError, match="unsupported"):
        device.keyboard_text("你好")
    with pytest.raises(ValueError, match="exceeds"):
        device.keyboard_text("a" * (MAX_TEXT_LENGTH + 1))
    assert device.client.calls == []


def test_app_validation_happens_before_socket(device):
    for invalid_bundle_id in ("", ".bad", "bad/id", "应用.example"):
        with pytest.raises(ValueError, match="bundle_id"):
            device.launch_app(invalid_bundle_id)
    assert device.client.calls == []

    device.launch_app("com.apple.Preferences")
    assert device.client.calls[0][0]["t"] == "app_launch"


def test_launch_app_falls_back_to_ssh_uiopen_for_legacy_host(monkeypatch):
    value = VPhoneDevice(
        "/tmp/unused-vphone.sock",
        guest_ssh=GuestSSHConfig(host="192.168.64.8", port=22222, user="root"),
    )

    class LegacyClient:
        def request(self, payload, *, timeout_sec=None):
            del payload, timeout_sec
            raise VPhoneSocketError("command_failed", "unknown command: app_launch")

    commands = []

    def fake_run(command, **kwargs):
        commands.append((command, kwargs))
        return SimpleNamespace(returncode=0, stdout="", stderr="")

    value.client = LegacyClient()
    monkeypatch.setattr("vphone.bridge.device.subprocess.run", fake_run)
    try:
        value.launch_app("com.apple.Preferences")
    finally:
        value.close()

    command, kwargs = commands[0]
    assert command[-4:] == [
        "root@192.168.64.8",
        "/var/jb/usr/bin/uiopen",
        "-b",
        "com.apple.Preferences",
    ]
    assert kwargs.get("shell", False) is False
    # The guest IP is re-detected per bridge start, so an unknown (fresh IP) or
    # changed (recycled IP) host key must not abort the launch under BatchMode.
    options = {command[i + 1] for i, arg in enumerate(command[:-1]) if arg == "-o"}
    assert "StrictHostKeyChecking=no" in options
    assert "UserKnownHostsFile=/dev/null" in options


def test_screenshot_uses_private_random_file_and_removes_it(device):
    paths: list[Path] = []

    class ScreenshotClient:
        def request(self, payload, *, timeout_sec=None):
            del timeout_sec
            path = Path(payload["path"])
            paths.append(path)
            Image.new("RGB", (1290, 2796), color=(20, 80, 140)).save(path, format="PNG")
            return {"ok": True, "path": str(path)}

    device.client = ScreenshotClient()
    jpeg, width, height, source_width, source_height = device.screenshot_jpeg()
    assert jpeg.startswith(b"\xff\xd8")
    assert (source_width, source_height) == (1290, 2796)
    assert width == 900 and height == round(2796 * 900 / 1290)
    assert len(paths) == 1
    assert paths[0].parent == Path(device._tempdir.name)
    assert not paths[0].exists()
