from pathlib import Path
import io
import json
import subprocess
import threading
import tomllib

from skillopt import webui
from skillopt.webui import INDEX_HTML, SkillOptJob, SkillOptWebApp, SkillOptWebUIConfig, build_skillopt_command


def test_mobilegym_template_instruction_adds_minimal_simulator_context():
    template = Path("benchmark/mobilegym/config/agent.toml.template")
    data = tomllib.loads(template.read_text(encoding="utf-8"))
    instruction = data["instruction"]

    assert "device-control" in instruction
    assert "MobileGym simulator" in instruction
    assert "screenshot" in instruction
    assert "touch_gesture" in instruction
    assert "unsupported" in instruction
    assert "same tool with the same arguments" in instruction
    assert "Do not call open_app" not in instruction
    assert "Phone Bridge" not in instruction
    assert "frame_service" not in instruction


def test_build_skillopt_command_uses_bridge_backend_options(tmp_path: Path):
    cmd = build_skillopt_command(
        {
            "skill": "device-operator",
            "backend": "mobilegym",
            "environment_url": "http://127.0.0.1:50196",
            "train_suite": "skillopt/device-operator/device_operator_train",
            "validation_suite": "skillopt/device-operator/device_operator_verification",
            "budget": 3,
            "edit_budget": 2,
            "daemon_image": "aiden-agent-daemon:local",
            "agent_config": str(tmp_path / "agent.toml"),
            "no_build_daemon_image": True,
            "no_judge": True,
        },
        run_id="skillopt-web-run",
        artifact_root=tmp_path,
    )

    assert cmd[:3] == [cmd[0], "-m", "skillopt"]
    assert cmd[cmd.index("--backend") + 1] == "mobilegym"
    assert cmd[cmd.index("--environment-url") + 1] == "http://127.0.0.1:50196"
    assert cmd[cmd.index("--daemon-image") + 1] == "aiden-agent-daemon:local"
    assert cmd[cmd.index("--agent-config") + 1] == str(tmp_path / "agent.toml")
    assert "--no-build-daemon-image" in cmd
    assert "--no-judge" in cmd
    assert cmd[cmd.index("--run-id") + 1] == "skillopt-web-run"
    assert cmd[cmd.index("--output") + 1] == str(tmp_path / "skillopt-web-run" / "best_skill.md")


def test_stop_job_terminates_process_group(tmp_path: Path, monkeypatch):
    app = SkillOptWebApp(SkillOptWebUIConfig(runs_dir=tmp_path / "runs"))
    job = SkillOptJob(
        id="job-1",
        command=["python", "-m", "skillopt"],
        log_path=str(tmp_path / "runs" / "job-1" / "skillopt.log"),
        run_dir=str(tmp_path / "runs" / "job-1"),
    )

    class FakeProcess:
        pid = 4321

        def poll(self):
            return None

        def terminate(self):
            raise AssertionError("terminate should not be used before process-group signal")

    job.process = FakeProcess()
    app._jobs[job.id] = job
    killed = []
    monkeypatch.setattr("skillopt.webui.os.name", "posix")
    monkeypatch.setattr("skillopt.webui.os.killpg", lambda pid, sig: killed.append((pid, sig)))

    payload = app.stop_job("job-1")

    assert payload["status"] == "stopping"
    assert killed == [(4321, 15)]


def test_run_job_does_not_spawn_after_pre_start_stop(tmp_path: Path, monkeypatch):
    app = SkillOptWebApp(SkillOptWebUIConfig(runs_dir=tmp_path / "runs"))
    run_dir = tmp_path / "runs" / "job-1"
    run_dir.mkdir(parents=True)
    job = SkillOptJob(
        id="job-1",
        command=["python", "-m", "skillopt"],
        log_path=str(run_dir / "skillopt.log"),
        run_dir=str(run_dir),
        status="stopping",
    )

    def fail_popen(*args, **kwargs):
        raise AssertionError("stopped job should not spawn a process")

    monkeypatch.setattr(subprocess, "Popen", fail_popen)

    app._run_job(job)

    assert job.status == "stopped"
    assert job.exit_code is None


