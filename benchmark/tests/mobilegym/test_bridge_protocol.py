import base64

from mobilegym.bridge.protocol import (
    BridgeTokens,
    bridge_error,
    bridge_ok,
    encode_screenshot,
)


def test_bridge_tokens_keep_runner_and_device_scopes_separate():
    tokens = BridgeTokens(control_token="control-token", device_token="device-token")

    assert tokens.require_control({"Authorization": "Bearer control-token"}) is True
    assert tokens.require_device({"Authorization": "Bearer device-token"}) is True
    assert tokens.require_control({"Authorization": "Bearer device-token"}) is False
    assert tokens.require_device({"Authorization": "Bearer control-token"}) is False
    assert tokens.require_control({}) is False
    assert tokens.require_device({"Authorization": "wrong"}) is False


def test_bridge_response_helpers_use_consistent_json_shape():
    assert bridge_ok({"ready": True}) == {"ok": True, "data": {"ready": True}}
    assert bridge_error("unauthorized", "wrong token", status=401) == {
        "ok": False,
        "error": {"code": "unauthorized", "message": "wrong token"},
        "status": 401,
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
