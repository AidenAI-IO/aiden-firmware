import json
from pathlib import Path

from runner import webui


def test_list_benchmark_suites_discovers_nested_benchmark_and_unit(tmp_path: Path):
    suites = tmp_path / "suites"
    (suites / "nested").mkdir(parents=True)
    (suites / "nested" / "memory.json").write_text(
        json.dumps(
            {
                "name": "memory_suite",
                "tasks": [
                    {
                        "id": "t1",
                        "category": "memory",
                    }
                ],
            }
        ),
        encoding="utf-8",
    )
    (suites / "unit.json").write_text(
        json.dumps(
            {
                "kind": "unit",
                "name": "tool_unit",
                "tests": [{"id": "case_1"}, {"id": "case_2"}],
            }
        ),
        encoding="utf-8",
    )

    result = webui.list_benchmark_suites(suites)

    by_key = {item["key"]: item for item in result}
    assert by_key["nested/memory.json"]["kind"] == "benchmark"
    assert by_key["nested/memory.json"]["task_count"] == 1
    assert by_key["nested/memory.json"]["categories"] == ["memory"]
    assert by_key["unit.json"]["kind"] == "unit"
    assert by_key["unit.json"]["task_count"] == 2


def test_resolve_suite_path_rejects_traversal(tmp_path: Path):
    suites = tmp_path / "suites"
    suites.mkdir()
    (suites / "ok.json").write_text("{}", encoding="utf-8")

    assert webui.resolve_suite_path(suites, "ok.json") == (suites / "ok.json").resolve()

    for bad in ("../x.json", "/tmp/x.json", "nested/../../x.json", "x.txt"):
        try:
            webui.resolve_suite_path(suites, bad)
        except ValueError:
            pass
        else:
            raise AssertionError(f"accepted invalid suite key {bad!r}")


def test_endpoint_for_docker_rewrites_localhost():
    assert webui.endpoint_for_docker("http://localhost:8080") == "http://host.docker.internal:8080"
    assert webui.endpoint_for_docker("http://127.0.0.1:9090/api") == "http://host.docker.internal:9090/api"
    assert webui.endpoint_for_docker("http://192.168.1.20:8080") == "http://192.168.1.20:8080"


def test_prepare_run_config_renders_template(tmp_path: Path, monkeypatch):
    base = tmp_path / "base"
    base.mkdir()
    (base / "agent.toml.template").write_text(
        '\n'.join(
            [
                '[model]',
                'provider = "{{MODEL_PROVIDER}}"',
                'model = "{{MODEL_NAME}}"',
                'api_key = "{{MODEL_API_KEY}}"',
                'control = "{{CONTROL_TOKEN_FILE}}"',
            ]
        ),
        encoding="utf-8",
    )
    monkeypatch.setenv("MODEL_PROVIDER", "openai")
    monkeypatch.setenv("MODEL_NAME", "gpt-test")
    monkeypatch.setenv("MODEL_API_KEY", "sk-test")

    dest = tmp_path / "dest"
    webui.prepare_run_config(base, dest)

    rendered = (dest / "agent.toml").read_text(encoding="utf-8")
    assert 'provider = "openai"' in rendered
    assert 'model = "gpt-test"' in rendered
    assert 'api_key = "sk-test"' in rendered
    assert 'control = "/config/control_token"' in rendered
    assert (dest / "control_token").exists()
    assert (dest / "memory").is_dir()
    assert (dest / "skill-state").is_dir()


def test_prepare_run_config_uses_agent_config_text(tmp_path: Path):
    base = tmp_path / "base"
    base.mkdir()
    (base / "agent.toml.template").write_text('[model]\nprovider = "template"\n', encoding="utf-8")
    agent_config = 'instruction = "custom"\n[model]\nprovider = "saved"\n'

    dest = tmp_path / "dest"
    webui.prepare_run_config(base, dest, agent_config_text=agent_config)

    assert (dest / "agent.toml").read_text(encoding="utf-8") == agent_config
    assert (dest / "control_token").exists()
    assert (dest / "memory").is_dir()


