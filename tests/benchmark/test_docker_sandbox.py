import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import unittest


REPO_ROOT = Path(__file__).resolve().parents[2]


def read_repo_file(relative_path: str) -> str:
    return (REPO_ROOT / relative_path).read_text(encoding="utf-8")


class DockerSandboxContractTest(unittest.TestCase):
    def test_root_compose_exposes_both_web_apps_and_persists_runtime_data(self):
        compose = read_repo_file("compose.yml")

        self.assertIn("docker/dev/Dockerfile", compose)
        self.assertIn('"127.0.0.1:${AIDEN_CONFIG_WEB_PORT:-8000}:80"', compose)
        self.assertIn('"127.0.0.1:${AIDEN_AGENT_WEB_PORT:-8080}:8080"', compose)
        self.assertIn("aiden-data:/userdata", compose)
        self.assertIn("stop_grace_period: 20s", compose)
        self.assertIn("http://127.0.0.1:8080/api/tools", compose)
        self.assertIn("AIDEN_ENVIRONMENT_BRIDGE_ENDPOINT", compose)
        self.assertIn(
            "ENVIRONMENT_BRIDGE_ENDPOINT: ${ENVIRONMENT_BRIDGE_ENDPOINT:-}",
            compose,
        )
        self.assertIn("AIDEN_BENCHMARK_TASK_ID", compose)
        self.assertIn("host.docker.internal:host-gateway", compose)

    def test_sandbox_image_builds_real_agent_and_config_web_binaries(self):
        dockerfile = read_repo_file("docker/dev/Dockerfile")

        self.assertIn("go build", dockerfile)
        self.assertIn("./cmd/daemon", dockerfile)
        self.assertIn("src/config_web.cpp", dockerfile)
        self.assertIn("src/config_web/web/ /oem/usr/share/aiden/config-web/", dockerfile)
        self.assertIn("COPY docker/dev/entrypoint.sh", dockerfile)

    def test_runtime_includes_firmware_python_and_cli_tooling(self):
        dockerfile = read_repo_file("docker/dev/Dockerfile")

        self.assertIn("debian:bookworm-slim AS runtime-base", dockerfile)
        self.assertIn("FROM runtime-base AS runtime-tools-builder", dockerfile)
        self.assertIn("FROM runtime-base AS runtime", dockerfile)
        self.assertIn("wader/fq/releases/download/v0.17.0", dockerfile)
        self.assertIn("mikefarah/yq/releases/download/v4.53.3", dockerfile)
        self.assertIn("BurntSushi/ripgrep/releases/download/15.2.0", dockerfile)
        self.assertIn("sha256sum -c -", dockerfile)
        for package in ("python3", "python3-pip"):
            self.assertIn(package, dockerfile)
        self.assertIn("/out/fq /usr/bin/fq", dockerfile)
        self.assertIn("/out/rg /usr/bin/rg", dockerfile)
        self.assertIn("/out/yq /usr/bin/yq", dockerfile)
        self.assertIn("PYTHONUSERBASE=/userdata/agent/python", dockerfile)
        self.assertIn("PIP_USER=1", dockerfile)
        self.assertIn("PIP_BREAK_SYSTEM_PACKAGES=1", dockerfile)

    def test_runtime_bootstrap_defaults_to_text_and_supports_bridge_setup_and_restart(self):
        entrypoint = read_repo_file("docker/dev/entrypoint.sh")
        service = read_repo_file("docker/dev/agent-service.sh")
        config = read_repo_file("docker/dev/agent.toml")

        self.assertIn('input_mode = "text"', config)
        self.assertIn('trigger_mode = "manual"', config)
        self.assertIn('device_type = "iOS"', config)
        self.assertNotIn("api_key", config)
        self.assertIn("/api/setup", service)
        self.assertIn("benchmark-task-id", service)
        self.assertIn("AIDEN_BRIDGE_EPISODE_ID", service)
        self.assertIn("9>&-", service)
        self.assertIn('setsid "$0" run', service)
        self.assertIn("/proc/1/fd/1", service)
        self.assertIn('>>"$log_file" 2>&1', service)
        self.assertIn("supervisor_start_failed", service)
        self.assertIn("AIDEN_AGENT_LOG_MAX_BYTES", service)
        self.assertIn("AIDEN_AGENT_LOG_RETAIN_BYTES", service)
        self.assertIn("AIDEN_AGENT_STOP_ATTEMPTS", service)
        self.assertIn('kill -TERM "-$supervisor_pid"', service)
        self.assertIn("/api/release", service)
        self.assertIn("valid_bridge_identifier", service)
        self.assertIn("load_system_env", service)
        self.assertIn('case "${1:-}" in', service)
        self.assertIn("restart|reload)", service)
        self.assertIn("AIDEN_AGENT_INIT_SCRIPT", entrypoint)
        self.assertIn("config_web", entrypoint)

    def test_start_and_update_helpers_share_health_checked_startup(self):
        start_script = read_repo_file("scripts/start_docker_sandbox.sh")
        script = read_repo_file("scripts/update_docker_sandbox.sh")
        makefile = read_repo_file("Makefile")

        self.assertIn("docker compose", start_script)
        self.assertIn("--wait", start_script)
        self.assertIn("--wait-timeout", start_script)
        self.assertIn("sandbox-start:", makefile)
        self.assertIn("./scripts/start_docker_sandbox.sh", makefile)
        self.assertIn("--build", script)
        self.assertIn("scripts/start_docker_sandbox.sh", script)
        self.assertNotIn("down -v", start_script)
        self.assertIn("sandbox-update:", makefile)
        self.assertIn("./scripts/update_docker_sandbox.sh", makefile)

    def test_try_aiden_on_pc_is_the_hardware_free_sandbox_entrypoint(self):
        guide = read_repo_file("docs/01-getting-started/try-aiden-on-pc.md")
        readme = read_repo_file("README.md")

        self.assertIn("docker compose up --build", guide)
        self.assertIn("make sandbox-start", guide)
        self.assertIn("make sandbox-update", guide)
        self.assertIn(
            "AIDEN_BRIDGE_EPISODE_ID=my-sandbox-session", guide
        )
        self.assertNotIn("python -m runner webui", guide)
        self.assertNotIn("docker-sandbox.md", readme)
        self.assertFalse(
            (REPO_ROOT / "docs/01-getting-started/docker-sandbox.md").exists()
        )

    def test_docker_smoke_requests_and_ci_step_have_finite_timeouts(self):
        smoke_script = read_repo_file("scripts/test_docker_sandbox.sh")
        workflow = read_repo_file(".github/workflows/ci.yml")

        curl_commands = re.findall(
            r"^[ \t]*curl -fsS.*$", smoke_script, re.MULTILINE
        )
        self.assertGreater(len(curl_commands), 0)
        for command in curl_commands:
            self.assertIn("--max-time", command)

        docker_step = workflow.split(
            "- name: Verify Docker sandbox contract", 1
        )[1].split("- name:", 1)[0]
        self.assertIn("timeout-minutes: 25", docker_step)

    def test_agent_supervisor_truncates_the_persistent_log(self):
        service = REPO_ROOT / "docker/dev/agent-service.sh"

        with tempfile.TemporaryDirectory() as temporary_directory:
            temporary_path = Path(temporary_directory)
            agent = temporary_path / "fake-agent.sh"
            agent.write_text(
                "#!/usr/bin/env python3\n"
                "import signal\n"
                "import sys\n"
                "import time\n"
                "signal.signal(signal.SIGINT, lambda *_: sys.exit(0))\n"
                "signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))\n"
                "sys.stdout.write('0123456789abcdef' * 512)\n"
                "sys.stdout.flush()\n"
                "time.sleep(30)\n",
                encoding="utf-8",
            )
            agent.chmod(0o755)

            agent_directory = temporary_path / "agent"
            log_file = agent_directory / "log/agent.log"
            environment = os.environ.copy()
            environment.update(
                {
                    "AIDEN_AGENT_BIN": str(agent),
                    "AIDEN_AGENT_DIR": str(agent_directory),
                    "AIDEN_AGENT_RUN_DIR": str(temporary_path / "run"),
                    "AIDEN_SYSTEM_ENV": str(temporary_path / "system-env"),
                    "AIDEN_AGENT_LOG_MAX_BYTES": "1024",
                    "AIDEN_AGENT_LOG_RETAIN_BYTES": "256",
                    "AIDEN_AGENT_LOG_CHECK_INTERVAL": "1",
                }
            )
            supervisor = subprocess.Popen(
                ["sh", str(service), "run"],
                env=environment,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                text=True,
            )
            try:
                deadline = time.monotonic() + 5
                while time.monotonic() < deadline:
                    if log_file.exists() and "log_rotated" in log_file.read_text(
                        encoding="utf-8"
                    ):
                        break
                    time.sleep(0.1)
                else:
                    self.fail("agent supervisor did not rotate the log")

                self.assertLess(log_file.stat().st_size, 1024)
            finally:
                supervisor.terminate()
                try:
                    supervisor.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    supervisor.kill()
                    supervisor.wait(timeout=5)

            stderr = supervisor.stderr.read()
            supervisor.stderr.close()
            self.assertEqual(supervisor.returncode, 0, stderr)


if __name__ == "__main__":
    unittest.main()
