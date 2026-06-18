from __future__ import annotations
import json
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any
from runner.agent_client import AgentClient, AgentRequestError, AgentTimeoutError


class ResetError(RuntimeError):
    pass


def environment_reset_endpoint(environment_url: str) -> str:
    raw = str(environment_url or "").strip()
    if not raw:
        raise ResetError("environment_url is required")
    parsed = urllib.parse.urlparse(raw)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ResetError(f"invalid environment_url: {environment_url!r}")
    path = parsed.path.rstrip("/")
    if path in {"", "/"}:
        path = "/api/reset"
    elif path not in {"/api/reset", "/reset"}:
        path = f"{path}/api/reset"
    return urllib.parse.urlunparse(parsed._replace(path=path, params="", query="", fragment=""))


def call_environment_reset(environment_url: str, timeout: int = 30) -> dict[str, Any]:
    url = environment_reset_endpoint(environment_url)
    req = urllib.request.Request(
        url,
        data=b"{}",
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read()
    except urllib.error.HTTPError as e:
        try:
            body = e.read()
        except Exception:
            body = b""
        raise ResetError(f"reset endpoint failed HTTP {e.code}: {body[:200]!r}") from e
    except urllib.error.URLError as e:
        raise ResetError(f"reset endpoint request failed: {e}") from e
    except TimeoutError as e:
        raise ResetError(f"reset endpoint timed out: {e}") from e
    try:
        payload = json.loads(body.decode("utf-8")) if body else {}
    except (UnicodeDecodeError, json.JSONDecodeError) as e:
        raise ResetError(f"reset endpoint returned invalid JSON: {body[:200]!r}") from e
    if isinstance(payload, dict) and payload.get("ok") is False:
        raise ResetError(f"reset endpoint failed: {payload.get('error') or payload}")
    return payload if isinstance(payload, dict) else {}


def run_tool_sequence(client: AgentClient, sequence: list[dict[str, Any]]) -> None:
    for step in sequence:
        tool = step.get("tool")
        args = step.get("args") or {}
        if tool == "wait_ms":
            try:
                ms = int(args.get("ms", 0))
            except (ValueError, TypeError) as e:
                raise ResetError(f"invalid wait_ms value: {args.get('ms')!r}") from e
            time.sleep(ms / 1000.0)
            continue
        if not tool:
            raise ResetError(f"reset step missing 'tool': {step!r}")
        try:
            result = client.invoke_tool(tool, args)
        except AgentTimeoutError as e:
            raise ResetError(f"tool {tool} timed out: {e}") from e
        except AgentRequestError as e:
            raise ResetError(f"tool {tool} failed: {e}") from e
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
        try:
            timeout = int(setup.get("timeout_sec", 90))
        except (ValueError, TypeError) as e:
            raise ResetError(f"invalid timeout_sec: {setup.get('timeout_sec')!r}") from e
        try:
            client.chat(prompt, timeout_sec=timeout)
        except AgentTimeoutError as e:
            raise ResetError(f"setup agent_prompt timed out: {e}") from e
        except AgentRequestError as e:
            raise ResetError(f"setup agent_prompt failed: {e}") from e
        clear_history_after = setup.get("clear_history_after", True)
        if not isinstance(clear_history_after, bool):
            raise ResetError(f"clear_history_after must be boolean: {clear_history_after!r}")
        if clear_history_after:
            # Clear the setup conversation so it does not pollute the actual task chat.
            try:
                client.clear_history()
            except AgentRequestError as e:
                raise ResetError(f"setup agent_prompt clear_history failed: {e}") from e
        return
    raise ResetError(f"unsupported setup form: {setup!r}")
