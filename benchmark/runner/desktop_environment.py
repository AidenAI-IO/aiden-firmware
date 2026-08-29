from __future__ import annotations

import os
import signal
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

from runner.environment import append_log, wait_for_http_health


def start_desktop_bridge_process(*, bridge_host: str, bridge_port: int, backend: str, screenshot_command: str, log_path: Path) -> subprocess.Popen:
    command = [sys.executable, "-m", "desktop.scripts.start_bridge", "--bridge-host", bridge_host, "--bridge-port", str(int(bridge_port)), "--backend", backend]
    if screenshot_command:
        command.extend(["--screenshot-command", screenshot_command])
    append_log(log_path, "$ " + " ".join(command))
    log_file = log_path.open("ab")
    try:
        kwargs: dict[str, Any] = {"stdout": log_file, "stderr": subprocess.STDOUT, "cwd": str(Path(__file__).resolve().parents[1])}
        if os.name == "posix": kwargs["start_new_session"] = True
        return subprocess.Popen(command, **kwargs)
    finally:
        log_file.close()


def pid_alive(pid: int) -> bool:
    if pid <= 0: return False
    try: os.kill(pid, 0)
    except OSError: return False
    return True


def terminate_pid(pid: int, wait_sec: float = 5.0) -> None:
    if not pid_alive(pid): return
    try: os.kill(pid, signal.SIGTERM)
    except OSError: return
    deadline = time.monotonic() + wait_sec
    while time.monotonic() < deadline and pid_alive(pid): time.sleep(0.1)
    if pid_alive(pid):
        try: os.kill(pid, signal.SIGKILL)
        except OSError: pass


def wait_for_desktop_bridge(endpoint: str, timeout_sec: int) -> None:
    wait_for_http_health(f"{endpoint.rstrip('/')}/health", timeout_sec)
