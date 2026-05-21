from __future__ import annotations
import time
from typing import Any
from runner.agent_client import AgentClient, AgentTimeoutError


class ResetError(RuntimeError):
    pass


def run_tool_sequence(client: AgentClient, sequence: list[dict[str, Any]]) -> None:
    for step in sequence:
        tool = step.get("tool")
        args = step.get("args") or {}
        if tool == "wait_ms":
            time.sleep(int(args.get("ms", 0)) / 1000.0)
            continue
        if not tool:
            raise ResetError(f"reset step missing 'tool': {step!r}")
        result = client.invoke_tool(tool, args)
        if result.is_error:
            raise ResetError(f"tool {tool} failed: {result.output}")


def global_reset(client: AgentClient, suite_global_reset: dict[str, Any]) -> None:
    seq = suite_global_reset.get("tool_sequence") or []
    run_tool_sequence(client, seq)


def per_task_setup(client: AgentClient, setup: dict[str, Any] | None) -> None:
    if setup is None:
        return
    seq = setup.get("tool_sequence")
    if seq:
        run_tool_sequence(client, seq)
        return
    if setup.get("type") == "agent_prompt":
        prompt = setup.get("prompt")
        if not prompt:
            raise ResetError(f"agent_prompt setup missing prompt: {setup!r}")
        timeout = int(setup.get("timeout_sec", 90))
        try:
            client.chat(prompt, timeout_sec=timeout)
        except AgentTimeoutError as e:
            raise ResetError(f"setup agent_prompt timed out: {e}") from e
        # Clear the setup conversation so it does not pollute the actual task chat.
        client.clear_history()
        return
    raise ResetError(f"unsupported setup form: {setup!r}")
