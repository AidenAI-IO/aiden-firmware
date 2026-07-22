#!/usr/bin/env python3
"""Start the VPhone iOS Environment Bridge on the macOS VM host."""

from __future__ import annotations

import argparse
import json
import os
import signal
import stat
import sys
import threading
from pathlib import Path

from dotenv import dotenv_values

from vphone.bridge.client import VPhoneSocketError
from vphone.bridge.device import (
    DEFAULT_JPEG_QUALITY,
    DEFAULT_SCREENSHOT_MAX_WIDTH,
    GuestSSHConfig,
    VPhoneDevice,
)
from vphone.bridge.server import VPhoneBridgeServer


DEFAULT_ENV_FILE = Path(__file__).resolve().parents[1] / "vphone.env"


def _parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    pre_parser = argparse.ArgumentParser(add_help=False)
    pre_parser.add_argument(
        "--env-file",
        default=os.environ.get("VPHONE_ENV_FILE", str(DEFAULT_ENV_FILE)),
    )
    pre_args, _ = pre_parser.parse_known_args(argv)
    env_file = Path(pre_args.env_file).expanduser().resolve()
    file_values = dotenv_values(env_file) if env_file.is_file() else {}
    defaults = {key: str(value) for key, value in file_values.items() if value is not None}
    defaults.update(os.environ)

    parser = argparse.ArgumentParser(prog="vphone.scripts.start_bridge")
    parser.add_argument("--env-file", default=str(env_file), help="VPhone environment file")
    parser.add_argument(
        "--socket",
        default=defaults.get("VPHONE_SOCKET", ""),
        help="Path to vm/vphone.sock",
    )
    parser.add_argument("--host", default=defaults.get("VPHONE_BRIDGE_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=defaults.get("VPHONE_BRIDGE_PORT", "8899"))
    parser.add_argument("--request-timeout-sec", type=float, default=120)
    parser.add_argument("--action-settle-sec", type=float, default=0.6)
    parser.add_argument("--screenshot-max-width", type=int, default=DEFAULT_SCREENSHOT_MAX_WIDTH)
    parser.add_argument("--jpeg-quality", type=int, default=DEFAULT_JPEG_QUALITY)
    parser.add_argument("--guest-ssh-host", default=defaults.get("VPHONE_GUEST_SSH_HOST", ""))
    parser.add_argument(
        "--guest-ssh-port",
        type=int,
        default=defaults.get("VPHONE_GUEST_SSH_PORT", "22222"),
    )
    parser.add_argument("--guest-ssh-user", default=defaults.get("VPHONE_GUEST_SSH_USER", "root"))
    parser.add_argument("--guest-ssh-identity", default=defaults.get("VPHONE_GUEST_SSH_IDENTITY", ""))
    parser.add_argument(
        "--benchmark-task-id",
        default=defaults.get("VPHONE_BENCHMARK_TASK_ID", "vphone-ios-cli"),
    )
    parser.add_argument("--no-guest-ssh-fallback", action="store_true")
    parser.add_argument("--allow-unready", action="store_true", help="Start even when the VM health check fails")
    parser.add_argument("--json", action="store_true")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = _parse_args(argv)

    if not args.socket:
        print("error: VPHONE_SOCKET is required in vphone.env or --socket", file=sys.stderr)
        return 2
    if not args.no_guest_ssh_fallback and not str(args.guest_ssh_host).strip():
        print(
            "error: VPHONE_GUEST_SSH_HOST is required in vphone.env or --guest-ssh-host",
            file=sys.stderr,
        )
        return 2

    socket_path = Path(args.socket).expanduser().resolve()
    try:
        mode = socket_path.stat().st_mode
    except FileNotFoundError:
        print(f"error: VPhone socket does not exist: {socket_path}", file=sys.stderr)
        return 2
    if not stat.S_ISSOCK(mode):
        print(f"error: path is not a Unix socket: {socket_path}", file=sys.stderr)
        return 2
    if args.port < 0 or args.request_timeout_sec <= 0 or args.action_settle_sec < 0:
        print("error: port and timeout/settle values are invalid", file=sys.stderr)
        return 2

    guest_ssh = None
    if not args.no_guest_ssh_fallback:
        guest_ssh = GuestSSHConfig(
            host=args.guest_ssh_host,
            port=args.guest_ssh_port,
            user=args.guest_ssh_user,
            identity_file=args.guest_ssh_identity,
        )
    device = VPhoneDevice(
        socket_path,
        timeout_sec=args.request_timeout_sec,
        screenshot_max_width=args.screenshot_max_width,
        jpeg_quality=args.jpeg_quality,
        guest_ssh=guest_ssh,
    )
    try:
        status = device.check_device()
    except VPhoneSocketError as exc:
        if not args.allow_unready:
            print(f"error: VPhone VM is not ready: [{exc.code}] {exc}", file=sys.stderr)
            device.close()
            return 1
        print(f"warning: VPhone VM is not ready: [{exc.code}] {exc}", file=sys.stderr, flush=True)
        status = {"screen_width": None, "screen_height": None, "capabilities": []}

    server = VPhoneBridgeServer(
        device,
        host=args.host,
        port=args.port,
        request_timeout_sec=args.request_timeout_sec,
        action_settle_sec=args.action_settle_sec,
    )
    try:
        environment_url = server.start()
    except OSError as exc:
        print(f"error: cannot start bridge on {args.host}:{args.port}: {exc}", file=sys.stderr)
        device.close()
        return 1
    daemon_command = (
        "uv run python -m runner start-agent-daemon "
        f"--environment-bridge-endpoint {environment_url} "
        f"--benchmark-task-id {args.benchmark_task_id}"
    )
    payload = {
        "type": "vphone-ios-env",
        "env_file": str(Path(args.env_file).expanduser().resolve()),
        "environment_url": environment_url,
        "socket": str(socket_path),
        "screen_width": status.get("screen_width"),
        "screen_height": status.get("screen_height"),
        "capabilities": status.get("capabilities") or [],
        "benchmark_task_id": args.benchmark_task_id,
        "agent_daemon_command": daemon_command,
    }
    if args.json:
        print(json.dumps(payload, ensure_ascii=False), flush=True)
    else:
        print("VPhone iOS environment started", flush=True)
        for key, value in payload.items():
            if key != "type":
                print(f"{key}={value}", flush=True)

    stop_event = threading.Event()

    def handle_signal(signum: int, frame: object) -> None:
        del signum, frame
        stop_event.set()

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)
    try:
        stop_event.wait()
    finally:
        server.stop()
        device.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
