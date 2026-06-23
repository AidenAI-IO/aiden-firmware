from __future__ import annotations
import json
import urllib.error
import urllib.parse
import urllib.request
from typing import Any
from runner.agent_client import AgentClient, AgentRequestError, AgentTimeoutError


class ResetError(RuntimeError):
    pass


def environment_setup_endpoint(environment_url: str) -> str:
    raw = str(environment_url or "").strip()
    if not raw:
        raise ResetError("environment_url is required")
    parsed = urllib.parse.urlparse(raw)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ResetError(f"invalid environment_url: {environment_url!r}")
    path = parsed.path.rstrip("/")
    if path in {"", "/"}:
        path = "/api/setup"
    elif path != "/api/setup":
        path = f"{path}/api/setup"
    return urllib.parse.urlunparse(parsed._replace(path=path, params="", query="", fragment=""))


def environment_release_endpoint(environment_url: str) -> str:
    raw = str(environment_url or "").strip()
    if not raw:
        raise ResetError("environment_url is required")
    parsed = urllib.parse.urlparse(raw)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ResetError(f"invalid environment_url: {environment_url!r}")
    path = parsed.path.rstrip("/")
    if path in {"", "/"}:
        path = "/api/release"
    elif path == "/api/setup":
        path = "/api/release"
    elif path != "/api/release":
        path = f"{path}/api/release"
    return urllib.parse.urlunparse(parsed._replace(path=path, params="", query="", fragment=""))


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


def per_task_setup(client: AgentClient, setup: dict[str, Any] | None) -> None:
    if setup is None:
        return
    if setup.get("type") != "agent_prompt":
        raise ResetError(f"unsupported setup form: {setup!r}")
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
        try:
            client.clear_history()
        except AgentRequestError as e:
            raise ResetError(f"setup agent_prompt clear_history failed: {e}") from e
