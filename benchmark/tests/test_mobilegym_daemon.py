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


def test_render_agent_toml_template_sets_android_device_type(tmp_path):
    template = tmp_path / "agent.toml.template"
    template.write_text(
        '\n'.join(
            [
                '[device]',
                'device_type = "iOS"',
                'backend = "mobilegym"',
                'bridge_url = "{{BRIDGE_URL}}"',
                'control_token_file = "{{CONTROL_TOKEN_FILE}}"',
                '',
            ]
        ),
        encoding="utf-8",
    )

    rendered = render_agent_toml(
        bridge_url="http://127.0.0.1:9090",
        bridge_token_file="/tmp/bridge.token",
        control_token_file="/tmp/control.token",
        template_path=template,
    )

    config = tomllib.loads(rendered)
    assert config["device"]["device_type"] == "Android"
    assert config["device"]["bridge_url"] == "http://127.0.0.1:9090"
    assert config["device"]["control_token_file"] == "/tmp/control.token"


def test_render_agent_toml_template_adds_missing_device_type(tmp_path):
    template = tmp_path / "agent.toml.template"
    template.write_text(
        '\n'.join(
            [
                '[device] # MobileGym target',
                'backend = "mobilegym"',
                'bridge_url = "{{BRIDGE_URL}}"',
                '',
            ]
        ),
        encoding="utf-8",
    )

    rendered = render_agent_toml(
        bridge_url="http://127.0.0.1:9090",
        bridge_token_file="/tmp/bridge.token",
        control_token_file="/tmp/control.token",
        template_path=template,
    )

    config = tomllib.loads(rendered)
    assert config["device"]["device_type"] == "Android"
    assert config["device"]["backend"] == "mobilegym"