def test_stop_job_leaves_completed_job_status_unchanged(tmp_path: Path):
    app = SkillOptWebApp(SkillOptWebUIConfig(runs_dir=tmp_path / "runs"))
    job = SkillOptJob(
        id="job-1",
        command=["python", "-m", "skillopt"],
        log_path=str(tmp_path / "runs" / "job-1" / "skillopt.log"),
        run_dir=str(tmp_path / "runs" / "job-1"),
        status="passed",
        exit_code=0,
    )
    app._jobs[job.id] = job

    payload = app.stop_job("job-1")

    assert payload["status"] == "passed"
    assert payload["exit_code"] == 0


def test_job_payload_omits_live_process_without_deepcopying_it(tmp_path: Path):
    app = SkillOptWebApp(SkillOptWebUIConfig(runs_dir=tmp_path / "runs"))
    job = SkillOptJob(
        id="job-1",
        command=["python", "-m", "skillopt"],
        log_path=str(tmp_path / "runs" / "job-1" / "skillopt.log"),
        run_dir=str(tmp_path / "runs" / "job-1"),
        status="running",
    )

    class ProcessLike:
        def __init__(self):
            self.lock = threading.Lock()

    job.process = ProcessLike()

    payload = app._job_payload(job)

    assert payload["id"] == "job-1"
    assert payload["status"] == "running"
    assert "process" not in payload


def test_start_job_writes_benchmark_agent_config(tmp_path: Path, monkeypatch):
    base_config_dir = tmp_path / "config"
    base_config_dir.mkdir()
    (base_config_dir / "agent.toml").write_text(
        '[model]\nprovider = "openrouter"\napi_key = "sk-test"\n',
        encoding="utf-8",
    )
    app = SkillOptWebApp(SkillOptWebUIConfig(
        runs_dir=tmp_path / "runs",
        base_config_dir=base_config_dir,
    ))

    monkeypatch.setattr(app, "_run_job", lambda job: None)

    job = app.start_job({"backend": "device", "environment_url": "http://127.0.0.1:50196"})

    command = job["command"]
    agent_config = Path(command[command.index("--agent-config") + 1])
    assert agent_config == tmp_path / "runs" / job["id"] / "agent.toml"
    assert agent_config.read_text(encoding="utf-8") == '[model]\nprovider = "openrouter"\napi_key = "sk-test"\n'


def test_start_mobilegym_job_applies_mobilegym_default_instruction(tmp_path: Path, monkeypatch):
    base_config_dir = tmp_path / "benchmark" / "config"
    base_config_dir.mkdir(parents=True)
    base_config_dir.joinpath("agent.toml").write_text(
        'instruction = ""\n[model]\nprovider = "openrouter"\napi_key = "sk-test"\n',
        encoding="utf-8",
    )
    mobilegym_config_dir = tmp_path / "benchmark" / "mobilegym" / "config"
    mobilegym_config_dir.mkdir(parents=True)
    mobilegym_config_dir.joinpath("agent.toml").write_text(
        'instruction = "You are controlling a MobileGym simulator."\n[model]\nprovider = "fake"\napi_key = ""\n',
        encoding="utf-8",
    )
    monkeypatch.setattr(webui, "REPO_ROOT", tmp_path)
    app = SkillOptWebApp(SkillOptWebUIConfig(
        runs_dir=tmp_path / "runs",
        base_config_dir=base_config_dir,
    ))

    monkeypatch.setattr(app, "_run_job", lambda job: None)

    job = app.start_job({"backend": "mobilegym", "environment_url": "http://127.0.0.1:50196"})

    command = job["command"]
    agent_config = Path(command[command.index("--agent-config") + 1])
    content = agent_config.read_text(encoding="utf-8")
    assert 'instruction = "You are controlling a MobileGym simulator."' in content
    assert 'api_key = "sk-test"' in content