def test_webui_agent_config_persists_under_runs_dir(tmp_path: Path, monkeypatch):
    base = tmp_path / "base"
    base.mkdir()
    (base / "agent.toml.template").write_text(
        '[model]\nprovider = "{{MODEL_PROVIDER}}"\nmodel = "{{MODEL_NAME}}"\n',
        encoding="utf-8",
    )
    monkeypatch.setenv("MODEL_PROVIDER", "openai")
    monkeypatch.setenv("MODEL_NAME", "gpt-test")
    app = webui.BenchmarkWebApp(webui.WebUIConfig(runs_dir=tmp_path / "runs", base_config_dir=base))

    initial = app.get_agent_config()
    assert initial["path"] == str(tmp_path / "runs" / "agent.toml")
    assert initial["source"] == "generated"
    assert 'provider = "openai"' in initial["content"]

    saved = 'instruction = "saved"\n[model]\nprovider = "fake"\n'
    updated = app.save_agent_config({"content": saved})
    assert updated["source"] == "saved"
    assert (tmp_path / "runs" / "agent.toml").read_text(encoding="utf-8") == saved
    assert app.get_agent_config()["content"] == saved


def test_run_job_uses_saved_webui_agent_config(tmp_path: Path, monkeypatch):
    base = tmp_path / "base"
    base.mkdir()
    app = webui.BenchmarkWebApp(webui.WebUIConfig(runs_dir=tmp_path / "runs", base_config_dir=base, build_daemon_image=False))
    saved = 'instruction = "from web ui"\n[model]\nprovider = "fake"\n'
    app.save_agent_config({"content": saved})
    job = webui.Job(
        id="job-test",
        endpoint="http://127.0.0.1:19090",
        docker_endpoint="http://host.docker.internal:19090",
        suites=["mobilegym_basic.json"],
        agent_url="http://127.0.0.1:18080",
        container_name="aiden-benchmark-agent-job-test",
        config_dir=str(tmp_path / "runs" / "job-test" / "config"),
        raw_runs_dir=str(tmp_path / "runs" / "job-test" / "raw"),
        state_file=str(tmp_path / "runs" / "job-test" / "state.json"),
        runner_log=str(tmp_path / "runs" / "job-test" / "runner.log"),
        daemon_log=str(tmp_path / "runs" / "job-test" / "daemon.log"),
    )
    app._jobs[job.id] = job

    monkeypatch.setattr(webui, "ensure_daemon_image", lambda *args, **kwargs: None)
    monkeypatch.setattr(webui, "start_docker_logs", lambda *args, **kwargs: None)
    monkeypatch.setattr(app, "_wait_for_daemon", lambda job: None)

    def fake_run_suite(job, suite_key):
        job.suite_results.append({"suite": suite_key, "exit_code": 0})

    monkeypatch.setattr(app, "_run_suite", fake_run_suite)
    monkeypatch.setattr(webui.subprocess, "check_output", lambda *args, **kwargs: "container-id\n")
    monkeypatch.setattr(webui.subprocess, "run", lambda *args, **kwargs: None)

    app._run_job(job)

    assert (Path(job.config_dir) / "agent.toml").read_text(encoding="utf-8") == saved
    assert job.status == "passed"


def test_build_docker_run_command_forwards_tools_to_environment(tmp_path: Path):
    config = tmp_path / "config"
    config.mkdir()

    cmd = webui.build_docker_run_command(
        image="aiden-mobilegym-daemon:local",
        container_name="aiden-benchmark-agent-test",
        host_port=18081,
        config_dir=config,
        tool_proxy_endpoint="http://host.docker.internal:18080",
    )

    assert cmd[:5] == ["docker", "run", "--rm", "-d", "--name"]
    assert "aiden-benchmark-agent-test" in cmd
    assert "127.0.0.1:18081:8080" in cmd
    assert f"{config.resolve()}:/config:ro" in cmd
    assert "TOOL_PROXY_ENDPOINT=http://host.docker.internal:18080" in cmd
    script = cmd[-1]
    assert "--tool-proxy-mode" in script
    assert '--tool-proxy-endpoint "$TOOL_PROXY_ENDPOINT"' in script
    assert '--forward-tools "*"' in script


