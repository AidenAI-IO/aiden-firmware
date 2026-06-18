from __future__ import annotations

import argparse
import dataclasses as dc
import json
import os
import signal
import shutil
import socket
import subprocess
import sys
import threading
import time
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Callable

from runner.agent_client import AgentClient
from runner.judge import JudgeConfig
from runner.unit import is_unit_suite


REPO_ROOT = Path(__file__).resolve().parents[2]
BENCHMARK_ROOT = REPO_ROOT / "benchmark"
DEFAULT_SUITES_DIR = BENCHMARK_ROOT / "suites"
DEFAULT_RUNS_DIR = BENCHMARK_ROOT / "runs" / "webui"
DEFAULT_BASE_CONFIG_DIR = BENCHMARK_ROOT / "config"
DEFAULT_DAEMON_IMAGE = "aiden-mobilegym-daemon:local"
DEFAULT_MOBILEGYM_IMAGE = "aiden-mobilegym-simulator:py311"
DEFAULT_DAEMON_READY_TIMEOUT_SEC = 90
DEFAULT_MOBILEGYM_READY_TIMEOUT_SEC = 120
DEFAULT_JUDGE_MODEL = JudgeConfig().model
WEBUI_SETTINGS_FILE = "webui-settings.json"
LOG_TAIL_BYTES = 96 * 1024
TERMINAL_JOB_STATUSES = {"passed", "failed", "stopped", "canceled"}
STOP_REQUESTED_JOB_STATUSES = {"stopping", "stopped", "canceled"}


class JobStopped(RuntimeError):
    pass


@dc.dataclass(frozen=True)
class WebUIConfig:
    suites_dir: Path = DEFAULT_SUITES_DIR
    runs_dir: Path = DEFAULT_RUNS_DIR
    base_config_dir: Path = DEFAULT_BASE_CONFIG_DIR
    agent_config_path: Path | None = None
    daemon_image: str = DEFAULT_DAEMON_IMAGE
    mobilegym_image: str = DEFAULT_MOBILEGYM_IMAGE
    build_daemon_image: bool = True
    build_mobilegym_image: bool = True
    daemon_ready_timeout_sec: int = DEFAULT_DAEMON_READY_TIMEOUT_SEC
    mobilegym_ready_timeout_sec: int = DEFAULT_MOBILEGYM_READY_TIMEOUT_SEC


@dc.dataclass
class Job:
    id: str
    endpoint: str
    docker_endpoint: str
    suites: list[str]
    environment_endpoint: str = ""
    environment_id: str = ""
    environment_name: str = ""
    environment_type: str = "device"
    environment_web_url: str = ""
    status: str = "queued"
    message: str = ""
    created_at: str = ""
    started_at: str = ""
    finished_at: str = ""
    agent_url: str = ""
    container_name: str = ""
    config_dir: str = ""
    raw_runs_dir: str = ""
    state_file: str = ""
    runner_log: str = ""
    daemon_log: str = ""
    suite_results: list[dict[str, Any]] = dc.field(default_factory=list)
    no_judge: bool = False
    judge_model: str = DEFAULT_JUDGE_MODEL
    judge_api_key_set: bool = False
    repeats: int | None = None


@dc.dataclass
class MobileGymEnvironment:
    id: str
    name: str
    endpoint: str
    public_endpoint: str
    web_url: str
    status: str = "starting"
    message: str = ""
    created_at: str = ""
    started_at: str = ""
    stopped_at: str = ""
    container_name: str = ""
    container_id: str = ""
    bridge_port: int = 0
    web_port: int = 0
    image: str = ""
    log_path: str = ""
    type: str = "mobilegym"


