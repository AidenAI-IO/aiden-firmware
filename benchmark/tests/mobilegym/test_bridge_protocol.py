import base64

from mobilegym.bridge.protocol import (
    bridge_error,
    bridge_ok,
    encode_screenshot,
)


def test_bridge_response_helpers_use_consistent_json_shape():
    assert bridge_ok({"ready": True}) == {"ok": True, "data": {"ready": True}}
    assert bridge_error("bad_request", "invalid input", status=400) == {
        "ok": False,
        "error": {"code": "bad_request", "message": "invalid input"},
        "status": 400,
    }


def test_encode_screenshot_returns_base64_payload_with_format():
    encoded = encode_screenshot(b"fake-png", mime_type="image/png", width=10, height=20)

    assert encoded == {
        "width": 10,
        "height": 20,
        "format": "png",
        "size": len(b"fake-png"),
        "data": base64.b64encode(b"fake-png").decode("ascii"),
    }