def test_build_mobilegym_environment_command_starts_preview_and_bridge(tmp_path: Path):
    benchmark_dir = tmp_path / "benchmark"
    benchmark_dir.mkdir()

    cmd = webui.build_mobilegym_environment_command(
        image=webui.DEFAULT_MOBILEGYM_IMAGE,
        container_name="aiden-mobilegym-env-test",
        host_web_port=18173,
        host_bridge_port=19090,
        benchmark_dir=benchmark_dir,
    )

    assert cmd[:5] == ["docker", "run", "--rm", "-d", "--name"]
    assert "aiden-mobilegym-env-test" in cmd
    assert "127.0.0.1:18173:4173" in cmd
    assert "19090:9090" in cmd
    assert f"{benchmark_dir.resolve()}:/app/benchmark:ro" in cmd
    assert "MOBILEGYM_ROOT=/mobilegym" in cmd
    assert "--entrypoint" in cmd
    assert cmd[-2] == "-c"
    script = cmd[-1]
    assert 'export PATH="/opt/venv/bin:$PATH"' in script
    assert "python3 -c" in script
    assert "npm run preview -- --host 0.0.0.0 --port 4173" in script
    assert "exec python3 /app/benchmark/mobilegym/scripts/start_simulator.py" in script
    assert "--mobilegym-root /mobilegym" in script
    assert "--env-url http://127.0.0.1:4173" in script
    assert "--bridge-host 0.0.0.0" in script
    assert "--bridge-port 9090" in script
    assert "--headless" in script
    assert webui.DEFAULT_MOBILEGYM_IMAGE in cmd


def test_start_mobilegym_environment_returns_docker_reachable_endpoint(tmp_path: Path, monkeypatch):
    config_dir = tmp_path / "config"
    config_dir.mkdir()
    app = webui.BenchmarkWebApp(
        webui.WebUIConfig(
            runs_dir=tmp_path / "runs",
            base_config_dir=config_dir,
            build_mobilegym_image=False,
        )
    )
    ports = iter([18173, 19090])
    health_urls = []
    commands = []

    monkeypatch.setattr(webui, "reserve_free_port", lambda: next(ports))
    monkeypatch.setattr(webui, "ensure_mobilegym_image", lambda *args, **kwargs: None)
    monkeypatch.setattr(webui, "wait_for_http_health", lambda url, timeout: health_urls.append((url, timeout)))
    monkeypatch.setattr(webui, "start_docker_logs", lambda *args, **kwargs: None)

    def fake_check_output(command, cwd=None, text=False):
        commands.append(command)
        return "container-id\n"

    monkeypatch.setattr(webui.subprocess, "check_output", fake_check_output)

    env = app.start_mobilegym_environment({"name": "MobileGym smoke"})

    assert env["name"] == "MobileGym smoke"
    assert env["type"] == "mobilegym"
    assert env["status"] == "running"
    assert env["endpoint"] == "http://host.docker.internal:19090"
    assert env["public_endpoint"] == "http://127.0.0.1:19090"
    assert env["web_url"] == "http://127.0.0.1:18173"
    assert env["container_id"] == "container-id"
    assert health_urls == [("http://127.0.0.1:19090/health", webui.DEFAULT_MOBILEGYM_READY_TIMEOUT_SEC)]
    assert commands and "aiden-mobilegym-env-" in commands[0][5]


def test_stop_mobilegym_environment_removes_container(tmp_path: Path, monkeypatch):
    app = webui.BenchmarkWebApp(webui.WebUIConfig(runs_dir=tmp_path / "runs", base_config_dir=tmp_path / "config"))
    env = webui.MobileGymEnvironment(
        id="mg-test",
        name="MobileGym",
        endpoint="http://host.docker.internal:19090",
        public_endpoint="http://127.0.0.1:19090",
        web_url="http://127.0.0.1:18173",
        status="running",
        container_name="aiden-mobilegym-env-mg-test",
        log_path=str(tmp_path / "env.log"),
    )
    app._mobilegym_environments[env.id] = env
    removed = []

    def fake_run(command, **kwargs):
        removed.append(command)

        class Result:
            returncode = 0

        return Result()

    monkeypatch.setattr(webui.subprocess, "run", fake_run)

    stopped = app.stop_mobilegym_environment("mg-test")

    assert stopped is not None
    assert stopped["status"] == "stopped"
    assert removed == [["docker", "rm", "-f", "aiden-mobilegym-env-mg-test"]]
