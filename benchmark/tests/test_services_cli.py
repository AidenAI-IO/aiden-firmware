import json
from pathlib import Path

from runner import services, webui


def test_start_mobilegym_env_prints_environment_urls(tmp_path: Path, monkeypatch, capsys):
    commands = []
    health_urls = []

    monkeypatch.setattr(services, "ensure_mobilegym_image", lambda *args, **kwargs: None)
    monkeypatch.setattr(services, "wait_for_http_health", lambda url, timeout: health_urls.append((url, timeout)))

    def fake_check_output(command, cwd=None, text=False):
        commands.append((command, cwd, text))
        return "mobilegym-container\n"

    monkeypatch.setattr(services.subprocess, "check_output", fake_check_output)

    args = _ns(
        name="smoke",
        runs_dir=str(tmp_path),
        parallel_envs=3,
        web_port=18173,
        bridge_port=19090,
        mobilegym_image="aiden-mobilegym-simulator:test",
        no_build_mobilegym_image=True,
        ready_timeout_sec=12,
        json=True,
    )

    assert services.cmd_start_mobilegym_env(args) == 0

    payload = json.loads(capsys.readouterr().out)
    assert payload["environment_url"] == "http://127.0.0.1:19090"
    assert payload["docker_environment_url"] == "http://host.docker.internal:19090"
    assert payload["web_url"] == "http://127.0.0.1:18173"
    assert "screen_url" not in payload
    assert payload["container_name"] == "aiden-mobilegym-env-mobilegym-smoke"
    assert payload["parallel_envs"] == 3
    assert payload["agent_daemon_command"] == (
        "uv run python -m runner start-agent-daemon "
        "--environment-bridge-endpoint http://127.0.0.1:19090"
    )
    assert payload["stop_command"] == "docker rm -f aiden-mobilegym-env-mobilegym-smoke"
    assert health_urls == [("http://127.0.0.1:19090/health", 12)]
    command, cwd, text = commands[0]
    assert cwd == services.BENCHMARK_ROOT.parent
    assert text is True
    assert "aiden-mobilegym-simulator:test" in command
    assert "127.0.0.1:18173:4173" in command
    assert "19090:9090" in command
    assert "--parallel-envs 3" in command[-1]


def test_start_mobilegym_env_uses_docker_assigned_ports_when_auto(tmp_path: Path, monkeypatch, capsys):
    commands = []
    health_urls = []

    monkeypatch.setattr(services, "ensure_mobilegym_image", lambda *args, **kwargs: None)
    monkeypatch.setattr(services, "wait_for_http_health", lambda url, timeout: health_urls.append((url, timeout)))
    monkeypatch.setattr(
        services,
        "docker_published_port",
        lambda container_name, container_port: 18173 if container_port == 4173 else 19090,
    )

    def fake_check_output(command, cwd=None, text=False):
        commands.append((command, cwd, text))
        return "mobilegym-container\n"

    monkeypatch.setattr(services.subprocess, "check_output", fake_check_output)

    args = _ns(
        name="auto",
        runs_dir=str(tmp_path),
        parallel_envs=2,
        web_port=0,
        bridge_port=0,
        mobilegym_image="aiden-mobilegym-simulator:test",
        no_build_mobilegym_image=True,
        ready_timeout_sec=12,
        json=True,
    )

    assert services.cmd_start_mobilegym_env(args) == 0

    payload = json.loads(capsys.readouterr().out)
    assert payload["environment_url"] == "http://127.0.0.1:19090"
    assert payload["web_url"] == "http://127.0.0.1:18173"
    assert health_urls == [("http://127.0.0.1:19090/health", 12)]
    command = commands[0][0]
    assert "127.0.0.1::4173" in command
    assert "127.0.0.1::9090" in command


