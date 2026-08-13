import tomllib

from mobilegym.adapter.daemon import render_agent_toml


def test_render_agent_toml_sets_android_device_type():
    rendered = render_agent_toml(
        bridge_url="http://127.0.0.1:9090",
        bridge_token_file="/tmp/bridge.token",
        control_token_file="/tmp/control.token",
    )

    config = tomllib.loads(rendered)
    assert config["device"]["device_type"] == "Android"
    assert config["device"]["backend"] == "mobilegym"
