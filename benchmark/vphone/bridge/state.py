"""Single-VM ownership and action state for the VPhone bridge."""

from __future__ import annotations

import dataclasses as dc
import threading
import time
import uuid
from typing import Any


BENCHMARK_TASK_ID_HEADER = "benchmark-task-id"
ACTION_LOG_LIMIT = 200


class NoBridgeEnvAvailableError(RuntimeError):
    """Raised when another benchmark task owns the only VM."""


def benchmark_task_id_from_headers(headers: Any) -> str:
    if headers is None:
        return ""
    for name in (BENCHMARK_TASK_ID_HEADER, "x-benchmark-task-id"):
        try:
            value = headers.get(name)
        except AttributeError:
            value = None
        if value:
            return str(value).strip()
    return ""


@dc.dataclass
class VPhoneBridgeState:
    device: Any
    active_task_id: str = ""
    active_episode_id: str = ""
    action_log: list[dict[str, Any]] = dc.field(default_factory=list)
    lock: threading.RLock = dc.field(default_factory=threading.RLock)

    def check_task_access(self, task_id: str) -> None:
        task_id = str(task_id or "").strip()
        with self.lock:
            if self.active_task_id and self.active_task_id != task_id:
                raise NoBridgeEnvAvailableError(
                    f"vphone bridge VM is owned by benchmark task {self.active_task_id!r}"
                )

    def acquire(self, task_id: str) -> tuple[str, bool]:
        task_id = str(task_id or "").strip()
        with self.lock:
            # Enforce single-VM ownership even for anonymous callers: once a task
            # owns the VM, an empty task_id must not reset its episode/action log.
            if self.active_task_id and self.active_task_id != task_id:
                raise NoBridgeEnvAvailableError(
                    f"vphone bridge VM is owned by benchmark task {self.active_task_id!r}"
                )
            newly_acquired = False
            if task_id:
                newly_acquired = self.active_task_id != task_id
                self.active_task_id = task_id
            self.active_episode_id = task_id or f"reset-{uuid.uuid4().hex}"
            self.action_log.clear()
            return self.active_episode_id, newly_acquired

    def release(self, task_id: str) -> bool:
        task_id = str(task_id or "").strip()
        with self.lock:
            if not task_id or self.active_task_id != task_id:
                return False
            self.active_task_id = ""
            self.active_episode_id = ""
            return True

    def log_action(
        self,
        *,
        tool_name: str,
        tool_input: dict[str, Any],
        summary: str,
        duration_ms: int,
        screenshot: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        entry = {
            "action_id": uuid.uuid4().hex,
            "ts": time.time(),
            "tool": tool_name,
            "input": tool_input,
            "vphone": summary,
            "duration_ms": duration_ms,
            "episode_id": self.active_episode_id,
            "screenshot_size": (screenshot or {}).get("size"),
        }
        with self.lock:
            self.action_log.append(entry)
            if len(self.action_log) > ACTION_LOG_LIMIT:
                del self.action_log[: len(self.action_log) - ACTION_LOG_LIMIT]
        return entry
