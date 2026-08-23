from __future__ import annotations
import json
import urllib.error
import urllib.request
from typing import Any
from runner.agent_client import AgentClient, AgentRequestError, AgentTimeoutError
from runner.environment_endpoint import EnvironmentEndpoint
from runner.suite import SETUP_KEYS


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
    if not isinstance(setup_type, str):
        raise ResetError(f"setup type must be a string: {setup!r}")
    allowed_keys = SETUP_KEYS.get(setup_type)
    if allowed_keys is None:
        raise ResetError(f"unsupported setup form: {setup!r}")
    unknown_keys = sorted(set(setup) - allowed_keys)
    if unknown_keys:
        raise ResetError(f"unsupported {setup_type} setup keys: {', '.join(unknown_keys)}")
    if setup_type == "agent_prompt":
        _per_task_setup_agent_prompt(client, setup, prompt_prefix=prompt_prefix)
        return
    if setup_type == "seed_memory":
        _per_task_setup_seed_memory(client, setup)
        return
    if setup_type == "seed_episode":
        _per_task_setup_seed_episode(client, setup)
        return
    if setup_type == "seed_notification":
        _per_task_setup_seed_notification(client, setup)
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
        result = client.chat(prompt, timeout_sec=timeout)
    except AgentTimeoutError as e:
        raise ResetError(f"setup agent_prompt timed out: {e}") from e
    except AgentRequestError as e:
        raise ResetError(f"setup agent_prompt failed: {e}") from e
    expected_response = setup.get("expected_response")
    if expected_response is not None:
        if not isinstance(expected_response, str) or not expected_response:
            raise ResetError(f"expected_response must be a non-empty string: {expected_response!r}")
        actual_response = str(getattr(result, "response", "") or "").strip()
        if actual_response != expected_response:
            raise ResetError(
                f"setup agent_prompt response mismatch: expected {expected_response!r}, got {actual_response!r}"
            )
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


def _per_task_setup_seed_episode(client: AgentClient, setup: dict[str, Any]) -> None:
    episode = setup.get("episode")
    if not isinstance(episode, dict):
        raise ResetError(f"seed_episode setup requires an 'episode' object: {setup!r}")
    episode_id = episode.get("id")
    if not isinstance(episode_id, str) or not episode_id.strip():
        raise ResetError("seed_episode episode missing required 'id'")
    if not isinstance(episode.get("user_goal"), str) or not episode["user_goal"].strip():
        raise ResetError(f"seed_episode episode {episode_id!r} missing required 'user_goal'")
    consolidate = setup.get("consolidate", False)
    if not isinstance(consolidate, bool):
        raise ResetError(f"seed_episode consolidate must be boolean: {consolidate!r}")
    try:
        timeout = int(setup.get("timeout_sec", 90 if consolidate else 30))
    except (ValueError, TypeError) as e:
        raise ResetError(f"invalid timeout_sec: {setup.get('timeout_sec')!r}") from e
    try:
        client.seed_episode(episode, timeout=timeout)
    except AgentTimeoutError as e:
        raise ResetError(f"seed_episode timed out for {episode_id!r}: {e}") from e
    except AgentRequestError as e:
        raise ResetError(f"seed_episode failed for {episode_id!r}: {e}") from e
    if not consolidate:
        return
    try:
        result = client.process_episode_memory(episode_id, timeout=timeout)
    except AgentTimeoutError as e:
        raise ResetError(f"episode memory consolidation timed out for {episode_id!r}: {e}") from e
    except AgentRequestError as e:
        raise ResetError(f"episode memory consolidation failed for {episode_id!r}: {e}") from e
    status = str(result.get("status") or "").strip().lower()
    if status == "ignored":
        raise ResetError(
            f"episode memory consolidation for {episode_id!r} was ignored by the worker"
        )
    if status != "done":
        raise ResetError(
            f"episode memory consolidation for {episode_id!r} did not reach a terminal status: {status or 'missing'}"
        )
    memory_ids = result.get("memory_ids")
    if not isinstance(memory_ids, list) or not memory_ids:
        raise ResetError(f"episode memory consolidation for {episode_id!r} produced no device memory")


