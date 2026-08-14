from __future__ import annotations
import base64
import binascii
import json
from pathlib import Path
from typing import Any
import urllib.error
import urllib.request

from runner.environment_endpoint import EnvironmentEndpoint


class CaptureError(RuntimeError):
    pass


def take_environment_screenshot(
    environment_url: str,
    out_path: Path,
    benchmark_task_id: str | None = None,
    timeout: int = 30,
) -> tuple[int, int]:
    """Read the environment bridge screen API and write screenshot bytes to out_path."""
    try:
        endpoint = EnvironmentEndpoint(environment_url).screen
    except ValueError as exc:
        raise CaptureError(str(exc)) from exc
    headers: dict[str, str] = {}
    task_id = str(benchmark_task_id or "").strip()
    if task_id:
        headers["benchmark-task-id"] = task_id
    req = urllib.request.Request(endpoint, headers=headers, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read()
    except urllib.error.HTTPError as e:
        try:
            body = e.read()
        except Exception:
            body = b""
        raise CaptureError(
            f"screen request failed HTTP {e.code}: {body[:200]!r}"
        ) from e
    except urllib.error.URLError as e:
        raise CaptureError(f"screen request failed: {e}") from e
    except TimeoutError as e:
        raise CaptureError(f"screen request timed out: {e}") from e

    try:
        payload = json.loads(body.decode("utf-8")) if body else {}
    except (UnicodeDecodeError, json.JSONDecodeError) as e:
        raise CaptureError(
            f"screen request returned invalid JSON: {body[:200]!r}"
        ) from e
    screenshot = _extract_screenshot_payload(payload)
    return _write_screenshot_payload(screenshot, out_path, context="screen")


def _extract_screenshot_payload(payload: Any) -> dict[str, Any]:
    if not isinstance(payload, dict):
        raise CaptureError(f"screen returned unexpected payload: {payload!r}")
    if payload.get("ok") is False:
        raise CaptureError(f"screen failed: {payload.get('error') or payload}")
    data = payload.get("data")
    if isinstance(data, dict) and "screenshot" in data:
        screenshot = data.get("screenshot")
        if not screenshot:
            status = data.get("status") or "no active screen"
            raise CaptureError(f"screen returned no screenshot: {status}")
        if not isinstance(screenshot, dict):
            raise CaptureError(
                f"screen returned invalid screenshot: {screenshot!r}"
            )
        return screenshot
    if "screenshot" in payload:
        screenshot = payload.get("screenshot")
        if isinstance(screenshot, dict):
            return screenshot
    if "data" in payload and "width" in payload and "height" in payload:
        return payload
    raise CaptureError(f"screen returned no screenshot field: {payload!r}")


def _write_screenshot_payload(
    payload: dict[str, Any],
    out_path: Path,
    *,
    context: str,
) -> tuple[int, int]:
    data = payload.get("data")
    if not data:
        raise CaptureError(f"{context} returned no data field")
    out_path.parent.mkdir(parents=True, exist_ok=True)
    try:
        out_path.write_bytes(base64.b64decode(data))
    except (binascii.Error, ValueError) as e:
        raise CaptureError(f"invalid base64 screenshot data: {e}") from e
    return int(payload.get("width", 0)), int(payload.get("height", 0))
