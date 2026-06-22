import json
import os
import signal
import subprocess
import sys
import time
from pathlib import Path


DOCKER_DIR = Path(__file__).resolve().parents[2] / "mobilegym" / "docker"


def install_fake_docker(tmp_path):
    log_path = tmp_path / "docker.log"
    if log_path.exists():
        log_path.unlink()
    fake_docker = tmp_path / "docker"
    fake_docker.write_text(
        "#!/usr/bin/env bash\n"
        "printf '%s|%s|%s\\n' \"${COMPOSE_PROJECT_NAME:-}\" \"${AIDEN_CONFIG_DIR:-}\" \"$*\" >> \"$DOCKER_LOG\"\n"
        "if [[ \"$*\" == *\" run \"* && -n \"${AIDEN_CONFIG_DIR:-}\" && -f \"$AIDEN_CONFIG_DIR/agent.toml\" ]]; then sed 's/^/AGENT_TOML:/' \"$AIDEN_CONFIG_DIR/agent.toml\" >> \"$DOCKER_LOG\"; fi\n"
        "if [[ \"$*\" == *\" logs\"* ]]; then printf 'compose logs for %s\\n' \"${COMPOSE_PROJECT_NAME:-}\"; fi\n"
        "if [[ \"$*\" == *\" run \"* && \"$*\" == *\"fail.Task\"* ]]; then exit 7; fi\n"
    )
    fake_docker.chmod(0o755)
    return log_path


def install_arg_logging_fake_docker(tmp_path):
    log_path = tmp_path / "docker.log"
    fake_docker = tmp_path / "docker"
    fake_docker.write_text(
        "#!/usr/bin/env bash\n"
        "printf '%s|%s|%s\n' \"${COMPOSE_PROJECT_NAME:-}\" \"${AIDEN_CONFIG_DIR:-}\" \"$*\" >> \"$DOCKER_LOG\"\n"
        "if [[ \"$*\" == *\" run \"* ]]; then for arg in \"$@\"; do printf 'ARG|%s\n' \"$arg\" >> \"$DOCKER_LOG\"; done; fi\n"
        "if [[ \"$*\" == *\" logs\"* ]]; then printf 'compose logs for %s\n' \"${COMPOSE_PROJECT_NAME:-}\"; fi\n"
    )
    fake_docker.chmod(0o755)
    return log_path


def install_image_check_fake_docker(tmp_path):
    log_path = tmp_path / "docker.log"
    built_marker = tmp_path / "images-built"
    fake_docker = tmp_path / "docker"
    fake_docker.write_text(
        "#!/usr/bin/env bash\n"
        "printf '%s|%s|%s\\n' \"${COMPOSE_PROJECT_NAME:-}\" \"${AIDEN_CONFIG_DIR:-}\" \"$*\" >> \"$DOCKER_LOG\"\n"
        f"if [[ \"$*\" == 'image inspect aiden-mobilegym-simulator:local aiden-mobilegym-daemon:local aiden-mobilegym-test-runner:local' && ! -f {built_marker} ]]; then exit 1; fi\n"
        "if [[ \"${1:-}\" == 'image' && \"${2:-}\" == 'inspect' && \"${3:-}\" == '--format' ]]; then printf '%s\\n' \"${FAKE_IMAGE_CREATED:-1970-01-01T00:00:00Z}\"; exit 0; fi\n"
        f"if [[ \"${{1:-}}\" == 'compose' && \"$*\" == *' build'* ]]; then touch {built_marker}; fi\n"
        "if [[ \"$*\" == *\" logs\"* ]]; then printf 'compose logs for %s\\n' \"${COMPOSE_PROJECT_NAME:-}\"; fi\n"
    )
    fake_docker.chmod(0o755)
    return log_path


def install_build_fail_fake_docker(tmp_path):
    log_path = tmp_path / "docker.log"
    fake_docker = tmp_path / "docker"
    fake_docker.write_text(
        "#!/usr/bin/env bash\n"
        "printf '%s|%s|%s\\n' \"${COMPOSE_PROJECT_NAME:-}\" \"${AIDEN_CONFIG_DIR:-}\" \"$*\" >> \"$DOCKER_LOG\"\n"
        "if [[ \"$*\" == 'image inspect aiden-mobilegym-simulator:local aiden-mobilegym-daemon:local aiden-mobilegym-test-runner:local' ]]; then exit 1; fi\n"
        "if [[ \"${1:-}\" == 'compose' && \"$*\" == *' build'* ]]; then printf 'build failed: registry timeout\\n' >&2; exit 12; fi\n"
    )
    fake_docker.chmod(0o755)
    return log_path


