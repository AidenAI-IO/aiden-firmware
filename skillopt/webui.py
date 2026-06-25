from __future__ import annotations

import argparse
import json
import os
import re
import signal
import subprocess
import sys
import threading
import time
import tomllib
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

from runner.environment import EnvironmentManager
from runner.config import AgentConfigManager
from runner.judge import JudgeConfig


DEFAULT_HOST = "127.0.0.1"
DEFAULT_PORT = 8766
SKILLOPT_ROOT = Path(__file__).resolve().parent
REPO_ROOT = SKILLOPT_ROOT.parent
BENCHMARK_ROOT = REPO_ROOT / "benchmark"
DEFAULT_BASE_CONFIG_DIR = BENCHMARK_ROOT / "config"
DEFAULT_JUDGE_MODEL = JudgeConfig().model


@dataclass
class SkillOptWebUIConfig:
    runs_dir: Path
    host: str = DEFAULT_HOST
    port: int = DEFAULT_PORT
    suites_dir: Path = field(default_factory=lambda: SKILLOPT_ROOT / "suites")
    base_config_dir: Path = field(default_factory=lambda: DEFAULT_BASE_CONFIG_DIR)
    agent_config_path: Path | None = None


@dataclass
class SkillOptJob:
    id: str
    command: list[str]
    log_path: str
    run_dir: str
    status: str = "queued"
    created_at: str = ""
    started_at: str = ""
    finished_at: str = ""
    exit_code: int | None = None
    message: str = ""
    report_url: str = ""
    process: subprocess.Popen | None = field(default=None, repr=False, compare=False)


class SkillOptWebApp:
    def __init__(self, config: SkillOptWebUIConfig):
        self.config = config
        self.config.runs_dir.mkdir(parents=True, exist_ok=True)
        self._jobs: dict[str, SkillOptJob] = {}
        self._lock = threading.Lock()
        self._webui_judge_api_key = ""
        self._job_judge_api_keys: dict[str, str] = {}  # job_id -> judge API key
        # Shared environment and config managers (no more HTTP calls to Benchmark WebUI)
        self.env_manager = EnvironmentManager(
            runs_dir=config.runs_dir,
            repo_root=REPO_ROOT,
        )
        self.config_manager = AgentConfigManager(
            base_config_dir=config.base_config_dir,
            config_path=config.agent_config_path or (config.runs_dir / "agent.toml"),
        )

    def shutdown(self) -> None:
        self.env_manager.shutdown_all()

    def list_jobs(self) -> list[dict[str, Any]]:
        with self._lock:
            return [self._job_payload(job) for job in sorted(self._jobs.values(), key=lambda item: item.created_at, reverse=True)]

    def get_job(self, job_id: str) -> dict[str, Any] | None:
        with self._lock:
            job = self._jobs.get(job_id)
            return self._job_payload(job) if job else None

    def start_job(self, payload: dict[str, Any]) -> dict[str, Any]:
        payload = dict(payload)
        self._resolve_target_payload(payload)
        self._resolve_environment_payload(payload)
        job_id = new_job_id()
        run_dir = self.config.runs_dir / job_id
        run_dir.mkdir(parents=True, exist_ok=True)
        log_path = run_dir / "skillopt.log"
        if not str(payload.get("agent_config") or "").strip():
            agent_config_info = self.benchmark_agent_config_info(include_content=True)
            agent_config = str(agent_config_info.get("content") or "")
            if not agent_config:
                raise ValueError("Benchmark agent.toml is unavailable. Open the Benchmark WebUI and save an agent config first.")
            if not agent_config_info.get("api_key_nonempty"):
                raise ValueError("Benchmark agent.toml does not contain a model api_key. Open the Benchmark WebUI and save an agent config with an API key first.")
            agent_config_path = run_dir / "agent.toml"
            agent_config_path.write_text(agent_config, encoding="utf-8")
            payload["agent_config"] = str(agent_config_path)
        command = build_skillopt_command(payload, run_id=job_id, artifact_root=self.config.runs_dir)
        job = SkillOptJob(
            id=job_id,
            command=command,
            log_path=str(log_path),
            run_dir=str(run_dir),
            status="queued",
            created_at=now_iso(),
            report_url=f"/runs/{job_id}/report.html",
        )
        # Store judge API key for this job if judge is enabled
        # Use provided key, or fall back to saved webui key
        judge_api_key = str(payload.get("judge_api_key") or "").strip()
        if not judge_api_key:
            with self._lock:
                judge_api_key = self._webui_judge_api_key
        if judge_api_key:
            with self._lock:
                self._job_judge_api_keys[job_id] = judge_api_key
        with self._lock:
            self._jobs[job.id] = job
        thread = threading.Thread(target=self._run_job, args=(job,), name=f"skillopt-{job.id}", daemon=True)
        thread.start()
        return self._job_payload(job)

    def stop_job(self, job_id: str) -> dict[str, Any] | None:
        with self._lock:
            job = self._jobs.get(job_id)
            if job is None:
                return None
            if job.status in {"passed", "failed", "stopped"}:
                return self._job_payload(job)
            proc = job.process
            job.status = "stopping"
            job.message = "stop requested"
        if proc is not None and proc.poll() is None:
            terminate_process(proc)
        return self._job_payload(job)

    def list_mobilegym_environments(self) -> list[dict[str, Any]]:
        environments = [
            self.env_manager.environment_payload(env)
            for env in self.env_manager.list_all()
        ]
        environments.sort(key=lambda item: item.get("created_at", ""), reverse=True)
        return environments

    def start_mobilegym_environment(self, payload: dict[str, Any]) -> dict[str, Any]:
        name = str(payload.get("name") or "SkillOpt MobileGym").strip() or "SkillOpt MobileGym"
        parallel_raw = payload.get("parallel_envs", 5)
        parallel_envs = 5 if parallel_raw in {None, ""} else int(parallel_raw)
        if parallel_envs <= 0:
            raise ValueError("parallel_envs must be positive")
        env = self.env_manager.start_mobilegym(name=name, parallel_envs=parallel_envs)
        return self.env_manager.environment_payload(env)

    def stop_mobilegym_environment(self, environment_id: str) -> dict[str, Any] | None:
        env = self.env_manager.stop(environment_id)
        if env is None:
            return None
        return self.env_manager.environment_payload(env)

    def delete_mobilegym_environment(self, environment_id: str) -> dict[str, Any] | None:
        env = self.env_manager.delete(environment_id)
        if env is None:
            return None
        return self.env_manager.environment_payload(env)

    def list_targets(self) -> list[dict[str, Any]]:
        return list_skillopt_targets(self.config.suites_dir)

    def _resolve_target_payload(self, payload: dict[str, Any]) -> None:
        if payload.get("skill") and payload.get("train_suite") and payload.get("validation_suite"):
            return
        targets = self.list_targets()
        if not targets:
            raise ValueError("No SkillOpt targets found")
        target_id = str(payload.get("target_id") or "").strip()
        target = None
        if target_id:
            target = next((item for item in targets if item.get("id") == target_id), None)
            if target is None:
                raise ValueError(f"unknown SkillOpt target: {target_id}")
        else:
            target = targets[0]
        payload.setdefault("skill", target["skill"])
        payload.setdefault("train_suite", target["train_suite"])
        payload.setdefault("validation_suite", target["validation_suite"])

    def _resolve_environment_payload(self, payload: dict[str, Any]) -> None:
        backend = str(payload.get("backend") or "mobilegym").strip()
        payload["backend"] = backend
        if backend != "mobilegym" or str(payload.get("environment_url") or "").strip():
            return
        envs = self.list_mobilegym_environments()
        env_id = str(payload.get("environment_id") or "").strip()
        candidates = [env for env in envs if mobilegym_environment_url(env) and str(env.get("status") or "running") == "running"]
        selected = None
        if env_id:
            selected = next((env for env in candidates if str(env.get("id") or "") == env_id), None)
            if selected is None:
                raise ValueError(f"running MobileGym environment not found: {env_id}")
        elif candidates:
            selected = candidates[0]
        if selected is None:
            raise ValueError("No running MobileGym environment found in Benchmark WebUI")
        payload["environment_url"] = mobilegym_environment_url(selected)

    def benchmark_agent_config_text(self) -> str:
        return str(self.benchmark_agent_config_info(include_content=True).get("content") or "")

    def benchmark_agent_config_info(self, *, include_content: bool = False) -> dict[str, Any]:
        try:
            content, source = self.config_manager.get_config()
        except Exception:
            empty = {
                "available": False,
                "path": str(self.config_manager.config_path),
                "source": "unavailable",
                "api_key_nonempty": False,
            }
            return {**empty, "content": ""} if include_content else empty
        info = {
            "available": bool(content),
            "path": str(self.config_manager.config_path),
            "source": source,
            "api_key_nonempty": agent_config_has_api_key(content),
        }
        if include_content:
            info["content"] = content
        return info

    def save_agent_config(self, payload: dict[str, Any]) -> dict[str, Any]:
        content = str(payload.get("content") or "")
        content, source = self.config_manager.save_config(content)
        return {
            "content": content,
            "path": str(self.config_manager.config_path),
            "source": source,
        }

    def reset_agent_config(self) -> dict[str, Any]:
        content, source = self.config_manager.reset_config()
        return {
            "content": content,
            "path": str(self.config_manager.config_path),
            "source": source,
        }

    def get_webui_settings(self) -> dict[str, Any]:
        return _sanitize_webui_settings(self._load_webui_settings(include_secrets=False))

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
                with self._lock:
                    self._webui_judge_api_key = api_key
                current_judge["has_api_key"] = True
        if "selected_environment_id" in payload:
            current["selected_environment_id"] = str(payload.get("selected_environment_id") or "")
        if "skillopt" in payload:
            skillopt_settings = payload.get("skillopt") if isinstance(payload.get("skillopt"), dict) else {}
            current_skillopt = current.setdefault("skillopt", {})
            for key in ("budget", "edit_budget", "min_delta", "selected_target_id"):
                if key in skillopt_settings:
                    current_skillopt[key] = skillopt_settings[key]
        normalized = _normalize_webui_settings(current, include_secrets=True)
        with self._lock:
            normalized["judge"]["api_key"] = self._webui_judge_api_key
        sanitized = _sanitize_webui_settings(normalized)
        if self._webui_judge_api_key:
            sanitized["judge"]["has_api_key"] = True
        _write_json_atomic(self._webui_settings_path(), sanitized)
        return sanitized

    def _webui_settings_path(self) -> Path:
        return self.config.runs_dir / "webui-settings.json"

    def _load_webui_settings(self, include_secrets: bool = False) -> dict[str, Any]:
        path = self._webui_settings_path()
        settings = _load_webui_settings_from_file(path, include_secrets=False)
        with self._lock:
            api_key = self._webui_judge_api_key
        if include_secrets:
            settings = _normalize_webui_settings(settings, include_secrets=True)
            settings["judge"]["api_key"] = api_key
        elif api_key:
            settings["judge"]["has_api_key"] = True
        return settings

    def _run_job(self, job: SkillOptJob) -> None:
        with self._lock:
            if job.status == "stopping":
                job.status = "stopped"
                job.finished_at = now_iso()
                return
            job.status = "running"
            job.started_at = now_iso()
        env = os.environ.copy()
        # Inject judge API key if available
        with self._lock:
            judge_api_key = self._job_judge_api_keys.get(job.id, "")
        if judge_api_key:
            env["OPENROUTER_API_KEY"] = judge_api_key
        with Path(job.log_path).open("wb") as log:
            log.write(("$ " + " ".join(job.command) + "\n").encode("utf-8"))
            proc = subprocess.Popen(
                job.command,
                cwd=Path(__file__).resolve().parents[1],
                stdout=log,
                stderr=subprocess.STDOUT,
                env=env,
                start_new_session=(os.name == "posix"),
            )
            with self._lock:
                job.process = proc
                stop_requested = job.status == "stopping"
            if stop_requested and proc.poll() is None:
                terminate_process(proc)
            exit_code = proc.wait()
        with self._lock:
            job.process = None
            job.exit_code = int(exit_code)
            job.finished_at = now_iso()
            if job.status == "stopping":
                job.status = "stopped"
            else:
                job.status = "passed" if exit_code == 0 else "failed"
            # Clean up judge API key after job completes
            self._job_judge_api_keys.pop(job.id, None)
            job.message = "" if exit_code == 0 else f"skillopt exited {exit_code}"

    def _job_payload(self, job: SkillOptJob) -> dict[str, Any]:
        payload = {
            "id": job.id,
            "command": job.command,
            "log_path": job.log_path,
            "run_dir": job.run_dir,
            "status": job.status,
            "created_at": job.created_at,
            "started_at": job.started_at,
            "finished_at": job.finished_at,
            "exit_code": job.exit_code,
            "message": job.message,
            "report_url": job.report_url,
        }
        if Path(job.log_path).exists():
            payload["log_tail"] = tail_text(Path(job.log_path))
        return payload


