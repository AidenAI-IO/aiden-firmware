from __future__ import annotations

import time
from typing import TYPE_CHECKING

from runner.agent_client import AgentClient, AgentRequestError, AgentTimeoutError
from runner.reset import ResetError, call_environment_reset, global_reset, per_task_setup

if TYPE_CHECKING:
    from runner.suite import Suite, TaskSpec


def recover_agent_after_timeout(
    client: AgentClient,
    timeout_sec: int = 90,
    poll_sec: float = 3.0,
) -> bool:
    return client.recover_after_timeout(timeout_sec=timeout_sec, poll_sec=poll_sec)


def wait_for_agent_ready(
    client: AgentClient,
    timeout_sec: int = 120,
    poll_sec: int = 3,
) -> bool:
    deadline = time.monotonic() + max(0, timeout_sec)
    while True:
        if client.health():
            return True
        now = time.monotonic()
        if now >= deadline:
            return False
        time.sleep(min(max(0, poll_sec), max(0, deadline - now)))


def prepare_task_isolation(
    client: AgentClient,
    suite: Suite,
    task: TaskSpec,
    *,
    environment_url: str | None = None,
    ready_timeout_sec: int = 120,
    setup_attempts: int = 3,
) -> None:
    if not wait_for_agent_ready(client, timeout_sec=ready_timeout_sec):
        raise ResetError(f"agent not ready within {ready_timeout_sec}s")
    if not recover_agent_after_timeout(client, timeout_sec=min(30, ready_timeout_sec)):
        raise ResetError("agent did not recover before task isolation")

    last_error: Exception | None = None
    for attempt in range(1, setup_attempts + 1):
        try:
            client.clear_history()
            if not task.input_screenshot:
                if environment_url:
                    call_environment_reset(environment_url)
                elif suite.global_reset.get("tool_sequence"):
                    global_reset(client, suite.global_reset)
                per_task_setup(client, task.setup)
            return
        except (ResetError, AgentTimeoutError, AgentRequestError) as e:
            last_error = e
            if attempt >= setup_attempts:
                break
            per_attempt_timeout = max(15, ready_timeout_sec // setup_attempts)
            wait_for_agent_ready(client, timeout_sec=per_attempt_timeout)
            time.sleep(1)

    if last_error is None:
        raise ResetError("task isolation failed for unknown reason")
    raise last_error