def test_start_agent_daemon_prints_agent_url_and_rewrites_environment_bridge(tmp_path: Path, monkeypatch, capsys):
    captured = {}

    monkeypatch.setattr(services, "ensure_daemon_image", lambda *args, **kwargs: None)
    monkeypatch.setattr(services, "_wait_for_agent", lambda *args, **kwargs: None)

    def fake_start_daemon_compose(job, **kwargs):
        captured["job"] = job
        captured["kwargs"] = kwargs
        return "agent-container"

    monkeypatch.setattr(services, "start_daemon_compose", fake_start_daemon_compose)
    monkeypatch.setattr(services, "stop_daemon_compose", lambda *args, **kwargs: None)

    args = _ns(
        port=18081,
        name="smoke",
        runs_dir=str(tmp_path),
        base_config_dir=str(tmp_path / "base"),
        agent_config="",
        daemon_image="aiden-agent-daemon:test",
        no_build_daemon_image=True,
        environment_bridge_endpoint="http://127.0.0.1:19090",
        benchmark_task_id="suite.json:t1",
        ready_timeout_sec=9,
        json=True,
    )

    assert services.cmd_start_agent_daemon(args) == 0

    payload = json.loads(capsys.readouterr().out)
    assert payload["agent_url"] == "http://127.0.0.1:18081"
    assert payload["environment_bridge_endpoint"] == "http://127.0.0.1:19090"
    assert payload["docker_environment_bridge_endpoint"] == "http://host.docker.internal:19090"
    assert payload["benchmark_task_id"] == "suite.json:t1"
    assert payload["container_id"] == "agent-container"
    assert payload["compose_project"] == "aiden-benchmark-agent-agent-smoke"
    assert "docker compose" in payload["stop_command"]

    assert captured["job"].agent_url == "http://127.0.0.1:18081"
    assert captured["job"].container_name == "aiden-benchmark-agent-agent-smoke"
    kwargs = captured["kwargs"]
    assert kwargs["image"] == "aiden-agent-daemon:test"
    assert kwargs["host_port"] == 18081
    assert kwargs["environment_bridge_endpoint"] == "http://host.docker.internal:19090"
    assert kwargs["benchmark_task_id"] == "suite.json:t1"
    assert kwargs["environment_bridge_mode"] is True


def test_start_agent_daemon_allows_no_environment_bridge(tmp_path: Path, monkeypatch, capsys):
    captured = {}

    monkeypatch.setattr(services, "ensure_daemon_image", lambda *args, **kwargs: None)
    monkeypatch.setattr(services, "_wait_for_agent", lambda *args, **kwargs: None)

    def fake_start_daemon_compose(job, **kwargs):
        captured["kwargs"] = kwargs
        return "agent-container"

    monkeypatch.setattr(services, "start_daemon_compose", fake_start_daemon_compose)
    monkeypatch.setattr(services, "stop_daemon_compose", lambda *args, **kwargs: None)

    args = _ns(
        port=18081,
        name="plain",
        runs_dir=str(tmp_path),
        base_config_dir=str(tmp_path / "base"),
        agent_config="",
        daemon_image="aiden-agent-daemon:test",
        no_build_daemon_image=True,
        environment_bridge_endpoint="",
        benchmark_task_id="suite.json:t1",
        ready_timeout_sec=9,
        json=True,
    )

    assert services.cmd_start_agent_daemon(args) == 0

    payload = json.loads(capsys.readouterr().out)
    assert payload["environment_bridge_endpoint"] == ""
    assert payload["benchmark_task_id"] == ""
    assert captured["kwargs"]["environment_bridge_endpoint"] == ""
    assert captured["kwargs"]["benchmark_task_id"] == ""
    assert captured["kwargs"]["environment_bridge_mode"] is False


def test_start_agent_daemon_uses_docker_assigned_port_when_auto(tmp_path: Path, monkeypatch, capsys):
    captured = {}

    monkeypatch.setattr(services, "ensure_daemon_image", lambda *args, **kwargs: None)
    monkeypatch.setattr(services, "_wait_for_agent", lambda *args, **kwargs: None)
    monkeypatch.setattr(services, "docker_published_port", lambda container_id, container_port: 18081)

    def fake_start_daemon_compose(job, **kwargs):
        captured["job"] = job
        captured["kwargs"] = kwargs
        return "agent-container"

    monkeypatch.setattr(services, "start_daemon_compose", fake_start_daemon_compose)
    monkeypatch.setattr(services, "stop_daemon_compose", lambda *args, **kwargs: None)

    args = _ns(
        port=0,
        name="auto",
        runs_dir=str(tmp_path),
        base_config_dir=str(tmp_path / "base"),
        agent_config="",
        daemon_image="aiden-agent-daemon:test",
        no_build_daemon_image=True,
        environment_bridge_endpoint="",
        benchmark_task_id="suite.json:t1",
        ready_timeout_sec=9,
        json=True,
    )

    assert services.cmd_start_agent_daemon(args) == 0

    payload = json.loads(capsys.readouterr().out)
    assert payload["agent_url"] == "http://127.0.0.1:18081"
    assert captured["kwargs"]["host_port"] == 0
    assert captured["job"].agent_url == "http://127.0.0.1:18081"


