from __future__ import annotations

import dataclasses as dc
import json
import logging
import os
import socket
import time
import urllib.error
import urllib.request
import uuid
from enum import Enum
from pathlib import Path
from typing import Any, Callable

from .artifacts import export_bridge_actions

logger = logging.getLogger(__name__)

EVIDENCE_FIELDS = (
    "aiden_suite_name",
    "aiden_task_id",
    "aiden_last_response",
    "aiden_last_chat_history",
    "description_for_judge",
    "rubric",
    "rubric_spec",
    "hard_assertions",
    "expected_answer",
    "answer_format",
    "expected_recalled_memory_ids",
)

try:
    from bench_env.agent import BaseAgent as _MobileGymBaseAgent
except Exception:
    class _MobileGymBaseAgent:  # type: ignore[no-redef]
        pass


class ActionType(str, Enum):
    COMPLETE = "COMPLETE"
    ERROR = "ERROR"


@dc.dataclass(frozen=True)
class Action:
    action_type: Any
    data: dict[str, Any]

    @classmethod
    def complete(cls, response: str) -> "Action":
        return cls(ActionType.COMPLETE, {"response": response})


class AidenAdapterError(RuntimeError):
    def __init__(self, message: str, *, worker_dirty: bool = False, cleanup_errors: list[str] | None = None):
        super().__init__(message)
        self.worker_dirty = worker_dirty
        self.cleanup_errors = cleanup_errors or []


class AidenAdapterTimeout(AidenAdapterError):
    pass


class AidenHTTPError(AidenAdapterError):
    def __init__(self, status: int, message: str):
        super().__init__(f"HTTP {status}: {message}")
        self.status = status


class JsonHTTPClient:
    def post_json(self, url: str, payload: dict[str, Any], *, token: str | None = None, timeout: float | None = None) -> Any:
        headers = {"Content-Type": "application/json"}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        data = json.dumps(payload).encode("utf-8")
        request = urllib.request.Request(url, data=data, headers=headers, method="POST")
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:  # noqa: S310
                raw = response.read().decode("utf-8")
                return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as exc:
            body = exc.read().decode("utf-8", errors="replace")
            raise AidenHTTPError(exc.code, body) from exc
        except urllib.error.URLError as exc:
            if isinstance(exc.reason, socket.timeout):
                raise AidenAdapterTimeout(str(exc)) from exc
            raise AidenAdapterError(str(exc)) from exc
        except TimeoutError:
            raise
        except OSError as exc:
            raise AidenAdapterError(str(exc)) from exc