class BenchmarkWebApp:
    def __init__(self, config: WebUIConfig):
        self.config = config
        self.config.runs_dir.mkdir(parents=True, exist_ok=True)
        self._lock = threading.RLock()
        self._jobs: dict[str, Job] = {}
        self._job_judge_api_keys: dict[str, str] = {}
        self._job_runner_procs: dict[str, subprocess.Popen] = {}
        self._mobilegym_environments: dict[str, MobileGymEnvironment] = {}
        self._mobilegym_log_procs: dict[str, subprocess.Popen] = {}

    def list_suites(self) -> list[dict[str, Any]]:
        return list_benchmark_suites(self.config.suites_dir)

    def list_jobs(self) -> list[dict[str, Any]]:
        with self._lock:
            jobs = [self._job_payload(job) for job in self._jobs.values()]
        jobs.sort(key=lambda item: item.get("created_at", ""), reverse=True)
        return jobs

    def get_agent_config(self) -> dict[str, Any]:
        path = self._agent_config_path()
        content, source = ensure_webui_agent_config(self.config.base_config_dir, path)
        return {
            "content": content,
            "path": str(path),
            "source": source,
        }

    def save_agent_config(self, payload: dict[str, Any]) -> dict[str, Any]:
        content = str(payload.get("content") or "")
        validate_agent_toml(content)
        path = self._agent_config_path()
        write_text_atomic(path, content)
        return {
            "content": content,
            "path": str(path),
            "source": "saved",
        }

    def reset_agent_config(self) -> dict[str, Any]:
        path = self._agent_config_path()
        if path.exists():
            path.unlink()
        content, source = ensure_webui_agent_config(self.config.base_config_dir, path)
        return {
            "content": content,
            "path": str(path),
            "source": source,
        }

    def get_webui_settings(self) -> dict[str, Any]:
        return sanitize_webui_settings(self._load_webui_settings(include_secrets=False))

    def save_webui_settings(self, payload: dict[str, Any]) -> dict[str, Any]:
        current = self._load_webui_settings(include_secrets=True)
        incoming_judge = payload.get("judge") if isinstance(payload.get("judge"), dict) else {}
        current_judge = current.setdefault("judge", {})
        if "enabled" in incoming_judge:
            current_judge["enabled"] = bool(incoming_judge.get("enabled"))
        if "model" in incoming_judge:
            model = str(incoming_judge.get("model") or "").strip() or DEFAULT_JUDGE_MODEL
            current_judge["model"] = model
        if "api_key" in incoming_judge:
            api_key = str(incoming_judge.get("api_key") or "").strip()
            if api_key:
                current_judge["api_key"] = api_key

        if "device_environments" in payload:
            current["device_environments"] = normalize_device_environments(payload.get("device_environments"))
        if "selected_environment_id" in payload:
            current["selected_environment_id"] = str(payload.get("selected_environment_id") or "")

        normalized = normalize_webui_settings(current, include_secrets=True)
        write_json_atomic(self._webui_settings_path(), normalized)
        return sanitize_webui_settings(normalized)

    def get_job(self, job_id: str) -> dict[str, Any] | None:
        with self._lock:
            job = self._jobs.get(job_id)
            if job is None:
                return None
            return self._job_payload(job)

    def read_job_log(self, job_id: str) -> str | None:
        with self._lock:
            job = self._jobs.get(job_id)
            if job is None:
                return None
            paths = [Path(job.runner_log), Path(job.daemon_log)]
        parts = []
        for title, path in (("runner", paths[0]), ("daemon", paths[1])):
            parts.append(f"== {title} ==")
            parts.append(tail_text(path, LOG_TAIL_BYTES))
        return "\n".join(parts)

    def list_mobilegym_environments(self) -> list[dict[str, Any]]:
        with self._lock:
            environments = [self._mobilegym_environment_payload(env) for env in self._mobilegym_environments.values()]
        environments.sort(key=lambda item: item.get("created_at", ""), reverse=True)
        return environments

    def start_mobilegym_environment(self, payload: dict[str, Any]) -> dict[str, Any]:
        name = str(payload.get("name") or "").strip() or "MobileGym"
        env_id = new_environment_id()
        env_dir = self.config.runs_dir / "environments" / env_id
        env_dir.mkdir(parents=True, exist_ok=True)
        web_port = reserve_free_port()
        bridge_port = reserve_free_port()
        env = MobileGymEnvironment(
            id=env_id,
            name=name,
            endpoint=f"http://host.docker.internal:{bridge_port}",
            public_endpoint=f"http://127.0.0.1:{bridge_port}",
            web_url=f"http://127.0.0.1:{web_port}",
            status="building",
            message="ensuring Docker image",
            created_at=now_iso(),
            container_name=f"aiden-mobilegym-env-{env_id}",
            bridge_port=bridge_port,
            web_port=web_port,
            image=self.config.mobilegym_image,
            log_path=str(env_dir / "mobilegym.log"),
        )
        with self._lock:
            self._mobilegym_environments[env.id] = env

        try:
            ensure_mobilegym_image(self.config.mobilegym_image, self.config.build_mobilegym_image, Path(env.log_path))
            self._set_mobilegym_environment(env, status="starting", message="starting container")
            command = build_mobilegym_environment_command(
                image=self.config.mobilegym_image,
                container_name=env.container_name,
                host_web_port=web_port,
                host_bridge_port=bridge_port,
                benchmark_dir=BENCHMARK_ROOT,
            )
            append_log(Path(env.log_path), "$ " + " ".join(command))
            container_id = subprocess.check_output(command, cwd=REPO_ROOT, text=True).strip()
            self._set_mobilegym_environment(env, container_id=container_id, started_at=now_iso(), message="waiting for bridge")
            log_proc = start_docker_logs(env.container_name, Path(env.log_path))
            if log_proc is not None:
                with self._lock:
                    self._mobilegym_log_procs[env.id] = log_proc
            wait_for_http_health(f"{env.public_endpoint}/health", self.config.mobilegym_ready_timeout_sec)
            self._set_mobilegym_environment(env, status="running", message="")
            return self._mobilegym_environment_payload(env)
        except Exception as exc:
            append_log(Path(env.log_path), f"ERROR: {exc}")
            subprocess.run(["docker", "rm", "-f", env.container_name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
            self._stop_mobilegym_log_proc(env.id)
            self._set_mobilegym_environment(env, status="failed", stopped_at=now_iso(), message=str(exc))
            raise RuntimeError(f"failed to start MobileGym environment: {exc}") from exc

    def stop_mobilegym_environment(self, environment_id: str) -> dict[str, Any] | None:
        with self._lock:
            env = self._mobilegym_environments.get(environment_id)
            if env is None:
                return None
            if env.status == "stopped":
                return self._mobilegym_environment_payload(env)
            env.status = "stopping"
            env.message = "stopping container"
        subprocess.run(["docker", "rm", "-f", env.container_name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        self._stop_mobilegym_log_proc(environment_id)
        self._set_mobilegym_environment(env, status="stopped", stopped_at=now_iso(), message="")
        return self._mobilegym_environment_payload(env)

    def delete_mobilegym_environment(self, environment_id: str) -> dict[str, Any] | None:
        env = self.stop_mobilegym_environment(environment_id)
        if env is None:
            return None
        with self._lock:
            removed = self._mobilegym_environments.pop(environment_id, None)
        return self._mobilegym_environment_payload(removed) if removed is not None else env

    def shutdown(self) -> None:
        with self._lock:
            job_ids = list(self._jobs)
            environment_ids = list(self._mobilegym_environments)
        for job_id in job_ids:
            self.stop_job(job_id)
        for environment_id in environment_ids:
            self.stop_mobilegym_environment(environment_id)

    def start_job(self, payload: dict[str, Any]) -> dict[str, Any]:
        endpoint = str(payload.get("endpoint") or "").strip()
        if not endpoint:
            raise ValueError("endpoint is required")
        parsed = urllib.parse.urlparse(endpoint)
        if parsed.scheme not in {"http", "https"} or not parsed.netloc:
            raise ValueError("endpoint must be an http(s) URL")

        suite_keys = payload.get("suites") or []
        if not isinstance(suite_keys, list) or not suite_keys:
            raise ValueError("at least one suite is required")
        suite_keys = [str(item) for item in suite_keys]
        for key in suite_keys:
            resolve_suite_path(self.config.suites_dir, key)

        repeats = payload.get("repeats")
        repeats_value = None
        if repeats not in (None, ""):
            repeats_value = int(repeats)
            if repeats_value <= 0:
                raise ValueError("repeats must be positive")

        environment_payload = payload.get("environment") if isinstance(payload.get("environment"), dict) else {}
        environment_type = str(payload.get("environment_type") or environment_payload.get("type") or "device")
        if environment_type not in {"device", "mobilegym"}:
            raise ValueError("environment_type must be device or mobilegym")

        settings = self._load_webui_settings(include_secrets=True)
        judge_settings = settings.get("judge") if isinstance(settings.get("judge"), dict) else {}
        no_judge = bool(payload.get("no_judge")) if "no_judge" in payload else not bool(judge_settings.get("enabled", True))
        judge_model = (
            str(payload.get("judge_model") or "").strip()
            or str(judge_settings.get("model") or "").strip()
            or DEFAULT_JUDGE_MODEL
        )
        judge_api_key = (
            str(payload.get("judge_api_key") or "").strip()
            or str(judge_settings.get("api_key") or "").strip()
        )

        job_id = new_job_id()
        job_dir = self.config.runs_dir / job_id
        raw_runs_dir = job_dir / "raw"
        job_dir.mkdir(parents=True, exist_ok=True)
        raw_runs_dir.mkdir(parents=True, exist_ok=True)
        port = reserve_free_port()
        now = now_iso()
        environment_id = str(payload.get("environment_id") or environment_payload.get("id") or "")
        environment_name = str(payload.get("environment_name") or environment_payload.get("name") or "")
        environment_endpoint = str(payload.get("environment_endpoint") or "").strip()
        environment_web_url = str(payload.get("environment_web_url") or environment_payload.get("web_url") or "").strip()
        if environment_type == "mobilegym":
            with self._lock:
                mobilegym_env = self._mobilegym_environments.get(environment_id)
            if mobilegym_env is not None:
                environment_endpoint = mobilegym_env.public_endpoint.rstrip("/")
                environment_web_url = mobilegym_env.web_url.rstrip("/")
            elif not environment_endpoint:
                public_endpoint = str(environment_payload.get("public_endpoint") or "").strip()
                if public_endpoint:
                    environment_endpoint = public_endpoint.rstrip("/")

        job = Job(
            id=job_id,
            endpoint=endpoint,
            docker_endpoint=endpoint_for_docker(endpoint),
            suites=suite_keys,
            environment_endpoint=environment_endpoint,
            environment_id=environment_id,
            environment_name=environment_name,
            environment_type=environment_type,
            environment_web_url=environment_web_url,
            status="queued",
            created_at=now,
            agent_url=f"http://127.0.0.1:{port}",
            container_name=f"aiden-benchmark-agent-{job_id}",
            config_dir=str(job_dir / "config"),
            raw_runs_dir=str(raw_runs_dir),
            state_file=str(job_dir / "state.json"),
            runner_log=str(job_dir / "runner.log"),
            daemon_log=str(job_dir / "daemon.log"),
            no_judge=no_judge,
            judge_model=judge_model,
            judge_api_key_set=bool(judge_api_key) and not no_judge,
            repeats=repeats_value,
        )
        with self._lock:
            self._jobs[job.id] = job
            if judge_api_key and not no_judge:
                self._job_judge_api_keys[job.id] = judge_api_key
        thread = threading.Thread(target=self._run_job, args=(job,), name=f"benchmark-{job.id}", daemon=True)
        thread.start()
        return self._job_payload(job)

    def cancel_job(self, job_id: str) -> dict[str, Any] | None:
        return self.stop_job(job_id)

    def stop_job(self, job_id: str) -> dict[str, Any] | None:
        with self._lock:
            job = self._jobs.get(job_id)
            if job is None:
                return None
            if job.status in TERMINAL_JOB_STATUSES:
                return self._job_payload(job)
            job.status = "stopping"
            job.message = "stop requested"
            proc = self._job_runner_procs.get(job.id)
            runner_log = Path(job.runner_log) if job.runner_log else None
            state_file = Path(job.state_file) if job.state_file else None
        if runner_log is not None:
            append_log(runner_log, "STOP requested")
        if state_file is not None:
            update_state_status(state_file, "stopping", run_id=job.id)
        terminate_process_tree(proc)
        subprocess.run(["docker", "rm", "-f", job.container_name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        return self._job_payload(job)

    def _set_job(self, job: Job, **updates: Any) -> None:
        with self._lock:
            if job.status in STOP_REQUESTED_JOB_STATUSES and updates.get("status") not in STOP_REQUESTED_JOB_STATUSES:
                updates.pop("status", None)
                updates.pop("message", None)
            for key, value in updates.items():
                setattr(job, key, value)

    def _set_mobilegym_environment(self, env: MobileGymEnvironment, **updates: Any) -> None:
        with self._lock:
            for key, value in updates.items():
                setattr(env, key, value)

    def _job_stop_requested(self, job: Job) -> bool:
        with self._lock:
            return job.status in STOP_REQUESTED_JOB_STATUSES

    def _raise_if_job_stop_requested(self, job: Job) -> None:
        if self._job_stop_requested(job):
            raise JobStopped("job stop requested")

    def _finish_stopped_job(self, job: Job) -> None:
        if job.state_file:
            update_state_status(Path(job.state_file), "stopped", run_id=job.id)
        self._set_job(job, status="stopped", finished_at=now_iso(), message="")

    def _agent_config_path(self) -> Path:
        return self.config.agent_config_path or (self.config.runs_dir / "agent.toml")

    def _webui_settings_path(self) -> Path:
        return self.config.runs_dir / WEBUI_SETTINGS_FILE

    def _load_webui_settings(self, include_secrets: bool = False) -> dict[str, Any]:
        return load_webui_settings(self._webui_settings_path(), include_secrets=include_secrets)

    def _job_payload(self, job: Job) -> dict[str, Any]:
        payload = dc.asdict(job)
        payload["progress"] = read_json_file(Path(job.state_file))
        payload["suite_results"] = list(job.suite_results)
        payload["totals"] = aggregate_totals(job.suite_results)
        return payload

    def _mobilegym_environment_payload(self, env: MobileGymEnvironment) -> dict[str, Any]:
        payload = dc.asdict(env)
        payload["log_tail"] = tail_text(Path(env.log_path), LOG_TAIL_BYTES)
        return payload

    def _stop_mobilegym_log_proc(self, environment_id: str) -> None:
        with self._lock:
            proc = self._mobilegym_log_procs.pop(environment_id, None)
        if proc is not None and proc.poll() is None:
            proc.terminate()

    def _run_job(self, job: Job) -> None:
        host_port = int(urllib.parse.urlparse(job.agent_url).port or 0)
        try:
            self._raise_if_job_stop_requested(job)
            self._set_job(job, status="preparing", started_at=now_iso(), message="preparing config")
            agent_config_text = self.get_agent_config()["content"]
            prepare_run_config(self.config.base_config_dir, Path(job.config_dir), agent_config_text=agent_config_text)
            self._raise_if_job_stop_requested(job)
            self._set_job(job, status="starting_agent", message="starting docker agent")
            ensure_daemon_image(
                self.config.daemon_image,
                self.config.build_daemon_image,
                Path(job.runner_log),
                stop_requested=lambda: self._job_stop_requested(job),
            )
            self._raise_if_job_stop_requested(job)
            command = build_docker_run_command(
                image=self.config.daemon_image,
                container_name=job.container_name,
                host_port=host_port,
                config_dir=Path(job.config_dir),
                tool_proxy_endpoint=job.docker_endpoint,
            )
            append_log(Path(job.runner_log), "$ " + " ".join(command))
            container_id = subprocess.check_output(command, cwd=REPO_ROOT, text=True).strip()
            append_log(Path(job.runner_log), f"container {container_id}")
            log_proc = start_docker_logs(job.container_name, Path(job.daemon_log))
            try:
                self._wait_for_daemon(job)
                self._raise_if_job_stop_requested(job)
                self._set_job(job, status="running", message="running suites")
                for suite_key in job.suites:
                    self._raise_if_job_stop_requested(job)
                    self._run_suite(job, suite_key)
            finally:
                if log_proc is not None:
                    log_proc.terminate()
                subprocess.run(["docker", "rm", "-f", job.container_name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
            self._raise_if_job_stop_requested(job)
            final_status = "passed" if job.suite_results and all(item.get("exit_code") == 0 for item in job.suite_results) else "failed"
            self._set_job(job, status=final_status, finished_at=now_iso(), message="")
        except JobStopped:
            append_log(Path(job.runner_log), "STOPPED")
            subprocess.run(["docker", "rm", "-f", job.container_name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
            self._finish_stopped_job(job)
        except Exception as exc:
            append_log(Path(job.runner_log), f"ERROR: {exc}")
            subprocess.run(["docker", "rm", "-f", job.container_name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
            if self._job_stop_requested(job):
                self._finish_stopped_job(job)
            else:
                self._set_job(job, status="failed", finished_at=now_iso(), message=str(exc))
        finally:
            with self._lock:
                self._job_judge_api_keys.pop(job.id, None)

    def _wait_for_daemon(self, job: Job) -> None:
        client = AgentClient(job.agent_url)
        deadline = time.monotonic() + max(1, self.config.daemon_ready_timeout_sec)
        while time.monotonic() < deadline:
            self._raise_if_job_stop_requested(job)
            if client.health():
                return
            time.sleep(1)
        raise RuntimeError(f"agent daemon did not become ready at {job.agent_url}")

    def _run_suite(self, job: Job, suite_key: str) -> None:
        self._raise_if_job_stop_requested(job)
        suite_path = resolve_suite_path(self.config.suites_dir, suite_key)
        suite_is_unit = is_unit_suite(suite_path)
        write_state(
            Path(job.state_file),
            {
                "status": "running",
                "suite": suite_key,
                "run_id": job.id,
                "total": 1 if suite_is_unit else 0,
                "completed": 0,
                "current": 1,
            },
        )
        existing = {p.name for p in Path(job.raw_runs_dir).iterdir() if p.is_dir()}
        cmd = [
            sys.executable,
            "-m",
            "runner.main",
            "unit" if suite_is_unit else "run",
            "--suite",
            str(suite_path),
            "--agent-url",
            job.agent_url,
            "--out",
            job.raw_runs_dir,
        ]
        if not suite_is_unit:
            cmd.extend(["--state-file", job.state_file])
            if job.environment_endpoint:
                cmd.extend(["--environment-url", job.environment_endpoint])
            if job.no_judge:
                cmd.append("--no-judge")
            else:
                cmd.extend(["--judge-model", job.judge_model or DEFAULT_JUDGE_MODEL])
            if job.repeats:
                cmd.extend(["--repeats", str(job.repeats)])
        append_log(Path(job.runner_log), "\n$ " + " ".join(cmd))
        env = os.environ.copy()
        if not suite_is_unit and not job.no_judge:
            with self._lock:
                judge_api_key = self._job_judge_api_keys.get(job.id, "")
            if judge_api_key:
                env["OPENROUTER_API_KEY"] = judge_api_key
        self._raise_if_job_stop_requested(job)
        popen_kwargs: dict[str, Any] = {}
        if os.name == "posix":
            popen_kwargs["start_new_session"] = True
        with Path(job.runner_log).open("ab") as log:
            proc = subprocess.Popen(
                cmd,
                cwd=BENCHMARK_ROOT,
                stdout=log,
                stderr=subprocess.STDOUT,
                env=env,
                **popen_kwargs,
            )
            with self._lock:
                self._job_runner_procs[job.id] = proc
            try:
                exit_code = proc.wait()
            finally:
                with self._lock:
                    if self._job_runner_procs.get(job.id) is proc:
                        self._job_runner_procs.pop(job.id, None)
        new_runs = sorted(
            (p for p in Path(job.raw_runs_dir).iterdir() if p.is_dir() and p.name not in existing),
            key=lambda p: p.stat().st_mtime,
        )
        result = {
            "suite": suite_key,
            "exit_code": exit_code,
            "run_id": new_runs[-1].name if new_runs else "",
        }
        if new_runs:
            manifest = read_json_file(new_runs[-1] / "manifest.json") or {}
            result["manifest"] = manifest
            result["report_url"] = f"/reports/{job.id}/{new_runs[-1].name}/report.html"
        if suite_is_unit:
            write_state(
                Path(job.state_file),
                {
                    "status": "stopped" if self._job_stop_requested(job) else "done" if exit_code == 0 else "failed",
                    "suite": suite_key,
                    "run_id": job.id,
                    "total": 1,
                    "completed": 1,
                    "current": 1,
                },
            )
        if self._job_stop_requested(job):
            update_state_status(Path(job.state_file), "stopped", run_id=job.id)
            result["stopped"] = True
        with self._lock:
            job.suite_results.append(result)
        if self._job_stop_requested(job):
            raise JobStopped("job stop requested")


def list_benchmark_suites(suites_dir: Path) -> list[dict[str, Any]]:
    suites = []
    if not suites_dir.exists():
        return suites
    for path in sorted(suites_dir.rglob("*.json")):
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except Exception as exc:
            item = {
                "key": path.relative_to(suites_dir).as_posix(),
                "name": path.stem,
                "kind": "invalid",
                "task_count": 0,
                "error": str(exc),
            }
        else:
            kind = "unit" if data.get("kind") == "unit" else "benchmark"
            entries = data.get("tests") if kind == "unit" else data.get("tasks")
            entries = entries if isinstance(entries, list) else []
            categories = sorted(
                {str(task.get("category")) for task in entries if isinstance(task, dict) and task.get("category")}
            )
            item = {
                "key": path.relative_to(suites_dir).as_posix(),
                "name": str(data.get("name") or path.stem),
                "kind": kind,
                "task_count": len(entries),
                "categories": categories,
            }
        suites.append(item)
    return suites


def resolve_suite_path(suites_dir: Path, key: str) -> Path:
    pure = Path(key)
    if pure.is_absolute() or ".." in pure.parts or not key.endswith(".json"):
        raise ValueError(f"invalid suite key: {key}")
    path = (suites_dir / pure).resolve()
    root = suites_dir.resolve()
    if root != path and root not in path.parents:
        raise ValueError(f"invalid suite key: {key}")
    if not path.exists() or not path.is_file():
        raise ValueError(f"suite not found: {key}")
    return path


def prepare_run_config(base_config_dir: Path, dest_dir: Path, agent_config_text: str | None = None) -> None:
    if dest_dir.exists():
        shutil.rmtree(dest_dir)
    dest_dir.mkdir(parents=True, exist_ok=True)
    if base_config_dir.exists():
        for item in base_config_dir.iterdir():
            target = dest_dir / item.name
            if item.is_dir():
                shutil.copytree(item, target)
            else:
                shutil.copy2(item, target)
    for name in ("log", "memory", "skill-state"):
        (dest_dir / name).mkdir(parents=True, exist_ok=True)
    token_path = dest_dir / "control_token"
    if not token_path.exists():
        token_path.write_text(os.urandom(24).hex(), encoding="utf-8")
    template = dest_dir / "agent.toml.template"
    config = dest_dir / "agent.toml"
    if agent_config_text is not None:
        config.write_text(agent_config_text, encoding="utf-8")
    else:
        if template.exists() and not config.exists():
            config.write_text(render_agent_template(template.read_text(encoding="utf-8")), encoding="utf-8")
        if not config.exists():
            config.write_text(default_agent_toml(), encoding="utf-8")


def ensure_webui_agent_config(base_config_dir: Path, agent_config_path: Path) -> tuple[str, str]:
    if agent_config_path.exists():
        return agent_config_path.read_text(encoding="utf-8"), "saved"
    content = initial_agent_config(base_config_dir)
    write_text_atomic(agent_config_path, content)
    return content, "generated"


def initial_agent_config(base_config_dir: Path) -> str:
    config = base_config_dir / "agent.toml"
    if config.exists():
        return config.read_text(encoding="utf-8")
    template = base_config_dir / "agent.toml.template"
    if template.exists():
        return render_agent_template(template.read_text(encoding="utf-8"))
    return default_agent_toml()


def validate_agent_toml(content: str) -> None:
    if not content.strip():
        raise ValueError("agent.toml cannot be empty")
    try:
        import tomllib
    except ModuleNotFoundError:
        return
    try:
        tomllib.loads(content)
    except tomllib.TOMLDecodeError as exc:
        raise ValueError(f"invalid agent.toml: {exc}") from exc


def default_webui_settings(include_secrets: bool = False) -> dict[str, Any]:
    judge: dict[str, Any] = {
        "enabled": True,
        "model": DEFAULT_JUDGE_MODEL,
    }
    if include_secrets:
        judge["api_key"] = ""
    else:
        judge["has_api_key"] = False
    return {
        "judge": judge,
        "device_environments": [],
        "selected_environment_id": "",
    }


def normalize_device_environments(raw: Any) -> list[dict[str, str]]:
    if not isinstance(raw, list):
        return []
    out: list[dict[str, str]] = []
    seen: set[str] = set()
    for index, item in enumerate(raw):
        if not isinstance(item, dict):
            continue
        env_id = str(item.get("id") or "").strip() or f"device-{index + 1}"
        name = str(item.get("name") or "").strip()
        endpoint = str(item.get("endpoint") or "").strip()
        if not name or not endpoint or env_id in seen:
            continue
        seen.add(env_id)
        out.append({
            "id": env_id,
            "name": name,
            "endpoint": endpoint,
        })
    return out


def normalize_webui_settings(data: Any, include_secrets: bool = False) -> dict[str, Any]:
    if not isinstance(data, dict):
        data = {}
    raw_judge = data.get("judge") if isinstance(data.get("judge"), dict) else {}
    api_key = str(raw_judge.get("api_key") or "").strip()
    has_api_key = bool(api_key) or bool(raw_judge.get("has_api_key", False))
    judge: dict[str, Any] = {
        "enabled": bool(raw_judge.get("enabled", True)),
        "model": str(raw_judge.get("model") or DEFAULT_JUDGE_MODEL).strip() or DEFAULT_JUDGE_MODEL,
    }
    if include_secrets:
        judge["api_key"] = api_key
    else:
        judge["has_api_key"] = has_api_key
    return {
        "judge": judge,
        "device_environments": normalize_device_environments(data.get("device_environments")),
        "selected_environment_id": str(data.get("selected_environment_id") or ""),
    }


def sanitize_webui_settings(data: dict[str, Any]) -> dict[str, Any]:
    return normalize_webui_settings(data, include_secrets=False)


def load_webui_settings(path: Path, include_secrets: bool = False) -> dict[str, Any]:
    if not path.exists():
        return default_webui_settings(include_secrets=include_secrets)
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        data = {}
    return normalize_webui_settings(data, include_secrets=include_secrets)


def write_text_atomic(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(content, encoding="utf-8")
    tmp.replace(path)


def write_json_atomic(path: Path, payload: dict[str, Any]) -> None:
    write_text_atomic(path, json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n")


def render_agent_template(text: str) -> str:
    replacements = {
        "MODEL_PROVIDER": os.getenv("MODEL_PROVIDER") or os.getenv("AIDEN_MODEL_PROVIDER") or "fake",
        "MODEL_NAME": os.getenv("MODEL_NAME") or os.getenv("AIDEN_MODEL") or os.getenv("OPENAI_MODEL") or "",
        "MODEL_BASE_URL": os.getenv("MODEL_BASE_URL") or os.getenv("AIDEN_MODEL_BASE_URL") or "",
        "MODEL_API_KEY": os.getenv("MODEL_API_KEY") or os.getenv("OPENROUTER_API_KEY") or os.getenv("AIDEN_MODEL_API_KEY") or "",
        "CONTROL_TOKEN_FILE": "/config/control_token",
    }
    rendered = text
    for key, value in replacements.items():
        rendered = rendered.replace("{{" + key + "}}", value.replace('"', '\\"'))
    return rendered


def default_agent_toml() -> str:
    return "\n".join(
        [
            'instruction = ""',
            'input_mode = "text"',
            'trigger_mode = "manual"',
            "max_iterations = -1",
            "force_simple_loop = false",
            "screenshot_keep_n = 3",
            "screenshot_prune_interval = 25",
            "screen_stable_timeout_ms = 3500",
            "screen_stable_ms = 500",
            "screen_stable_diff_threshold = 2",
            "",
            "[model]",
            'provider = "openrouter"',
            'model = "qwen3.6-35b"',
            'base_url = ""',
            'api_key = ""',
            "temperature = 0.2",
            "max_response_tokens = 1000",
            "",
        ]
    )


def endpoint_for_docker(endpoint: str) -> str:
    parsed = urllib.parse.urlparse(endpoint)
    if parsed.hostname not in {"localhost", "127.0.0.1", "::1"}:
        return endpoint.rstrip("/")
    netloc = "host.docker.internal"
    if parsed.port:
        netloc += f":{parsed.port}"
    if parsed.username:
        userinfo = parsed.username
        if parsed.password:
            userinfo += f":{parsed.password}"
        netloc = f"{userinfo}@{netloc}"
    return urllib.parse.urlunparse((parsed.scheme, netloc, parsed.path, parsed.params, parsed.query, parsed.fragment)).rstrip("/")


def reserve_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def build_docker_run_command(
    *,
    image: str,
    container_name: str,
    host_port: int,
    config_dir: Path,
    tool_proxy_endpoint: str,
) -> list[str]:
    script = (
        'set -eu; '
        'runtime_config_dir="${AIDEN_RUNTIME_CONFIG_DIR:-/tmp/aiden-config}"; '
        'rm -rf "$runtime_config_dir"; '
        'mkdir -p "$runtime_config_dir"; '
        'cp -a /config/. "$runtime_config_dir"/; '
        'mkdir -p "$runtime_config_dir/log" "$runtime_config_dir/memory" "$runtime_config_dir/skill-state"; '
        'exec daemon -config "$runtime_config_dir" -addr 0.0.0.0:8080 '
        '--tool-proxy-mode --tool-proxy-endpoint "$TOOL_PROXY_ENDPOINT" --forward-tools "*"'
    )
    return [
        "docker",
        "run",
        "--rm",
        "-d",
        "--name",
        container_name,
        "--add-host",
        "host.docker.internal:host-gateway",
        "-p",
        f"127.0.0.1:{host_port}:8080",
        "-v",
        f"{config_dir.resolve()}:/config:ro",
        "-e",
        f"TOOL_PROXY_ENDPOINT={tool_proxy_endpoint}",
        "-e",
        f"NO_PROXY={docker_no_proxy(tool_proxy_endpoint)}",
        "-e",
        f"no_proxy={docker_no_proxy(tool_proxy_endpoint)}",
        image,
        "sh",
        "-lc",
        script,
    ]


def build_mobilegym_environment_command(
    *,
    image: str,
    container_name: str,
    host_web_port: int,
    host_bridge_port: int,
    benchmark_dir: Path,
    bridge_port: int = 9090,
) -> list[str]:
    script = "\n".join(
        [
            "set -eu",
            'export PATH="/opt/venv/bin:$PATH"',
            "cd /mobilegym",
            "npm run preview -- --host 0.0.0.0 --port 4173 >/tmp/mobilegym-preview.log 2>&1 &",
            'preview_pid="$!"',
            'cleanup() { kill "$preview_pid" 2>/dev/null || true; }',
            "trap cleanup INT TERM EXIT",
            "ready=0",
            "for _ in $(seq 1 120); do",
            (
                "  if python3 -c \"import urllib.request; "
                "urllib.request.urlopen('http://127.0.0.1:4173', timeout=1).read(1)\" "
                ">/dev/null 2>&1; then"
            ),
            "    ready=1",
            "    break",
            "  fi",
            '  if ! kill -0 "$preview_pid" 2>/dev/null; then',
            "    cat /tmp/mobilegym-preview.log >&2",
            "    exit 1",
            "  fi",
            "  sleep 1",
            "done",
            'if [ "$ready" != "1" ]; then',
            "  cat /tmp/mobilegym-preview.log >&2",
            "  exit 1",
            "fi",
            (
                "exec python3 /app/benchmark/mobilegym/scripts/start_simulator.py "
                "--mobilegym-root /mobilegym "
                "--env-url http://127.0.0.1:4173 "
                "--bridge-host 0.0.0.0 "
                f"--bridge-port {bridge_port} "
                "--headless"
            ),
        ]
    )
    bridge_endpoint = f"http://host.docker.internal:{host_bridge_port}"
    return [
        "docker",
        "run",
        "--rm",
        "-d",
        "--name",
        container_name,
        "--add-host",
        "host.docker.internal:host-gateway",
        "-p",
        f"127.0.0.1:{host_web_port}:4173",
        "-p",
        f"{host_bridge_port}:{bridge_port}",
        "-v",
        f"{benchmark_dir.resolve()}:/app/benchmark:ro",
        "-e",
        "MOBILEGYM_ROOT=/mobilegym",
        "-e",
        f"NO_PROXY={docker_no_proxy(bridge_endpoint)}",
        "-e",
        f"no_proxy={docker_no_proxy(bridge_endpoint)}",
        "--entrypoint",
        "sh",
        image,
        "-c",
        script,
    ]


def docker_no_proxy(endpoint: str) -> str:
    base = ["localhost", "127.0.0.1", "host.docker.internal"]
    host = urllib.parse.urlparse(endpoint).hostname
    if host and host not in base:
        base.append(host)
    existing = os.getenv("NO_PROXY") or os.getenv("no_proxy") or ""
    for item in existing.split(","):
        item = item.strip()
        if item and item not in base:
            base.append(item)
    return ",".join(base)


def ensure_daemon_image(
    image: str,
    build_missing: bool,
    log_path: Path,
    *,
    stop_requested: Callable[[], bool] | None = None,
) -> None:
    ensure_docker_image(image, build_missing, log_path, "daemon-runtime", stop_requested=stop_requested)


def ensure_mobilegym_image(image: str, build_missing: bool, log_path: Path) -> None:
    ensure_docker_image(image, build_missing, log_path, "mobilegym-base")


def ensure_docker_image(
    image: str,
    build_missing: bool,
    log_path: Path,
    target: str,
    *,
    stop_requested: Callable[[], bool] | None = None,
) -> None:
    inspect = subprocess.run(["docker", "image", "inspect", image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
    if inspect.returncode == 0:
        return
    if not build_missing:
        raise RuntimeError(f"Docker image not found: {image}")
    cmd = [
        "docker",
        "build",
        "-f",
        str(REPO_ROOT / "benchmark" / "mobilegym" / "docker" / "Dockerfile"),
        "--target",
        target,
        "-t",
        image,
        str(REPO_ROOT),
    ]
    append_log(log_path, "$ " + " ".join(cmd))
    popen_kwargs: dict[str, Any] = {}
    if os.name == "posix":
        popen_kwargs["start_new_session"] = True
    with log_path.open("ab") as log:
        proc = subprocess.Popen(cmd, stdout=log, stderr=subprocess.STDOUT, **popen_kwargs)
        while proc.poll() is None:
            if stop_requested is not None and stop_requested():
                terminate_process_tree(proc)
                raise JobStopped("job stop requested")
            time.sleep(0.25)
    if proc.returncode != 0:
        raise subprocess.CalledProcessError(proc.returncode, cmd)


def wait_for_http_health(url: str, timeout_sec: int) -> None:
    deadline = time.monotonic() + max(1, timeout_sec)
    last_error = ""
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=2) as response:
                if 200 <= response.status < 300:
                    return
        except Exception as exc:
            last_error = str(exc)
        time.sleep(1)
    suffix = f": {last_error}" if last_error else ""
    raise RuntimeError(f"service did not become ready at {url}{suffix}")


def start_docker_logs(container_name: str, log_path: Path) -> subprocess.Popen | None:
    try:
        log_file = log_path.open("ab")
        try:
            return subprocess.Popen(["docker", "logs", "-f", container_name], stdout=log_file, stderr=subprocess.STDOUT)
        finally:
            log_file.close()
    except Exception:
        return None


def aggregate_totals(results: list[dict[str, Any]]) -> dict[str, int]:
    totals = {"tasks": 0, "passed": 0, "failed": 0, "skipped": 0, "judge_error": 0, "timeout": 0}
    for result in results:
        manifest = result.get("manifest") or {}
        for key, value in (manifest.get("totals") or {}).items():
            if isinstance(value, int):
                totals[key] = totals.get(key, 0) + value
    return totals


def read_json_file(path: Path) -> dict[str, Any] | None:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
        return data if isinstance(data, dict) else None
    except Exception:
        return None


def write_state(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(payload, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")
    tmp.replace(path)


def update_state_status(path: Path, status: str, *, run_id: str = "") -> None:
    payload = read_json_file(path) or {}
    payload["status"] = status
    if run_id and not payload.get("run_id"):
        payload["run_id"] = run_id
    write_state(path, payload)


def terminate_process_tree(proc: subprocess.Popen | None, timeout_sec: float = 3.0) -> None:
    if proc is None or proc.poll() is not None:
        return
    try:
        if os.name == "posix":
            os.killpg(proc.pid, signal.SIGTERM)
        else:
            proc.terminate()
    except ProcessLookupError:
        return
    except Exception:
        try:
            proc.terminate()
        except Exception:
            return
    try:
        proc.wait(timeout=timeout_sec)
        return
    except subprocess.TimeoutExpired:
        pass
    except Exception:
        return
    try:
        if os.name == "posix":
            os.killpg(proc.pid, signal.SIGKILL)
        else:
            proc.kill()
    except ProcessLookupError:
        return
    except Exception:
        try:
            proc.kill()
        except Exception:
            return
    try:
        proc.wait(timeout=1)
    except Exception:
        return


def tail_text(path: Path, max_bytes: int) -> str:
    try:
        with path.open("rb") as handle:
            handle.seek(0, os.SEEK_END)
            size = handle.tell()
            handle.seek(max(0, size - max_bytes))
            return handle.read().decode("utf-8", errors="replace")
    except Exception:
        return ""


def append_log(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as handle:
        handle.write(text.rstrip() + "\n")


def new_job_id() -> str:
    stamp = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    return f"{stamp}-{os.urandom(3).hex()}"


def new_environment_id() -> str:
    stamp = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    return f"mg-{stamp}-{os.urandom(3).hex()}"


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


class WebHandler(BaseHTTPRequestHandler):
    server: "BenchmarkHTTPServer"

    def do_GET(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        if path == "/":
            self._send_html(INDEX_HTML)
            return
        if path == "/api/suites":
            self._send_json({"suites": self.server.app.list_suites()})
            return
        if path == "/api/agent-config":
            self._send_json({"config": self.server.app.get_agent_config()})
            return
        if path == "/api/webui-settings":
            self._send_json({"settings": self.server.app.get_webui_settings()})
            return
        if path == "/api/jobs":
            self._send_json({"jobs": self.server.app.list_jobs()})
            return
        if path == "/api/environments/mobilegym":
            self._send_json({"environments": self.server.app.list_mobilegym_environments()})
            return
        if path.startswith("/api/jobs/"):
            self._handle_get_job(path)
            return
        if path.startswith("/reports/"):
            self._handle_report(path)
            return
        self.send_error(HTTPStatus.NOT_FOUND)

    def do_POST(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        if path == "/api/jobs":
            try:
                payload = self._read_json()
                job = self.server.app.start_job(payload)
            except Exception as exc:
                self._send_json({"error": str(exc)}, status=HTTPStatus.BAD_REQUEST)
                return
            self._send_json({"job": job}, status=HTTPStatus.CREATED)
            return
        if path == "/api/environments/mobilegym":
            try:
                payload = self._read_json()
                environment = self.server.app.start_mobilegym_environment(payload)
            except Exception as exc:
                self._send_json({"error": str(exc)}, status=HTTPStatus.BAD_REQUEST)
                return
            self._send_json({"environment": environment}, status=HTTPStatus.CREATED)
            return
        if path == "/api/agent-config":
            try:
                payload = self._read_json()
                config = self.server.app.save_agent_config(payload)
            except Exception as exc:
                self._send_json({"error": str(exc)}, status=HTTPStatus.BAD_REQUEST)
                return
            self._send_json({"config": config})
            return
        if path == "/api/agent-config/reset":
            try:
                config = self.server.app.reset_agent_config()
            except Exception as exc:
                self._send_json({"error": str(exc)}, status=HTTPStatus.BAD_REQUEST)
                return
            self._send_json({"config": config})
            return
        if path == "/api/webui-settings":
            try:
                payload = self._read_json()
                settings = self.server.app.save_webui_settings(payload)
            except Exception as exc:
                self._send_json({"error": str(exc)}, status=HTTPStatus.BAD_REQUEST)
                return
            self._send_json({"settings": settings})
            return
        if path.startswith("/api/environments/mobilegym/") and path.endswith("/stop"):
            parts = path.strip("/").split("/")
            if len(parts) != 5:
                self.send_error(HTTPStatus.NOT_FOUND)
                return
            environment = self.server.app.stop_mobilegym_environment(parts[3])
            if environment is None:
                self.send_error(HTTPStatus.NOT_FOUND)
                return
            self._send_json({"environment": environment})
            return
        if path.startswith("/api/jobs/") and path.endswith("/stop"):
            job_id = path.split("/")[3]
            job = self.server.app.stop_job(job_id)
            if job is None:
                self.send_error(HTTPStatus.NOT_FOUND)
                return
            self._send_json({"job": job})
            return
        if path.startswith("/api/jobs/") and path.endswith("/cancel"):
            job_id = path.split("/")[3]
            job = self.server.app.cancel_job(job_id)
            if job is None:
                self.send_error(HTTPStatus.NOT_FOUND)
                return
            self._send_json({"job": job})
            return
        self.send_error(HTTPStatus.NOT_FOUND)

    def do_DELETE(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        if path.startswith("/api/environments/mobilegym/"):
            parts = path.strip("/").split("/")
            if len(parts) != 4:
                self.send_error(HTTPStatus.NOT_FOUND)
                return
            environment = self.server.app.delete_mobilegym_environment(parts[3])
            if environment is None:
                self.send_error(HTTPStatus.NOT_FOUND)
                return
            self._send_json({"environment": environment})
            return
        self.send_error(HTTPStatus.NOT_FOUND)

    def log_message(self, fmt: str, *args: Any) -> None:
        return

    def _handle_get_job(self, path: str) -> None:
        parts = path.strip("/").split("/")
        if len(parts) == 3:
            job = self.server.app.get_job(parts[2])
            if job is None:
                self.send_error(HTTPStatus.NOT_FOUND)
                return
            self._send_json({"job": job})
            return
        if len(parts) == 4 and parts[3] == "log":
            text = self.server.app.read_job_log(parts[2])
            if text is None:
                self.send_error(HTTPStatus.NOT_FOUND)
                return
            self._send_text(text)
            return
        self.send_error(HTTPStatus.NOT_FOUND)

    def _handle_report(self, path: str) -> None:
        parts = path.strip("/").split("/")
        if len(parts) != 4 or parts[0] != "reports" or parts[3] != "report.html":
            self.send_error(HTTPStatus.NOT_FOUND)
            return
        job_id, run_id = parts[1], parts[2]
        job = self.server.app.get_job(job_id)
        if job is None:
            self.send_error(HTTPStatus.NOT_FOUND)
            return
        raw_runs_dir = Path(job["raw_runs_dir"]).resolve()
        report = (raw_runs_dir / run_id / "report.html").resolve()
        if raw_runs_dir not in report.parents or not report.exists():
            self.send_error(HTTPStatus.NOT_FOUND)
            return
        self._send_html(report.read_text(encoding="utf-8"))

    def _read_json(self) -> dict[str, Any]:
        length = int(self.headers.get("Content-Length") or "0")
        if length <= 0:
            return {}
        data = json.loads(self.rfile.read(length).decode("utf-8"))
        if not isinstance(data, dict):
            raise ValueError("request body must be an object")
        return data

    def _send_json(self, payload: dict[str, Any], status: HTTPStatus = HTTPStatus.OK) -> None:
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _send_html(self, text: str) -> None:
        data = text.encode("utf-8")
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _send_text(self, text: str) -> None:
        data = text.encode("utf-8")
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class BenchmarkHTTPServer(ThreadingHTTPServer):
    def __init__(self, server_address: tuple[str, int], app: BenchmarkWebApp):
        super().__init__(server_address, WebHandler)
        self.app = app


def serve(config: WebUIConfig, host: str, port: int) -> None:
    app = BenchmarkWebApp(config)
    server = BenchmarkHTTPServer((host, port), app)
    bound_host, bound_port = server.server_address
    print(f"Benchmark Web UI: http://{bound_host}:{bound_port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        app.shutdown()
        server.server_close()


def cli(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="benchmark.runner webui")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8765)
    parser.add_argument("--suites-dir", default=str(DEFAULT_SUITES_DIR))
    parser.add_argument("--runs-dir", default=str(DEFAULT_RUNS_DIR))
    parser.add_argument("--base-config-dir", default=str(DEFAULT_BASE_CONFIG_DIR))
    parser.add_argument("--agent-config", default="")
    parser.add_argument("--daemon-image", default=DEFAULT_DAEMON_IMAGE)
    parser.add_argument("--mobilegym-image", default=DEFAULT_MOBILEGYM_IMAGE)
    parser.add_argument("--no-build-daemon-image", action="store_true")
    parser.add_argument("--no-build-mobilegym-image", action="store_true")
    args = parser.parse_args(argv)
    serve(
        WebUIConfig(
            suites_dir=Path(args.suites_dir),
            runs_dir=Path(args.runs_dir),
            base_config_dir=Path(args.base_config_dir),
            agent_config_path=Path(args.agent_config) if args.agent_config else None,
            daemon_image=args.daemon_image,
            mobilegym_image=args.mobilegym_image,
            build_daemon_image=not args.no_build_daemon_image,
            build_mobilegym_image=not args.no_build_mobilegym_image,
        ),
        args.host,
        args.port,
    )
    return 0


INDEX_HTML = r"""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Aiden Benchmark</title>
  <style>
    :root {
      --bg: #f4f4f4;
      --layer: #ffffff;
      --layer-alt: #f4f4f4;
      --field: #f4f4f4;
      --border: #e0e0e0;
      --border-strong: #8d8d8d;
      --text: #161616;
      --muted: #525252;
      --muted-2: #6f6f6f;
      --blue: #0f62fe;
      --blue-hover: #0353e9;
      --gray-button: #393939;
      --gray-button-hover: #4c4c4c;
      --green: #198038;
      --red: #da1e28;
      --orange: #ba4e00;
      --purple: #8a3ffc;
      --focus: #0f62fe;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background: var(--bg);
      color: var(--text);
      font-family: "IBM Plex Sans", Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      font-size: 14px;
      line-height: 1.4;
      letter-spacing: 0;
    }
    .topbar {
      height: 48px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 0 16px;
      background: #161616;
      color: #fff;
    }
    .brand {
      display: flex;
      align-items: center;
      gap: 12px;
      min-width: 0;
    }
    .brand-mark {
      width: 20px;
      height: 20px;
      background: var(--blue);
      display: grid;
      place-items: center;
      font-size: 12px;
      font-weight: 700;
    }
    .brand-title { font-size: 15px; font-weight: 600; }
    .header-meta {
      color: #c6c6c6;
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: 0.02em;
    }
    .layout {
      display: grid;
      grid-template-columns: 360px minmax(0, 1fr);
      min-height: calc(100vh - 48px);
      gap: 1px;
      background: var(--border);
    }
    .side,
    .workspace {
      background: var(--bg);
      min-width: 0;
    }
    .side {
      display: grid;
      align-content: start;
      gap: 1px;
    }
    .workspace {
      display: grid;
      grid-template-rows: auto auto auto minmax(360px, 1fr);
      gap: 1px;
    }
    .tile {
      background: var(--layer);
      border-radius: 0;
      padding: 16px;
      min-width: 0;
    }
    .tile-header,
    .toolbar {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      margin-bottom: 16px;
    }
    .tile-title {
      margin: 0;
      font-size: 16px;
      font-weight: 600;
      letter-spacing: 0;
    }
    .tile-kicker {
      margin-top: 2px;
      color: var(--muted);
      font-size: 12px;
    }
    .form-grid {
      display: grid;
      grid-template-columns: 1fr;
      gap: 12px;
      align-items: stretch;
    }
    .field {
      display: grid;
      gap: 6px;
      min-width: 0;
      background: var(--layer);
    }
    .form-grid button {
      justify-self: start;
      min-width: 120px;
    }
    .field label,
    .check-label {
      color: var(--muted);
      font-size: 12px;
      font-weight: 600;
    }
    input[type="text"],
    input:not([type]),
    input[type="url"],
    input[type="password"],
    input[type="search"],
    select,
    textarea {
      width: 100%;
      border: 0;
      border-bottom: 1px solid var(--border-strong);
      border-radius: 0;
      color: var(--text);
      background: var(--field);
      font: inherit;
    }
    input[type="text"],
    input:not([type]),
    input[type="url"],
    input[type="password"],
    input[type="search"],
    select {
      height: 40px;
      padding: 0 12px;
    }
    textarea {
      min-height: 220px;
      max-height: 360px;
      resize: vertical;
      padding: 12px;
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 12px;
      line-height: 1.45;
    }
    input:focus,
    textarea:focus,
    button:focus,
    a:focus {
      outline: 2px solid var(--focus);
      outline-offset: -2px;
    }
    button {
      height: 40px;
      border: 0;
      border-radius: 0;
      padding: 0 16px;
      background: var(--gray-button);
      color: #fff;
      font: inherit;
      font-weight: 600;
      cursor: pointer;
      white-space: nowrap;
    }
    button:hover { background: var(--gray-button-hover); }
    button.primary { background: var(--blue); color: #fff; }
    button.primary:hover { background: var(--blue-hover); }
    button.danger { background: transparent; color: var(--red); padding: 0 8px; }
    button.danger:hover { background: #fff1f1; }
    button:disabled { opacity: 0.45; cursor: not-allowed; }
    .ghost-button {
      background: transparent;
      color: var(--blue);
      padding: 0 8px;
    }
    .ghost-button:hover { background: #edf5ff; }
    .config-actions {
      display: flex;
      align-items: center;
      gap: 8px;
      margin-top: 12px;
    }
    .config-actions span {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .segmented {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      border: 1px solid var(--border-strong);
      margin-bottom: 16px;
    }
    .segmented button {
      height: 32px;
      min-width: 0;
      background: var(--layer);
      color: var(--text);
      border-right: 1px solid var(--border-strong);
      font-weight: 500;
    }
    .segmented button:last-child { border-right: 0; }
    .segmented button.active {
      background: var(--gray-button);
      color: #fff;
    }
    .env-panel[hidden] { display: none; }
    .table-wrap {
      border-top: 1px solid var(--border);
      max-height: 430px;
      overflow: auto;
      background: var(--layer);
    }
    .suite-table-wrap { max-height: calc(100vh - 360px); min-height: 360px; }
    .job-table-wrap { max-height: 240px; }
    table {
      width: 100%;
      border-collapse: collapse;
      table-layout: fixed;
    }
    thead { background: #e0e0e0; }
    th, td {
      border-bottom: 1px solid var(--border);
      padding: 10px 12px;
      text-align: left;
      vertical-align: middle;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    th {
      height: 40px;
      color: var(--muted);
      font-size: 12px;
      font-weight: 600;
    }
    tbody tr { background: var(--layer); }
    tbody tr:hover { background: #f4f4f4; }
    td:first-child input[type="checkbox"],
    td:first-child input[type="radio"] {
      display: block;
      margin: 0 auto;
    }
    .muted { color: var(--muted); }
    .cell-main {
      display: grid;
      gap: 2px;
      min-width: 0;
    }
    .cell-main span,
    .cell-main small {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .cell-main small {
      color: var(--muted-2);
      font-size: 12px;
    }
    .status {
      display: inline-flex;
      align-items: center;
      min-height: 24px;
      padding: 0 8px;
      border: 0;
      background: #e0e0e0;
      color: #393939;
      font-size: 12px;
      font-weight: 600;
      text-transform: uppercase;
    }
    .status.passed { background: #defbe6; color: var(--green); }
    .status.failed { background: #fff1f1; color: var(--red); }
    .status.running, .status.starting, .status.starting_agent, .status.preparing, .status.building { background: #edf5ff; color: var(--blue); }
    .status.canceled, .status.stopping { background: #fff8e1; color: var(--orange); }
    .status.stopped, .status.device { background: #e0e0e0; color: #525252; }
    .status.mobilegym { background: #e8daff; color: var(--purple); }
    .status-actions {
      display: flex;
      gap: 8px;
      align-items: center;
    }
    .progress {
      height: 8px;
      background: #e0e0e0;
      overflow: hidden;
    }
    .progress > div { height: 100%; width: 0%; background: var(--blue); transition: width 160ms linear; }
    .summary-strip {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 16px;
      align-items: center;
    }
    .run-config-grid {
      display: grid;
      grid-template-columns: minmax(220px, 1fr) minmax(360px, 1.2fr) auto;
      gap: 16px;
      align-items: end;
    }
    .run-meta {
      display: grid;
      gap: 4px;
      min-width: 0;
    }
    .run-meta strong {
      font-size: 20px;
      font-weight: 500;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .run-actions {
      display: flex;
      gap: 12px;
      align-items: center;
    }
    .judge-inline {
      display: grid;
      grid-template-columns: auto minmax(220px, 1fr) minmax(180px, 0.8fr);
      gap: 12px;
      align-items: end;
    }
    .check-label {
      display: flex;
      gap: 8px;
      align-items: center;
      min-height: 40px;
      white-space: nowrap;
    }
    .check-label input { width: 16px; height: 16px; margin: 0; }
    .metric-grid {
      display: grid;
      grid-template-columns: repeat(6, minmax(0, 1fr));
      gap: 1px;
      background: var(--border);
      margin-top: 16px;
    }
    .metric {
      background: var(--layer-alt);
      min-height: 88px;
      padding: 12px;
    }
    .metric span {
      display: block;
      color: var(--muted);
      font-size: 12px;
      margin-bottom: 8px;
    }
    .metric strong {
      display: block;
      font-size: 28px;
      line-height: 1;
      font-weight: 400;
    }
    .detail-grid {
      display: grid;
      grid-template-columns: minmax(360px, 0.95fr) minmax(420px, 1.05fr);
      gap: 1px;
      min-width: 0;
      min-height: 0;
      background: var(--border);
    }
    .detail-grid .tile {
      min-height: 0;
    }
    .detail-grid textarea {
      min-height: 300px;
      max-height: 520px;
    }
    pre {
      margin: 0;
      min-height: 300px;
      height: 100%;
      max-height: 420px;
      overflow: auto;
      border: 0;
      background: #262626;
      color: #f4f4f4;
      padding: 16px;
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 12px;
      line-height: 1.45;
      white-space: pre-wrap;
    }
    a { color: var(--blue); text-decoration: none; }
    a:hover { text-decoration: underline; }
    .empty-row {
      color: var(--muted);
      height: 48px;
    }
    @media (max-width: 980px) {
      .layout { grid-template-columns: 1fr; }
      .suite-table-wrap { max-height: 360px; }
      .metric-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
      .summary-strip { grid-template-columns: 1fr; }
      .run-config-grid { grid-template-columns: 1fr; align-items: stretch; }
      .judge-inline { grid-template-columns: 1fr; align-items: stretch; }
      .detail-grid { grid-template-columns: 1fr; }
    }
  </style>
</head>
<body>
  <header class="topbar">
    <div class="brand">
      <div class="brand-mark">A</div>
      <div class="brand-title">Aiden Benchmark</div>
    </div>
    <div id="headerStatus" class="header-meta">Idle</div>
  </header>
  <main class="layout">
    <aside class="side">
      <section class="tile">
        <div class="tile-header">
          <div>
            <h2 class="tile-title">Environments</h2>
            <div class="tile-kicker">device / mobilegym</div>
          </div>
        </div>
        <div class="segmented" role="tablist" aria-label="Environment type">
          <button id="deviceTab" class="active" type="button">Device</button>
          <button id="mobilegymTab" type="button">MobileGym</button>
        </div>
        <div id="devicePanel" class="env-panel">
          <div class="form-grid">
            <div class="field"><label for="envName">Name</label><input id="envName" autocomplete="off"></div>
            <div class="field"><label for="envEndpoint">Endpoint</label><input id="envEndpoint" placeholder="http://host:8080" autocomplete="off"></div>
            <button id="saveEnv" class="primary" type="button">Save device</button>
          </div>
        </div>
        <div id="mobilegymPanel" class="env-panel" hidden>
          <div class="form-grid">
            <div class="field"><label for="mobilegymName">Name</label><input id="mobilegymName" placeholder="MobileGym" autocomplete="off"></div>
            <button id="startMobileGym" class="primary" type="button">Start MobileGym</button>
          </div>
        </div>
        <div class="table-wrap" style="margin-top:16px; max-height:220px">
          <table>
            <thead><tr><th style="width:40px"></th><th style="width:128px">Environment</th><th>Endpoint</th><th style="width:104px"></th></tr></thead>
            <tbody id="envRows"></tbody>
          </table>
        </div>
      </section>

      <section class="tile">
        <div class="toolbar">
          <div>
            <h2 class="tile-title">Suites</h2>
            <div class="tile-kicker">Select one or more suites</div>
          </div>
          <input id="suiteFilter" type="search" style="max-width:172px" placeholder="Filter">
        </div>
        <div class="table-wrap suite-table-wrap">
          <table>
            <thead><tr><th style="width:40px"></th><th>Suite</th><th style="width:96px">Kind</th><th style="width:72px">Tasks</th></tr></thead>
            <tbody id="suiteRows"></tbody>
          </table>
        </div>
      </section>
    </aside>

    <section class="workspace">
      <section class="tile">
        <div class="run-config-grid">
          <div class="run-meta">
            <span class="tile-kicker">Run configuration</span>
            <strong><span id="selectedEnvLabel">No environment</span></strong>
            <span class="muted"><span id="selectedSuitesLabel">0 suites</span> selected - <span id="selectedJudgeLabel">judge enabled</span></span>
          </div>
          <div class="judge-inline">
            <label class="check-label"><input id="judgeEnabled" type="checkbox" checked> Enable judge</label>
            <div class="field"><label for="judgeModel">Judge model</label><input id="judgeModel" autocomplete="off" placeholder="anthropic/claude-sonnet-4-6"></div>
            <div class="field"><label for="judgeApiKey">API key</label><input id="judgeApiKey" type="password" autocomplete="off" placeholder="OPENROUTER_API_KEY"></div>
          </div>
          <div class="run-actions">
            <button id="runBtn" class="primary">Run selected</button>
          </div>
        </div>
      </section>

      <section class="tile">
        <div class="tile-header">
          <div>
            <h2 class="tile-title">Progress</h2>
            <div id="activeJobLabel" class="tile-kicker">No active job</div>
          </div>
          <div class="status-actions">
            <span id="activeJobStatus" class="status">idle</span>
            <button id="activeStopJob" class="danger" type="button" hidden>Stop</button>
          </div>
        </div>
        <div class="progress"><div id="progressBar"></div></div>
        <div class="metric-grid">
          <div class="metric"><span class="muted">Tasks</span><strong id="mTasks">0</strong></div>
          <div class="metric"><span class="muted">Passed</span><strong id="mPassed">0</strong></div>
          <div class="metric"><span class="muted">Failed</span><strong id="mFailed">0</strong></div>
          <div class="metric"><span class="muted">Skipped</span><strong id="mSkipped">0</strong></div>
          <div class="metric"><span class="muted">Judge</span><strong id="mJudge">0</strong></div>
          <div class="metric"><span class="muted">Timeout</span><strong id="mTimeout">0</strong></div>
        </div>
      </section>

      <section class="tile">
        <div class="tile-header">
          <div>
            <h2 class="tile-title">Jobs</h2>
            <div class="tile-kicker">Recent benchmark runs</div>
          </div>
        </div>
        <div class="table-wrap job-table-wrap">
          <table>
            <thead><tr><th>Job</th><th>Environment</th><th style="width:120px">Status</th><th style="width:120px">Reports</th><th style="width:96px"></th></tr></thead>
            <tbody id="jobRows"></tbody>
          </table>
        </div>
      </section>

      <div class="detail-grid">
        <section class="tile">
          <div class="tile-header">
            <div>
              <h2 class="tile-title">Agent config</h2>
              <div id="agentConfigPath" class="tile-kicker">agent.toml</div>
            </div>
          </div>
          <div class="field">
            <label for="agentConfigText">agent.toml</label>
            <textarea id="agentConfigText" spellcheck="false" readonly></textarea>
          </div>
          <div class="config-actions">
            <button id="editAgentConfig" class="primary" type="button">Edit</button>
            <button id="saveAgentConfig" class="primary" type="button">Save</button>
            <button id="resetAgentConfig" class="ghost-button" type="button">Reset</button>
            <span id="agentConfigStatus" class="muted"></span>
          </div>
        </section>

        <section class="tile">
          <div class="tile-header">
            <div>
              <h2 class="tile-title">Log</h2>
              <div class="tile-kicker">Runner and daemon output</div>
            </div>
          </div>
          <pre id="logBox"></pre>
        </section>
      </div>
    </section>
  </main>
  <script>
    const DEFAULT_JUDGE_MODEL = 'anthropic/claude-sonnet-4-6';
    let deviceEnvironments = [];
    let mobilegymEnvironments = [];
    let selectedEnvironmentId = '';
    let editingDeviceEnvId = null;
    let suites = [];
    let selectedSuites = new Set();
    let jobs = [];
    let activeJobId = null;
    let agentConfigDirty = false;
    let agentConfigLoaded = false;
    let agentConfigEditing = false;

    function uid(){ return Date.now().toString(36) + Math.random().toString(36).slice(2, 8); }
    function normalizeDeviceEnv(env, index){
      return {
        id: String(env.id || uid()),
        name: String(env.name || `Device ${index + 1}`),
        endpoint: String(env.endpoint || ''),
        type: 'device',
        status: 'device'
      };
    }
    async function loadWebuiSettings(){
      try {
        const res = await fetch('/api/webui-settings');
        const body = await res.json();
        if(!res.ok) throw new Error(body.error || 'failed to load settings');
        applyWebuiSettings(body.settings || {});
      } catch (err) {
        document.getElementById('logBox').textContent = err.message || String(err);
        applyWebuiSettings({});
      }
    }
    function applyWebuiSettings(settings){
      deviceEnvironments = (Array.isArray(settings.device_environments) ? settings.device_environments : [])
        .map(normalizeDeviceEnv)
        .filter(env => env.name && env.endpoint);
      selectedEnvironmentId = String(settings.selected_environment_id || '');
      const judge = settings.judge || {};
      document.getElementById('judgeEnabled').checked = judge.enabled !== false;
      document.getElementById('judgeModel').value = String(judge.model || DEFAULT_JUDGE_MODEL);
      const keyInput = document.getElementById('judgeApiKey');
      keyInput.value = '';
      keyInput.placeholder = judge.has_api_key ? 'Saved; leave blank to keep' : 'OPENROUTER_API_KEY';
      syncJudgePanel();
      renderEnvs();
      syncRunState();
    }
    async function saveWebuiSettings(options = {}){
      const judge = currentJudgeSettings();
      const judgePayload = {enabled: judge.enabled, model: judge.model};
      if(judge.apiKey) judgePayload.api_key = judge.apiKey;
      const payload = {
        judge: judgePayload,
        device_environments: deviceEnvironments.map(env => ({id: env.id, name: env.name, endpoint: env.endpoint})),
        selected_environment_id: selectedEnvironmentId
      };
      try {
        const res = await fetch('/api/webui-settings', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(payload)
        });
        const body = await res.json();
        if(!res.ok) throw new Error(body.error || 'failed to save settings');
        if(!options.keepInputs) applyWebuiSettings(body.settings || {});
        return true;
      } catch (err) {
        document.getElementById('logBox').textContent = err.message || String(err);
        return false;
      }
    }
    function allEnvironments(){ return [...deviceEnvironments, ...mobilegymEnvironments]; }
    function envCanRun(env){ return !!env && !!env.endpoint && (env.type === 'device' || env.status === 'running'); }
    function selectedEnv(){
      const current = allEnvironments().find(env => env.id === selectedEnvironmentId);
      if(envCanRun(current)) return current;
      return allEnvironments().find(envCanRun) || null;
    }
    function setSelectedEnv(id){
      const env = allEnvironments().find(item => item.id === id);
      if(!envCanRun(env)) return;
      selectedEnvironmentId = id;
      renderEnvs();
      syncRunState();
      saveWebuiSettings({keepInputs: true});
    }
    function setEnvMode(mode){
      const isMobileGym = mode === 'mobilegym';
      document.getElementById('deviceTab').classList.toggle('active', !isMobileGym);
      document.getElementById('mobilegymTab').classList.toggle('active', isMobileGym);
      document.getElementById('devicePanel').hidden = isMobileGym;
      document.getElementById('mobilegymPanel').hidden = !isMobileGym;
    }

    function currentJudgeSettings(){
      const enabled = document.getElementById('judgeEnabled').checked;
      const model = document.getElementById('judgeModel').value.trim() || DEFAULT_JUDGE_MODEL;
      const apiKey = document.getElementById('judgeApiKey').value.trim();
      return {enabled, model, apiKey};
    }

    function persistJudgeSettings(){
      syncJudgePanel();
      syncRunState();
      saveWebuiSettings({keepInputs: true});
    }

    function syncJudgePanel(){
      const enabled = document.getElementById('judgeEnabled').checked;
      document.getElementById('judgeModel').disabled = !enabled;
      document.getElementById('judgeApiKey').disabled = !enabled;
    }

    function setAgentConfigStatus(text, isError = false){
      const node = document.getElementById('agentConfigStatus');
      node.textContent = text || '';
      node.style.color = isError ? 'var(--red)' : '';
    }

    function setAgentConfigEditing(editing){
      agentConfigEditing = !!editing;
      const text = document.getElementById('agentConfigText');
      text.readOnly = !agentConfigEditing;
      document.getElementById('editAgentConfig').disabled = agentConfigEditing;
      document.getElementById('saveAgentConfig').disabled = !agentConfigEditing;
      document.getElementById('resetAgentConfig').disabled = !agentConfigEditing;
      if(agentConfigEditing){
        text.focus();
        setAgentConfigStatus(agentConfigDirty ? 'Modified' : 'Editing');
      }
    }

    function applyAgentConfig(config){
      document.getElementById('agentConfigText').value = config.content || '';
      document.getElementById('agentConfigPath').textContent = config.path || 'agent.toml';
      agentConfigDirty = false;
      agentConfigLoaded = true;
      setAgentConfigEditing(false);
      setAgentConfigStatus(config.source === 'generated' ? 'Generated' : 'Saved');
    }

    async function loadAgentConfig(){
      try {
        const res = await fetch('/api/agent-config');
        const body = await res.json();
        if(!res.ok) throw new Error(body.error || 'failed to load agent.toml');
        applyAgentConfig(body.config || {});
      } catch (err) {
        setAgentConfigStatus(err.message || String(err), true);
      }
    }

    async function saveAgentConfig(options = {}){
      const content = document.getElementById('agentConfigText').value;
      try {
        const res = await fetch('/api/agent-config', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({content})
        });
        const body = await res.json();
        if(!res.ok) throw new Error(body.error || 'failed to save agent.toml');
        applyAgentConfig(body.config || {content});
        setAgentConfigStatus(options.silent ? 'Saved for run' : 'Saved');
        return true;
      } catch (err) {
        const message = err.message || String(err);
        setAgentConfigStatus(message, true);
        document.getElementById('logBox').textContent = message;
        return false;
      }
    }

    async function resetAgentConfig(){
      const res = await fetch('/api/agent-config/reset', {method: 'POST'});
      const body = await res.json();
      if(!res.ok){
        const message = body.error || 'failed to reset agent.toml';
        setAgentConfigStatus(message, true);
        document.getElementById('logBox').textContent = message;
        return;
      }
      applyAgentConfig(body.config || {});
      setAgentConfigStatus('Reset');
    }

    function renderEnvs(){
      const tbody = document.getElementById('envRows');
      const current = selectedEnv();
      const envs = allEnvironments();
      tbody.innerHTML = '';
      if(!envs.length){
        tbody.innerHTML = '<tr><td class="empty-row" colspan="4">No environments saved</td></tr>';
      }
      envs.forEach(env => {
        const selectable = envCanRun(env);
        const displayEndpoint = env.type === 'mobilegym' ? (env.public_endpoint || env.endpoint) : env.endpoint;
        const endpointDetail = env.type === 'mobilegym' ? env.endpoint : 'manual';
        const status = env.type === 'mobilegym' ? (env.status || 'mobilegym') : 'device';
        const actionHtml = env.type === 'device'
          ? `<button class="ghost-button" data-edit="${escapeHtml(env.id)}">Edit</button> <button class="danger" data-delete="${escapeHtml(env.id)}">Delete</button>`
          : mobilegymActionHtml(env);
        const tr = document.createElement('tr');
        tr.innerHTML = `<td><input type="radio" name="activeEnv" ${current && current.id === env.id ? 'checked' : ''} ${selectable ? '' : 'disabled'}></td>
          <td title="${escapeHtml(env.name)}"><div class="cell-main"><span>${escapeHtml(env.name)}</span><small>${escapeHtml(status)}</small></div></td>
          <td title="${escapeHtml(env.endpoint)}"><div class="cell-main"><span>${escapeHtml(displayEndpoint)}</span><small>${escapeHtml(endpointDetail)}</small></div></td>
          <td>${actionHtml}</td>`;
        tr.querySelector('input').onchange = () => setSelectedEnv(env.id);
        const edit = tr.querySelector('[data-edit]');
        if(edit) edit.onclick = () => {
          editingDeviceEnvId = env.id;
          document.getElementById('envName').value = env.name;
          document.getElementById('envEndpoint').value = env.endpoint;
          setEnvMode('device');
        };
        const del = tr.querySelector('[data-delete]');
        if(del) del.onclick = () => {
          deviceEnvironments = deviceEnvironments.filter(e => e.id !== env.id);
          if(selectedEnvironmentId === env.id) selectedEnvironmentId = '';
          renderEnvs();
          syncRunState();
          saveWebuiSettings({keepInputs: true});
        };
        const stop = tr.querySelector('[data-stop]');
        if(stop) stop.onclick = () => stopMobileGym(env.id);
        const remove = tr.querySelector('[data-remove]');
        if(remove) remove.onclick = () => removeMobileGym(env.id);
        tbody.appendChild(tr);
      });
      document.getElementById('selectedEnvLabel').textContent = current ? current.name : 'No environment';
    }

    function mobilegymActionHtml(env){
      if(['building', 'starting', 'running', 'stopping'].includes(env.status)){
        return `<button class="danger" data-stop="${escapeHtml(env.id)}" ${env.status === 'stopping' ? 'disabled' : ''}>Stop</button>`;
      }
      return `<button class="danger" data-remove="${escapeHtml(env.id)}">Remove</button>`;
    }

    function saveEnvFromForm(){
      const name = document.getElementById('envName').value.trim();
      const endpoint = document.getElementById('envEndpoint').value.trim();
      if(!name || !endpoint) return;
      if(editingDeviceEnvId){
        const env = deviceEnvironments.find(e => e.id === editingDeviceEnvId);
        if(env){ env.name = name; env.endpoint = endpoint; }
      } else {
        const env = {id: uid(), name, endpoint, type: 'device', status: 'device'};
        deviceEnvironments.push(env);
        selectedEnvironmentId = env.id;
      }
      editingDeviceEnvId = null;
      document.getElementById('envName').value = '';
      document.getElementById('envEndpoint').value = '';
      renderEnvs();
      syncRunState();
      saveWebuiSettings({keepInputs: true});
    }

    async function loadMobileGymEnvironments(){
      try {
        const res = await fetch('/api/environments/mobilegym');
        const body = await res.json();
        mobilegymEnvironments = (body.environments || []).map(env => ({...env, type: 'mobilegym'}));
      } catch {
        mobilegymEnvironments = [];
      }
      renderEnvs();
      syncRunState();
    }

    async function startMobileGym(){
      const name = document.getElementById('mobilegymName').value.trim() || 'MobileGym';
      const button = document.getElementById('startMobileGym');
      const previous = button.textContent;
      button.disabled = true;
      button.textContent = 'Starting';
      try {
        const res = await fetch('/api/environments/mobilegym', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({name})
        });
        const body = await res.json();
        if(!res.ok){
          document.getElementById('logBox').textContent = body.error || 'failed to start MobileGym';
          return;
        }
        document.getElementById('mobilegymName').value = '';
        if(body.environment){
          mobilegymEnvironments = [body.environment, ...mobilegymEnvironments.filter(env => env.id !== body.environment.id)];
          selectedEnvironmentId = body.environment.id;
          saveWebuiSettings({keepInputs: true});
        }
        await loadMobileGymEnvironments();
      } finally {
        button.disabled = false;
        button.textContent = previous;
      }
    }

    async function stopMobileGym(id){
      if(selectedEnvironmentId === id){
        selectedEnvironmentId = '';
        saveWebuiSettings({keepInputs: true});
      }
      const res = await fetch(`/api/environments/mobilegym/${encodeURIComponent(id)}/stop`, {method: 'POST'});
      if(!res.ok) document.getElementById('logBox').textContent = await res.text();
      await loadMobileGymEnvironments();
    }

    async function removeMobileGym(id){
      if(selectedEnvironmentId === id){
        selectedEnvironmentId = '';
        saveWebuiSettings({keepInputs: true});
      }
      const res = await fetch(`/api/environments/mobilegym/${encodeURIComponent(id)}`, {method: 'DELETE'});
      if(!res.ok) document.getElementById('logBox').textContent = await res.text();
      await loadMobileGymEnvironments();
    }

    async function loadSuites(){
      const res = await fetch('/api/suites');
      suites = (await res.json()).suites || [];
      renderSuites();
    }

    function renderSuites(){
      const filter = document.getElementById('suiteFilter').value.toLowerCase();
      const tbody = document.getElementById('suiteRows');
      tbody.innerHTML = '';
      const filtered = suites.filter(s => !filter || (s.name + ' ' + s.key).toLowerCase().includes(filter));
      if(!filtered.length){
        tbody.innerHTML = '<tr><td class="empty-row" colspan="4">No suites found</td></tr>';
      }
      filtered.forEach(s => {
        const tr = document.createElement('tr');
        tr.innerHTML = `<td><input type="checkbox" ${selectedSuites.has(s.key) ? 'checked' : ''}></td>
          <td title="${escapeHtml(s.key)}"><div class="cell-main"><span>${escapeHtml(s.name)}</span><small>${escapeHtml(s.key)}</small></div></td>
          <td><span class="status">${escapeHtml(s.kind)}</span></td>
          <td>${s.task_count || 0}</td>`;
        tr.querySelector('input').onchange = e => {
          if(e.target.checked) selectedSuites.add(s.key); else selectedSuites.delete(s.key);
          syncRunState();
        };
        tbody.appendChild(tr);
      });
      syncRunState();
    }

    function syncRunState(){
      const env = selectedEnv();
      const judge = currentJudgeSettings();
      document.getElementById('selectedSuitesLabel').textContent = `${selectedSuites.size} suites`;
      document.getElementById('selectedEnvLabel').textContent = env ? env.name : 'No environment';
      document.getElementById('selectedJudgeLabel').textContent = judge.enabled ? `judge: ${judge.model}` : 'judge: off';
      document.getElementById('runBtn').disabled = !envCanRun(env) || selectedSuites.size === 0;
    }

    async function startRun(){
      const env = selectedEnv();
      if(!envCanRun(env) || selectedSuites.size === 0) return;
      if(!agentConfigLoaded) await loadAgentConfig();
      if(agentConfigDirty){
        const saved = await saveAgentConfig({silent: true});
        if(!saved) return;
      }
      const judge = currentJudgeSettings();
      selectedEnvironmentId = env.id;
      const settingsSaved = await saveWebuiSettings({keepInputs: true});
      if(!settingsSaved) return;
      const res = await fetch('/api/jobs', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({
          endpoint: env.endpoint,
          environment: {id: env.id, name: env.name, type: env.type, public_endpoint: env.public_endpoint || '', web_url: env.web_url || ''},
          suites: Array.from(selectedSuites),
          no_judge: !judge.enabled,
          judge_model: judge.model
        })
      });
      const body = await res.json();
      if(!res.ok){ document.getElementById('logBox').textContent = body.error || 'failed'; return; }
      activeJobId = body.job.id;
      await refreshJobs();
    }

    async function refreshJobs(){
      const res = await fetch('/api/jobs');
      jobs = (await res.json()).jobs || [];
      if(!activeJobId && jobs.length) activeJobId = jobs[0].id;
      if(activeJobId && !jobs.find(job => job.id === activeJobId)) activeJobId = jobs[0] ? jobs[0].id : null;
      renderJobs();
      if(activeJobId) await loadActiveJob(); else resetActiveJob();
    }

    function renderJobs(){
      const tbody = document.getElementById('jobRows');
      tbody.innerHTML = '';
      if(!jobs.length){
        tbody.innerHTML = '<tr><td class="empty-row" colspan="5">No jobs yet</td></tr>';
      }
      jobs.forEach(job => {
        const reports = (job.suite_results || []).filter(r => r.report_url).map(r => `<a href="${escapeHtml(r.report_url)}" target="_blank" rel="noreferrer">report</a>`).join(' ');
        const envLabel = job.environment_name || job.endpoint;
        const envType = job.environment_type || 'device';
        const screenLink = job.environment_web_url
          ? ` · <a href="${escapeHtml(job.environment_web_url)}" target="_blank" rel="noreferrer">screen</a>`
          : '';
        const actionHtml = jobCanStop(job)
          ? `<button class="danger" data-stop-job="${escapeHtml(job.id)}" ${job.status === 'stopping' ? 'disabled' : ''}>Stop</button>`
          : '';
        const tr = document.createElement('tr');
        tr.innerHTML = `<td><div class="cell-main"><a href="#" data-job="${job.id}">${escapeHtml(job.id)}</a><small>${escapeHtml(job.created_at || '')}</small></div></td>
          <td title="${escapeHtml(job.endpoint)}"><div class="cell-main"><span>${escapeHtml(envLabel)}</span><small>${escapeHtml(envType)} - ${escapeHtml((job.suites || []).length)} suites${screenLink}</small></div></td>
          <td><span class="status ${cssToken(job.status)}">${escapeHtml(job.status)}</span></td>
          <td>${reports || '<span class="muted">none</span>'}</td>
          <td>${actionHtml}</td>`;
        tr.querySelector('[data-job]').onclick = e => { e.preventDefault(); activeJobId = job.id; loadActiveJob(); };
        const stop = tr.querySelector('[data-stop-job]');
        if(stop) stop.onclick = () => stopJob(job.id);
        tbody.appendChild(tr);
      });
    }

    async function loadActiveJob(){
      const res = await fetch(`/api/jobs/${activeJobId}`);
      if(!res.ok) return;
      const job = (await res.json()).job;
      renderActiveJob(job);
      const logRes = await fetch(`/api/jobs/${activeJobId}/log`);
      document.getElementById('logBox').textContent = await logRes.text();
    }

    function renderActiveJob(job){
      const activeLabel = document.getElementById('activeJobLabel');
      const screenLink = job.environment_web_url
        ? ` · <a href="${escapeHtml(job.environment_web_url)}" target="_blank" rel="noreferrer">screen</a>`
        : '';
      activeLabel.innerHTML = `${escapeHtml(job.id)} - ${escapeHtml(job.agent_url)}${screenLink}`;
      const st = document.getElementById('activeJobStatus');
      st.className = 'status ' + job.status;
      st.textContent = job.status;
      const stop = document.getElementById('activeStopJob');
      stop.hidden = !jobCanStop(job);
      stop.disabled = job.status === 'stopping';
      const progress = job.progress || {};
      const total = progress.total || (job.totals && job.totals.tasks) || 0;
      const completed = progress.completed || 0;
      document.getElementById('progressBar').style.width = total ? Math.min(100, Math.round(completed * 100 / total)) + '%' : '0%';
      const totals = job.totals || {};
      document.getElementById('mTasks').textContent = totals.tasks || total || 0;
      document.getElementById('mPassed').textContent = totals.passed || 0;
      document.getElementById('mFailed').textContent = totals.failed || 0;
      document.getElementById('mSkipped').textContent = totals.skipped || 0;
      document.getElementById('mJudge').textContent = totals.judge_error || 0;
      document.getElementById('mTimeout').textContent = totals.timeout || 0;
      document.getElementById('headerStatus').textContent = job.status;
    }

    function resetActiveJob(){
      document.getElementById('activeJobLabel').textContent = 'No active job';
      const st = document.getElementById('activeJobStatus');
      st.className = 'status';
      st.textContent = 'idle';
      const stop = document.getElementById('activeStopJob');
      stop.hidden = true;
      stop.disabled = false;
      document.getElementById('progressBar').style.width = '0%';
      ['mTasks', 'mPassed', 'mFailed', 'mSkipped', 'mJudge', 'mTimeout'].forEach(id => document.getElementById(id).textContent = '0');
      document.getElementById('headerStatus').textContent = 'Idle';
    }

    function jobCanStop(job){
      return job && !['passed', 'failed', 'stopped', 'canceled'].includes(job.status || '');
    }

    async function stopJob(id){
      const res = await fetch(`/api/jobs/${encodeURIComponent(id)}/stop`, {method: 'POST'});
      if(!res.ok) document.getElementById('logBox').textContent = await res.text();
      await refreshJobs();
    }

    function cssToken(value){
      return String(value ?? '').toLowerCase().replace(/[^a-z0-9_-]/g, '_');
    }

    function escapeHtml(value){
      return String(value ?? '').replace(/[&<>"']/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]));
    }

    document.getElementById('deviceTab').onclick = () => setEnvMode('device');
    document.getElementById('mobilegymTab').onclick = () => setEnvMode('mobilegym');
    document.getElementById('saveEnv').onclick = saveEnvFromForm;
    document.getElementById('startMobileGym').onclick = startMobileGym;
    document.getElementById('editAgentConfig').onclick = () => setAgentConfigEditing(true);
    document.getElementById('saveAgentConfig').onclick = () => saveAgentConfig();
    document.getElementById('resetAgentConfig').onclick = resetAgentConfig;
    document.getElementById('judgeEnabled').onchange = persistJudgeSettings;
    document.getElementById('judgeModel').oninput = persistJudgeSettings;
    document.getElementById('judgeApiKey').oninput = syncRunState;
    document.getElementById('judgeApiKey').onchange = persistJudgeSettings;
    document.getElementById('agentConfigText').oninput = () => {
      if(!agentConfigEditing) return;
      agentConfigDirty = true;
      setAgentConfigStatus('Modified');
    };
    document.getElementById('suiteFilter').oninput = renderSuites;
    document.getElementById('runBtn').onclick = startRun;
    document.getElementById('activeStopJob').onclick = () => { if(activeJobId) stopJob(activeJobId); };
    setAgentConfigEditing(false);
    renderEnvs();
    loadWebuiSettings();
    loadAgentConfig();
    loadMobileGymEnvironments();
    loadSuites();
    refreshJobs();
    setInterval(refreshJobs, 2000);
    setInterval(loadMobileGymEnvironments, 5000);
  </script>
</body>
</html>
"""


if __name__ == "__main__":
    raise SystemExit(cli())
