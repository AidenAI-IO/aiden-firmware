from __future__ import annotations

import json
import urllib.error
import urllib.request
from typing import Any

from runner.environment_endpoint import EnvironmentEndpoint


class EnvironmentStateError(RuntimeError):
    pass


DEFAULT_ENVIRONMENT_STATE_TIMEOUT_SEC = 30


def read_environment_state(
    environment_url: str,
    benchmark_task_id: str | None = None,
    timeout: int = DEFAULT_ENVIRONMENT_STATE_TIMEOUT_SEC,
) -> dict[str, Any]:
    endpoints = EnvironmentEndpoint(environment_url)
    state = _post_json(endpoints.state, benchmark_task_id, timeout)
    route = _post_json(endpoints.route, benchmark_task_id, timeout)
    return {**state, "route": route}


def _post_json(
    endpoint: str,
    benchmark_task_id: str | None,
    timeout: int,
) -> dict[str, Any]:
    headers = {"Content-Type": "application/json"}
    task_id = str(benchmark_task_id or "").strip()
    if task_id:
        headers["benchmark-task-id"] = task_id
    request = urllib.request.Request(
        endpoint,
        data=b"{}",
        headers=headers,
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read()
    except urllib.error.HTTPError as exc:
        try:
            raw = exc.read()
        except Exception:
            raw = b""
        raise EnvironmentStateError(
            f"state request failed HTTP {exc.code}: {raw[:200]!r}"
        ) from exc
    except urllib.error.URLError as exc:
        raise EnvironmentStateError(f"state request failed: {exc}") from exc
    except TimeoutError as exc:
        raise EnvironmentStateError(f"state request timed out: {exc}") from exc

    try:
        payload = json.loads(raw.decode("utf-8")) if raw else {}
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise EnvironmentStateError(
            f"state request returned invalid JSON: {raw[:200]!r}"
        ) from exc
    if not isinstance(payload, dict):
        raise EnvironmentStateError(f"state returned unexpected payload: {payload!r}")
    if payload.get("ok") is False:
        raise EnvironmentStateError(f"state request failed: {payload.get('error') or payload}")
    state = payload.get("data")
    if not isinstance(state, dict):
        raise EnvironmentStateError(f"state response has no object data: {payload!r}")
    return state