def test_list_targets_discovers_skill_suite_pairs(tmp_path: Path):
    suites_dir = tmp_path / "suites"
    skill_dir = suites_dir / "device-operator"
    skill_dir.mkdir(parents=True)
    (skill_dir / "device_operator_train.json").write_text(json.dumps({
        "name": "device_operator_train",
        "tasks": [{"id": "train_one"}, {"id": "train_two"}],
    }), encoding="utf-8")
    (skill_dir / "device_operator_verification.json").write_text(json.dumps({
        "name": "device_operator_verification",
        "tasks": [{"id": "verify_one"}],
    }), encoding="utf-8")
    app = SkillOptWebApp(SkillOptWebUIConfig(runs_dir=tmp_path / "runs", suites_dir=suites_dir))

    assert app.list_targets() == [{
        "id": "device-operator",
        "skill": "device-operator",
        "name": "device-operator",
        "train_suite": "skillopt/device-operator/device_operator_train",
        "validation_suite": "skillopt/device-operator/device_operator_verification",
        "train_task_count": 2,
        "validation_task_count": 1,
    }]


def test_list_targets_discovers_multiple_skill_suite_pairs(tmp_path: Path):
    suites_dir = tmp_path / "suites"
    for skill, train_count, validation_count in [
        ("device-operator", 2, 1),
        ("memory-curator", 3, 2),
    ]:
        skill_dir = suites_dir / skill
        skill_dir.mkdir(parents=True)
        (skill_dir / f"{skill.replace('-', '_')}_train.json").write_text(json.dumps({
            "tasks": [{"id": f"train_{idx}"} for idx in range(train_count)],
        }), encoding="utf-8")
        (skill_dir / f"{skill.replace('-', '_')}_verification.json").write_text(json.dumps({
            "tasks": [{"id": f"verify_{idx}"} for idx in range(validation_count)],
        }), encoding="utf-8")

    app = SkillOptWebApp(SkillOptWebUIConfig(runs_dir=tmp_path / "runs", suites_dir=suites_dir))

    assert [target["skill"] for target in app.list_targets()] == ["device-operator", "memory-curator"]
    assert app.list_targets()[1]["train_suite"] == "skillopt/memory-curator/memory_curator_train"
    assert app.list_targets()[1]["validation_task_count"] == 2


def test_start_job_resolves_target_and_running_environment(tmp_path: Path, monkeypatch):
    suites_dir = tmp_path / "suites"
    skill_dir = suites_dir / "device-operator"
    skill_dir.mkdir(parents=True)
    (skill_dir / "device_operator_train.json").write_text(json.dumps({"tasks": [{"id": "train_one"}]}), encoding="utf-8")
    (skill_dir / "device_operator_verification.json").write_text(json.dumps({"tasks": [{"id": "verify_one"}]}), encoding="utf-8")
    base_config_dir = tmp_path / "config"
    base_config_dir.mkdir()
    (base_config_dir / "agent.toml").write_text('[model]\napi_key = "sk-test"\n', encoding="utf-8")
    app = SkillOptWebApp(SkillOptWebUIConfig(
        runs_dir=tmp_path / "runs",
        suites_dir=suites_dir,
        base_config_dir=base_config_dir,
    ))

    # Inject a running environment directly into env_manager
    from runner.environment import MobileGymEnvironment
    env = MobileGymEnvironment(
        id="mg-1",
        name="MobileGym",
        endpoint="http://host.docker.internal:50196",
        public_endpoint="http://127.0.0.1:50196",
        web_url="http://127.0.0.1:50197",
        status="running",
    )
    app.env_manager._environments["mg-1"] = env

    monkeypatch.setattr(app, "_run_job", lambda job: None)

    job = app.start_job({"target_id": "device-operator", "environment_id": "mg-1", "budget": 1, "edit_budget": 1})

    command = job["command"]
    assert command[command.index("--skill") + 1] == "device-operator"
    assert command[command.index("--train-suite") + 1] == "skillopt/device-operator/device_operator_train"
    assert command[command.index("--validation-suite") + 1] == "skillopt/device-operator/device_operator_verification"
    assert command[command.index("--environment-url") + 1] == "http://127.0.0.1:50196"


