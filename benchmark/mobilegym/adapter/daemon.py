from __future__ import annotations

import dataclasses as dc
import json
import os
import secrets
import signal
import subprocess
import tempfile
from pathlib import Path
from typing import Any, Mapping, Sequence


DEFAULT_INSTRUCTION = "You are controlling an Android-like MobileGym simulator. Use screenshot and touch tools."
DEFAULT_INPUT_MODE = "text"
DEFAULT_MAX_ITERATIONS = 20


@dc.dataclass(frozen=True)
class AttemptConfig:
    config_dir: Path
    config_path: Path
    bridge_token_file: Path
    control_token_file: Path
    memory_dir: Path
    skill_state_dir: Path
    log_dir: Path
    episode_records_dir: Path
    bridge_url: str
    bridge_token: str
    control_token: str


@dc.dataclass
class AidenDaemonHandle:
    base_url: str
    control_token: str
    bridge_device_token: str
    process: Any | None = None
    attempt_config: AttemptConfig | None = None
    dirty: bool = False
    cleanup_errors: list[str] = dc.field(default_factory=list)

    def mark_dirty(self) -> None:
        self.dirty = True

    def stop(self, timeout: float = 5) -> None:
        if self.process is None:
            return
        try:
            self.process.terminate()
            wait = getattr(self.process, "wait", None)
            if wait is not None:
                try:
                    wait(timeout=timeout)
                except subprocess.TimeoutExpired:
                    self.mark_dirty()
                    self.kill()
        except Exception as exc:
            self.cleanup_errors.append(f"stop: {exc}")
            self.mark_dirty()

    def kill(self) -> None:
        if self.process is None:
            return
        try:
            self.process.kill()
            self._kill_process_group_if_applicable()
            wait = getattr(self.process, "wait", None)
            if wait is not None:
                wait(timeout=2)
        except Exception as exc:
            self.cleanup_errors.append(f"kill: {exc}")
            self.mark_dirty()

    def _kill_process_group_if_applicable(self) -> None:
        pid = getattr(self.process, "pid", None)
        if pid is None or not hasattr(os, "getpgid") or not hasattr(os, "killpg"):
            return
        try:
            pgid = os.getpgid(pid)
            if pgid != os.getpgrp():
                os.killpg(pgid, signal.SIGKILL)
        except OSError as exc:
            self.cleanup_errors.append(f"killpg: {exc}")
            self.mark_dirty()


def create_attempt_config(
    root_dir: str | Path,
    *,
    bridge_url: str,
    bridge_token: str | None = None,
    control_token: str | None = None,
    template_path: str | Path | None = None,
    instruction: str = DEFAULT_INSTRUCTION,
    input_mode: str = DEFAULT_INPUT_MODE,
    max_iterations: int = DEFAULT_MAX_ITERATIONS,
    model_provider: str | None = None,
    model_name: str | None = None,
    model_base_url: str | None = None,
    model_api_key: str | None = None,
) -> AttemptConfig:
    root = Path(root_dir)
    root.mkdir(parents=True, exist_ok=True)
    config_dir = Path(tempfile.mkdtemp(prefix="aiden-go-", dir=str(root)))
    token_dir = config_dir / "tokens"
    bridge_token_file = token_dir / "bridge-device.token"
    control_token_file = token_dir / "daemon-control.token"
    memory_dir = config_dir / "memory"
    skill_state_dir = config_dir / "skill-state"
    log_dir = config_dir / "logs"
    episode_records_dir = config_dir / "episode-records"

    for path in (token_dir, memory_dir, skill_state_dir, log_dir, episode_records_dir, config_dir / "skills"):
        path.mkdir(parents=True, exist_ok=True)

    bridge_token = bridge_token or secrets.token_urlsafe(32)
    control_token = control_token or secrets.token_urlsafe(32)
    _write_secret(bridge_token_file, bridge_token)
    _write_secret(control_token_file, control_token)

    config_path = config_dir / "agent.toml"
    rendered = render_agent_toml(
        bridge_url=bridge_url,
        bridge_token_file=bridge_token_file,
        control_token_file=control_token_file,
        template_path=template_path,
        instruction=instruction,
        input_mode=input_mode,
        max_iterations=max_iterations,
        model_provider=_model_value(model_provider, "AIDEN_MODEL_PROVIDER", "MODEL_PROVIDER", default="fake"),
        model_name=_model_value(model_name, "AIDEN_MODEL_NAME", "MODEL_NAME"),
        model_base_url=_model_value(model_base_url, "AIDEN_MODEL_BASE_URL", "MODEL_BASE_URL"),
        model_api_key=_model_value(model_api_key, "AIDEN_MODEL_API_KEY", "MODEL_API_KEY"),
    )
    config_path.write_text(rendered)
    return AttemptConfig(
        config_dir=config_dir,
        config_path=config_path,
        bridge_token_file=bridge_token_file,
        control_token_file=control_token_file,
        memory_dir=memory_dir,
        skill_state_dir=skill_state_dir,
        log_dir=log_dir,
        episode_records_dir=episode_records_dir,
        bridge_url=bridge_url,
        bridge_token=bridge_token,
        control_token=control_token,
    )


