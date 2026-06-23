from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from runner.agent_client import AgentClient
from runner.webui import (
    BENCHMARK_ROOT,
    DEFAULT_BASE_CONFIG_DIR,
    DEFAULT_DAEMON_IMAGE,
    DEFAULT_DAEMON_READY_TIMEOUT_SEC,
    DEFAULT_MOBILEGYM_IMAGE,
    DEFAULT_MOBILEGYM_READY_TIMEOUT_SEC,
    Job,
    append_log,
    build_mobilegym_environment_command,
    daemon_compose_command,
    endpoint_for_docker,
    ensure_daemon_image,
    ensure_mobilegym_image,
    mobilegym_screen_url,
    prepare_run_config,
    reserve_free_port,
    start_daemon_compose,
    stop_daemon_compose,
    wait_for_http_health,
)


DEFAULT_CLI_RUNS_DIR = BENCHMARK_ROOT / "runs" / "cli-services"
DEFAULT_CLI_BENCHMARK_TASK_ID = "cli-task"


def add_service_parsers(subparsers: argparse._SubParsersAction[argparse.ArgumentParser]) -> None:
    p_agent = subparsers.add_parser(
        "start-agent-daemon",
        help="Start an isolated benchmark agent daemon Docker container.",
    )
    p_agent.add_argument("--port", type=int, default=0, help="Host port for the daemon API (default: auto)")
    p_agent.add_argument("--name", default="", help="Stable name suffix for the service/container")
    p_agent.add_argument("--runs-dir", default=str(DEFAULT_CLI_RUNS_DIR))
    p_agent.add_argument("--base-config-dir", default=str(DEFAULT_BASE_CONFIG_DIR))
    p_agent.add_argument("--agent-config", default="", help="Optional agent.toml to use instead of the base config template")
    p_agent.add_argument("--daemon-image", default=DEFAULT_DAEMON_IMAGE)
    p_agent.add_argument("--no-build-daemon-image", action="store_true")
    p_agent.add_argument("--tool-proxy-endpoint", default="", help="Environment/tool endpoint to forward device tools to")
    p_agent.add_argument("--benchmark-task-id", default=DEFAULT_CLI_BENCHMARK_TASK_ID)
    p_agent.add_argument("--ready-timeout-sec", type=int, default=DEFAULT_DAEMON_READY_TIMEOUT_SEC)
    p_agent.add_argument("--json", action="store_true", help="Print machine-readable JSON")

    p_mobilegym = subparsers.add_parser(
        "start-mobilegym-env",
        help="Start a MobileGym simulator and bridge environment Docker container.",
    )
    p_mobilegym.add_argument("--name", default="", help="Stable name suffix for the environment container")
    p_mobilegym.add_argument("--runs-dir", default=str(DEFAULT_CLI_RUNS_DIR))
    p_mobilegym.add_argument("--envs", "--parallel-envs", dest="parallel_envs", type=int, default=1)
    p_mobilegym.add_argument("--web-port", type=int, default=0, help="Host port for the MobileGym web UI (default: auto)")
    p_mobilegym.add_argument("--bridge-port", type=int, default=0, help="Host port for the bridge API (default: auto)")
    p_mobilegym.add_argument("--mobilegym-image", default=DEFAULT_MOBILEGYM_IMAGE)
    p_mobilegym.add_argument("--no-build-mobilegym-image", action="store_true")
    p_mobilegym.add_argument("--benchmark-task-id", default=DEFAULT_CLI_BENCHMARK_TASK_ID)
    p_mobilegym.add_argument("--ready-timeout-sec", type=int, default=DEFAULT_MOBILEGYM_READY_TIMEOUT_SEC)
    p_mobilegym.add_argument("--json", action="store_true", help="Print machine-readable JSON")


