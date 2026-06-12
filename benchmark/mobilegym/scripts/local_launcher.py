#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime
import json
import os
import re
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import urlparse
from urllib.request import Request, urlopen


SCRIPT_PATH = Path(__file__).resolve()
BENCHMARK_ROOT = SCRIPT_PATH.parents[2]
HOST = "127.0.0.1"
PORT = 4174
LOG_PATH = Path("/tmp/mobilegym_run.log")
PID_PATH = Path("/tmp/mobilegym_runner.pid")
STATE_NAME = "state.json"
TAIL_BYTES = 64 * 1024

_SAFE_SUITE_SEGMENT = re.compile(r"^[A-Za-z0-9_.\-]+$")
_SAFE_RUN_ID = re.compile(r"^[A-Za-z0-9_\-]+$")


class LauncherError(RuntimeError):
    pass


class RunCommand:
    def __init__(self, argv: list[str], cwd: Path, env: dict[str, str]) -> None:
        self.argv = argv
        self.cwd = cwd
        self.env = env


_process: subprocess.Popen[bytes] | None = None
_process_lock = threading.Lock()


def valid_suite_name(suite: str) -> bool:
    if not suite or suite.startswith("/"):
        return False
    for part in suite.split("/"):
        if part in {"", ".", ".."} or not _SAFE_SUITE_SEGMENT.match(part):
            return False
    return True


def is_safe_run_id(run_id: str) -> bool:
    return bool(run_id and _SAFE_RUN_ID.match(run_id))


def scan_suites(benchmark_root: str | Path = BENCHMARK_ROOT) -> list[dict[str, Any]]:
    root = Path(benchmark_root)
    items: list[dict[str, Any]] = []
    suites_dir = root / "suites"
    if suites_dir.exists():
        for suite_path in sorted(suites_dir.rglob("*.json")):
            if suite_path.name.startswith("._"):
                continue
            rel = suite_path.relative_to(suites_dir)
            items.append(
                {
                    "name": suite_path.stem,
                    "path": str(rel),
                    "custom": rel.parts[:1] == ("custom",),
                    "type": "aiden",
                    "task_count": count_aiden_tasks(suite_path),
                    "concurrent": True,
                }
            )

    items.extend(scan_mobilegym_builtins(root))
    return sorted(items, key=lambda item: (item["type"] != "aiden", item["name"]))


def scan_mobilegym_builtins(root: Path) -> list[dict[str, Any]]:
    all_tasks = root / "mobilegym" / "suites" / "all_tasks.txt"
    try:
        lines = all_tasks.read_text(encoding="utf-8").splitlines()
    except OSError:
        return []

    counts: dict[str, int] = {}
    for line in lines:
        task = line.strip()
        if not task or "." not in task:
            continue
        suite, _ = task.split(".", 1)
        counts[suite] = counts.get(suite, 0) + 1

    return [
        {
            "name": name,
            "type": "mobilegym_builtin",
            "task_count": count,
            "concurrent": True,
        }
        for name, count in sorted(counts.items())
    ]