class AidenGoAgent(_MobileGymBaseAgent):
    def __init__(
        self,
        *,
        bridge_url: str,
        bridge_control_token: str,
        daemon: Any,
        http_client: Any | None = None,
        episode_id_factory: Callable[[], str] | None = None,
        chat_timeout_sec: float = 300,
        episode_timeout_sec: float = 30,
        artifact_dir: str | Path | None = None,
    ):
        try:
            super().__init__()
        except Exception:
            pass
        if not hasattr(self, "_history"):
            self._history = []
        self.bridge_url = bridge_url.rstrip("/")
        self.bridge_control_token = bridge_control_token
        self.daemon = daemon
        self.http_client = http_client or JsonHTTPClient()
        self.episode_id_factory = episode_id_factory or (lambda: uuid.uuid4().hex)
        self.chat_timeout_sec = chat_timeout_sec
        self.episode_timeout_sec = episode_timeout_sec
        self.artifact_dir = Path(artifact_dir) if artifact_dir is not None else None
        self.task: Any | None = None
        self.last_episode_id: str | None = None

    @property
    def name(self) -> str:
        return "aiden_go"

    @property
    def task(self) -> Any | None:
        return getattr(self, "_task", None)

    @task.setter
    def task(self, value: Any | None) -> None:
        self._task = value

    @property
    def history(self) -> list[Any]:
        return self._history

    def reset_history(self) -> None:
        self._history.clear()

    def build_messages(self, obs: Any) -> list[dict[str, Any]]:
        del obs
        return []

    def parse_response(self, response_text: str) -> Any:
        return complete_action(response_text)

    def reset(self, task: Any) -> None:
        self.task = task
        self._prepare_aiden_suite_task(task)

    def act(self, obs: Any) -> Any:
        del obs
        episode_id = self.episode_id_factory()
        self.last_episode_id = episode_id
        daemon_bound = False
        chat_started = False
        chat_error: AidenAdapterError | None = None
        cleanup_errors: list[str] = []
        chat_result: Any = None

        try:
            self._post_bridge("/episode/start", {"episode_id": episode_id})
            self._post_daemon(
                "/api/mobilegym/episode/start",
                {
                    "episode_id": episode_id,
                    "bridge_url": self.bridge_url,
                    "bridge_token": self._daemon_bridge_token(),
                },
            )
            daemon_bound = True
            # Clear daemon history so prior tasks' conversation/screenshots
            # don't bleed into this episode's context window. Mirrors what
            # benchmark/runner/recovery.py does for the HDMI backend.
            self._post_daemon("/api/clear", {}, timeout=self.episode_timeout_sec)
            chat_started = True
            chat_result = self._post_daemon(
                "/api/chat",
                {"message": self._task_input(), "episode_id": episode_id},
                timeout=self.chat_timeout_sec,
            )
            self._record_task_chat_result(chat_result)
        except TimeoutError as exc:
            chat_error = AidenAdapterTimeout(f"Aiden /api/chat timed out: {exc}", worker_dirty=True)
        except AidenAdapterError as exc:
            chat_error = exc
            chat_error.worker_dirty = chat_started or chat_error.worker_dirty
        except Exception as exc:
            chat_error = AidenAdapterError(str(exc), worker_dirty=chat_started)
        finally:
            if daemon_bound:
                self._cleanup_call(
                    cleanup_errors,
                    self._post_daemon,
                    "/api/mobilegym/episode/end",
                    {"episode_id": episode_id},
                    timeout=self.episode_timeout_sec,
                )
            bridge_end_response = self._cleanup_call(
                cleanup_errors,
                self._post_bridge,
                "/episode/end",
                {"episode_id": episode_id},
                timeout=self.episode_timeout_sec,
            )
            if self.artifact_dir is not None:
                try:
                    artifact_dir = _task_artifact_dir(self.artifact_dir, self.task)
                    _write_task_meta(artifact_dir, self.task)
                    action_log = _extract_action_log(bridge_end_response)
                    if action_log:
                        export_bridge_actions(artifact_dir, action_log)
                except Exception as e:
                    logger.warning(f"Failed to export bridge actions: {e}")

        if chat_error is not None:
            if chat_started:
                self._stop_dirty_daemon(cleanup_errors)
                chat_error.worker_dirty = True
            if cleanup_errors:
                message = f"{chat_error}; cleanup failed: {'; '.join(cleanup_errors)}"
                if isinstance(chat_error, AidenAdapterTimeout):
                    raise AidenAdapterTimeout(message, worker_dirty=True, cleanup_errors=cleanup_errors) from chat_error
                raise AidenAdapterError(message, worker_dirty=chat_error.worker_dirty, cleanup_errors=cleanup_errors) from chat_error
            raise chat_error

        if cleanup_errors:
            self._mark_daemon_dirty()
            raise AidenAdapterError(
                f"episode cleanup failed: {'; '.join(cleanup_errors)}",
                worker_dirty=True,
                cleanup_errors=cleanup_errors,
            )

        return complete_action(_response_text(chat_result))

    def _task_input(self) -> str:
        task = self.task
        if task is None:
            return ""
        if isinstance(task, str):
            return task
        if isinstance(task, dict):
            for key in ("instruction", "prompt", "goal", "task", "query"):
                if task.get(key):
                    return str(task[key])
        for name in ("instruction", "prompt", "goal", "task", "query"):
            value = getattr(task, name, None)
            if value:
                return str(value)
        return str(task)

    def _post_bridge(self, path: str, payload: dict[str, Any], *, timeout: float | None = None) -> Any:
        return self.http_client.post_json(
            _join_url(self.bridge_url, path),
            payload,
            token=self.bridge_control_token,
            timeout=timeout,
        )

    def _post_daemon(self, path: str, payload: dict[str, Any], *, timeout: float | None = None) -> Any:
        return self.http_client.post_json(
            _join_url(str(self.daemon.base_url).rstrip("/"), path),
            payload,
            token=self._daemon_control_token(),
            timeout=timeout,
        )

    def _prepare_aiden_suite_task(self, task: Any) -> None:
        metadata = _task_metadata(task)
        if not metadata or not _is_aiden_suite_task(metadata):
            return
        metadata.pop("aiden_last_response", None)
        metadata.pop("aiden_last_chat_history", None)
        try:
            self._post_daemon("/api/clear", {}, timeout=self.episode_timeout_sec)
            self._run_setup(metadata.get("global_reset"))
            self._run_setup(metadata.get("setup"))
        except AidenAdapterError:
            raise
        except TimeoutError as exc:
            raise AidenAdapterTimeout(f"Aiden suite setup timed out: {exc}") from exc
        except Exception as exc:
            raise AidenAdapterError(f"Aiden suite setup failed: {exc}") from exc

    def _run_setup(self, setup: Any) -> None:
        if not isinstance(setup, dict) or not setup:
            return
        sequence = setup.get("tool_sequence")
        if sequence:
            if not isinstance(sequence, list):
                raise AidenAdapterError(f"setup tool_sequence must be a list: {setup!r}")
            self._run_tool_sequence(sequence)
            return
        if setup.get("type") == "agent_prompt":
            prompt = setup.get("prompt")
            if not prompt:
                raise AidenAdapterError(f"agent_prompt setup missing prompt: {setup!r}")
            timeout = _float_value(setup.get("timeout_sec"), 90.0)
            self._post_daemon("/api/chat", {"message": str(prompt)}, timeout=timeout)
            clear_history_after = setup.get("clear_history_after", True)
            if not isinstance(clear_history_after, bool):
                raise AidenAdapterError(f"clear_history_after must be boolean: {clear_history_after!r}")
            if clear_history_after:
                self._post_daemon("/api/clear", {}, timeout=self.episode_timeout_sec)
            return
        raise AidenAdapterError(f"unsupported setup form: {setup!r}")

    def _run_tool_sequence(self, sequence: list[Any]) -> None:
        for step in sequence:
            if not isinstance(step, dict):
                raise AidenAdapterError(f"setup step must be an object: {step!r}")
            tool = step.get("tool")
            args = dict(step.get("args") or {})
            if tool == "wait_ms":
                time.sleep(_float_value(args.get("ms"), 0.0) / 1000.0)
                continue
            if not tool:
                raise AidenAdapterError(f"setup step missing tool: {step!r}")
            if tool == "shell":
                args = _rewrite_shell_memory_path(args, self._daemon_memory_dir())
            timeout = _float_value(step.get("timeout_sec"), 90.0)
            result = self._post_daemon(
                f"/api/tools/{tool}",
                {"input": args},
                timeout=timeout,
            )
            if isinstance(result, dict) and result.get("is_error"):
                output = result.get("output") or result.get("error") or result
                raise AidenAdapterError(f"setup tool {tool} failed: {output}")

    def _daemon_memory_dir(self) -> str:
        attempt_config = getattr(self.daemon, "attempt_config", None)
        memory_dir = getattr(attempt_config, "memory_dir", None)
        if memory_dir is not None:
            return str(memory_dir)
        runtime_config_dir = os.getenv("AIDEN_RUNTIME_CONFIG_DIR") or "/tmp/aiden-config"
        return f"{runtime_config_dir.rstrip('/')}/memory"

    def _record_task_chat_result(self, payload: Any) -> None:
        metadata = _task_metadata(self.task)
        if metadata is None:
            return
        metadata["aiden_last_response"] = _response_text(payload)
        metadata["aiden_last_chat_history"] = _history_payload(payload)

    def _daemon_control_token(self) -> str:
        return str(getattr(self.daemon, "control_token", ""))

    def _daemon_bridge_token(self) -> str:
        return str(
            getattr(
                self.daemon,
                "bridge_device_token",
                getattr(self.daemon, "device_token", ""),
            )
        )

    def _cleanup_call(self, errors: list[str], func: Callable[..., Any], path: str, payload: dict[str, Any], *, timeout: float) -> Any:
        try:
            return func(path, payload, timeout=timeout)
        except Exception as exc:
            errors.append(f"{path}: {exc}")
            return None

    def _stop_dirty_daemon(self, errors: list[str]) -> None:
        self._mark_daemon_dirty()
        for method_name in ("stop", "kill"):
            method = getattr(self.daemon, method_name, None)
            if method is None:
                continue
            try:
                method()
            except Exception as exc:
                errors.append(f"{method_name}: {exc}")
                self._mark_daemon_dirty()

    def _mark_daemon_dirty(self) -> None:
        marker = getattr(self.daemon, "mark_dirty", None)
        if marker is not None:
            marker()
            return
        try:
            setattr(self.daemon, "dirty", True)
        except Exception:
            pass


