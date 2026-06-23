import json
from pathlib import Path

from runner import services


def test_start_mobilegym_env_prints_environment_urls(tmp_path: Path, monkeypatch, capsys):
    commands = []
    health_urls = []

    monkeypatch.setattr(services, "reserve_free_port", lambda: 19090)
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
        benchmark_task_id="suite.json:t1",
        ready_timeout_sec=12,
        json=True,
    )

    assert services.cmd_start_mobilegym_env(args) == 0

    payload = json.loads(capsys.readouterr().out)
    assert payload["environment_url"] == "http://127.0.0.1:19090"
    assert payload["docker_environment_url"] == "http://host.docker.internal:19090"
    assert payload["web_url"] == "http://127.0.0.1:18173"
    assert payload["screen_url"] == "http://127.0.0.1:19090/screen?benchmark-task-id=suite.json%3At1"
    assert payload["container_name"] == "aiden-mobilegym-env-mobilegym-smoke"
    assert payload["parallel_envs"] == 3
    assert payload["stop_command"] == "docker rm -f aiden-mobilegym-env-mobilegym-smoke"
    assert health_urls == [("http://127.0.0.1:19090/health", 12)]
    command, cwd, text = commands[0]
    assert cwd == services.BENCHMARK_ROOT.parent
    assert text is True
    assert "aiden-mobilegym-simulator:test" in command
    assert "127.0.0.1:18173:4173" in command
    assert "19090:9090" in command
    assert "--parallel-envs 3" in command[-1]


def test_start_agent_daemon_prints_agent_url_and_rewrites_tool_proxy(tmp_path: Path, monkeypatch, capsys):
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
        tool_proxy_endpoint="http://127.0.0.1:19090",
        benchmark_task_id="suite.json:t1",
        ready_timeout_sec=9,
        json=True,
    )

    assert services.cmd_start_agent_daemon(args) == 0

    payload = json.loads(capsys.readouterr().out)
    assert payload["agent_url"] == "http://127.0.0.1:18081"
    assert payload["tool_proxy_endpoint"] == "http://127.0.0.1:19090"
    assert payload["docker_tool_proxy_endpoint"] == "http://host.docker.internal:19090"
    assert payload["benchmark_task_id"] == "suite.json:t1"
    assert payload["container_id"] == "agent-container"
    assert payload["compose_project"] == "aiden-benchmark-agent-agent-smoke"
    assert "docker compose" in payload["stop_command"]

    assert captured["job"].agent_url == "http://127.0.0.1:18081"
    assert captured["job"].container_name == "aiden-benchmark-agent-agent-smoke"
    kwargs = captured["kwargs"]
    assert kwargs["image"] == "aiden-agent-daemon:test"
    assert kwargs["host_port"] == 18081
    assert kwargs["tool_proxy_endpoint"] == "http://host.docker.internal:19090"
    assert kwargs["benchmark_task_id"] == "suite.json:t1"
    assert kwargs["tool_proxy_mode"] is True


def test_start_agent_daemon_allows_no_tool_proxy(tmp_path: Path, monkeypatch, capsys):
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
        tool_proxy_endpoint="",
        benchmark_task_id="suite.json:t1",
        ready_timeout_sec=9,
        json=True,
    )

    assert services.cmd_start_agent_daemon(args) == 0

    payload = json.loads(capsys.readouterr().out)
    assert payload["tool_proxy_endpoint"] == ""
    assert payload["benchmark_task_id"] == ""
    assert captured["kwargs"]["tool_proxy_endpoint"] == ""
    assert captured["kwargs"]["benchmark_task_id"] == ""
    assert captured["kwargs"]["tool_proxy_mode"] is False


def _ns(**kwargs):
    class Namespace:
        pass

    ns = Namespace()
    for key, value in kwargs.items():
        setattr(ns, key, value)
    return ns