def count_aiden_tasks(suite_path: Path) -> int:
    try:
        raw = json.loads(suite_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return 0
    tasks = raw.get("tasks")
    return len(tasks) if isinstance(tasks, list) else 0


def build_run_command(
    benchmark_root: str | Path = BENCHMARK_ROOT,
    payload: dict[str, Any] | None = None,
) -> RunCommand:
    payload = payload or {}
    suite = str(payload.get("suite") or "")
    suite_type = str(payload.get("suite_type") or "mobilegym_builtin")
    if not valid_suite_name(suite):
        raise LauncherError(f"invalid suite name: {suite!r}")

    root = Path(benchmark_root)
    docker_dir = root / "mobilegym" / "docker"
    runner = docker_dir / "parallel_run.sh"
    if not runner.exists():
        raise LauncherError(f"missing runner script: {runner}")

    if suite_type == "aiden":
        argv = ["./parallel_run.sh", "--aiden-suite", suite]
    elif suite_type in {"mobilegym_builtin", ""}:
        argv = ["./parallel_run.sh", "--suite", suite]
    else:
        raise LauncherError(f"unknown suite_type: {suite_type!r}")

    limit = int(payload.get("limit") or 0)
    if limit > 0:
        argv.extend(["--limit", str(limit)])

    parallel = int(payload.get("parallel") or 4)
    if parallel < 1:
        parallel = 1
    env = os.environ.copy()
    env.update(model_config_from_payload(payload))
    env["PARALLEL"] = str(parallel)
    env["MOBILEGYM_RUNS_ROOT"] = str(root / "runs" / "mobilegym")
    env.setdefault("MOBILEGYM_BATCH_ID", "batch-" + datetime.datetime.now().strftime("%Y%m%d-%H%M%S"))
    return RunCommand(argv=argv, cwd=docker_dir, env=env)


def start_run(benchmark_root: Path, payload: dict[str, Any]) -> dict[str, Any]:
    global _process
    with _process_lock:
        if _process is not None and _process.poll() is None:
            raise LauncherError("MobileGym benchmark is already running")

        command = build_run_command(benchmark_root, payload)
        validate_model_environment(command.env)
        LOG_PATH.write_text("Starting Mac-local MobileGym benchmark...\n", encoding="utf-8")
        log_file = LOG_PATH.open("ab")
        try:
            _process = subprocess.Popen(
                command.argv,
                cwd=command.cwd,
                env=command.env,
                stdout=log_file,
                stderr=subprocess.STDOUT,
                start_new_session=True,
            )
        finally:
            log_file.close()

        PID_PATH.write_text(str(_process.pid), encoding="utf-8")
        write_state(
            benchmark_root,
            {
                "status": "running",
                "mode": "mobilegym",
                "run_id": command.env.get("MOBILEGYM_BATCH_ID", ""),
                "suite": payload.get("suite"),
                "suite_type": payload.get("suite_type") or "mobilegym_builtin",
                "parallel": int(payload.get("parallel") or 4),
                "total": int(payload.get("limit") or 0),
                "current": 1 if int(payload.get("limit") or 0) else 0,
                "completed": 0,
                "model": current_model_label(command.env),
            },
        )
        threading.Thread(target=watch_process, args=(benchmark_root, _process), daemon=True).start()
        return {"ok": True, "status": "running", "pid": _process.pid}


def watch_process(benchmark_root: Path, process: subprocess.Popen[bytes]) -> None:
    global _process
    rc = process.wait()
    try:
        PID_PATH.unlink()
    except OSError:
        pass
    write_state(benchmark_root, {"status": "idle", "exit_code": rc})
    with _process_lock:
        if _process is process:
            _process = None


def write_state(benchmark_root: Path, state: dict[str, Any]) -> None:
    try:
        (benchmark_root / STATE_NAME).write_text(
            json.dumps(state, separators=(",", ":")),
            encoding="utf-8",
        )
    except OSError:
        pass


def current_status(benchmark_root: Path) -> dict[str, Any]:
    with _process_lock:
        running = _process is not None and _process.poll() is None
    if running:
        try:
            raw = json.loads((benchmark_root / STATE_NAME).read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            raw = {}
        raw["status"] = "running"
        return raw
    return {"status": "idle"}


def read_log() -> str:
    try:
        data = LOG_PATH.read_bytes()
    except OSError:
        return ""
    if len(data) > TAIL_BYTES:
        data = data[-TAIL_BYTES:]
    return data.decode("utf-8", errors="replace")


def list_runs(benchmark_root: Path) -> list[dict[str, Any]]:
    runs_dir = benchmark_root / "runs" / "mobilegym"
    state = read_json_file(benchmark_root / STATE_NAME)
    try:
        run_ids = sorted((p.name for p in runs_dir.iterdir() if p.is_dir()), reverse=True)
    except OSError:
        return []
    items = []
    for run_id in run_ids[:20]:
        summary = read_json_file(runs_dir / run_id / "summary.json")
        state_for_run = state if state.get("run_id") == run_id and state.get("status") == "running" else {}
        totals = {
            "tasks": int(summary.get("tasks") or 0),
            "passed": int(summary.get("passed") or 0),
            "failed": int(summary.get("failed") or 0) + int(summary.get("error") or 0) + int(summary.get("worker_failed") or 0) + int(summary.get("unknown") or 0),
        }
        total = totals["tasks"]
        done = total if total else 0
        items.append(
            {
                "run_id": run_id,
                "suite": summary_suite(summary) or str(state_for_run.get("suite") or "mobilegym"),
                "status": str(state_for_run.get("status") or "done"),
                "progress": f"{done}/{total}" if total else "",
                "model": str(summary.get("model") or state_for_run.get("model") or current_model_label()),
                "totals": totals,
            }
        )
    return items


def read_json_file(path: Path) -> dict[str, Any]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}
    return data if isinstance(data, dict) else {}


def summary_suite(summary: dict[str, Any]) -> str:
    suite = summary.get("suite")
    if isinstance(suite, str) and suite:
        return suite
    suites = summary.get("suites")
    if isinstance(suites, list) and len(suites) == 1 and isinstance(suites[0], dict):
        value = suites[0].get("suite")
        return str(value) if value else ""
    return ""


def current_model_label(env: dict[str, str] | None = None) -> str:
    env = env or os.environ
    for name in ("AIDEN_MODEL", "MODEL_NAME", "MODEL", "OPENAI_MODEL"):
        value = env.get(name)
        if value:
            return value
    return "aiden-go"


def model_config_from_payload(payload: dict[str, Any]) -> dict[str, str]:
    board_url = str(payload.get("board_url") or "").strip()
    if not board_url:
        return {}
    return fetch_board_model_config(board_url)


def fetch_board_model_config(board_url: str) -> dict[str, str]:
    base = board_url.rstrip("/")
    if not base.startswith(("http://", "https://")):
        raise LauncherError("invalid board_url")
    command = "awk '/^\\[model\\]/{in_model=1;next} /^\\[/{in_model=0} in_model{print}' /userdata/agent/agent.toml"
    body = json.dumps({"input": {"command": command, "timeout": 5}}).encode("utf-8")
    request = Request(
        base + "/api/tools/shell",
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urlopen(request, timeout=8) as response:
            raw = response.read().decode("utf-8", errors="replace")
    except OSError as exc:
        raise LauncherError(f"failed to fetch board model config: {exc}") from exc

    try:
        data = json.loads(raw)
    except json.JSONDecodeError:
        output = raw
    else:
        output = str(data.get("output") or data.get("Output") or raw) if isinstance(data, dict) else raw
    return parse_agent_model_config(output)


def parse_agent_model_config(text: str) -> dict[str, str]:
    in_model = "[model]" not in text
    values: dict[str, str] = {}
    for raw_line in text.splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("[") and line.endswith("]"):
            in_model = line == "[model]"
            continue
        if not in_model or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip().split(" #", 1)[0].strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in {'"', "'"}:
            value = value[1:-1]
        if key in {"provider", "model", "base_url", "api_key"} and value:
            values[key] = value

    mapping = {
        "provider": "MODEL_PROVIDER",
        "model": "MODEL_NAME",
        "base_url": "MODEL_BASE_URL",
        "api_key": "MODEL_API_KEY",
    }
    return {env_key: values[key] for key, env_key in mapping.items() if key in values}


def validate_model_environment(env: dict[str, str] | None = None) -> None:
    env = env or os.environ
    provider = env.get("MODEL_PROVIDER") or env.get("AIDEN_MODEL_PROVIDER") or "openrouter"
    if provider == "openrouter" and not (
        env.get("MODEL_API_KEY") or env.get("OPENROUTER_API_KEY") or env.get("AIDEN_MODEL_API_KEY")
    ):
        raise LauncherError(
            "MobileGym model config missing: set MODEL_API_KEY or OPENROUTER_API_KEY "
            "before starting the Mac MobileGym launcher"
        )


def make_handler(benchmark_root: str | Path = BENCHMARK_ROOT) -> type[BaseHTTPRequestHandler]:
    root = Path(benchmark_root)

    class LocalLauncherHandler(BaseHTTPRequestHandler):
        def do_OPTIONS(self) -> None:
            self.send_response(204)
            self.send_common_headers("text/plain; charset=utf-8")
            self.end_headers()

        def do_GET(self) -> None:
            path = urlparse(self.path).path
            if path == "/benchmark/suites":
                self.send_json(scan_suites(root))
            elif path == "/benchmark/status":
                self.send_json(current_status(root))
            elif path == "/benchmark/log":
                self.send_text(read_log())
            elif path == "/benchmark/runs":
                self.send_json(list_runs(root))
            elif path.startswith("/benchmark/report/"):
                self.send_report(path.removeprefix("/benchmark/report/"))
            else:
                self.send_error(404, "not found")

        def do_POST(self) -> None:
            path = urlparse(self.path).path
            if path != "/benchmark/run":
                self.send_error(404, "not found")
                return
            try:
                payload = self.read_json()
                self.send_json(start_run(root, payload))
            except LauncherError as exc:
                self.send_json({"error": str(exc)}, status=400)
            except Exception as exc:
                self.send_json({"error": str(exc)}, status=500)

        def read_json(self) -> dict[str, Any]:
            length = int(self.headers.get("Content-Length") or "0")
            if length == 0:
                return {}
            raw = self.rfile.read(length)
            data = json.loads(raw.decode("utf-8"))
            if not isinstance(data, dict):
                raise LauncherError("request body must be a JSON object")
            return data

        def send_json(self, payload: Any, status: int = 200) -> None:
            body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
            self.send_response(status)
            self.send_common_headers("application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def send_text(self, text: str, status: int = 200) -> None:
            body = text.encode("utf-8")
            self.send_response(status)
            self.send_common_headers("text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def send_report(self, run_id: str) -> None:
            if not is_safe_run_id(run_id):
                self.send_error(404, "not found")
                return
            report_path = root / "runs" / "mobilegym" / run_id / "index.html"
            try:
                body = report_path.read_bytes()
            except OSError:
                self.send_error(404, "not found")
                return
            self.send_response(200)
            self.send_common_headers("text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def send_common_headers(self, content_type: str) -> None:
            self.send_header("Content-Type", content_type)
            self.send_header("Access-Control-Allow-Origin", "*")
            self.send_header("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
            self.send_header("Access-Control-Allow-Headers", "Content-Type")
            self.send_header("Access-Control-Allow-Private-Network", "true")

        def log_message(self, fmt: str, *args: Any) -> None:
            print(f"{self.address_string()} - {fmt % args}", file=sys.stderr)

    return LocalLauncherHandler


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Mac-local MobileGym launcher for the board benchmark UI.")
    parser.add_argument("--benchmark-root", type=Path, default=BENCHMARK_ROOT)
    parser.add_argument("--host", default=HOST)
    parser.add_argument("--port", type=int, default=PORT)
    args = parser.parse_args(argv)

    server = ThreadingHTTPServer((args.host, args.port), make_handler(args.benchmark_root))
    print(f"MobileGym local launcher listening on http://{args.host}:{args.port}")
    print(f"Benchmark root: {args.benchmark_root}")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nStopping MobileGym local launcher")
        return 130
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