def complete_action(response: str) -> Any:
    action_cls, action_type_cls = _action_classes()
    if hasattr(action_cls, "complete"):
        return action_cls.complete(response)
    action_type = getattr(action_type_cls, "COMPLETE", "COMPLETE")
    data = {"response": response}
    try:
        return action_cls(action_type=action_type, data=data)
    except TypeError:
        return action_cls(action_type, data)


def _action_classes() -> tuple[type[Any], Any]:
    try:
        from bench_env.env.base import Action as MobileGymAction
        from bench_env.env.base import ActionType as MobileGymActionType

        return MobileGymAction, MobileGymActionType
    except Exception:
        return Action, ActionType


def _join_url(base_url: str, path: str) -> str:
    return f"{base_url.rstrip('/')}/{path.lstrip('/')}"


def _response_text(payload: Any) -> str:
    if isinstance(payload, str):
        return payload
    if isinstance(payload, dict):
        for key in ("response", "output", "message", "result"):
            if payload.get(key) is not None:
                return str(payload[key])
        data = payload.get("data")
        if isinstance(data, dict):
            for key in ("response", "output", "message", "result"):
                if data.get(key) is not None:
                    return str(data[key])
    return ""


def _history_payload(payload: Any) -> list[dict[str, Any]]:
    if not isinstance(payload, dict):
        return []
    history = payload.get("history")
    if isinstance(history, list):
        return [entry for entry in history if isinstance(entry, dict)]
    data = payload.get("data")
    if isinstance(data, dict) and isinstance(data.get("history"), list):
        return [entry for entry in data["history"] if isinstance(entry, dict)]
    return []


