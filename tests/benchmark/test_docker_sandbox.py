from pathlib import Path
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
        self.assertIn("AIDEN_BENCHMARK_TASK_ID", compose)
        self.assertIn("host.docker.internal:host-gateway", compose)

    def test_sandbox_image_builds_real_agent_and_config_web_binaries(self):
        dockerfile = read_repo_file("docker/dev/Dockerfile")

        self.assertIn("go build", dockerfile)
        self.assertIn("./cmd/daemon", dockerfile)
        self.assertIn("src/config_web.cpp", dockerfile)
        self.assertIn("src/config_web/web/ /oem/usr/share/aiden/config-web/", dockerfile)
        self.assertIn("COPY docker/dev/entrypoint.sh", dockerfile)

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
        self.assertIn('kill -TERM "-$supervisor_pid"', service)
        self.assertIn("/api/release", service)
        self.assertIn("valid_bridge_identifier", service)
        self.assertIn("load_system_env", service)
        self.assertIn('case "${1:-}" in', service)
        self.assertIn("restart|reload)", service)
        self.assertIn("AIDEN_AGENT_INIT_SCRIPT", entrypoint)
        self.assertIn("config_web", entrypoint)

    def test_update_helper_rebuilds_the_sandbox_and_waits_until_it_is_healthy(self):
        script = read_repo_file("scripts/update_docker_sandbox.sh")
        makefile = read_repo_file("Makefile")

        self.assertIn("docker compose", script)
        self.assertIn("--build", script)
        self.assertIn("--wait", script)
        self.assertIn("--wait-timeout", script)
        self.assertNotIn("down -v", script)
        self.assertIn("sandbox-update:", makefile)
        self.assertIn("./scripts/update_docker_sandbox.sh", makefile)


if __name__ == "__main__":
    unittest.main()
