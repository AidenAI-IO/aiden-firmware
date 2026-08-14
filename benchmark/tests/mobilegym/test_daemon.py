import tomllib

from mobilegym.adapter import daemon


def test_render_agent_toml_uses_default_system_instruction(tmp_path):
    rendered = daemon.render_agent_toml(
        bridge_url="http://127.0.0.1:19090",
        bridge_token_file=tmp_path / "bridge.token",
        control_token_file=tmp_path / "control.token",
    )

    assert tomllib.loads(rendered)["instruction"] == ""


def test_render_agent_toml_preserves_template_device_type(tmp_path):
    template = tmp_path / "agent.toml.template"
    template.write_text(
        '[model]\nprovider = "fake"\n[device]\ndevice_type = "iOS"\n',
        encoding="utf-8",
    )

    rendered = daemon.render_agent_toml(
        bridge_url="http://127.0.0.1:19090",
        bridge_token_file=tmp_path / "bridge.token",
        control_token_file=tmp_path / "control.token",
        template_path=template,
    )

    assert tomllib.loads(rendered)["device"]["device_type"] == "iOS"


def test_launch_daemon_passes_android_as_runtime_device_type(tmp_path, monkeypatch):
    attempt = daemon.create_attempt_config(
        tmp_path,
        bridge_url="http://127.0.0.1:19090",
    )
    captured = {}

    class FakeProcess:
        pass

    def fake_popen(command, **kwargs):
        captured["command"] = command
        return FakeProcess()

    monkeypatch.setattr(daemon.subprocess, "Popen", fake_popen)

    daemon.launch_daemon(
        ["aiden-agent", "daemon", "-dir", str(attempt.config_dir)],
        attempt_config=attempt,
        base_url="http://127.0.0.1:18080",
    )

    assert captured["command"][-2:] == ["--device-type", "android"]


def test_launch_daemon_preserves_explicit_device_type_equals_form(
    tmp_path, monkeypatch
):
    attempt = daemon.create_attempt_config(
        tmp_path,
        bridge_url="http://127.0.0.1:19090",
    )
    captured = {}

    def fake_popen(command, **kwargs):
        captured["command"] = command
        return object()

    monkeypatch.setattr(daemon.subprocess, "Popen", fake_popen)

    daemon.launch_daemon(
        ["aiden-agent", "daemon", "--device-type=android"],
        attempt_config=attempt,
        base_url="http://127.0.0.1:18080",
    )

    assert captured["command"].count("--device-type=android") == 1
    assert "--device-type" not in captured["command"]
    assert "--target-platform" not in captured["command"]
