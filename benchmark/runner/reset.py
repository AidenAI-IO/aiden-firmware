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


def per_task_setup(
    client: AgentClient,
    setup: dict[str, Any] | None,
    *,
    prompt_prefix: str = "",
    consolidation_expectation: Any = None,
) -> dict[str, Any] | None:
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
        return None
    if setup_type == "seed_memory":
        _per_task_setup_seed_memory(client, setup)
        return None
    if setup_type == "seed_episode":
        has_consolidation_expectation = (
            setup.get("consolidation_expectation") is not None
            or consolidation_expectation is not None
        )
        if has_consolidation_expectation and setup.get("consolidate", False) is not True:
            raise ResetError(
                "seed_episode consolidation_expectation requires consolidate=true"
            )
        return _per_task_setup_seed_episode(
            client,
            setup,
            consolidation_expectation=consolidation_expectation,
        )
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


def _per_task_setup_seed_episode(
    client: AgentClient,
    setup: dict[str, Any],
    *,
    consolidation_expectation: Any = None,
) -> dict[str, Any]:
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
        return {"type": "seed_episode", "episode_id": episode_id, "consolidated": False}
    try:
        result = client.process_episode_memory(episode_id, timeout=timeout)
    except AgentTimeoutError as e:
        raise ResetError(f"episode memory consolidation timed out for {episode_id!r}: {e}") from e
    except AgentRequestError as e:
        raise ResetError(f"episode memory consolidation failed for {episode_id!r}: {e}") from e
    expectation = setup.get("consolidation_expectation")
    if consolidation_expectation is not None:
        expectation = vars(consolidation_expectation)

    def fail(message: str) -> None:
        error = ResetError(message)
        error.consolidation = result
        raise error

    status = str(result.get("status") or "").strip().lower()
    expected_status = (expectation or {}).get("expected_status") if isinstance(expectation, dict) else None
    if expected_status is not None and status != expected_status:
        fail(
            f"episode memory consolidation for {episode_id!r} status mismatch: expected {expected_status!r}, got {status or 'missing'!r}"
        )
    if status == "ignored" and expected_status != "ignored":
        fail(
            f"episode memory consolidation for {episode_id!r} was ignored by the worker"
        )
    if status == "ignored":
        return {
            "type": "seed_episode",
            "episode_id": episode_id,
            "consolidated": True,
            "consolidation": result,
        }
    if status != "done":
        fail(
            f"episode memory consolidation for {episode_id!r} did not reach a terminal status: {status or 'missing'}"
        )
    _validate_consolidation_result(episode_id, result, expectation)
    _validate_consolidated_memory_content(client, episode_id, result, expectation, timeout)
    return {
        "type": "seed_episode",
        "episode_id": episode_id,
        "consolidated": True,
        "consolidation": result,
    }


def _validate_consolidation_result(
    episode_id: str,
    result: dict[str, Any],
    expectation: dict[str, Any] | None,
) -> None:
    def fail(message: str) -> None:
        error = ResetError(message)
        error.consolidation = result
        raise error

    memory_ids = result.get("memory_ids")
    if not isinstance(memory_ids, list) or not all(isinstance(item, str) and item.strip() for item in memory_ids):
        fail(
            f"episode memory consolidation for {episode_id!r} returned invalid memory_ids"
        )
    memory_count = len(memory_ids)
    if expectation is None:
        if not memory_count:
            fail(
                f"episode memory consolidation for {episode_id!r} produced no device memory"
            )
        return
    if not isinstance(expectation, dict):
        raise ResetError("seed_episode consolidation_expectation must be an object")
    expected_goal = expectation.get("goal_result")
    assessment = result.get("assessment")
    if expected_goal is not None:
        actual_goal = assessment.get("goal_result") if isinstance(assessment, dict) else None
        if actual_goal != expected_goal:
            fail(
                f"episode memory consolidation for {episode_id!r} goal_result mismatch: expected {expected_goal!r}, got {actual_goal or 'missing'!r}"
            )
    if expectation.get("required_assessment_evidence"):
        refs = assessment.get("evidence_refs") if isinstance(assessment, dict) else None
        if not isinstance(refs, list) or not refs:
            fail(
                f"episode memory consolidation for {episode_id!r} returned no assessment evidence_refs"
            )
    try:
        min_memory_ids = int(expectation.get("min_memory_ids", 0))
        max_memory_ids = expectation.get("max_memory_ids")
        max_memory_ids = None if max_memory_ids is None else int(max_memory_ids)
    except (TypeError, ValueError) as exc:
        raise ResetError("seed_episode consolidation_expectation has invalid memory bounds") from exc
    if memory_count < min_memory_ids:
        fail(
            f"episode memory consolidation for {episode_id!r} produced {memory_count} device memories, expected at least {min_memory_ids}"
        )
    if max_memory_ids is not None and memory_count > max_memory_ids:
        fail(
            f"episode memory consolidation for {episode_id!r} produced {memory_count} device memories, expected at most {max_memory_ids}"
        )
    has_positive_memory_contract = any(
        expectation.get(field)
        for field in (
            "required_memory_substrings",
            "required_memory_types",
            "required_memory_scope",
        )
    )
    if memory_count == 0 and (
        has_positive_memory_contract or not expectation.get("allow_empty_memory", False)
    ):
        fail(
            f"episode memory consolidation for {episode_id!r} produced no device memory"
        )