def build_skillopt_command(payload: dict[str, Any], *, run_id: str, artifact_root: Path) -> list[str]:
    skill = str(payload.get("skill") or "device-operator").strip()
    backend = str(payload.get("backend") or "mobilegym").strip()
    train_suite = str(payload.get("train_suite") or "skillopt/device-operator/device_operator_train").strip()
    validation_suite = str(payload.get("validation_suite") or "skillopt/device-operator/device_operator_verification").strip()
    output = Path(artifact_root) / run_id / "best_skill.md"
    command = [
        sys.executable,
        "-m",
        "skillopt",
        "--skill",
        skill,
        "--backend",
        backend,
        "--train-suite",
        train_suite,
        "--validation-suite",
        validation_suite,
        "--budget",
        str(int(payload.get("budget") or 10)),
        "--edit-budget",
        str(int(payload.get("edit_budget") or 4)),
        "--min-delta",
        str(float(payload.get("min_delta") or 0.03)),
        "--artifact-root",
        str(artifact_root),
        "--run-id",
        run_id,
        "--output",
        str(output),
    ]
    environment_url = str(payload.get("environment_url") or "").strip()
    if environment_url:
        command.extend(["--environment-url", environment_url])
    agent_url = str(payload.get("agent_url") or "").strip()
    if agent_url:
        command.extend(["--agent-url", agent_url])
    daemon_image = str(payload.get("daemon_image") or "").strip()
    if daemon_image:
        command.extend(["--daemon-image", daemon_image])
    agent_config = str(payload.get("agent_config") or "").strip()
    if agent_config:
        command.extend(["--agent-config", agent_config])
    if payload.get("no_build_daemon_image"):
        command.append("--no-build-daemon-image")
    if payload.get("no_judge"):
        command.append("--no-judge")
    optimizer_model = str(payload.get("optimizer_model") or "").strip()
    if optimizer_model:
        command.extend(["--optimizer-model", optimizer_model])
    judge_model = str(payload.get("judge_model") or "").strip()
    if judge_model:
        command.extend(["--judge-model", judge_model])
    return command


def list_skillopt_targets(suites_dir: Path) -> list[dict[str, Any]]:
    """List all SkillOpt targets from suites directory.

    Each skill directory can contain multiple suite pairs.
    A pair is identified by matching prefixes in filenames:
    - <prefix>_train.json pairs with <prefix>_verification.json (or _validation.json)
    - If no prefix pairs found, falls back to legacy single-pair matching
    """
    if not suites_dir.exists():
        return []
    targets: list[dict[str, Any]] = []

    for skill_dir in sorted(path for path in suites_dir.iterdir() if path.is_dir()):
        skill_name = skill_dir.name
        suite_pairs = _find_suite_pairs(skill_dir)

        if not suite_pairs:
            # Legacy fallback: single pair per skill
            train = _pick_suite_file(skill_dir, ["train"])
            validation = _pick_suite_file(skill_dir, ["verification", "validation", "selection"])
            if train and validation:
                suite_pairs = [{"prefix": "", "train": train, "validation": validation}]

        for pair in suite_pairs:
            prefix = pair["prefix"]
            train = pair["train"]
            validation = pair["validation"]

            # Generate target ID: skill-name or skill-name-scenario
            target_id = skill_name if not prefix else f"{skill_name}-{prefix}"
            # Display name: use prefix if available, otherwise skill name
            display_name = prefix.replace("_", " ").title() if prefix else skill_name

            targets.append({
                "id": target_id,
                "skill": skill_name,
                "name": f"{skill_name}: {display_name}" if prefix else skill_name,
                "train_suite": f"skillopt/{skill_name}/{train.stem}",
                "validation_suite": f"skillopt/{skill_name}/{validation.stem}",
                "train_task_count": _suite_task_count(train),
                "validation_task_count": _suite_task_count(validation),
            })

    return targets