def install_registry_timeout_then_cn_fake_docker(tmp_path):
    log_path = tmp_path / "docker.log"
    fake_docker = tmp_path / "docker"
    fake_docker.write_text(
        "#!/usr/bin/env bash\n"
        "printf '%s|%s|%s\\n' \"${COMPOSE_PROJECT_NAME:-}\" \"${AIDEN_CONFIG_DIR:-}\" \"$*\" >> \"$DOCKER_LOG\"\n"
        "if [[ \"$*\" == 'image inspect aiden-mobilegym-simulator:local aiden-mobilegym-daemon:local aiden-mobilegym-test-runner:local' ]]; then exit 1; fi\n"
        "if [[ \"${1:-}\" == 'compose' && \"$*\" == *'docker-compose.cn.yml'* && \"$*\" == *' build'* ]]; then exit 0; fi\n"
        "if [[ \"${1:-}\" == 'compose' && \"$*\" == *' build'* ]]; then printf 'failed to fetch anonymous token: i/o timeout\\n' >&2; exit 12; fi\n"
        "if [[ \"$*\" == *\" logs\"* ]]; then printf 'compose logs for %s\\n' \"${COMPOSE_PROJECT_NAME:-}\"; fi\n"
    )
    fake_docker.chmod(0o755)
    return log_path


