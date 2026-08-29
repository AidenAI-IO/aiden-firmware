from __future__ import annotations

import dataclasses as dc
import threading
import time
from typing import Any


class NoBridgeEnvAvailableError(RuntimeError):
    pass


def benchmark_task_id_from_headers(headers: Any) -> str:
    if headers is None:
        return ""
    for name in ("benchmark-task-id", "x-benchmark-task-id"):
        try:
            value = headers.get(name)
        except AttributeError:
            value = None
        if value:
            return str(value).strip()
    return ""


@dc.dataclass
class DesktopBridgeState:
    device: Any
    active_task_id: str = ""
    active_episode_id: str = ""
    active_task_expires_at: float = 0.0
    task_lease_ttl_sec: float = 30 * 60
    lock: threading.RLock = dc.field(default_factory=threading.RLock)
    screenshot_seq: int = 0

    def _renew(self) -> None:
        self.active_task_expires_at = time.monotonic() + max(1.0, float(self.task_lease_ttl_sec))

    def _clear_expired(self) -> None:
        if self.active_task_id and self.active_task_expires_at and time.monotonic() >= self.active_task_expires_at:
            self.active_task_id = ""
            self.active_episode_id = ""
            self.active_task_expires_at = 0.0

    def active_task_lease_state(self) -> str:
        with self.lock:
            self._clear_expired()
            return "active" if self.active_task_id else ""

    def check_task_access(self, task_id: str) -> None:
        task_id = str(task_id or "").strip()
        if not task_id:
            raise NoBridgeEnvAvailableError("benchmark task id is required")
        with self.lock:
            self._clear_expired()
            if not self.active_task_id:
                raise NoBridgeEnvAvailableError(
                    "desktop environment is not leased to a benchmark task"
                )
            if self.active_task_id != task_id:
                raise NoBridgeEnvAvailableError(
                    f"desktop environment is owned by benchmark task {self.active_task_id!r}"
                )
            self._renew()

    def acquire(self, task_id: str) -> tuple[str, bool]:
        task_id = str(task_id or "").strip()
        with self.lock:
            self._clear_expired()
            if not task_id:
                raise ValueError("benchmark task id is required")
            if self.active_task_id and self.active_task_id != task_id:
                raise NoBridgeEnvAvailableError(
                    f"desktop environment is owned by benchmark task {self.active_task_id!r}"
                )
            newly_acquired = self.active_task_id != task_id
            self.active_task_id = task_id
            self._renew()
            self.active_episode_id = task_id
            return self.active_episode_id, newly_acquired

    def release(self, task_id: str) -> bool:
        task_id = str(task_id or "").strip()
        with self.lock:
            self._clear_expired()
            if not task_id or task_id != self.active_task_id:
                return False
            self.active_task_id = ""
            self.active_task_expires_at = 0.0
            self.active_episode_id = ""
            return True
