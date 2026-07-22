from pathlib import Path

import pytest
from PIL import Image

from vphone.bridge.device import MAX_TEXT_LENGTH, VPhoneDevice, unsupported_vphone_text_chars


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


def test_app_and_url_validation_happens_before_socket(device):
    for invalid_bundle_id in ("", ".bad", "bad/id", "应用.example"):
        with pytest.raises(ValueError, match="bundle_id"):
            device.launch_app(invalid_bundle_id)
    for invalid_url in ("", "javascript:alert(1)", "file:///tmp/a", "https:///missing-host"):
        with pytest.raises(ValueError, match="absolute http/https"):
            device.open_url(invalid_url)
    assert device.client.calls == []

    device.launch_app("com.apple.Preferences")
    device.open_url("https://www.apple.com.cn/")
    assert device.client.calls[0][0]["t"] == "app_launch"
    assert device.client.calls[1][0]["t"] == "open_url"


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