def _task_metadata(task: Any | None) -> dict[Any, Any] | None:
    if task is None:
        return None
    if isinstance(task, dict):
        metadata = task.get("metadata")
    else:
        metadata = getattr(task, "metadata", None)
    return metadata if isinstance(metadata, dict) else None


def _is_aiden_suite_task(metadata: dict[Any, Any]) -> bool:
    return bool(metadata.get("aiden_suite_name") or metadata.get("setup") or metadata.get("global_reset"))


def _rewrite_shell_memory_path(args: dict[str, Any], memory_dir: str) -> dict[str, Any]:
    command = args.get("command")
    if isinstance(command, str):
        args["command"] = command.replace("/userdata/agent/memory", memory_dir)
    return args


def _float_value(value: Any, default: float) -> float:
    try:
        return float(value)
    except (TypeError, ValueError):
        return default


def _extract_action_log(payload: Any) -> list[dict[str, Any]]:
    if not isinstance(payload, dict):
        return []
    data = payload.get("data")
    if isinstance(data, dict) and isinstance(data.get("action_log"), list):
        return [entry for entry in data["action_log"] if isinstance(entry, dict)]
    if isinstance(payload.get("action_log"), list):
        return [entry for entry in payload["action_log"] if isinstance(entry, dict)]
    return []


def _task_artifact_dir(root: Path, task: Any | None) -> Path:
    task_id = _task_id(task)
    if not task_id:
        return root
    return root / "trajectory" / task_id.replace(".", "_")


def _write_task_meta(path: Path, task: Any | None) -> None:
    task_id = _task_id(task)
    if not task_id:
        return
    path.mkdir(parents=True, exist_ok=True)
    payload: dict[str, Any] = {"task_id": task_id}
    metadata = _task_metadata(task)
    if metadata is not None:
        for field in EVIDENCE_FIELDS:
            if field not in metadata:
                continue
            value = metadata[field]
            try:
                json.dumps(value, ensure_ascii=False)
            except (TypeError, ValueError):
                continue
            payload[field] = value
    (path / "meta.json").write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def _task_id(task: Any | None) -> str:
    if task is None:
        return ""
    if isinstance(task, str):
        return task
    if isinstance(task, dict):
        for key in ("id", "task_id", "name"):
            if task.get(key):
                return str(task[key])
        return ""
    for name in ("id", "task_id", "name"):
        value = getattr(task, name, None)
        if value:
            return str(value)
    return ""
