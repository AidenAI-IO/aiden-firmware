from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Callable
from typing import Any


class BridgeEpisodeError(RuntimeError):
    pass


class StaleEpisodeError(BridgeEpisodeError):
    pass


class BridgeEpisodeState:
    def __init__(self, env: Any, owner_loop: asyncio.AbstractEventLoop | None = None):
        self.env = env
        self.owner_loop = owner_loop or asyncio.get_event_loop()
        self.active_episode_id: str | None = None
        self.action_log: list[dict[str, Any]] = []
        self._action_counter = 0
        self._env_lock = asyncio.Lock()

    async def start_episode(self, episode_id: str) -> dict[str, str]:
        episode_id = str(episode_id or "").strip()
        if not episode_id:
            raise ValueError("episode_id is required")
        async with self._env_lock:
            self.active_episode_id = episode_id
            self.action_log = []
            self._action_counter = 0
        return {"episode_id": episode_id}

    async def reset_episode(self, episode_id: str) -> dict[str, Any]:
        episode_id = str(episode_id or "").strip()
        if not episode_id:
            raise ValueError("episode_id is required")
        async with self._env_lock:
            reset = getattr(self.env, "reset", None)
            reset_ran = False
            if reset is not None:
                result = reset()
                if asyncio.iscoroutine(result) or isinstance(result, Awaitable):
                    await result
                reset_ran = True
            self.active_episode_id = episode_id
            self.action_log = []
            self._action_counter = 0
        return {"episode_id": episode_id, "reset": reset_ran}

    async def end_episode(self, episode_id: str) -> dict[str, Any]:
        episode_id = self.require_active(episode_id)
        async with self._env_lock:
            if self.active_episode_id != episode_id:
                raise StaleEpisodeError("stale episode_id")
            logs = list(self.action_log)
            self.active_episode_id = None
        return {"episode_id": episode_id, "action_log": logs}

    def require_active(self, episode_id: str | None) -> str:
        episode_id = str(episode_id or "").strip()
        if not episode_id or episode_id != self.active_episode_id:
            raise StaleEpisodeError("stale episode_id")
        return episode_id

    async def run_env(self, fn: Callable[[Any], Any | Awaitable[Any]]) -> Any:
        running_loop = asyncio.get_running_loop()
        if running_loop is not self.owner_loop:
            raise RuntimeError("env work must run on the MobileGym owner loop")
        async with self._env_lock:
            result = fn(self.env)
            if asyncio.iscoroutine(result) or isinstance(result, Awaitable):
                return await result
            return result

    def log_action(
        self,
        tool_name: str,
        tool_input: dict[str, Any],
        mobilegym_action: dict[str, Any],
        duration_ms: int,
        error: str | None,
        episode_id: str | None = None,
        screenshot: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        active_episode_id = self.require_active(episode_id or self.active_episode_id)
        self._action_counter += 1
        entry = {
            "episode_id": active_episode_id,
            "action_id": f"{active_episode_id}:{self._action_counter:04d}",
            "tool_name": tool_name,
            "tool_input": dict(tool_input),
            "mobilegym_action": dict(mobilegym_action),
            "screenshot": screenshot,
            "duration_ms": int(duration_ms),
            "error": error,
        }
        self.action_log.append(entry)
        return entry
