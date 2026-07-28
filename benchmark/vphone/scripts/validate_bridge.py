#!/usr/bin/env python3
"""Validate a live VPhone bridge through the same endpoint used by the runner."""

from __future__ import annotations

import argparse
import base64
import io
import json
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

from PIL import Image


class BridgeValidationError(RuntimeError):
    pass


def _validate_jpeg(payload: bytes, expected_width: Any, expected_height: Any) -> None:
    try:
        with Image.open(io.BytesIO(payload)) as image:
            image.load()
            if image.format != "JPEG":
                raise BridgeValidationError(f"expected JPEG, got {image.format}")
            if image.mode != "RGB":
                raise BridgeValidationError(f"expected a color RGB screenshot, got mode {image.mode}")
            if image.size != (int(expected_width), int(expected_height)):
                raise BridgeValidationError(
                    f"JPEG dimensions {image.size} do not match metadata "
                    f"{(expected_width, expected_height)}"
                )
    except BridgeValidationError:
        raise
    except Exception as exc:
        raise BridgeValidationError(f"cannot decode bridge JPEG: {exc}") from exc


def _request(
    endpoint: str,
    path: str,
    *,
    method: str = "GET",
    task_id: str = "",
    payload: dict[str, Any] | None = None,
) -> tuple[int, dict[str, Any]]:
    headers = {"Accept": "application/json"}
    data = None
    if task_id:
        headers["benchmark-task-id"] = task_id
    if method == "POST":
        headers["Content-Type"] = "application/json"
        data = json.dumps(payload or {}).encode("utf-8")
    request = urllib.request.Request(
        endpoint.rstrip("/") + path,
        data=data,
        headers=headers,
        method=method,
    )
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            return response.status, json.loads(response.read())
    except urllib.error.HTTPError as exc:
        return exc.code, json.loads(exc.read())


def validate_bridge(
    endpoint: str,
    *,
    task_id: str = "vphone-ios-cli",
    screenshot_path: Path | None = None,
) -> dict[str, Any]:
    checks: list[str] = []
    acquired = False
    try:
        status, health = _request(endpoint, "/health")
        if status != 200 or health.get("ok") is not True:
            raise BridgeValidationError(f"health failed: HTTP {status}: {health}")
        health_data = health.get("data") or {}
        if health_data.get("bridge_type") != "vphone_ios":
            raise BridgeValidationError(f"unexpected bridge type: {health_data}")
        if not health_data.get("screen_width") or not health_data.get("screen_height"):
            raise BridgeValidationError(f"health omitted native screen dimensions: {health_data}")
        if health_data.get("vphoned_connected") is False:
            raise BridgeValidationError("vphoned is not connected")
        checks.append("health")

        status, concurrent = _request(endpoint, "/api/concurrent")
        if status != 200 or (concurrent.get("data") or {}).get("concurrent") != 1:
            raise BridgeValidationError(f"concurrency contract failed: HTTP {status}: {concurrent}")
        checks.append("concurrent=1")

        status, screen = _request(endpoint, "/api/screen", task_id=task_id)
        screenshot = (screen.get("data") or {}).get("screenshot") or {}
        image = base64.b64decode(screenshot.get("data") or "", validate=True)
        if status != 200:
            raise BridgeValidationError(f"screen is not a JPEG: HTTP {status}")
        _validate_jpeg(image, screenshot.get("width"), screenshot.get("height"))
        if not screenshot.get("source_width") or not screenshot.get("source_height"):
            raise BridgeValidationError(f"screen omitted native dimensions: {screenshot}")
        if screenshot_path is not None:
            screenshot_path.parent.mkdir(parents=True, exist_ok=True)
            screenshot_path.write_bytes(image)
        checks.append("screen-jpeg")

        status, setup = _request(endpoint, "/api/setup", method="POST", task_id=task_id)
        if status != 200 or (setup.get("data") or {}).get("episode_id") != task_id:
            raise BridgeValidationError(f"setup failed: HTTP {status}: {setup}")
        acquired = True
        checks.append("setup-home")

        status, conflict = _request(
            endpoint,
            "/api/setup",
            method="POST",
            task_id=task_id + "-conflict",
        )
        if status != 429 or (conflict.get("error") or {}).get("code") != "no_bridge_env_available":
            raise BridgeValidationError(f"ownership conflict contract failed: HTTP {status}: {conflict}")
        checks.append("ownership-429")

        status, catalog = _request(endpoint, "/api/tools")
        names = {item.get("name") for item in catalog.get("tools") or []}
        required = {"screenshot", "touch_gesture", "quick_action"}
        if status != 200 or not required.issubset(names):
            raise BridgeValidationError(f"tool catalog is incomplete: HTTP {status}: {sorted(names)}")
        checks.append("tool-catalog")

        status, tool = _request(
            endpoint,
            "/api/tools/screenshot",
            method="POST",
            task_id=task_id,
            payload={"input": "{}"},
        )
        if status != 200 or tool.get("is_error") is not False:
            raise BridgeValidationError(f"screenshot tool failed: HTTP {status}: {tool}")
        tool_screen = json.loads(tool.get("output") or "{}")
        tool_image = base64.b64decode(tool_screen.get("data") or "", validate=True)
        _validate_jpeg(tool_image, tool_screen.get("width"), tool_screen.get("height"))
        checks.append("screenshot-tool")
    finally:
        if acquired:
            status, release = _request(
                endpoint,
                "/api/release",
                method="POST",
                task_id=task_id,
            )
            if status == 200 and (release.get("data") or {}).get("released") is True:
                checks.append("release")
            elif sys.exc_info()[0] is None:
                raise BridgeValidationError(f"release failed: HTTP {status}: {release}")
            else:
                # A validation failure is already propagating. main() only prints
                # str(exc), so raising here would replace the root cause the
                # operator needs; report the cleanup failure separately instead.
                print(
                    f"warning: release failed while another error was propagating: HTTP {status}: {release}",
                    file=sys.stderr,
                )

    return {
        "ok": True,
        "endpoint": endpoint.rstrip("/"),
        "task_id": task_id,
        "checks": checks,
        "screen_width": health_data["screen_width"],
        "screen_height": health_data["screen_height"],
        "capabilities": health_data.get("capabilities") or [],
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="vphone.scripts.validate_bridge")
    parser.add_argument("--endpoint", default="http://127.0.0.1:8899")
    parser.add_argument("--benchmark-task-id", default="vphone-ios-cli")
    parser.add_argument("--screenshot-out", default="")
    args = parser.parse_args(argv)
    screenshot_path = Path(args.screenshot_out).expanduser() if args.screenshot_out else None
    try:
        result = validate_bridge(
            args.endpoint,
            task_id=args.benchmark_task_id,
            screenshot_path=screenshot_path,
        )
    except (BridgeValidationError, OSError, ValueError, urllib.error.URLError) as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False))
        return 1
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