def render_agent_toml(
    *,
    bridge_url: str,
    bridge_token_file: str | Path,
    control_token_file: str | Path,
    template_path: str | Path | None = None,
    instruction: str = DEFAULT_INSTRUCTION,
    input_mode: str = DEFAULT_INPUT_MODE,
    max_iterations: int = DEFAULT_MAX_ITERATIONS,
    model_provider: str = "fake",
    model_name: str = "",
    model_base_url: str = "",
    model_api_key: str = "",
) -> str:
    values = {
        "MODEL_PROVIDER": model_provider,
        "MODEL_NAME": model_name,
        "MODEL_BASE_URL": model_base_url,
        "MODEL_API_KEY": model_api_key,
        "BRIDGE_URL": bridge_url,
        "BRIDGE_TOKEN_FILE": str(bridge_token_file),
        "CONTROL_TOKEN_FILE": str(control_token_file),
    }
    if template_path is not None:
        rendered = Path(template_path).read_text()
        for name, value in values.items():
            rendered = rendered.replace("{{" + name + "}}", value)
        return rendered

    lines = [
        f"instruction = {_toml_string(instruction)}",
        f"input_mode = {_toml_string(input_mode)}",
        f"max_iterations = {int(max_iterations)}",
        "",
        "[model_providers.benchmark]",
        f"type = {_toml_string(model_provider)}",
        f"base_url = {_toml_string(model_base_url)}",
        f"api_key = {_toml_string(model_api_key)}",
        "",
        "[model]",
        'provider = "benchmark"',
        f"model = {_toml_string(model_name)}",
        "",
        "[device]",
        'backend = "mobilegym"',
        f"bridge_url = {_toml_string(bridge_url)}",
        f"bridge_token_file = {_toml_string(str(bridge_token_file))}",
        f"control_token_file = {_toml_string(str(control_token_file))}",
        "",
    ]
    return "\n".join(lines)


def launch_daemon(
    command: Sequence[str],
    *,
    attempt_config: AttemptConfig,
    base_url: str,
    env: Mapping[str, str] | None = None,
) -> AidenDaemonHandle:
    log_path = attempt_config.log_dir / "daemon.log"
    log_file = log_path.open("ab")
    try:
        process = subprocess.Popen(  # noqa: S603
            list(command),
            env=dict(os.environ, **dict(env or {})),
            stdout=log_file,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    finally:
        log_file.close()
    return AidenDaemonHandle(
        base_url=base_url,
        control_token=attempt_config.control_token,
        bridge_device_token=attempt_config.bridge_token,
        process=process,
        attempt_config=attempt_config,
    )


def _write_secret(path: Path, value: str) -> None:
    path.write_text(value)
    try:
        path.chmod(0o600)
    except OSError:
        pass


def _toml_string(value: str) -> str:
    return json.dumps(str(value))


def _model_value(value: str | None, *env_names: str, default: str = "") -> str:
    if value is not None:
        return value
    for name in env_names:
        env_value = os.getenv(name)
        if env_value:
            return env_value
    return default