def cmd_start_agent_daemon(args: argparse.Namespace) -> int:
    if args.port < 0:
        print("Error: --port must be zero or positive", file=sys.stderr)
        return 2
    if args.ready_timeout_sec <= 0:
        print("Error: --ready-timeout-sec must be positive", file=sys.stderr)
        return 2

    host_port = args.port or reserve_free_port()
    service_id = _service_id("agent", args.name)
    project = f"aiden-benchmark-agent-{service_id}"
    service_dir = Path(args.runs_dir) / service_id
    config_dir = service_dir / "config"
    log_path = service_dir / "agent-daemon.log"
    service_dir.mkdir(parents=True, exist_ok=True)

    agent_config_text = None
    if args.agent_config:
        agent_config_text = Path(args.agent_config).read_text(encoding="utf-8")
    prepare_run_config(Path(args.base_config_dir), config_dir, agent_config_text=agent_config_text)

    tool_proxy_endpoint = str(args.tool_proxy_endpoint or "").strip().rstrip("/")
    docker_tool_proxy_endpoint = endpoint_for_docker(tool_proxy_endpoint) if tool_proxy_endpoint else ""
    benchmark_task_id = str(args.benchmark_task_id or "").strip()
    agent_url = f"http://127.0.0.1:{host_port}"
    job = Job(
        id=service_id,
        endpoint=tool_proxy_endpoint,
        docker_endpoint=docker_tool_proxy_endpoint,
        suites=[],
        agent_url=agent_url,
        container_name=project,
        config_dir=str(config_dir),
        runner_log=str(log_path),
    )

    try:
        ensure_daemon_image(args.daemon_image, not args.no_build_daemon_image, log_path)
        container_id = start_daemon_compose(
            job,
            image=args.daemon_image,
            host_port=host_port,
            config_dir=config_dir,
            tool_proxy_endpoint=docker_tool_proxy_endpoint,
            benchmark_task_id=benchmark_task_id if docker_tool_proxy_endpoint else "",
            tool_proxy_mode=bool(docker_tool_proxy_endpoint),
            log_path=log_path,
        )
        _wait_for_agent(agent_url, args.ready_timeout_sec)
    except Exception as exc:
        stop_daemon_compose(job)
        append_log(log_path, f"ERROR: {exc}")
        print(f"Error: failed to start agent daemon: {exc}", file=sys.stderr)
        return 1

    payload = {
        "type": "agent-daemon",
        "agent_url": agent_url,
        "container_id": container_id,
        "compose_project": project,
        "config_dir": str(config_dir),
        "log_path": str(log_path),
        "tool_proxy_endpoint": tool_proxy_endpoint,
        "docker_tool_proxy_endpoint": docker_tool_proxy_endpoint,
        "benchmark_task_id": benchmark_task_id if docker_tool_proxy_endpoint else "",
        "stop_command": " ".join(
            daemon_compose_command(
                "down",
                "--volumes",
                "--remove-orphans",
                project=project,
            )
        ),
    }
    payload["run_command"] = _agent_run_command(payload)
    _print_agent_payload(payload, json_output=bool(args.json))
    return 0