def test_agent_daemon_compose_passes_benchmark_token_file_env_var():
    compose = (webui.BENCHMARK_DOCKER_DIR / "docker-compose.agent-daemon.yml").read_text(encoding="utf-8")

    assert "AIDEN_BENCHMARK_TOKEN_FILE: ${AIDEN_BENCHMARK_TOKEN_FILE:-}" in compose


def test_daemon_compose_env_enables_benchmark_token_for_config_dir(tmp_path: Path):
    env = webui.daemon_compose_env(
        image="aiden-agent-daemon:test",
        host_port=18081,
        config_dir=tmp_path / "config",
    )

    assert env["AIDEN_BENCHMARK_TOKEN_FILE"] == "/config/control_token"


def test_start_adb_android_env_prints_environment_urls(tmp_path: Path, monkeypatch, capsys):
    launched = []
    health_urls = []

    class FakeProc:
        pid = 4242

    def fake_start_bridge(**kwargs):
        launched.append(kwargs)
        return FakeProc()

    monkeypatch.setattr(services, "start_adb_bridge_process", fake_start_bridge)
    monkeypatch.setattr(services, "wait_for_http_health", lambda url, timeout: health_urls.append((url, timeout)))

    args = _ns(
        adb_serial="127.0.0.1:6555",
        adb_path="adb",
        name="smoke",
        runs_dir=str(tmp_path),
        bridge_host="127.0.0.1",
        bridge_port=18899,
        ready_timeout_sec=12,
        json=True,
    )

    assert services.cmd_start_adb_android_env(args) == 0

    payload = json.loads(capsys.readouterr().out)
    assert payload["type"] == "adb-android-env"
    assert payload["environment_url"] == "http://127.0.0.1:18899"
    assert payload["docker_environment_url"] == "http://host.docker.internal:18899"
    assert payload["adb_serial"] == "127.0.0.1:6555"
    assert payload["pid"] == 4242
    assert payload["stop_command"] == "kill -TERM 4242"
    assert payload["agent_daemon_command"] == (
        "uv run python -m runner start-agent-daemon "
        "--environment-bridge-endpoint http://127.0.0.1:18899"
    )
    assert health_urls == [("http://127.0.0.1:18899/health", 12)]
    assert launched[0]["serial"] == "127.0.0.1:6555"
    assert launched[0]["bridge_port"] == 18899

    pid_path = Path(payload["pid_path"])
    assert pid_path.read_text(encoding="utf-8") == "4242"
    manifest = json.loads(Path(payload["manifest_path"]).read_text(encoding="utf-8"))
    assert manifest["public_endpoint"] == "http://127.0.0.1:18899"
    assert manifest["pid"] == 4242


def test_start_adb_android_env_requires_serial(tmp_path: Path, capsys):
    args = _ns(
        adb_serial="",
        adb_path="adb",
        name="",
        runs_dir=str(tmp_path),
        bridge_host="127.0.0.1",
        bridge_port=0,
        ready_timeout_sec=12,
        json=True,
    )
    assert services.cmd_start_adb_android_env(args) == 2


def test_start_adb_android_env_terminates_process_on_health_failure(tmp_path: Path, monkeypatch, capsys):
    killed = []

    class FakeProc:
        pid = 5151

    monkeypatch.setattr(services, "start_adb_bridge_process", lambda **kwargs: FakeProc())
    monkeypatch.setattr(services, "terminate_pid", lambda pid: killed.append(pid))

    def failing_health(url, timeout):
        raise RuntimeError("bridge never became healthy")

    monkeypatch.setattr(services, "wait_for_http_health", failing_health)

    args = _ns(
        adb_serial="127.0.0.1:6555",
        adb_path="adb",
        name="fail",
        runs_dir=str(tmp_path),
        bridge_host="127.0.0.1",
        bridge_port=18899,
        ready_timeout_sec=1,
        json=True,
    )
    assert services.cmd_start_adb_android_env(args) == 1
    assert killed == [5151]


def _ns(**kwargs):
    class Namespace:
        pass

    ns = Namespace()
    for key, value in kwargs.items():
        setattr(ns, key, value)
    return ns