def _validate_consolidated_memory_content(
    client: AgentClient,
    episode_id: str,
    result: dict[str, Any],
    expectation: dict[str, Any] | None,
    timeout: int,
) -> None:
    if not isinstance(expectation, dict):
        return
    forbidden = expectation.get("forbidden_memory_substrings", [])
    required_substrings = expectation.get("required_memory_substrings", [])
    required_types = expectation.get("required_memory_types", [])
    required_scope = expectation.get("required_memory_scope", {})
    if not any((forbidden, required_substrings, required_types, required_scope)):
        return
    memory_ids = result.get("memory_ids")
    if not isinstance(memory_ids, list) or not memory_ids:
        if any((required_substrings, required_types, required_scope)):
            error = ResetError(
                f"episode memory consolidation for {episode_id!r} produced no device memory for the required content/type/scope contract"
            )
            error.consolidation = result
            raise error
        return

    def fail(message: str) -> None:
        error = ResetError(message)
        error.consolidation = result
        raise error

    generated_memories: list[dict[str, Any]] = []
    for memory_id in memory_ids:
        try:
            recalled = client.invoke_tool(
                "recall_device_memory",
                {"terms": [memory_id], "limit": 5},
                timeout=timeout,
            )
        except (AgentTimeoutError, AgentRequestError) as exc:
            fail(
                f"episode memory consolidation for {episode_id!r} could not inspect memory {memory_id!r}: {exc}"
            )
        if recalled.is_error:
            fail(
                f"episode memory consolidation for {episode_id!r} could not inspect memory {memory_id!r}"
            )
        try:
            payload = json.loads(recalled.output)
        except (TypeError, json.JSONDecodeError) as exc:
            fail(
                f"episode memory consolidation for {episode_id!r} returned invalid recall output for memory {memory_id!r}: {exc}"
            )
        matches = payload.get("results") if isinstance(payload, dict) else None
        exact = next(
            (item for item in matches or [] if isinstance(item, dict) and item.get("id") == memory_id),
            None,
        )
        if exact is None:
            fail(
                f"episode memory consolidation for {episode_id!r} could not recall generated memory {memory_id!r}"
            )
        generated_memories.append(exact)

    serialized = json.dumps(generated_memories, ensure_ascii=False).casefold()
    leaked = [value for value in forbidden if value.casefold() in serialized]
    if leaked:
        fail(
            f"episode memory consolidation for {episode_id!r} persisted forbidden value(s): {', '.join(leaked)}"
        )
    required_type_set = {value.strip().casefold() for value in required_types}

    def matches_required_memory(item: dict[str, Any]) -> bool:
        item_serialized = json.dumps(item, ensure_ascii=False).casefold()
        if required_type_set and str(item.get("type") or "").strip().casefold() not in required_type_set:
            return False
        if any(value.casefold() not in item_serialized for value in required_substrings):
            return False
        applicability = item.get("applicability")
        if required_scope and (
            not isinstance(applicability, dict)
            or any(str(applicability.get(key) or "") != value for key, value in required_scope.items())
        ):
            return False
        return True

    if any((required_substrings, required_types, required_scope)) and not any(
        matches_required_memory(item) for item in generated_memories
    ):
        fail(
            f"episode memory consolidation for {episode_id!r} produced no single memory matching required type/content/scope"
        )