def cmd_start_mobilegym_env(args: argparse.Namespace) -> int:
    if args.parallel_envs <= 0:
        print("Error: --envs must be positive", file=sys.stderr)
        return 2
    if args.web_port < 0 or args.bridge_port < 0:
        print("Error: --web-port and --bridge-port must be zero or positive", file=sys.stderr)
        return 2
    if args.ready_timeout_sec <= 0:
        print("Error: --ready-timeout-sec must be positive", file=sys.stderr)
        return 2

    web_port = args.web_port or reserve_free_port()
    bridge_port = args.bridge_port or reserve_free_port()
    service_id = _service_id("mobilegym", args.name)
    container_name = f"aiden-mobilegym-env-{service_id}"
    service_dir = Path(args.runs_dir) / service_id
    log_path = service_dir / "mobilegym-env.log"
    service_dir.mkdir(parents=True, exist_ok=True)

    public_endpoint = f"http://127.0.0.1:{bridge_port}"
    docker_endpoint = f"http://host.docker.internal:{bridge_port}"
    benchmark_task_id = str(args.benchmark_task_id or "").strip()
    screen_url = mobilegym_screen_url(public_endpoint, benchmark_task_id)

    try:
        ensure_mobilegym_image(args.mobilegym_image, not args.no_build_mobilegym_image, log_path)
        command = build_mobilegym_environment_command(
            image=args.mobilegym_image,
            container_name=container_name,
            host_web_port=web_port,
            host_bridge_port=bridge_port,
            benchmark_dir=BENCHMARK_ROOT,
            parallel_envs=args.parallel_envs,
        )
        append_log(log_path, "$ " + " ".join(command))
        container_id = subprocess.check_output(command, cwd=BENCHMARK_ROOT.parent, text=True).strip()
        wait_for_http_health(f"{public_endpoint}/health", args.ready_timeout_sec)
    except Exception as exc:
        subprocess.run(["docker", "rm", "-f", container_name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        append_log(log_path, f"ERROR: {exc}")
        print(f"Error: failed to start MobileGym environment: {exc}", file=sys.stderr)
        return 1

    payload = {
        "type": "mobilegym-env",
        "environment_url": public_endpoint,
        "docker_environment_url": docker_endpoint,
        "web_url": f"http://127.0.0.1:{web_port}",
        "screen_url": screen_url,
        "container_id": container_id,
        "container_name": container_name,
        "parallel_envs": args.parallel_envs,
        "benchmark_task_id": benchmark_task_id,
        "log_path": str(log_path),
        "logs_command": f"docker logs -f {container_name}",
        "stop_command": f"docker rm -f {container_name}",
    }
    payload["agent_daemon_command"] = (
        "uv run python -m runner start-agent-daemon "
        f"--tool-proxy-endpoint {public_endpoint}"
        + (f" --benchmark-task-id {benchmark_task_id}" if benchmark_task_id else "")
    )
    _print_mobilegym_payload(payload, json_output=bool(args.json))
    return 0


def _wait_for_agent(agent_url: str, timeout_sec: int) -> None:
    client = AgentClient(agent_url)
    deadline = time.monotonic() + max(1, timeout_sec)
    try:
        while time.monotonic() < deadline:
            if client.health():
                return
            time.sleep(1)
    finally:
        client.close()
    raise RuntimeError(f"agent daemon did not become ready at {agent_url}")


def _service_id(prefix: str, name: str) -> str:
    raw = str(name or "").strip() or datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    slug = re.sub(r"[^a-z0-9_-]+", "-", raw.lower()).strip("-_")
    if not slug:
        slug = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    if slug.startswith(f"{prefix}-"):
        return slug
    return f"{prefix}-{slug}"


def _print_agent_payload(payload: dict[str, Any], *, json_output: bool) -> None:
    if json_output:
        print(json.dumps(payload, ensure_ascii=False, indent=2), flush=True)
        return
    print("Agent daemon started", flush=True)
    _print_kv("agent_url", payload["agent_url"])
    if payload.get("tool_proxy_endpoint"):
        _print_kv("tool_proxy_endpoint", payload["tool_proxy_endpoint"])
        _print_kv("docker_tool_proxy_endpoint", payload["docker_tool_proxy_endpoint"])
        _print_kv("benchmark_task_id", payload["benchmark_task_id"])
    _print_kv("container_id", payload["container_id"])
    _print_kv("compose_project", payload["compose_project"])
    _print_kv("config_dir", payload["config_dir"])
    _print_kv("log_path", payload["log_path"])
    _print_kv("stop_command", payload["stop_command"])
    _print_kv("run_command", payload["run_command"])
    print("", flush=True)
    print(f"export AIDEN_AGENT_URL={payload['agent_url']}", flush=True)
    if payload.get("benchmark_task_id"):
        print(f"export AIDEN_BENCHMARK_TASK_ID={payload['benchmark_task_id']}", flush=True)


def _print_mobilegym_payload(payload: dict[str, Any], *, json_output: bool) -> None:
    if json_output:
        print(json.dumps(payload, ensure_ascii=False, indent=2), flush=True)
        return
    print("MobileGym environment started", flush=True)
    _print_kv("environment_url", payload["environment_url"])
    _print_kv("docker_environment_url", payload["docker_environment_url"])
    _print_kv("web_url", payload["web_url"])
    _print_kv("screen_url", payload["screen_url"])
    _print_kv("parallel_envs", payload["parallel_envs"])
    _print_kv("benchmark_task_id", payload["benchmark_task_id"])
    _print_kv("container_id", payload["container_id"])
    _print_kv("container_name", payload["container_name"])
    _print_kv("log_path", payload["log_path"])
    _print_kv("logs_command", payload["logs_command"])
    _print_kv("stop_command", payload["stop_command"])
    _print_kv("agent_daemon_command", payload["agent_daemon_command"])
    print("", flush=True)
    print(f"export AIDEN_ENVIRONMENT_URL={payload['environment_url']}", flush=True)
    if payload.get("benchmark_task_id"):
        print(f"export AIDEN_BENCHMARK_TASK_ID={payload['benchmark_task_id']}", flush=True)


def _print_kv(key: str, value: Any) -> None:
    print(f"{key}={value}", flush=True)


def _agent_run_command(payload: dict[str, Any]) -> str:
    parts = [
        "uv run python -m runner run",
        "--suite <suite.json>",
        f"--agent-url {payload['agent_url']}",
    ]
    if payload.get("tool_proxy_endpoint"):
        parts.append(f"--environment-url {payload['tool_proxy_endpoint']}")
    if payload.get("benchmark_task_id"):
        parts.append(f"--benchmark-task-id {payload['benchmark_task_id']}")
    return " ".join(parts)