def _find_suite_pairs(skill_dir: Path) -> list[dict[str, Any]]:
    """Find all train/validation suite pairs in a skill directory.

    Matches files by prefix pattern:
    - <prefix>_train.json + <prefix>_verification.json
    - <prefix>_train.json + <prefix>_validation.json

    Returns list of {prefix, train, validation} dicts.
    """
    train_files: dict[str, Path] = {}  # prefix -> train file
    validation_files: dict[str, Path] = {}  # prefix -> validation file

    for suite_file in skill_dir.glob("*.json"):
        stem = suite_file.stem.lower()

        # Match train files
        if "_train" in stem:
            prefix = stem.split("_train")[0]
            train_files[prefix] = suite_file

        # Match validation/verification files
        elif "_verification" in stem or "_validation" in stem:
            for suffix in ["_verification", "_validation"]:
                if suffix in stem:
                    prefix = stem.split(suffix)[0]
                    validation_files[prefix] = suite_file
                    break

    # Pair up matching prefixes
    pairs = []
    for prefix in sorted(set(train_files.keys()) & set(validation_files.keys())):
        pairs.append({
            "prefix": prefix,
            "train": train_files[prefix],
            "validation": validation_files[prefix],
        })

    return pairs


def _pick_suite_file(skill_dir: Path, tokens: list[str]) -> Path | None:
    for suite in sorted(skill_dir.glob("*.json")):
        stem = suite.stem.lower()
        if any(token in stem for token in tokens):
            return suite
    return None


def _suite_task_count(path: Path) -> int:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except Exception:
        return 0
    tasks = data.get("tasks") if isinstance(data, dict) else None
    return len(tasks) if isinstance(tasks, list) else 0


def mobilegym_environment_url(env: dict[str, Any]) -> str:
    return str(env.get("public_endpoint") or env.get("endpoint") or "").strip()


def agent_config_has_api_key(content: str) -> bool:
    try:
        data = tomllib.loads(content)
    except tomllib.TOMLDecodeError:
        match = re.search(r"(?m)^\s*api_key\s*=\s*(['\"])(.*?)\1", content)
        return bool(match and match.group(2).strip())
    model = data.get("model")
    if isinstance(model, dict):
        return bool(str(model.get("api_key") or "").strip())
    return bool(str(data.get("api_key") or "").strip())


