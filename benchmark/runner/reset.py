from __future__ import annotations
import json
import re
import urllib.error
import urllib.parse
import urllib.request
from typing import Any
from runner.agent_client import AgentClient, AgentRequestError, AgentTimeoutError


class ResetError(RuntimeError):
    pass


STALE_ADB_OWNER_LEASE_STATES = {"expired", "abandoned"}


def _environment_api_endpoint(environment_url: str, endpoint: str) -> str:
    raw = str(environment_url or "").strip()
    if not raw:
        raise ResetError("environment_url is required")
    parsed = urllib.parse.urlparse(raw)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ResetError(f"invalid environment_url: {environment_url!r}")
    path = parsed.path.rstrip("/")
    if path in {"", "/"}:
        path = f"/api/{endpoint}"
    else:
        for suffix in ("/api/setup", "/api/release", "/api/screen"):
            if path == suffix or path.endswith(suffix):
                path = f"{path[:-len(suffix)]}/api/{endpoint}"
                break
        else:
            path = f"{path}/api/{endpoint}"
    return urllib.parse.urlunparse(parsed._replace(path=path, params="", query="", fragment=""))


def environment_setup_endpoint(environment_url: str) -> str:
    return _environment_api_endpoint(environment_url, "setup")


def environment_release_endpoint(environment_url: str) -> str:
    return _environment_api_endpoint(environment_url, "release")


def environment_health_endpoint(environment_url: str) -> str:
    raw = str(environment_url or "").strip()
    if not raw:
        raise ResetError("environment_url is required")
    parsed = urllib.parse.urlparse(raw)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ResetError(f"invalid environment_url: {environment_url!r}")
    return urllib.parse.urlunparse(parsed._replace(path="/health", params="", query="", fragment=""))


def _environment_headers(task_id: str | None = None) -> dict[str, str]:
    headers = {"Content-Type": "application/json"}
    task_id = str(task_id or "").strip()
    if task_id:
        headers["benchmark-task-id"] = task_id
    return headers


def _post_environment(endpoint: str, *, timeout: int, headers: dict[str, str], action: str) -> dict[str, Any]:
    req = urllib.request.Request(
        endpoint,
        data=b"{}",
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


def call_environment_setup(environment_url: str, timeout: int = 30, task_id: str | None = None) -> dict[str, Any]:
    return _post_environment(
        environment_setup_endpoint(environment_url),
        timeout=timeout,
        headers=_environment_headers(task_id),
        action="setup",
    )


def call_environment_release(environment_url: str, timeout: int = 30, task_id: str | None = None) -> dict[str, Any]:
    return _post_environment(
        environment_release_endpoint(environment_url),
        timeout=timeout,
        headers=_environment_headers(task_id),
        action="release",
    )


def clear_stale_adb_android_owner(environment_url: str, timeout: float = 2.0) -> str:
    """Release a leftover ADB Android bridge owner before a fresh benchmark run."""
    req = urllib.request.Request(environment_health_endpoint(environment_url), method="GET")
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
    benchmark_task_id: str | None = None,
) -> None:
    if setup is None:
        return
    setup_type = setup.get("type")
    if setup_type == "agent_prompt":
        _per_task_setup_agent_prompt(
            client,
            setup,
            prompt_prefix=prompt_prefix,
            benchmark_task_id=benchmark_task_id,
        )
        return
    if setup_type == "seed_memory":
        _per_task_setup_seed_memory(client, setup)
        return
    raise ResetError(f"unsupported setup form: {setup!r}")


def _per_task_setup_agent_prompt(
    client: AgentClient,
    setup: dict[str, Any],
    *,
    prompt_prefix: str = "",
    benchmark_task_id: str | None = None,
) -> None:
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
        chat_kwargs = {"timeout_sec": timeout}
        if str(benchmark_task_id or "").strip():
            chat_kwargs["benchmark_task_id"] = str(benchmark_task_id).strip()
        chat = client.chat(prompt, **chat_kwargs)
    except AgentTimeoutError as e:
        raise ResetError(f"setup agent_prompt timed out: {e}") from e
    except AgentRequestError as e:
        raise ResetError(f"setup agent_prompt failed: {e}") from e
    if _setup_response_reports_blocker(chat.response, chat.history):
        raise ResetError("setup agent_prompt did not establish a usable state: agent reported an operation blocker")
    clear_history_after = setup.get("clear_history_after", True)
    if not isinstance(clear_history_after, bool):
        raise ResetError(f"clear_history_after must be boolean: {clear_history_after!r}")
    if clear_history_after:
        try:
            client.clear_history()
        except AgentRequestError as e:
            raise ResetError(f"setup agent_prompt clear_history failed: {e}") from e


_SETUP_BLOCKER_PATTERNS = (
    re.compile(r"\bstop reason\s*:\s*(?:no_progress|budget_exceeded)\b", re.IGNORECASE),
    re.compile(r"\bapp_connected\s*:\s*false\b", re.IGNORECASE),
    re.compile(r"\bcurrent environment\b.{0,80}\btools?\b.{0,40}\bunavailable\b", re.IGNORECASE | re.DOTALL),
    re.compile(r"无法(?:直接)?(?:获取截图|执行触控|操作(?:手机|设备|界面))"),
    re.compile(r"(?:手机控制|截图|触控).{0,30}(?:工具|功能).{0,20}(?:不可用|有限|缺失)"),
    re.compile(r"please (?:confirm|connect|open).{0,80}(?:device|phone|page)", re.IGNORECASE | re.DOTALL),
)


def _setup_response_reports_blocker(response: str, history: list[dict[str, Any]]) -> bool:
    text = str(response or "").strip()
    if any(pattern.search(text) for pattern in _SETUP_BLOCKER_PATTERNS):
        return True
    for message in history or []:
        if message.get("type") != "tool_call":
            continue
        if str(message.get("tool_name") or "").strip() == "request_human_handoff":
            return True
    return False


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