def run_parallel(tmp_path, args, **env_overrides):
    log_path = install_fake_docker(tmp_path)
    env = os.environ.copy()
    env.update(
        {
            "PATH": f"{tmp_path}{os.pathsep}{env['PATH']}",
            "DOCKER_LOG": str(log_path),
            "MOBILEGYM_RUNS_ROOT": str(tmp_path / "runs"),
            "MOBILEGYM_BATCH_ID": env_overrides.pop("MOBILEGYM_BATCH_ID", "batch-test"),
            "CHAT_TIMEOUT_SEC": env_overrides.pop("CHAT_TIMEOUT_SEC", "777"),
        }
    )
    env.update(env_overrides)
    result = subprocess.run(
        ["./parallel_run.sh", *args],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    lines = log_path.read_text().splitlines() if log_path.exists() else []
    return result, lines, Path(env["MOBILEGYM_RUNS_ROOT"]) / env["MOBILEGYM_BATCH_ID"]


def run_parallel_without_uv(tmp_path, args, **env_overrides):
    log_path = install_fake_docker(tmp_path)
    (tmp_path / "python3").symlink_to(sys.executable)
    env = os.environ.copy()
    env.update(
        {
            "PATH": f"{tmp_path}{os.pathsep}/usr/bin:/bin:/usr/sbin:/sbin",
            "DOCKER_LOG": str(log_path),
            "MOBILEGYM_RUNS_ROOT": str(tmp_path / "runs"),
            "MOBILEGYM_BATCH_ID": env_overrides.pop("MOBILEGYM_BATCH_ID", "batch-no-uv"),
            "CHAT_TIMEOUT_SEC": env_overrides.pop("CHAT_TIMEOUT_SEC", "777"),
        }
    )
    env.update(env_overrides)
    result = subprocess.run(
        ["./parallel_run.sh", *args],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    lines = log_path.read_text().splitlines() if log_path.exists() else []
    return result, lines, Path(env["MOBILEGYM_RUNS_ROOT"]) / env["MOBILEGYM_BATCH_ID"]


def command_lines(lines, needle):
    return [line for line in lines if needle in line]


def test_parallel_run_builds_missing_local_images_before_workers(tmp_path):
    log_path = install_image_check_fake_docker(tmp_path)
    env = make_env(tmp_path, log_path, PARALLEL="1")

    result = subprocess.run(
        ["./parallel_run.sh", "clock.CountAlarms"],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )

    assert result.returncode == 0, result.stderr
    lines = log_path.read_text().splitlines()
    build_index = next(i for i, line in enumerate(lines) if " build" in line)
    run_index = next(i for i, line in enumerate(lines) if " run --rm test " in line)
    assert build_index < run_index


def test_parallel_run_rebuilds_when_sources_are_newer_than_images(tmp_path):
    log_path = install_image_check_fake_docker(tmp_path)
    (tmp_path / "images-built").write_text("present\n")
    env = make_env(tmp_path, log_path, PARALLEL="1")

    result = subprocess.run(
        ["./parallel_run.sh", "clock.CountAlarms"],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )

    assert result.returncode == 0, result.stderr
    assert any(" build" in line for line in log_path.read_text().splitlines())


def test_parallel_run_reports_missing_docker_clearly(tmp_path):
    fake_bin = tmp_path / "bin"
    fake_bin.mkdir()
    env = os.environ.copy()
    env.update(
        {
            "PATH": f"{fake_bin}{os.pathsep}/bin:/usr/bin",
            "MOBILEGYM_RUNS_ROOT": str(tmp_path / "runs"),
            "MOBILEGYM_BATCH_ID": "batch-test",
        }
    )

    result = subprocess.run(
        ["./parallel_run.sh", "clock.CountAlarms"],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )

    assert result.returncode == 2
    assert "Docker CLI not found" in result.stderr


def test_parallel_run_writes_report_when_image_build_fails(tmp_path):
    log_path = install_build_fail_fake_docker(tmp_path)
    env = make_env(tmp_path, log_path, PARALLEL="1")

    result = subprocess.run(
        ["./parallel_run.sh", "clock.CountAlarms"],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )

    batch = Path(env["MOBILEGYM_RUNS_ROOT"]) / env["MOBILEGYM_BATCH_ID"]
    assert result.returncode != 0
    assert (batch / "summary.json").exists()
    assert (batch / "index.html").exists()
    summary = json.loads((batch / "summary.json").read_text())
    assert summary["worker_failed"] == 1
    assert summary["tasks"] == 1


def test_parallel_run_passes_build_proxy_args(tmp_path):
    log_path = install_image_check_fake_docker(tmp_path)
    env = make_env(
        tmp_path,
        log_path,
        PARALLEL="1",
        MOBILEGYM_DOCKER_PROXY="http://host.docker.internal:7897",
        MOBILEGYM_PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST="https://cdn.playwright.dev",
    )

    result = subprocess.run(
        ["./parallel_run.sh", "clock.CountAlarms"],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )

    assert result.returncode == 0, result.stderr
    build_line = next(line for line in log_path.read_text().splitlines() if " build" in line)
    assert "--profile test" in build_line
    assert "--build-arg HTTP_PROXY=http://host.docker.internal:7897" in build_line
    assert "--build-arg HTTPS_PROXY=http://host.docker.internal:7897" in build_line
    assert "--build-arg PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST=https://cdn.playwright.dev" in build_line


def test_parallel_run_falls_back_to_cn_compose_on_docker_hub_timeout(tmp_path):
    log_path = install_registry_timeout_then_cn_fake_docker(tmp_path)
    env = make_env(tmp_path, log_path, PARALLEL="1")

    result = subprocess.run(
        ["./parallel_run.sh", "clock.CountAlarms"],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )

    assert result.returncode == 0, result.stderr
    build_lines = [line for line in log_path.read_text().splitlines() if " build" in line]
    assert any("docker-compose.yml" in line for line in build_lines)
    assert any("docker-compose.cn.yml" in line for line in build_lines)


def test_parallel_run_uses_isolated_projects_configs_logs_and_reports(tmp_path):
    result, lines, batch_dir = run_parallel(
        tmp_path,
        ["clock.CountAlarms", "clock.ToggleAlarm"],
        PARALLEL="2",
    )

    assert result.returncode == 0, result.stderr
    run_lines = command_lines(lines, " run --rm test ")
    assert len(run_lines) == 2
    project_names = {line.split("|", 1)[0] for line in run_lines}
    config_dirs = {line.split("|")[1] for line in run_lines}
    assert len(project_names) == 2
    assert len(config_dirs) == 2
    assert all(name.startswith("mobilegym-") for name in project_names)
    assert all("docker-compose.parallel.yml" in line for line in run_lines)
    assert all("--task-id" in line for line in run_lines)
    assert all(" --headless" in line for line in run_lines)
    assert all("--env-url http://mobilegym:4173" in line for line in run_lines)
    assert all("--chat-timeout-sec 777" in line for line in run_lines)
    assert len(command_lines(lines, " logs")) == 2
    assert len(command_lines(lines, " down --volumes --remove-orphans")) == 2
    assert (batch_dir / "index.html").exists()
    assert (batch_dir / "tasks" / "summary.json").exists()


def test_parallel_run_writes_report_when_uv_is_not_on_path(tmp_path):
    result, lines, batch_dir = run_parallel_without_uv(
        tmp_path,
        ["clock.CountAlarms"],
        PARALLEL="1",
    )

    assert result.returncode == 0, result.stderr
    assert len(command_lines(lines, " run --rm test ")) == 1
    assert (batch_dir / "summary.json").exists()
    assert (batch_dir / "index.html").exists()


def test_parallel_run_shards_suite_and_writes_worker_metadata(tmp_path):
    result, lines, batch_dir = run_parallel(
        tmp_path,
        ["--suite", "phone_control_v1"],
        PARALLEL="2",
        MODEL_NAME="qwen3.6-35b",
    )

    assert result.returncode == 0, result.stderr
    run_lines = command_lines(lines, " run --rm test ")
    assert len(run_lines) == 2
    assert any("--suite phone_control_v1 --shard-index 0 --shard-count 2" in line for line in run_lines)
    assert any("--suite phone_control_v1 --shard-index 1 --shard-count 2" in line for line in run_lines)
    assert all("--runs-dir /app/benchmark/runs/mobilegym/batch-test/phone_control_v1/shard-" in line for line in run_lines)
    assert all("--shard-metadata-file /app/benchmark/runs/mobilegym/batch-test/phone_control_v1/shard-" in line for line in run_lines)
    assert all(" --headless" in line for line in run_lines)
    assert all("--env-url http://mobilegym:4173" in line for line in run_lines)
    assert all("--chat-timeout-sec 777" in line for line in run_lines)
    shard_json = batch_dir / "phone_control_v1" / "shard-0" / "shard.json"
    assert '"suite": "phone_control_v1"' in shard_json.read_text()
    assert '"model": "qwen3.6-35b"' in shard_json.read_text()
    assert '"exit_code": 0' in shard_json.read_text()
    assert (batch_dir / "phone_control_v1" / "shard-0" / "runner.log").exists()
    assert (batch_dir / "phone_control_v1" / "shard-0" / "compose.log").exists()


def test_parallel_run_rejects_unsafe_suite_names(tmp_path):
    log_path = install_fake_docker(tmp_path)
    env = make_env(tmp_path, log_path, PARALLEL="1")

    result = subprocess.run(
        ["./parallel_run.sh", "--suite", "../evil"],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )

    assert result.returncode == 2
    assert "invalid suite name" in result.stderr
    assert not command_lines(log_path.read_text().splitlines(), " run --rm test ")


def test_parallel_run_allows_nested_aiden_suite_names(tmp_path):
    result, lines, batch_dir = run_parallel(
        tmp_path,
        ["--aiden-suite", "perception/perception_v1"],
        PARALLEL="1",
    )

    assert result.returncode == 0, result.stderr
    run_line = command_lines(lines, " run --rm test ")[0]
    assert "--aiden-suite perception/perception_v1" in run_line
    assert (batch_dir / "perception" / "perception_v1" / "shard-0").exists()


def test_parallel_run_passes_aiden_task_ids_as_single_argument(tmp_path):
    script = (DOCKER_DIR / "parallel_run.sh").read_text()
    assert "local -a aiden_task_id_args=()" in script
    assert '${AIDEN_TASK_IDS:+--aiden-task-ids "$AIDEN_TASK_IDS"}' not in script

    log_path = install_arg_logging_fake_docker(tmp_path)
    env = make_env(
        tmp_path,
        log_path,
        PARALLEL="1",
    )

    result = subprocess.run(
        [
            "./parallel_run.sh",
            "--aiden-suite",
            "skillopt/device-operator/device_operator_train",
            "--aiden-task-ids",
            "case_one,case_two",
        ],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )

    assert result.returncode == 0, result.stderr
    arg_lines = [line for line in log_path.read_text().splitlines() if line.startswith("ARG|")]
    assert "ARG|--aiden-task-ids" in arg_lines
    assert "ARG|case_one,case_two" in arg_lines


def test_parallel_run_renders_agent_template_from_environment(tmp_path):
    source_config = tmp_path / "source-config"
    source_config.mkdir()
    (source_config / "agent.toml.template").write_text(
        '[model]\nprovider = "{{MODEL_PROVIDER}}"\nmodel = "{{MODEL_NAME}}"\nbase_url = "{{MODEL_BASE_URL}}"\napi_key = "{{MODEL_API_KEY}}"\n[device]\ncontrol_token_file = "{{CONTROL_TOKEN_FILE}}"\n'
    )

    result, lines, _ = run_parallel(
        tmp_path,
        ["--suite", "clock"],
        PARALLEL="1",
        AIDEN_SOURCE_CONFIG_DIR=str(source_config),
        MODEL_PROVIDER="openrouter",
        MODEL_NAME="google/gemini-3.5-flash",
        MODEL_BASE_URL="https://openrouter.ai/api/v1",
        MODEL_API_KEY="test-key",
    )

    assert result.returncode == 0, result.stderr
    agent_toml = "\n".join(line.removeprefix("AGENT_TOML:") for line in lines if line.startswith("AGENT_TOML:"))
    assert '{{MODEL_PROVIDER}}' not in agent_toml
    assert 'provider = "openrouter"' in agent_toml
    assert 'model = "google/gemini-3.5-flash"' in agent_toml
    assert 'control_token_file = "' in agent_toml
    assert 'control_token' in agent_toml


def test_parallel_run_expands_multiple_suites_and_keeps_reporting_after_failure(tmp_path):
    result, lines, batch_dir = run_parallel(
        tmp_path,
        ["--suites", "clock,phone_control_v1"],
        PARALLEL="2",
        MAX_JOBS="2",
    )

    assert result.returncode == 0, result.stderr
    run_lines = command_lines(lines, " run --rm test ")
    assert len(run_lines) == 4
    assert (batch_dir / "clock" / "summary.json").exists()
    assert (batch_dir / "phone_control_v1" / "summary.json").exists()
    assert (batch_dir / "index.html").exists()

    failed, failed_lines, failed_batch = run_parallel(
        tmp_path,
        ["ok.Task", "fail.Task"],
        MOBILEGYM_BATCH_ID="batch-fail",
    )
    assert failed.returncode != 0
    assert len(command_lines(failed_lines, " run --rm test ")) == 2
    assert (failed_batch / "index.html").exists()


def test_parallel_run_respects_compose_files_and_appends_parallel_override(tmp_path):
    result, lines, _ = run_parallel(
        tmp_path,
        ["--suite", "clock"],
        PARALLEL="1",
        COMPOSE_FILES="docker-compose.cn.yml",
    )

    assert result.returncode == 0, result.stderr
    run_line = command_lines(lines, " run --rm test ")[0]
    assert "-f docker-compose.cn.yml" in run_line
    assert "-f docker-compose.parallel.yml" in run_line


def test_mobilegym_compose_files_do_not_pin_container_names():
    compose_files = sorted(DOCKER_DIR.glob("docker-compose*.yml"))
    assert compose_files
    offenders = [path.name for path in compose_files if "container_name:" in path.read_text()]
    assert offenders == []


def test_mobilegym_compose_files_use_stable_images_and_config_dir_variable():
    for compose_name in ("docker-compose.yml", "docker-compose.cn.yml"):
        content = (DOCKER_DIR / compose_name).read_text()
        assert "image: aiden-mobilegym-simulator:local" in content
        assert "image: aiden-mobilegym-daemon:local" in content
        assert "image: aiden-mobilegym-test-runner:local" in content
        assert "${AIDEN_CONFIG_DIR:-../config}:/config:ro" in content
        assert "../../suites:/app/benchmark/suites:ro" in content


def test_mobilegym_compose_files_pass_proxy_environment_to_daemon():
    for compose_name in ("docker-compose.yml", "docker-compose.cn.yml"):
        content = (DOCKER_DIR / compose_name).read_text()
        assert "HTTPS_PROXY=${HTTPS_PROXY:-}" in content
        assert "HTTP_PROXY=${HTTP_PROXY:-}" in content
        assert "ALL_PROXY=${ALL_PROXY:-}" in content
        assert "NO_PROXY=localhost,127.0.0.1,daemon,mobilegym,test" in content


def test_mobilegym_test_runner_bypasses_proxy_for_internal_services():
    for compose_name in ("docker-compose.yml", "docker-compose.cn.yml"):
        content = (DOCKER_DIR / compose_name).read_text()
        test_section = content.split("\n  test:\n", 1)[1].split("\nnetworks:", 1)[0]
        assert "NO_PROXY=localhost,127.0.0.1,daemon,mobilegym,test" in test_section
        assert "no_proxy=localhost,127.0.0.1,daemon,mobilegym,test" in test_section


def test_mobilegym_dockerfiles_support_playwright_download_mirror():
    standard = (DOCKER_DIR / "Dockerfile").read_text()
    china = (DOCKER_DIR / "Dockerfile.cn").read_text()

    assert "ARG PLAYWRIGHT_DOWNLOAD_HOST" in standard
    assert "ARG PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST" in standard
    assert "PLAYWRIGHT_DOWNLOAD_HOST=${PLAYWRIGHT_DOWNLOAD_HOST}" in standard
    assert "PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST=${PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST}" in standard
    assert "PUPPETEER_SKIP_DOWNLOAD=true" in standard
    assert "COPY benchmark/runner /app/benchmark/runner" in standard
    assert "ARG HTTP_PROXY" in china
    assert "HTTP_PROXY=${HTTP_PROXY}" in china
    assert "PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST=${PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST:-https://playwright-akamai.azureedge.net}" in china
    assert "PUPPETEER_SKIP_DOWNLOAD=true" in china
    assert "COPY benchmark/runner /app/benchmark/runner" in china
    assert "for attempt in 1 2 3" in china
    assert "git clone" in china


def test_mobilegym_agent_template_limits_screenshot_context():
    template = (DOCKER_DIR.parent / "config" / "agent.toml.template").read_text()

    assert "screenshot_keep_n = 1" in template
    assert "screenshot_prune_interval = 2" in template


def test_parallel_compose_config_removes_host_port_bindings():
    for compose_name in ("docker-compose.yml", "docker-compose.cn.yml"):
        result = subprocess.run(
            ["docker", "compose", "-f", compose_name, "-f", "docker-compose.parallel.yml", "--profile", "test", "config"],
            cwd=DOCKER_DIR,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )

        assert result.returncode == 0, result.stderr
        assert "published:" not in result.stdout


def install_timing_fake_docker(tmp_path, run_sleep_sec=0.4, fail_down_for=None):
    log_path = tmp_path / "docker.log"
    timing_path = tmp_path / "docker.timing"
    for path in (log_path, timing_path):
        if path.exists():
            path.unlink()
    fail_down_for = fail_down_for or ""
    fake_docker = tmp_path / "docker"
    fake_docker.write_text(
        "#!/usr/bin/env bash\n"
        "printf '%s|%s|%s\\n' \"${COMPOSE_PROJECT_NAME:-}\" \"${AIDEN_CONFIG_DIR:-}\" \"$*\" >> \"$DOCKER_LOG\"\n"
        "if [[ \"$*\" == *\" run \"* ]]; then\n"
        "    printf 'start|%s|%s\\n' \"${COMPOSE_PROJECT_NAME:-}\" \"$(date +%s.%N)\" >> \"$TIMING_LOG\"\n"
        f"    sleep {run_sleep_sec}\n"
        "    printf 'end|%s|%s\\n' \"${COMPOSE_PROJECT_NAME:-}\" \"$(date +%s.%N)\" >> \"$TIMING_LOG\"\n"
        "fi\n"
        "if [[ \"$*\" == *\" logs\"* ]]; then printf 'compose logs for %s\\n' \"${COMPOSE_PROJECT_NAME:-}\"; fi\n"
        f"if [[ \"$*\" == *\" down \"* || \"$*\" == *\" down --\"* ]] && [[ -n \"{fail_down_for}\" && \"${{COMPOSE_PROJECT_NAME:-}}\" == *\"{fail_down_for}\"* ]]; then exit 5; fi\n"
    )
    fake_docker.chmod(0o755)
    return log_path, timing_path


def make_env(tmp_path, log_path, timing_path=None, **overrides):
    env = os.environ.copy()
    env.update(
        {
            "PATH": f"{tmp_path}{os.pathsep}{env['PATH']}",
            "DOCKER_LOG": str(log_path),
            "MOBILEGYM_RUNS_ROOT": str(tmp_path / "runs"),
            "MOBILEGYM_BATCH_ID": overrides.pop("MOBILEGYM_BATCH_ID", "batch-test"),
            "CHAT_TIMEOUT_SEC": overrides.pop("CHAT_TIMEOUT_SEC", "777"),
        }
    )
    if timing_path is not None:
        env["TIMING_LOG"] = str(timing_path)
    env.update(overrides)
    return env


def test_max_jobs_one_serializes_workers(tmp_path):
    log_path, timing_path = install_timing_fake_docker(tmp_path, run_sleep_sec=0.3)
    env = make_env(tmp_path, log_path, timing_path, PARALLEL="2", MAX_JOBS="1")
    result = subprocess.run(
        ["./parallel_run.sh", "alpha.Task", "beta.Task"],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )

    assert result.returncode == 0, result.stderr
    events = [line.split("|") for line in timing_path.read_text().splitlines() if line]
    starts = {project: float(ts) for kind, project, ts in events if kind == "start"}
    ends = {project: float(ts) for kind, project, ts in events if kind == "end"}
    assert len(starts) == 2 and len(ends) == 2
    ordered_projects = sorted(starts, key=starts.get)
    first, second = ordered_projects
    assert starts[second] >= ends[first] - 0.01, (
        f"second worker started before first ended: {events}"
    )


def test_int_trap_cleans_up_running_workers_and_skips_queued(tmp_path):
    log_path, _timing = install_timing_fake_docker(tmp_path, run_sleep_sec=5)
    env = make_env(tmp_path, log_path, _timing, PARALLEL="1", MAX_JOBS="1")
    proc = subprocess.Popen(
        ["./parallel_run.sh", "first.Task", "second.Task", "third.Task"],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    deadline = time.time() + 5
    while time.time() < deadline:
        if log_path.exists() and any(" run " in line for line in log_path.read_text().splitlines()):
            break
        time.sleep(0.05)
    else:
        proc.kill()
        raise AssertionError("first worker never invoked docker run")

    os.killpg(os.getpgid(proc.pid), signal.SIGINT)
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()
        raise

    assert proc.returncode != 0
    log_lines = log_path.read_text().splitlines()
    started_runs = [line for line in log_lines if " run " in line]
    cleanup_downs = [line for line in log_lines if " down " in line or " down --" in line]
    assert len(started_runs) == 1, f"queued workers should not start after SIGINT, got: {started_runs}"
    assert cleanup_downs, "cleanup did not invoke compose down"


def test_slug_collisions_keep_distinct_shard_dirs(tmp_path):
    log_path = install_fake_docker(tmp_path)
    env = make_env(tmp_path, log_path, PARALLEL="2")
    result = subprocess.run(
        ["./parallel_run.sh", "clock.Foo", "clock.foo"],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    assert result.returncode == 0, result.stderr
    tasks_dir = Path(env["MOBILEGYM_RUNS_ROOT"]) / env["MOBILEGYM_BATCH_ID"] / "tasks"
    shard_dirs = sorted(p.name for p in tasks_dir.iterdir() if p.is_dir())
    assert len(shard_dirs) == 2, f"expected two distinct slug dirs, got {shard_dirs}"
    assert shard_dirs[0] != shard_dirs[1]
    assert all(name.startswith("clock-foo-") for name in shard_dirs)


def test_cleanup_failure_marks_worker_failed_and_records_in_shard_json(tmp_path):
    log_path, timing_path = install_timing_fake_docker(
        tmp_path, run_sleep_sec=0.05, fail_down_for="alpha-task"
    )
    env = make_env(tmp_path, log_path, timing_path, PARALLEL="1")
    result = subprocess.run(
        ["./parallel_run.sh", "alpha.Task"],
        cwd=DOCKER_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )

    assert result.returncode != 0
    tasks_dir = Path(env["MOBILEGYM_RUNS_ROOT"]) / env["MOBILEGYM_BATCH_ID"] / "tasks"
    shard_dirs = [path for path in tasks_dir.iterdir() if path.is_dir()]
    assert shard_dirs
    payload = json.loads((shard_dirs[0] / "shard.json").read_text())
    assert payload["cleanup_failed"] == 1
    assert payload["exit_code"] == 0
