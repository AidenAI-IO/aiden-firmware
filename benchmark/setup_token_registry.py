from __future__ import annotations

import dataclasses as dc
import threading
from collections.abc import Callable, Hashable
from typing import Any


@dc.dataclass
class _SetupEntry:
    completed: threading.Event = dc.field(default_factory=threading.Event)
    result: Any = None
    error: BaseException | None = None


class SetupTokenRegistry:
    """Run one setup operation per process-local idempotency key."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._entries: dict[Hashable, _SetupEntry] = {}
        self._clear_when_completed: set[Hashable] = set()

    def run(self, key: Hashable, operation: Callable[[], Any]) -> Any:
        with self._lock:
            entry = self._entries.get(key)
            if entry is None:
                entry = _SetupEntry()
                self._entries[key] = entry
                owns_operation = True
            else:
                owns_operation = False

        if owns_operation:
            try:
                result = operation()
            except BaseException as exc:
                with self._lock:
                    entry.error = exc
                    if self._entries.get(key) is entry:
                        self._entries.pop(key)
                    self._clear_when_completed.discard(key)
                    entry.completed.set()
                raise
            with self._lock:
                entry.result = result
                entry.completed.set()
                if key in self._clear_when_completed:
                    self._entries.pop(key, None)
                    self._clear_when_completed.discard(key)
            return result

        entry.completed.wait()
        if entry.error is not None:
            raise entry.error
        return entry.result

    def clear_completed_for_task(self, task_id: str) -> int:
        removed = 0
        with self._lock:
            for key, entry in list(self._entries.items()):
                if not isinstance(key, tuple) or not key or key[0] != task_id:
                    continue
                if entry.completed.is_set():
                    self._entries.pop(key, None)
                    self._clear_when_completed.discard(key)
                    removed += 1
                else:
                    self._clear_when_completed.add(key)
        return removed


def setup_token_from_payload(payload: dict[str, Any]) -> str:
    value = payload.get("setup_token")
    if value is None or value == "":
        return ""
    if not isinstance(value, str):
        raise ValueError("setup_token must be a string")
    value = value.strip()
    if not value:
        return ""
    if len(value) > 128:
        raise ValueError("setup_token must not exceed 128 characters")
    return value
