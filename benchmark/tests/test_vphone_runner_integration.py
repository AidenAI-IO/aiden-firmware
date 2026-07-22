import io
from pathlib import Path

from PIL import Image

from runner.suite import load_suite
from runner.webui import endpoint_for_docker
from vphone.scripts.validate_bridge import validate_bridge

from tests.test_vphone_bridge import FakeVPhoneDevice
from vphone.bridge.server import VPhoneBridgeServer


def test_vphone_ios_basic_suite_loads():
    suite = load_suite(Path(__file__).parents[1] / "suites" / "vphone_ios_basic.json")
    assert suite.name == "vphone_ios_basic"
    assert [task.id for task in suite.tasks] == [
        "screenshot_home",
        "go_home",
        "open_settings",
        "swipe_screen",
        "type_english_text",
        "clock_count_alarms",
        "settings_check_wifi",
        "open_app_library",
    ]
    assert all(task.repeats == 1 for task in suite.tasks)


def test_vphone_loopback_endpoint_is_rewritten_for_agent_container():
    assert endpoint_for_docker("http://127.0.0.1:8899") == "http://host.docker.internal:8899"


def test_live_validation_flow_matches_runner_contract(tmp_path):
    class ProbeDevice(FakeVPhoneDevice):
        def screenshot_jpeg(self):
            self.calls.append(("screenshot_jpeg",))
            output = io.BytesIO()
            Image.new("RGB", (72, 156), color=(25, 90, 160)).save(output, format="JPEG")
            return output.getvalue(), 72, 156, self.width, self.height

    server = VPhoneBridgeServer(ProbeDevice(), port=0, action_settle_sec=0)
    endpoint = server.start()
    screenshot = tmp_path / "probe.jpg"
    try:
        result = validate_bridge(endpoint, screenshot_path=screenshot)
    finally:
        server.stop()
    assert result["ok"] is True
    assert result["checks"] == [
        "health",
        "concurrent=1",
        "screen-jpeg",
        "setup-home",
        "ownership-429",
        "tool-catalog",
        "screenshot-tool",
        "release",
    ]
    with Image.open(screenshot) as image:
        assert image.format == "JPEG"
        assert image.mode == "RGB"
        assert image.size == (72, 156)