def serve(config: SkillOptWebUIConfig) -> None:
    app = SkillOptWebApp(config)
    handler = make_handler(app)
    server = ThreadingHTTPServer((config.host, config.port), handler)
    print(f"SkillOpt Web UI: http://{config.host}:{config.port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        app.shutdown()
        server.server_close()


def cli(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="python -m skillopt webui")
    parser.add_argument("--host", default=DEFAULT_HOST)
    parser.add_argument("--port", type=int, default=DEFAULT_PORT)
    parser.add_argument("--runs-dir", default=str(Path(__file__).resolve().parent / "runs" / "webui"))
    parser.add_argument("--base-config-dir", default=str(DEFAULT_BASE_CONFIG_DIR))
    parser.add_argument("--agent-config", default="")
    args = parser.parse_args(argv)
    serve(SkillOptWebUIConfig(
        runs_dir=Path(args.runs_dir),
        host=args.host,
        port=args.port,
        base_config_dir=Path(args.base_config_dir),
        agent_config_path=Path(args.agent_config) if args.agent_config else None,
    ))
    return 0


def make_handler(app: SkillOptWebApp):
    class SkillOptHandler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            path = urllib.parse.urlparse(self.path).path
            if path == "/":
                self._send_bytes(200, "text/html; charset=utf-8", INDEX_HTML.encode("utf-8"))
                return
            if path == "/api/jobs":
                self._send_json(200, {"jobs": app.list_jobs()})
                return
            if path == "/api/environments/mobilegym":
                self._send_json(200, {"environments": app.list_mobilegym_environments()})
                return
            if path == "/api/targets":
                self._send_json(200, {"targets": app.list_targets()})
                return
            if path == "/api/webui-settings":
                self._send_json(200, {"settings": app.get_webui_settings()})
                return
            if path == "/api/benchmark/agent-config":
                self._send_json(200, {"config": app.benchmark_agent_config_info(include_content=True)})
                return
            if path.startswith("/api/jobs/"):
                job = app.get_job(path.removeprefix("/api/jobs/"))
                if job is None:
                    self._send_json(404, {"error": "job not found"})
                    return
                self._send_json(200, {"job": job})
                return
            if path.startswith("/runs/"):
                self._serve_run_file(path.removeprefix("/runs/"))
                return
            self._send_json(404, {"error": "not found"})

        def do_POST(self) -> None:
            path = urllib.parse.urlparse(self.path).path
            if path == "/api/benchmark/agent-config/reset":
                try:
                    config = app.reset_agent_config()
                except Exception as exc:
                    self._send_json(400, {"error": str(exc)})
                    return
                self._send_json(200, {"config": config})
                return
            payload = self._read_json()
            if payload is None:
                return
            if path == "/api/jobs":
                try:
                    self._send_json(200, {"job": app.start_job(payload)})
                except Exception as exc:
                    self._send_json(400, {"error": str(exc)})
                return
            if path == "/api/environments/mobilegym":
                try:
                    self._send_json(200, {"environment": app.start_mobilegym_environment(payload)})
                except Exception as exc:
                    self._send_json(400, {"error": str(exc)})
                return
            if path == "/api/benchmark/agent-config":
                try:
                    config = app.save_agent_config(payload)
                except Exception as exc:
                    self._send_json(400, {"error": str(exc)})
                    return
                self._send_json(200, {"config": config})
                return
            if path == "/api/webui-settings":
                try:
                    settings = app.save_webui_settings(payload)
                except Exception as exc:
                    self._send_json(400, {"error": str(exc)})
                    return
                self._send_json(200, {"settings": settings})
                return
            if path.startswith("/api/environments/mobilegym/") and path.endswith("/stop"):
                env_id = path.removeprefix("/api/environments/mobilegym/").removesuffix("/stop")
                env = app.stop_mobilegym_environment(env_id)
                if env is None:
                    self._send_json(404, {"error": "environment not found"})
                    return
                self._send_json(200, {"environment": env})
                return
            if path.startswith("/api/jobs/") and path.endswith("/stop"):
                job_id = path.removeprefix("/api/jobs/").removesuffix("/stop")
                job = app.stop_job(job_id)
                if job is None:
                    self._send_json(404, {"error": "job not found"})
                    return
                self._send_json(200, {"job": job})
                return
            self._send_json(404, {"error": "not found"})

        def do_DELETE(self) -> None:
            path = urllib.parse.urlparse(self.path).path
            if path.startswith("/api/environments/mobilegym/"):
                env_id = path.removeprefix("/api/environments/mobilegym/")
                env = app.delete_mobilegym_environment(env_id)
                if env is None:
                    self._send_json(404, {"error": "environment not found"})
                    return
                self._send_json(200, {"environment": env})
                return
            self._send_json(404, {"error": "not found"})

        def log_message(self, format: str, *args: Any) -> None:
            return

        def _read_json(self) -> dict[str, Any] | None:
            try:
                length = int(self.headers.get("Content-Length", "0") or "0")
                raw = self.rfile.read(length) if length else b"{}"
                payload = json.loads(raw.decode("utf-8"))
            except Exception:
                self._send_json(400, {"error": "invalid JSON"})
                return None
            if not isinstance(payload, dict):
                self._send_json(400, {"error": "JSON body must be an object"})
                return None
            return payload

        def _serve_run_file(self, rel: str) -> None:
            target = (app.config.runs_dir / rel).resolve()
            root = app.config.runs_dir.resolve()
            if not target.is_relative_to(root) or not target.exists() or not target.is_file():
                self._send_json(404, {"error": "not found"})
                return
            content_type = "text/html; charset=utf-8" if target.suffix == ".html" else "text/plain; charset=utf-8"
            self._send_bytes(200, content_type, target.read_bytes())

        def _send_json(self, status: int, payload: dict[str, Any]) -> None:
            self._send_bytes(status, "application/json; charset=utf-8", json.dumps(payload, ensure_ascii=False).encode("utf-8"))

        def _send_bytes(self, status: int, content_type: str, data: bytes) -> None:
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    return SkillOptHandler


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def new_job_id() -> str:
    return "skillopt-" + datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S") + f"-{int(time.time() * 1000) % 10000:04d}"


def tail_text(path: Path, limit: int = 12000) -> str:
    data = path.read_bytes()
    if len(data) > limit:
        data = data[-limit:]
    return data.decode("utf-8", errors="replace")


def terminate_process(proc: subprocess.Popen) -> None:
    if proc.poll() is not None:
        return
    if os.name == "posix":
        try:
            os.killpg(proc.pid, signal.SIGTERM)
            return
        except ProcessLookupError:
            return
        except Exception:
            pass
    proc.terminate()


def _default_webui_settings(include_secrets: bool = False) -> dict[str, Any]:
    judge: dict[str, Any] = {"enabled": True, "model": DEFAULT_JUDGE_MODEL}
    if include_secrets:
        judge["api_key"] = ""
    else:
        judge["has_api_key"] = False
    return {
        "judge": judge,
        "selected_environment_id": "",
        "skillopt": {
            "budget": 10,
            "edit_budget": 4,
            "min_delta": 0.03,
            "selected_target_id": "",
        },
    }


def _normalize_webui_settings(data: Any, include_secrets: bool = False) -> dict[str, Any]:
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
    raw_skillopt = data.get("skillopt") if isinstance(data.get("skillopt"), dict) else {}
    try:
        budget = int(raw_skillopt.get("budget", 10) or 10)
    except (TypeError, ValueError):
        budget = 10
    try:
        edit_budget = int(raw_skillopt.get("edit_budget", 4) or 4)
    except (TypeError, ValueError):
        edit_budget = 4
    try:
        min_delta = float(raw_skillopt.get("min_delta", 0.03) or 0.03)
    except (TypeError, ValueError):
        min_delta = 0.03
    skillopt_settings = {
        "budget": max(1, budget),
        "edit_budget": max(1, edit_budget),
        "min_delta": max(0.0, min_delta),
        "selected_target_id": str(raw_skillopt.get("selected_target_id") or ""),
    }
    return {
        "judge": judge,
        "selected_environment_id": str(data.get("selected_environment_id") or ""),
        "skillopt": skillopt_settings,
    }


def _sanitize_webui_settings(data: dict[str, Any]) -> dict[str, Any]:
    return _normalize_webui_settings(data, include_secrets=False)


def _load_webui_settings_from_file(path: Path, include_secrets: bool = False) -> dict[str, Any]:
    if not path.exists():
        return _default_webui_settings(include_secrets=include_secrets)
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        data = {}
    return _normalize_webui_settings(data, include_secrets=include_secrets)


def _write_json_atomic(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    tmp.replace(path)


INDEX_HTML = r"""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Aiden SkillOpt</title>
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
    .brand { display: flex; align-items: center; gap: 12px; min-width: 0; }
    .brand-mark {
      width: 20px; height: 20px;
      background: var(--purple);
      display: grid; place-items: center;
      font-size: 12px; font-weight: 700;
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
      grid-template-columns: 600px minmax(0, 1fr);
      min-height: calc(100vh - 48px);
      gap: 1px;
      background: var(--border);
    }
    .side, .workspace { background: var(--bg); min-width: 0; }
    .side { display: grid; align-content: start; gap: 1px; }
    .workspace { display: grid; grid-template-rows: auto auto auto auto minmax(360px, 1fr); gap: 1px; }
    .tile { background: var(--layer); border-radius: 0; padding: 16px; min-width: 0; }
    .tile-header, .toolbar {
      display: flex; align-items: center; justify-content: space-between;
      gap: 12px; margin-bottom: 16px;
    }
    .tile-title { margin: 0; font-size: 16px; font-weight: 600; letter-spacing: 0; }
    .tile-kicker { margin-top: 2px; color: var(--muted); font-size: 12px; }
    .form-grid { display: grid; grid-template-columns: 1fr; gap: 12px; align-items: stretch; }
    .field { display: grid; gap: 6px; min-width: 0; background: var(--layer); }
    .form-grid button { justify-self: start; min-width: 120px; }
    .field label, .check-label { color: var(--muted); font-size: 12px; font-weight: 600; }
    input[type="text"], input:not([type]), input[type="url"], input[type="number"],
    input[type="password"], input[type="search"], select, textarea {
      width: 100%; border: 0;
      border-bottom: 1px solid var(--border-strong);
      border-radius: 0;
      color: var(--text); background: var(--field); font: inherit;
    }
    input[type="text"], input:not([type]), input[type="url"], input[type="number"],
    input[type="password"], input[type="search"], select { height: 40px; padding: 0 12px; }
    textarea {
      min-height: 220px; max-height: 360px; resize: vertical; padding: 12px;
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 12px; line-height: 1.45;
    }
    input:focus, textarea:focus, button:focus, a:focus {
      outline: 2px solid var(--focus); outline-offset: -2px;
    }
    button {
      height: 40px; border: 0; border-radius: 0; padding: 0 16px;
      background: var(--gray-button); color: #fff; font: inherit;
      font-weight: 600; cursor: pointer; white-space: nowrap;
    }
    button:hover { background: var(--gray-button-hover); }
    button.primary { background: var(--blue); color: #fff; }
    button.primary:hover { background: var(--blue-hover); }
    button.danger { background: transparent; color: var(--red); padding: 0 8px; }
    button.danger:hover { background: #fff1f1; }
    button:disabled { opacity: 0.45; cursor: not-allowed; }
    .ghost-button { background: transparent; color: var(--blue); padding: 0 8px; }
    .ghost-button:hover { background: #edf5ff; }
    .config-actions { display: flex; align-items: center; gap: 8px; margin-top: 12px; }
    .config-actions span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .segmented {
      display: grid; grid-template-columns: repeat(2, minmax(0, 1fr));
      border: 1px solid var(--border-strong); margin-bottom: 16px;
    }
    .segmented button {
      height: 32px; min-width: 0; background: var(--layer); color: var(--text);
      border-right: 1px solid var(--border-strong); font-weight: 500;
    }
    .segmented button:last-child { border-right: 0; }
    .segmented button.active { background: var(--gray-button); color: #fff; }
    .env-panel[hidden] { display: none; }
    .table-wrap { border-top: 1px solid var(--border); max-height: 430px; overflow: auto; background: var(--layer); }
    .suite-table-wrap { max-height: calc(100vh - 180px); min-height: 520px; }
    .job-table-wrap { max-height: 240px; }
    .task-table-wrap { max-height: 280px; min-height: 180px; }
    table { width: 100%; border-collapse: collapse; table-layout: fixed; }
    thead { background: #e0e0e0; }
    th, td {
      border-bottom: 1px solid var(--border);
      padding: 10px 12px; text-align: left; vertical-align: middle;
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
    th { height: 40px; color: var(--muted); font-size: 12px; font-weight: 600; }
    tbody tr { background: var(--layer); }
    tbody tr:hover { background: #f4f4f4; }
    tbody tr.selected-row { box-shadow: inset 3px 0 0 var(--blue); }
    td:first-child input[type="checkbox"], td:first-child input[type="radio"] {
      display: block; margin: 0 auto;
    }
    .muted { color: var(--muted); }
    .cell-main { display: grid; gap: 2px; min-width: 0; }
    .cell-main span, .cell-main small {
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
    .cell-main small { color: var(--muted-2); font-size: 12px; }
    .status {
      display: inline-flex; align-items: center; min-height: 24px;
      padding: 0 8px; border: 0; background: #e0e0e0; color: #393939;
      font-size: 12px; font-weight: 600; text-transform: uppercase;
    }
    .status.passed { background: #defbe6; color: var(--green); }
    .status.failed { background: #fff1f1; color: var(--red); }
    .status.running, .status.queued, .status.starting, .status.starting_agent,
    .status.preparing, .status.building { background: #edf5ff; color: var(--blue); }
    .status.canceled, .status.stopping { background: #fff8e1; color: var(--orange); }
    .status.stopped, .status.device { background: #e0e0e0; color: #525252; }
    .status.mobilegym { background: #e8daff; color: var(--purple); }
    .status.ready { background: #defbe6; color: var(--green); }
    .status.missing { background: #fff1f1; color: var(--red); }
    .status-actions { display: flex; gap: 8px; align-items: center; }
    .inline-actions { display: flex; gap: 10px; align-items: center; min-width: 0; }
    .env-actions { justify-content: flex-end; gap: 8px; overflow: visible; }
    .env-actions button { flex: 0 0 auto; }
    .table-button { height: 28px; padding: 0 8px; min-width: 0; font-size: 12px; }
    .progress { height: 8px; background: #e0e0e0; overflow: hidden; }
    .progress > div { height: 100%; width: 0%; background: var(--blue); transition: width 160ms linear; }
    .run-config-grid {
      display: grid;
      grid-template-columns: minmax(220px, 1fr) minmax(360px, 1.2fr) auto;
      gap: 16px; align-items: end;
    }
    .run-meta { display: grid; gap: 4px; min-width: 0; }
    .run-meta strong {
      font-size: 20px; font-weight: 500;
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
    .run-actions { display: flex; gap: 12px; align-items: center; }
    .judge-inline {
      display: grid;
      grid-template-columns: auto minmax(220px, 1fr) minmax(180px, 0.8fr);
      gap: 12px; align-items: end;
    }
    .skillopt-inline {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr)) auto;
      gap: 12px; align-items: end;
    }
    .check-label {
      display: flex; gap: 8px; align-items: center;
      min-height: 40px; white-space: nowrap;
    }
    .check-label input { width: 16px; height: 16px; margin: 0; }
    .metric-grid {
      display: grid;
      grid-template-columns: repeat(6, minmax(0, 1fr));
      gap: 1px; background: var(--border); margin-top: 16px;
    }
    .metric { background: var(--layer-alt); min-height: 88px; padding: 12px; }
    .metric span { display: block; color: var(--muted); font-size: 12px; margin-bottom: 8px; }
    .metric strong { display: block; font-size: 28px; line-height: 1; font-weight: 400; }
    .detail-grid {
      display: grid;
      grid-template-columns: minmax(360px, 0.95fr) minmax(420px, 1.05fr);
      gap: 1px; min-width: 0; min-height: 0; background: var(--border);
    }
    .detail-grid .tile { min-height: 0; }
    .detail-grid textarea { min-height: 300px; max-height: 520px; }
    .modal-backdrop {
      position: fixed; inset: 0; z-index: 20;
      display: grid; place-items: center; padding: 24px;
      background: rgba(22, 22, 22, 0.42);
    }
    .modal-backdrop[hidden] { display: none; }
    .modal {
      width: min(960px, 100%); max-height: calc(100vh - 48px); overflow: auto;
      background: var(--layer); border: 1px solid var(--border-strong);
      box-shadow: 0 16px 48px rgba(0, 0, 0, 0.22);
    }
    .modal-header, .modal-footer {
      display: flex; align-items: center; justify-content: space-between;
      gap: 12px; padding: 16px; border-bottom: 1px solid var(--border);
    }
    .modal-footer { justify-content: flex-end; border-top: 1px solid var(--border); border-bottom: 0; }
    .modal-body { padding: 16px; }
    .modal-env-grid {
      display: grid;
      grid-template-columns: minmax(220px, 0.42fr) minmax(420px, 1fr);
      gap: 16px; align-items: start;
    }
    .modal-env-table { max-height: 360px; border: 1px solid var(--border); }
    pre {
      margin: 0; min-height: 300px; height: 100%; max-height: 420px;
      overflow: auto; border: 0; background: #262626; color: #f4f4f4;
      padding: 16px;
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 12px; line-height: 1.45; white-space: pre-wrap;
    }
    a { color: var(--blue); text-decoration: none; }
    a:hover { text-decoration: underline; }
    .empty-row { color: var(--muted); height: 48px; }
    @media (max-width: 980px) {
      .layout { grid-template-columns: 1fr; }
      .suite-table-wrap { max-height: 360px; }
      .metric-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
      .run-config-grid { grid-template-columns: 1fr; align-items: stretch; }
      .judge-inline, .skillopt-inline { grid-template-columns: 1fr; align-items: stretch; }
      .detail-grid { grid-template-columns: 1fr; }
      .modal-env-grid { grid-template-columns: 1fr; }
    }
  </style>
</head>
<body>
  <header class="topbar">
    <div class="brand">
      <div class="brand-mark">S</div>
      <div class="brand-title">Aiden SkillOpt</div>
    </div>
    <div id="headerStatus" class="header-meta">Idle</div>
  </header>
  <main class="layout">
    <aside class="side">
      <section class="tile">
        <div class="toolbar">
          <div>
            <h2 class="tile-title">Targets</h2>
            <div class="tile-kicker">Select a SkillOpt target</div>
          </div>
          <input id="suiteFilter" type="search" style="max-width:172px" placeholder="Filter">
        </div>
        <div class="table-wrap suite-table-wrap">
          <table>
            <thead><tr><th style="width:40px"></th><th>Target</th><th style="width:96px">Train</th><th style="width:72px">Verify</th></tr></thead>
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
            <strong><span id="selectedTargetLabel">No target</span></strong>
            <span class="muted"><span id="selectedSuitesLabel">0 / 0</span> tasks - <span id="selectedEnvLabel">No environment</span> - <span id="selectedJudgeLabel">judge enabled</span></span>
          </div>
          <div class="judge-inline">
            <label class="check-label"><input id="judgeEnabled" type="checkbox" checked> Enable judge</label>
            <div class="field"><label for="judgeModel">Judge model</label><input id="judgeModel" autocomplete="off" placeholder="anthropic/claude-sonnet-4-6"></div>
            <div class="field"><label for="judgeApiKey">API key</label><input id="judgeApiKey" type="password" autocomplete="off" placeholder="OPENROUTER_API_KEY"></div>
          </div>
          <div class="run-actions">
            <button id="runBtn" class="primary">Run selected target</button>
          </div>
        </div>
        <div class="skillopt-inline" style="margin-top:16px">
          <div class="field"><label for="budget">Budget</label><input id="budget" type="number" min="1" step="1" value="10"></div>
          <div class="field"><label for="editBudget">Edit budget</label><input id="editBudget" type="number" min="1" step="1" value="4"></div>
          <div class="field"><label for="minDelta">Min delta</label><input id="minDelta" type="number" min="0" step="0.01" value="0.03"></div>
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
          <div class="metric"><span class="muted">Budget</span><strong id="mBudget">10</strong></div>
          <div class="metric"><span class="muted">Edit budget</span><strong id="mEditBudget">4</strong></div>
          <div class="metric"><span class="muted">Iterations</span><strong id="mIterations">0</strong></div>
          <div class="metric"><span class="muted">Best score</span><strong id="mScore">-</strong></div>
          <div class="metric"><span class="muted">Exit</span><strong id="mExit">-</strong></div>
          <div class="metric"><span class="muted">Reports</span><strong id="mReport">0</strong></div>
        </div>
      </section>

      <section class="tile">
        <div class="tile-header">
          <div>
            <h2 class="tile-title">Jobs</h2>
            <div class="tile-kicker">Recent SkillOpt runs</div>
          </div>
        </div>
        <div class="table-wrap job-table-wrap">
          <table>
            <thead><tr><th>Job</th><th style="width:220px">Target</th><th>Environment</th><th style="width:120px">Status</th><th style="width:120px">Report</th><th style="width:96px"></th></tr></thead>
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
            <span id="agentConfigBadge" class="status missing">missing</span>
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
              <div id="logScopeLabel" class="tile-kicker">SkillOpt runner output</div>
            </div>
            <button id="showJobLog" class="ghost-button table-button" type="button">Job log</button>
          </div>
          <pre id="logBox">Select or start a job.</pre>
        </section>
      </div>
    </section>
  </main>

  <div id="runEnvDialog" class="modal-backdrop" hidden>
    <section class="modal" role="dialog" aria-modal="true" aria-labelledby="runEnvTitle">
      <div class="modal-header">
        <div>
          <h2 id="runEnvTitle" class="tile-title">Choose Environment</h2>
          <div class="tile-kicker">MobileGym environments</div>
        </div>
        <button id="closeRunEnv" class="ghost-button table-button" type="button">Close</button>
      </div>
      <div class="modal-body">
        <div class="modal-env-grid">
          <section>
            <div class="form-grid">
              <div class="field"><label for="mobilegymName">Name</label><input id="mobilegymName" placeholder="SkillOpt MobileGym" autocomplete="off"></div>
              <div class="field"><label for="mobilegymParallelEnvs">Envs</label><input id="mobilegymParallelEnvs" type="number" min="1" step="1" value="5"></div>
              <button id="startMobileGym" class="primary" type="button">Start MobileGym</button>
              <span id="mobilegymStatus" class="muted"></span>
            </div>
          </section>
          <section>
            <div class="table-wrap modal-env-table">
              <table>
                <thead><tr><th style="width:40px"></th><th style="width:128px">Environment</th><th>Endpoint</th><th style="width:160px"></th></tr></thead>
                <tbody id="envRows"></tbody>
              </table>
            </div>
          </section>
        </div>
      </div>
      <div class="modal-footer">
        <button id="cancelRunEnv" class="ghost-button" type="button">Cancel</button>
        <button id="confirmRunBtn" class="primary" type="button">Run selected target</button>
      </div>
    </section>
  </div>
  <script>
    const DEFAULT_JUDGE_MODEL = 'anthropic/claude-sonnet-4-6';
    let targets = [];
    let selectedTargetId = '';
    let mobilegymEnvironments = [];
    let selectedEnvironmentId = '';
    let jobs = [];
    let activeJobId = null;
    let activeTaskLogId = null;
    let agentConfig = null;
    let agentConfigDirty = false;
    let agentConfigEditing = false;

    function escapeHtml(value){ return String(value ?? '').replace(/[&<>"']/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch])); }
    function cssToken(value){ return String(value ?? '').toLowerCase().replace(/[^a-z0-9_-]/g, '_'); }

    function selectedTarget(){
      return targets.find(t => t.id === selectedTargetId) || targets[0] || null;
    }
    function setSelectedTarget(id){
      selectedTargetId = id;
      renderTargets();
      syncRunState();
      saveWebuiSettings({keepInputs: true});
    }

    function envCanRun(env){ return !!env && !!env.endpoint && (env.type !== 'mobilegym' || env.status === 'running'); }
    function selectedEnv(){
      const cur = mobilegymEnvironments.find(e => e.id === selectedEnvironmentId);
      if(envCanRun(cur)) return cur;
      return mobilegymEnvironments.find(envCanRun) || null;
    }
    function setSelectedEnv(id){
      const env = mobilegymEnvironments.find(e => e.id === id);
      if(!envCanRun(env)) return;
      selectedEnvironmentId = id;
      renderEnvs();
      syncRunState();
      saveWebuiSettings({keepInputs: true});
    }

    function currentJudgeSettings(){
      const enabled = document.getElementById('judgeEnabled').checked;
      const model = document.getElementById('judgeModel').value.trim() || DEFAULT_JUDGE_MODEL;
      const apiKey = document.getElementById('judgeApiKey').value.trim();
      return {enabled, model, apiKey};
    }
    function syncJudgePanel(){
      const enabled = document.getElementById('judgeEnabled').checked;
      document.getElementById('judgeModel').disabled = !enabled;
      document.getElementById('judgeApiKey').disabled = !enabled;
    }
    function persistJudgeSettings(){
      syncJudgePanel();
      syncRunState();
      saveWebuiSettings({keepInputs: true});
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
      selectedEnvironmentId = String(settings.selected_environment_id || '');
      const judge = settings.judge || {};
      document.getElementById('judgeEnabled').checked = judge.enabled !== false;
      document.getElementById('judgeModel').value = String(judge.model || DEFAULT_JUDGE_MODEL);
      const keyInput = document.getElementById('judgeApiKey');
      keyInput.value = '';
      keyInput.placeholder = judge.has_api_key ? 'Saved; leave blank to keep' : 'OPENROUTER_API_KEY';
      const so = settings.skillopt || {};
      if(so.budget) document.getElementById('budget').value = String(so.budget);
      if(so.edit_budget) document.getElementById('editBudget').value = String(so.edit_budget);
      if(so.min_delta != null) document.getElementById('minDelta').value = String(so.min_delta);
      if(so.selected_target_id) selectedTargetId = String(so.selected_target_id);
      syncJudgePanel();
      renderEnvs();
      renderTargets();
      syncRunState();
    }
    async function saveWebuiSettings(options = {}){
      const judge = currentJudgeSettings();
      const judgePayload = {enabled: judge.enabled, model: judge.model};
      if(judge.apiKey) judgePayload.api_key = judge.apiKey;
      const payload = {
        judge: judgePayload,
        selected_environment_id: selectedEnvironmentId,
        skillopt: {
          budget: parseInt(document.getElementById('budget').value || '10', 10) || 10,
          edit_budget: parseInt(document.getElementById('editBudget').value || '4', 10) || 4,
          min_delta: parseFloat(document.getElementById('minDelta').value || '0.03') || 0.03,
          selected_target_id: selectedTargetId
        }
      };
      try {
        const res = await fetch('/api/webui-settings', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(payload)
        });
        const body = await res.json();
        if(!res.ok) throw new Error(body.error || 'failed to save settings');
        return true;
      } catch (err) {
        document.getElementById('logBox').textContent = err.message || String(err);
        return false;
      }
    }

    async function loadTargets(){
      try {
        const res = await fetch('/api/targets');
        const body = await res.json();
        targets = body.targets || [];
      } catch (err) {
        targets = [];
        document.getElementById('logBox').textContent = err.message || String(err);
      }
      if(!selectedTargetId && targets.length) selectedTargetId = targets[0].id;
      renderTargets();
      syncRunState();
    }

    function renderTargets(){
      const filter = document.getElementById('suiteFilter').value.toLowerCase();
      const tbody = document.getElementById('suiteRows');
      const current = selectedTarget();
      const filtered = targets.filter(t => !filter || `${t.name} ${t.skill}`.toLowerCase().includes(filter));
      tbody.innerHTML = '';
      if(!filtered.length){
        tbody.innerHTML = '<tr><td class="empty-row" colspan="4">No SkillOpt targets found</td></tr>';
      }
      filtered.forEach(t => {
        const tr = document.createElement('tr');
        tr.className = current && current.id === t.id ? 'selected-row' : '';
        tr.innerHTML = `<td><input type="radio" name="activeTarget" ${current && current.id === t.id ? 'checked' : ''}></td>
          <td title="${escapeHtml(t.train_suite + ' / ' + t.validation_suite)}"><div class="cell-main"><span>${escapeHtml(t.name || t.skill)}</span><small>${escapeHtml(t.skill)}</small></div></td>
          <td>${t.train_task_count || 0}</td>
          <td>${t.validation_task_count || 0}</td>`;
        tr.querySelector('input').onchange = () => setSelectedTarget(t.id);
        tbody.appendChild(tr);
      });
    }

    async function loadEnvironments(){
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

    function renderEnvs(){
      const tbody = document.getElementById('envRows');
      if(!tbody) return;
      const current = selectedEnv();
      tbody.innerHTML = '';
      if(!mobilegymEnvironments.length){
        tbody.innerHTML = '<tr><td class="empty-row" colspan="4">No MobileGym environments yet</td></tr>';
      }
      mobilegymEnvironments.forEach(env => {
        const selectable = envCanRun(env);
        const displayEndpoint = env.public_endpoint || env.endpoint;
        const endpointDetail = `${env.endpoint || ''} · ${env.parallel_envs || 5} envs`;
        const status = env.status || 'mobilegym';
        const actionHtml = ['building', 'starting', 'running', 'stopping'].includes(status)
          ? `<button class="danger" data-stop="${escapeHtml(env.id)}" ${status === 'stopping' ? 'disabled' : ''}>Stop</button>`
          : `<button class="danger" data-remove="${escapeHtml(env.id)}">Remove</button>`;
        const tr = document.createElement('tr');
        tr.innerHTML = `<td><input type="radio" name="activeEnv" ${current && current.id === env.id ? 'checked' : ''} ${selectable ? '' : 'disabled'}></td>
          <td title="${escapeHtml(env.name || env.id)}"><div class="cell-main"><span>${escapeHtml(env.name || env.id || 'MobileGym')}</span><small><span class="status mobilegym">${escapeHtml(status)}</span></small></div></td>
          <td title="${escapeHtml(env.endpoint || '')}"><div class="cell-main"><span>${escapeHtml(displayEndpoint)}</span><small>${escapeHtml(endpointDetail)}</small></div></td>
          <td><div class="inline-actions env-actions">${actionHtml}</div></td>`;
        tr.querySelector('input').onchange = () => setSelectedEnv(env.id);
        const stop = tr.querySelector('[data-stop]');
        if(stop) stop.onclick = () => stopMobileGym(env.id);
        const remove = tr.querySelector('[data-remove]');
        if(remove) remove.onclick = () => removeMobileGym(env.id);
        tbody.appendChild(tr);
      });
    }

    async function startMobileGym(){
      const button = document.getElementById('startMobileGym');
      const status = document.getElementById('mobilegymStatus');
      const previous = button.textContent;
      const name = document.getElementById('mobilegymName').value.trim() || 'SkillOpt MobileGym';
      const parallelEnvs = Math.max(1, parseInt(document.getElementById('mobilegymParallelEnvs').value || '5', 10) || 5);
      button.disabled = true;
      button.textContent = 'Starting';
      status.textContent = 'Starting MobileGym container';
      try {
        const res = await fetch('/api/environments/mobilegym', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({name, parallel_envs: parallelEnvs})
        });
        const body = await res.json();
        if(!res.ok || body.error) throw new Error(body.error || 'failed to start MobileGym');
        if(body.environment){
          mobilegymEnvironments = [body.environment, ...mobilegymEnvironments.filter(e => e.id !== body.environment.id)];
          selectedEnvironmentId = body.environment.id;
        }
        status.textContent = 'MobileGym started';
        renderEnvs();
        syncRunState();
        await loadEnvironments();
      } catch (err) {
        const message = err.message || String(err);
        status.textContent = message;
        document.getElementById('logBox').textContent = message;
      } finally {
        button.disabled = false;
        button.textContent = previous;
      }
    }

    async function stopMobileGym(id){
      if(selectedEnvironmentId === id){ selectedEnvironmentId = ''; saveWebuiSettings({keepInputs: true}); }
      const res = await fetch(`/api/environments/mobilegym/${encodeURIComponent(id)}/stop`, {method: 'POST'});
      if(!res.ok) document.getElementById('logBox').textContent = await res.text();
      await loadEnvironments();
    }
    async function removeMobileGym(id){
      if(selectedEnvironmentId === id){ selectedEnvironmentId = ''; saveWebuiSettings({keepInputs: true}); }
      const res = await fetch(`/api/environments/mobilegym/${encodeURIComponent(id)}`, {method: 'DELETE'});
      if(!res.ok) document.getElementById('logBox').textContent = await res.text();
      await loadEnvironments();
    }

    async function loadAgentConfigStatus(){
      try {
        const res = await fetch('/api/benchmark/agent-config');
        const body = await res.json();
        if(!res.ok) throw new Error(body.error || 'failed to load agent config');
        agentConfig = body.config || {};
      } catch (err) {
        agentConfig = {available: false, api_key_nonempty: false, source: 'unavailable', path: '', error: err.message || String(err)};
      }
      if(!agentConfigEditing && agentConfig && typeof agentConfig.content === 'string'){
        document.getElementById('agentConfigText').value = agentConfig.content;
        agentConfigDirty = false;
      }
      renderAgentConfigStatus();
      syncRunState();
    }

    function renderAgentConfigStatus(){
      const ready = !!(agentConfig && agentConfig.api_key_nonempty);
      const badge = document.getElementById('agentConfigBadge');
      badge.className = 'status ' + (ready ? 'ready' : 'missing');
      badge.textContent = ready ? 'ready' : 'missing';
      document.getElementById('agentConfigPath').textContent = agentConfig && agentConfig.path ? agentConfig.path : 'agent.toml';
      const statusNode = document.getElementById('agentConfigStatus');
      if(!agentConfigEditing){
        statusNode.textContent = ready ? 'model.api_key is saved' : 'missing model.api_key';
        statusNode.style.color = '';
      }
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
        document.getElementById('agentConfigStatus').textContent = agentConfigDirty ? 'Modified' : 'Editing';
      }
    }

    async function saveAgentConfig(){
      const content = document.getElementById('agentConfigText').value;
      try {
        const res = await fetch('/api/benchmark/agent-config', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({content})
        });
        const body = await res.json();
        if(!res.ok) throw new Error(body.error || 'failed to save agent.toml');
        agentConfigDirty = false;
        setAgentConfigEditing(false);
        document.getElementById('agentConfigStatus').textContent = 'Saved';
        await loadAgentConfigStatus();
      } catch (err) {
        const node = document.getElementById('agentConfigStatus');
        node.textContent = err.message || String(err);
        node.style.color = 'var(--red)';
      }
    }

    async function resetAgentConfig(){
      try {
        const res = await fetch('/api/benchmark/agent-config/reset', {method: 'POST'});
        const body = await res.json();
        if(!res.ok) throw new Error(body.error || 'failed to reset agent.toml');
        document.getElementById('agentConfigText').value = body.config.content || '';
        agentConfigDirty = false;
        setAgentConfigEditing(false);
        document.getElementById('agentConfigStatus').textContent = 'Reset';
        await loadAgentConfigStatus();
      } catch (err) {
        const node = document.getElementById('agentConfigStatus');
        node.textContent = err.message || String(err);
        node.style.color = 'var(--red)';
      }
    }

    function syncRunState(){
      const target = selectedTarget();
      const env = selectedEnv();
      const judge = currentJudgeSettings();
      document.getElementById('selectedTargetLabel').textContent = target ? (target.name || target.skill) : 'No target';
      document.getElementById('selectedSuitesLabel').textContent = target ? `${target.train_task_count || 0} train / ${target.validation_task_count || 0} verify` : '0 / 0';
      document.getElementById('selectedEnvLabel').textContent = env ? (env.name || env.id || 'MobileGym') : 'No environment';
      document.getElementById('selectedJudgeLabel').textContent = judge.enabled ? `judge: ${judge.model}` : 'judge: off';
      document.getElementById('mBudget').textContent = document.getElementById('budget').value || '0';
      document.getElementById('mEditBudget').textContent = document.getElementById('editBudget').value || '0';
      const ready = !!(agentConfig && agentConfig.api_key_nonempty);
      document.getElementById('runBtn').disabled = !ready || !target;
      const confirm = document.getElementById('confirmRunBtn');
      if(confirm) confirm.disabled = !envCanRun(env) || !target || !ready;
    }

    async function openRunEnvironmentDialog(){
      if(!selectedTarget()) return;
      await loadEnvironments();
      renderEnvs();
      syncRunState();
      document.getElementById('runEnvDialog').hidden = false;
    }
    function closeRunEnvironmentDialog(){ document.getElementById('runEnvDialog').hidden = true; }

    async function confirmRun(){
      const env = selectedEnv();
      const target = selectedTarget();
      if(!envCanRun(env) || !target) return;
      const started = await startRun(env, target);
      if(started) closeRunEnvironmentDialog();
    }

    async function startRun(env, target){
      if(agentConfigDirty){
        const saved = await saveAgentConfig();
        if(!saved) return false;
      }
      const judge = currentJudgeSettings();
      selectedEnvironmentId = env.id;
      const settingsSaved = await saveWebuiSettings({keepInputs: true});
      if(!settingsSaved) return false;
      const payload = {
        target_id: target.id,
        environment_id: env.id,
        backend: 'mobilegym',
        budget: parseInt(document.getElementById('budget').value || '10', 10) || 10,
        edit_budget: parseInt(document.getElementById('editBudget').value || '4', 10) || 4,
        min_delta: parseFloat(document.getElementById('minDelta').value || '0.03') || 0.03,
        no_build_daemon_image: true,
        no_judge: !judge.enabled,
        judge_model: judge.model,
        judge_api_key: judge.apiKey
      };
      const res = await fetch('/api/jobs', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(payload)
      });
      const body = await res.json();
      if(!res.ok || body.error){
        document.getElementById('logBox').textContent = body.error || 'failed to start SkillOpt';
        return false;
      }
      activeJobId = body.job.id;
      activeTaskLogId = null;
      await refreshJobs();
      return true;
    }

    async function refreshJobs(){
      const res = await fetch('/api/jobs');
      const body = await res.json();
      jobs = body.jobs || [];
      const previous = activeJobId;
      if(!activeJobId && jobs.length) activeJobId = jobs[0].id;
      if(activeJobId && !jobs.find(j => j.id === activeJobId)) activeJobId = jobs[0] ? jobs[0].id : null;
      if(previous !== activeJobId) activeTaskLogId = null;
      renderJobs();
      if(activeJobId) await loadActiveJob();
      else resetActiveJob();
    }

    function jobCanStop(job){ return job && !['passed','failed','stopped','canceled'].includes(job.status || ''); }
    function taskCanStop(task){ return task && !['passed','failed','stopped','canceled'].includes(task.status || ''); }

    function renderJobs(){
      const tbody = document.getElementById('jobRows');
      tbody.innerHTML = '';
      if(!jobs.length){
        tbody.innerHTML = '<tr><td class="empty-row" colspan="6">No jobs yet</td></tr>';
      }
      jobs.forEach(job => {
        const targetLabel = job.target_id || job.skill || (job.command || []).slice(-1)[0] || '';
        const envLabel = job.environment_name || job.environment_url || '';
        const report = job.report_url ? `<a href="${escapeHtml(job.report_url)}" target="_blank" rel="noreferrer">report</a>` : '<span class="muted">none</span>';
        const actionHtml = jobCanStop(job) ? `<button class="danger" data-stop-job="${escapeHtml(job.id)}" ${job.status === 'stopping' ? 'disabled' : ''}>Stop</button>` : '';
        const tr = document.createElement('tr');
        tr.className = activeJobId === job.id ? 'selected-row' : '';
        tr.innerHTML = `<td><div class="cell-main"><a href="#" data-job="${escapeHtml(job.id)}">${escapeHtml(job.id)}</a><small>${escapeHtml(job.created_at || '')}</small></div></td>
          <td title="${escapeHtml(targetLabel)}"><div class="cell-main"><span>${escapeHtml(targetLabel)}</span><small>${escapeHtml(job.run_dir || '')}</small></div></td>
          <td title="${escapeHtml(envLabel)}"><div class="cell-main"><span>${escapeHtml(envLabel)}</span><small>mobilegym</small></div></td>
          <td><span class="status ${cssToken(job.status)}">${escapeHtml(job.status)}</span></td>
          <td>${report}</td>
          <td>${actionHtml}</td>`;
        tr.querySelector('[data-job]').onclick = e => { e.preventDefault(); activeJobId = job.id; activeTaskLogId = null; loadActiveJob(); renderJobs(); };
        const stop = tr.querySelector('[data-stop-job]');
        if(stop) stop.onclick = () => stopJob(job.id);
        tbody.appendChild(tr);
      });
    }

    async function loadActiveJob(){
      const res = await fetch(`/api/jobs/${encodeURIComponent(activeJobId)}`);
      if(!res.ok) return;
      const job = (await res.json()).job;
      renderActiveJob(job);
    }

    function renderActiveJob(job){
      document.getElementById('activeJobLabel').textContent = `${job.id} - ${job.run_dir || ''}`;
      const st = document.getElementById('activeJobStatus');
      st.className = 'status ' + cssToken(job.status);
      st.textContent = job.status;
      const stop = document.getElementById('activeStopJob');
      stop.hidden = !jobCanStop(job);
      stop.disabled = job.status === 'stopping';
      document.getElementById('headerStatus').textContent = job.status || 'Idle';
      document.getElementById('mExit').textContent = job.exit_code === null || job.exit_code === undefined ? '-' : String(job.exit_code);
      document.getElementById('mReport').textContent = job.report_url ? '1' : '0';
      document.getElementById('progressBar').style.width = progressWidth(job.status);
      document.getElementById('logBox').textContent = job.log_tail || 'Waiting for output.';
      document.getElementById('logScopeLabel').textContent = 'SkillOpt runner output';
      renderJobsTable();
    }

    function resetActiveJob(){
      activeTaskLogId = null;
      document.getElementById('activeJobLabel').textContent = 'No active job';
      const st = document.getElementById('activeJobStatus');
      st.className = 'status';
      st.textContent = 'idle';
      document.getElementById('activeStopJob').hidden = true;
      document.getElementById('progressBar').style.width = '0%';
      ['mExit','mReport'].forEach(id => document.getElementById(id).textContent = id === 'mExit' ? '-' : '0');
      document.getElementById('mIterations').textContent = '0';
      document.getElementById('mScore').textContent = '-';
      document.getElementById('headerStatus').textContent = 'Idle';
      document.getElementById('logScopeLabel').textContent = 'SkillOpt runner output';
    }

    function progressWidth(status){
      if(['passed','failed','stopped'].includes(status)) return '100%';
      if(['running','stopping'].includes(status)) return '62%';
      if(status === 'queued') return '18%';
      return '0%';
    }

    async function stopJob(id){
      const res = await fetch(`/api/jobs/${encodeURIComponent(id)}/stop`, {method: 'POST'});
      if(!res.ok) document.getElementById('logBox').textContent = await res.text();
      await refreshJobs();
    }

    document.getElementById('startMobileGym').onclick = startMobileGym;
    document.getElementById('runBtn').onclick = openRunEnvironmentDialog;
    document.getElementById('confirmRunBtn').onclick = confirmRun;
    document.getElementById('closeRunEnv').onclick = closeRunEnvironmentDialog;
    document.getElementById('cancelRunEnv').onclick = closeRunEnvironmentDialog;
    document.getElementById('runEnvDialog').onclick = e => { if(e.target.id === 'runEnvDialog') closeRunEnvironmentDialog(); };
    document.getElementById('activeStopJob').onclick = () => { if(activeJobId) stopJob(activeJobId); };
    document.getElementById('showJobLog').onclick = () => { if(activeJobId) loadActiveJob(); };
    document.getElementById('suiteFilter').oninput = renderTargets;
    document.getElementById('judgeEnabled').onchange = persistJudgeSettings;
    document.getElementById('judgeModel').oninput = persistJudgeSettings;
    document.getElementById('judgeApiKey').oninput = syncRunState;
    document.getElementById('judgeApiKey').onchange = persistJudgeSettings;
    document.getElementById('editAgentConfig').onclick = () => setAgentConfigEditing(true);
    document.getElementById('saveAgentConfig').onclick = saveAgentConfig;
    document.getElementById('resetAgentConfig').onclick = resetAgentConfig;
    document.getElementById('agentConfigText').oninput = () => {
      if(!agentConfigEditing) return;
      agentConfigDirty = true;
      const node = document.getElementById('agentConfigStatus');
      node.textContent = 'Modified';
      node.style.color = '';
    };
    ['budget','editBudget','minDelta'].forEach(id => {
      document.getElementById(id).oninput = () => { syncRunState(); saveWebuiSettings({keepInputs: true}); };
    });

    setAgentConfigEditing(false);
    loadWebuiSettings();
    loadTargets();
    loadEnvironments();
    loadAgentConfigStatus();
    refreshJobs();
    setInterval(refreshJobs, 3000);
    setInterval(loadEnvironments, 5000);
    setInterval(() => { if(!agentConfigEditing) loadAgentConfigStatus(); }, 10000);
  </script>
</body>
</html>
"""




