import importlib
import subprocess


def test_attempt_config_creates_fresh_dirs_and_renders_mobilegym_device(tmp_path):
    daemon = importlib.import_module("mobilegym.adapter.daemon")

    first = daemon.create_attempt_config(
        tmp_path,
        bridge_url="http://127.0.0.1:19001",
        bridge_token="device-one",
        control_token="control-one",
        model_provider="fake",
        model_name="fake-model",
        model_base_url="http://model.local",
        model_api_key="model-key",
    )
    second = daemon.create_attempt_config(
        tmp_path,
        bridge_url="http://127.0.0.1:19002",
        bridge_token="device-two",
        control_token="control-two",
        model_provider="fake",
    )

    assert first.config_dir != second.config_dir
    assert first.config_path.exists()
    assert second.config_path.exists()
    assert first.bridge_token_file.read_text() == "device-one"
    assert first.control_token_file.read_text() == "control-one"

    isolated_dirs = ["memory", "skill-state", "logs", "episode-records"]
    for dirname in isolated_dirs:
        first_path = first.config_dir / dirname
        second_path = second.config_dir / dirname
        assert first_path.exists()
        assert second_path.exists()
        assert first_path != second_path

    toml = first.config_path.read_text()
    assert 'backend = "mobilegym"' in toml
    assert 'bridge_url = "http://127.0.0.1:19001"' in toml
    assert f'bridge_token_file = "{first.bridge_token_file}"' in toml
    assert f'control_token_file = "{first.control_token_file}"' in toml


def test_rendered_agent_toml_keeps_root_keys_before_model_and_device(tmp_path):
    daemon = importlib.import_module("mobilegym.adapter.daemon")

    attempt = daemon.create_attempt_config(
        tmp_path,
        bridge_url="http://bridge.local",
        bridge_token="device-token",
        control_token="control-token",
        instruction="custom instruction",
        max_iterations=7,
        model_provider="openai",
        model_name="gpt-test",
        model_base_url="http://llm.local/v1",
        model_api_key="secret",
    )
    toml = attempt.config_path.read_text()

    assert toml.index('instruction = "custom instruction"') < toml.index("[model]")
    assert toml.index('input_mode = "text"') < toml.index("[model]")
    assert toml.index("max_iterations = 7") < toml.index("[model]")
    assert toml.index("[model]") < toml.index("[device]")
    assert 'provider = "openai"' in toml
    assert 'model = "gpt-test"' in toml
    assert 'base_url = "http://llm.local/v1"' in toml
    assert 'api_key = "secret"' in toml


def test_aiden_daemon_handle_marks_dirty_when_stop_or_kill_fails():
    daemon = importlib.import_module("mobilegym.adapter.daemon")

    class BadProcess:
        def terminate(self):
            raise OSError("terminate failed")

        def kill(self):
            raise OSError("kill failed")

    handle = daemon.AidenDaemonHandle(
        base_url="http://daemon.local",
        control_token="control-token",
        bridge_device_token="device-token",
        process=BadProcess(),
    )

    handle.stop()
    handle.kill()

    assert handle.dirty is True


def test_aiden_daemon_handle_kills_process_when_stop_wait_times_out():
    daemon = importlib.import_module("mobilegym.adapter.daemon")

    class SlowProcess:
        def __init__(self):
            self.terminated = False
            self.killed = False
            self.wait_timeouts = []

        def terminate(self):
            self.terminated = True

        def wait(self, *, timeout=None):
            self.wait_timeouts.append(timeout)
            if not self.killed:
                raise subprocess.TimeoutExpired("aiden-daemon", timeout)
            return 0

        def kill(self):
            self.killed = True

    process = SlowProcess()
    handle = daemon.AidenDaemonHandle(
        base_url="http://daemon.local",
        control_token="control-token",
        bridge_device_token="device-token",
        process=process,
    )

    handle.stop(timeout=0.01)

    assert process.terminated is True
    assert process.killed is True
    assert process.wait_timeouts == [0.01, 2]
