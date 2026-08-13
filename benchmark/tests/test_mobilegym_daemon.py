import tomllib

from mobilegym.adapter.daemon import render_agent_toml


def test_render_agent_toml_injects_android_device_type_into_template(tmp_path):
    template = tmp_path / "agent.toml.template"
    template.write_text(
        '[model]\nprovider = "fake"\n[device]\ndevice_type = "iOS"\n',
        encoding="utf-8",
    )

    rendered = render_agent_toml(
        bridge_url="http://127.0.0.1:19090",
        bridge_token_file=tmp_path / "bridge.token",
        control_token_file=tmp_path / "control.token",
        template_path=template,
    )

    assert tomllib.loads(rendered)["device"]["device_type"] == "Android"
