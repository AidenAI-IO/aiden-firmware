"""Single-device state and task-routing semantics for the ADB Android bridge.

Mirrors the parts of benchmark/mobilegym/bridge/episode.py that the runner and
Go agent rely on, collapsed to one device:

- empty benchmark-task-id falls through to the single device state (this is
  what the WebUI serial path produces: setup/screen/release carry per-task
  route ids while daemon tool calls carry no header at all);
- a non-empty task id takes sticky ownership; a different task id gets HTTP
  429 (no_bridge_env_available) until the owner releases.
"""

from __future__ import annotations

import dataclasses as dc
import threading
import time
import uuid
from typing import Any


BENCHMARK_TASK_ID_HEADER = "benchmark-task-id"
ACTION_LOG_LIMIT = 200


class MissingBenchmarkTaskIDError(ValueError):
    pass


class NoBridgeEnvAvailableError(RuntimeError):
    pass


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
class ADBBridgeState:
    device: Any
    active_task_id: str = ""
    active_episode_id: str = ""
    action_log: list[dict[str, Any]] = dc.field(default_factory=list)
    lock: threading.RLock = dc.field(default_factory=threading.RLock)

    def check_task_access(self, task_id: str) -> None:
        """Raise NoBridgeEnvAvailableError when a different task owns the device.

        An empty task id always passes: the single-device bridge serves it from
        the one state without ownership bookkeeping.
        """
        task_id = str(task_id or "").strip()
        if not task_id:
            return
        with self.lock:
            if self.active_task_id and self.active_task_id != task_id:
                raise NoBridgeEnvAvailableError(
                    f"adb bridge device is owned by benchmark task {self.active_task_id!r}"
                )

    def acquire(self, task_id: str) -> tuple[str, bool]:
        """Take (or idempotently re-take) ownership and start a fresh episode.

        Returns (episode_id, newly_acquired). newly_acquired is True only when
        this call transferred ownership to task_id; callers use it to roll back
        ownership if the device reset that follows fails, without dropping
        ownership a running task already held (idempotent re-setup).
        """
        task_id = str(task_id or "").strip()
        with self.lock:
            newly_acquired = False
            if task_id:
                if self.active_task_id and self.active_task_id != task_id:
                    raise NoBridgeEnvAvailableError(
                        f"adb bridge device is owned by benchmark task {self.active_task_id!r}"
                    )
                newly_acquired = self.active_task_id != task_id
                self.active_task_id = task_id
            self.active_episode_id = task_id or f"reset-{uuid.uuid4().hex}"
            self.action_log.clear()
            return self.active_episode_id, newly_acquired

    def release(self, task_id: str) -> bool:
        """Release ownership held by task_id. Empty/mismatched ids release nothing."""
        task_id = str(task_id or "").strip()
        with self.lock:
            if not task_id or self.active_task_id != task_id:
                return False
            self.active_task_id = ""
            return True

    def log_action(
        self,
        *,
        tool_name: str,
        tool_input: dict[str, Any],
        adb_summary: str,
        duration_ms: int,
        screenshot: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        entry = {
            "action_id": uuid.uuid4().hex,
            "ts": time.time(),
            "tool": tool_name,
            "input": tool_input,
            "adb": adb_summary,
            "duration_ms": duration_ms,
            "episode_id": self.active_episode_id,
            "screenshot_size": (screenshot or {}).get("size"),
        }
        with self.lock:
            self.action_log.append(entry)
            if len(self.action_log) > ACTION_LOG_LIMIT:
                del self.action_log[: len(self.action_log) - ACTION_LOG_LIMIT]
        return entry