def _per_task_setup_seed_notification(client: AgentClient, setup: dict[str, Any]) -> None:
    events = setup.get("events")
    if not isinstance(events, list) or not events or len(events) > 100:
        raise ResetError("seed_notification setup requires 1 to 100 events")
    for index, event in enumerate(events):
        if not isinstance(event, dict):
            raise ResetError(f"seed_notification events[{index}] must be an object")
        if not str(event.get("title") or event.get("message") or "").strip():
            raise ResetError(f"seed_notification events[{index}] requires a title or message")
    consolidate = setup.get("consolidate", False)
    if not isinstance(consolidate, bool):
        raise ResetError(f"seed_notification consolidate must be boolean: {consolidate!r}")
    try:
        timeout = int(setup.get("timeout_sec", 90 if consolidate else 30))
    except (ValueError, TypeError) as e:
        raise ResetError(f"invalid timeout_sec: {setup.get('timeout_sec')!r}") from e
    try:
        seeded = client.seed_notification(events, timeout=timeout)
        if not isinstance(seeded, dict):
            raise ResetError("seed_notification returned an invalid response")
        context_ids = seeded.get("context_ids")
        if not isinstance(context_ids, list) or len(context_ids) != len(events):
            raise ResetError(
                "seed_notification did not persist every fixture event: "
                f"expected {len(events)} context ids, got {context_ids!r}"
            )
        if not consolidate:
            return
        result = client.process_notification_memory(timeout=timeout)
    except AgentTimeoutError as e:
        raise ResetError(f"notification benchmark setup timed out: {e}") from e
    except AgentRequestError as e:
        raise ResetError(f"notification benchmark setup failed: {e}") from e
    expected_cursor = str(context_ids[-1]).strip() if context_ids else ""
    cursor = str(result.get("memory_cursor") or "").strip()
    if expected_cursor and cursor != expected_cursor:
        raise ResetError(f"notification memory cursor mismatch: expected {expected_cursor!r}, got {cursor or 'missing'}")
    memory_ids = result.get("memory_ids")
    if not isinstance(memory_ids, list):
        raise ResetError("notification memory consolidation returned invalid memory_ids")
    expected_count = setup.get("expected_memory_count")
    if expected_count is not None:
        try:
            expected_count = int(expected_count)
        except (ValueError, TypeError) as e:
            raise ResetError(
                f"invalid expected_memory_count: {setup.get('expected_memory_count')!r}"
            ) from e
        if expected_count < 0 or len(memory_ids) != expected_count:
            raise ResetError(
                "notification memory count mismatch: "
                f"expected {expected_count}, got {len(memory_ids)}"
            )
    expected_scope = str(setup.get("expected_memory_scope") or "").strip().lower()
    if not expected_scope:
        return
    if expected_scope not in {"temporary", "long_term"}:
        raise ResetError(
            f"invalid expected_memory_scope: {setup.get('expected_memory_scope')!r}"
        )
    try:
        recalled = client.invoke_tool(
            "recall_memory",
            {"tags": ["notification"], "limit": 20},
            timeout=timeout,
        )
    except (AgentTimeoutError, AgentRequestError) as e:
        raise ResetError(f"notification memory scope check failed: {e}") from e
    if recalled.is_error:
        raise ResetError("notification memory scope check returned an error")
    try:
        payload = json.loads(recalled.output)
    except (TypeError, json.JSONDecodeError) as e:
        raise ResetError(f"notification memory scope check returned invalid JSON: {e}") from e
    matches = payload.get("results") if isinstance(payload, dict) else []
    scopes_by_id = {
        item.get("id"): str(item.get("memory_scope") or "").strip().lower()
        for item in matches or []
        if isinstance(item, dict) and item.get("id")
    }
    for memory_id in memory_ids:
        actual_scope = scopes_by_id.get(memory_id, "")
        if actual_scope != expected_scope:
            raise ResetError(
                f"notification memory {memory_id!r} scope mismatch: "
                f"expected {expected_scope!r}, got {actual_scope or 'missing'}"
            )
