from __future__ import annotations
import json
import urllib.error
import urllib.request
from typing import Any
from runner.agent_client import AgentClient, AgentRequestError, AgentTimeoutError
from runner.environment_endpoint import EnvironmentEndpoint


class ResetError(RuntimeError):
    pass


STALE_ADB_OWNER_LEASE_STATES = {"expired", "abandoned"}


def _environment_endpoint(environment_url: str) -> EnvironmentEndpoint:
    try:
        return EnvironmentEndpoint(environment_url)
    except ValueError as exc:
        raise ResetError(str(exc)) from exc


def _environment_headers(task_id: str | None = None) -> dict[str, str]:
    headers = {"Content-Type": "application/json"}
    task_id = str(task_id or "").strip()
    if task_id:
        headers["benchmark-task-id"] = task_id
    return headers


def _post_environment(
    endpoint: str,
    *,
    timeout: int,
    headers: dict[str, str],
    action: str,
    payload: dict[str, Any] | None = None,
) -> dict[str, Any]:
    req = urllib.request.Request(
        endpoint,
        data=json.dumps(payload or {}, ensure_ascii=False).encode("utf-8"),
        headers=headers,
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
        raise ResetError(f"{action} endpoint failed HTTP {e.code}: {body[:200]!r}") from e
    except urllib.error.URLError as e:
        raise ResetError(f"{action} endpoint request failed: {e}") from e
    except TimeoutError as e:
        raise ResetError(f"{action} endpoint timed out: {e}") from e
    try:
        payload = json.loads(body.decode("utf-8")) if body else {}
    except (UnicodeDecodeError, json.JSONDecodeError) as e:
        raise ResetError(f"{action} endpoint returned invalid JSON: {body[:200]!r}") from e
    if isinstance(payload, dict) and payload.get("ok") is False:
        raise ResetError(f"{action} endpoint failed: {payload.get('error') or payload}")
    return payload if isinstance(payload, dict) else {}


def call_environment_setup(
    environment_url: str,
    timeout: int = 30,
    task_id: str | None = None,
    app_ids: list[str] | None = None,
) -> dict[str, Any]:
    return _post_environment(
        _environment_endpoint(environment_url).setup,
        timeout=timeout,
        headers=_environment_headers(task_id),
        action="setup",
        payload={"app_ids": list(app_ids or [])},
    )


def call_environment_release(environment_url: str, timeout: int = 30, task_id: str | None = None) -> dict[str, Any]:
    return _post_environment(
        _environment_endpoint(environment_url).release,
        timeout=timeout,
        headers=_environment_headers(task_id),
        action="release",
    )


def clear_stale_adb_android_owner(environment_url: str, timeout: float = 2.0) -> str:
    """Release a leftover ADB Android bridge owner before a fresh benchmark run."""
    req = urllib.request.Request(_environment_endpoint(environment_url).health, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read()
    except urllib.error.URLError:
        return ""
    except TimeoutError:
        return ""
    try:
        payload = json.loads(body.decode("utf-8")) if body else {}
    except (UnicodeDecodeError, json.JSONDecodeError):
        return ""
    if not isinstance(payload, dict):
        return ""
    data = payload.get("data") if isinstance(payload.get("data"), dict) else payload
    if not isinstance(data, dict):
        return ""
    if str(data.get("bridge_type") or "").strip().lower() != "adb_android":
        return ""
    active_task_id = str(data.get("active_task_id") or "").strip()
    if not active_task_id:
        return ""
    lease_state = str(data.get("active_task_lease_state") or "").strip().lower()
    if lease_state not in STALE_ADB_OWNER_LEASE_STATES:
        return ""
    try:
        call_environment_release(environment_url, timeout=max(1, int(timeout)), task_id=active_task_id)
    except ResetError:
        return ""
    return active_task_id


def per_task_setup(client: AgentClient, setup: dict[str, Any] | None, *, prompt_prefix: str = "") -> None:
    if setup is None:
        return
    setup_type = setup.get("type")
    if setup_type == "agent_prompt":
        _per_task_setup_agent_prompt(client, setup, prompt_prefix=prompt_prefix)
        return
    if setup_type == "seed_memory":
        _per_task_setup_seed_memory(client, setup)
        return
    raise ResetError(f"unsupported setup form: {setup!r}")


def _per_task_setup_agent_prompt(client: AgentClient, setup: dict[str, Any], *, prompt_prefix: str = "") -> None:
    prompt = setup.get("prompt")
    if not prompt:
        raise ResetError(f"agent_prompt setup missing prompt: {setup!r}")
    prefix = str(prompt_prefix or "").strip()
    if prefix:
        prompt = f"{prefix}\n\n{prompt}"
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
        try:
            client.clear_history()
        except AgentRequestError as e:
            raise ResetError(f"setup agent_prompt clear_history failed: {e}") from e


def _per_task_setup_seed_memory(client: AgentClient, setup: dict[str, Any]) -> None:
    memories = setup.get("memories")
    if not isinstance(memories, list) or not memories:
        raise ResetError(f"seed_memory setup requires non-empty 'memories' list: {setup!r}")
    try:
        timeout = int(setup.get("timeout_sec", 30))
    except (ValueError, TypeError) as e:
        raise ResetError(f"invalid timeout_sec: {setup.get('timeout_sec')!r}") from e
    for index, memory in enumerate(memories):
        if not isinstance(memory, dict):
            raise ResetError(f"seed_memory memories[{index}] must be a dict, got {type(memory).__name__}")
        memory_id = memory.get("id")
        if not isinstance(memory_id, str) or not memory_id.strip():
            raise ResetError(f"seed_memory memories[{index}] missing required 'id'")
        if not isinstance(memory.get("content"), str) or not memory["content"].strip():
            raise ResetError(f"seed_memory memories[{index}] (id={memory_id!r}) missing required 'content'")
        try:
            client.seed_memory(memory, timeout=timeout)
        except AgentTimeoutError as e:
            raise ResetError(f"seed_memory timed out at index {index} (id={memory_id!r}): {e}") from e
        except AgentRequestError as e:
            raise ResetError(f"seed_memory failed at index {index} (id={memory_id!r}): {e}") from e
    if setup.get("clear_history_after"):
        try:
            client.clear_history()
        except AgentRequestError as e:
            raise ResetError(f"seed_memory clear_history failed: {e}") from e