def test_start_mobilegym_environment_proxies_to_benchmark_webui(tmp_path: Path, monkeypatch):
    """SkillOpt now manages MobileGym environments directly via env_manager."""
    app = SkillOptWebApp(SkillOptWebUIConfig(runs_dir=tmp_path / "runs"))
    calls = []

    from runner.environment import MobileGymEnvironment

    def fake_start(name, parallel_envs=5):
        calls.append({"name": name, "parallel_envs": parallel_envs})
        env = MobileGymEnvironment(
            id="mg-1",
            name=name,
            endpoint="http://host.docker.internal:50196",
            public_endpoint="http://127.0.0.1:50196",
            web_url="http://127.0.0.1:50197",
            status="running",
            parallel_envs=parallel_envs,
        )
        app.env_manager._environments[env.id] = env
        return env

    monkeypatch.setattr(app.env_manager, "start_mobilegym", fake_start)

    environment = app.start_mobilegym_environment({"name": "SkillOpt", "parallel_envs": 3})

    assert environment["id"] == "mg-1"
    assert environment["status"] == "running"
    assert calls == [{"name": "SkillOpt", "parallel_envs": 3}]


def test_start_mobilegym_environment_rejects_nonpositive_parallel_envs(tmp_path: Path, monkeypatch):
    app = SkillOptWebApp(SkillOptWebUIConfig(runs_dir=tmp_path / "runs"))

    def fail(*args, **kwargs):
        raise AssertionError("should not start environment")

    monkeypatch.setattr(app.env_manager, "start_mobilegym", fail)

    try:
        app.start_mobilegym_environment({"parallel_envs": 0})
    except ValueError as exc:
        assert "parallel_envs must be positive" in str(exc)
    else:
        raise AssertionError("nonpositive parallel_envs should be rejected")


def test_start_job_rejects_missing_benchmark_agent_api_key(tmp_path: Path, monkeypatch):
    base_config_dir = tmp_path / "config"
    base_config_dir.mkdir()
    (base_config_dir / "agent.toml").write_text('[model]\napi_key = ""\n', encoding="utf-8")
    app = SkillOptWebApp(SkillOptWebUIConfig(
        runs_dir=tmp_path / "runs",
        base_config_dir=base_config_dir,
    ))

    monkeypatch.setattr(app, "_run_job", lambda job: (_ for _ in ()).throw(AssertionError("job should not start")))

    try:
        app.start_job({"environment_url": "http://127.0.0.1:50196"})
    except ValueError as exc:
        assert "Benchmark agent.toml does not contain a model api_key" in str(exc)
    else:
        raise AssertionError("missing api key should reject the SkillOpt job")


def test_index_html_reuses_benchmark_webui_shell():
    assert '<header class="topbar">' in INDEX_HTML
    assert 'class="brand-title">Aiden SkillOpt' in INDEX_HTML
    assert 'class="tile"' in INDEX_HTML
    assert 'id="suiteRows"' in INDEX_HTML
    assert 'id="runBtn"' in INDEX_HTML
    assert 'id="agentConfigStatus"' in INDEX_HTML
    assert 'id="envRows"' in INDEX_HTML
    assert 'id="startMobileGym"' in INDEX_HTML
    assert 'id="mobilegymParallelEnvs"' in INDEX_HTML
    assert 'id="activeStopJob"' in INDEX_HTML
    assert 'id="jobRows"' in INDEX_HTML
    assert 'id="logBox"' in INDEX_HTML
    assert 'id="judgeEnabled"' in INDEX_HTML
    assert 'id="judgeModel"' in INDEX_HTML
    assert 'id="judgeApiKey"' in INDEX_HTML
    assert 'id="runEnvDialog"' in INDEX_HTML
    assert 'id="skill"' not in INDEX_HTML
    assert 'id="environmentUrl"' not in INDEX_HTML
    assert 'id="agentUrl"' not in INDEX_HTML
