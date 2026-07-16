"""HTTP server for the ADB Android environment bridge.

Exposes the same environment-bridge surface as the MobileGym bridge server
(health/concurrent/screen/setup/release plus /api/tools), backed by a single
adb-controlled Android device.
"""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from .adb import ADBAndroidDevice, ADBCommandError
from .protocol import bridge_error, bridge_ok, encode_screenshot
from .state import (
    ADBBridgeState,
    NoBridgeEnvAvailableError,
    benchmark_task_id_from_headers,
)
from .tools_api import ADBToolsAPIHandler


DEFAULT_BRIDGE_REQUEST_TIMEOUT_SEC = 120
MAX_REQUEST_BODY_BYTES = 10 * 1024 * 1024  # 10MB


class ADBBridgeServer:
    def __init__(
        self,
        device: ADBAndroidDevice,
        host: str = "127.0.0.1",
        port: int = 0,
        request_timeout_sec: float = DEFAULT_BRIDGE_REQUEST_TIMEOUT_SEC,
        action_settle_sec: float | None = None,
    ):
        self.state = ADBBridgeState(device=device)
        self.host = host
        self.port = port
        self.request_timeout_sec = request_timeout_sec
        self._httpd: ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None
        self.base_url = ""
        tools_kwargs = {} if action_settle_sec is None else {"action_settle_sec": action_settle_sec}
        self.tools_api = ADBToolsAPIHandler(self.state, request_timeout_sec, **tools_kwargs)

    def start(self) -> str:
        if self._httpd is not None:
            return self.base_url
        handler = _handler_for(self)
        self._httpd = ThreadingHTTPServer((self.host, self.port), handler)
        self._httpd.daemon_threads = True
        host, port = self._httpd.server_address[:2]
        self.base_url = f"http://{host}:{port}"
        self._thread = threading.Thread(
            target=self._httpd.serve_forever, name="adb-android-bridge-http", daemon=True
        )
        self._thread.start()
        return self.base_url

    def stop(self) -> None:
        if self._httpd is None:
            return
        self._httpd.shutdown()
        self._httpd.server_close()
        if self._thread is not None:
            self._thread.join(timeout=2)
        self._httpd = None
        self._thread = None


def _handler_for(bridge: ADBBridgeServer):
    class ADBBridgeRequestHandler(BaseHTTPRequestHandler):
        # Socket read timeout: without this a stalled client (e.g. a partial
        # request body) would pin its connection thread forever. Timeouts are
        # caught by BaseHTTPRequestHandler.handle_one_request, which logs and
        # closes the connection.
        timeout = bridge.request_timeout_sec

        def do_GET(self) -> None:
            path = self.path.split("?", 1)[0]
            if path == "/api/tools":
                bridge.tools_api.handle_request(self, path)
                return
            if path == "/api/concurrent":
                self._send_json(200, bridge_ok(_concurrent_payload(bridge)))
                return
            if path == "/api/screen":
                try:
                    self._handle_api_screen()
                except ADBCommandError as exc:
                    self._send_error(500, "adb_error", str(exc))
                return
            if path != "/health":
                self._send_error(404, "not_found", "unknown endpoint")
                return
            self._handle_health()

        def do_POST(self) -> None:
            request_path = self.path.split("?", 1)[0]
            if request_path.startswith("/api/tools/"):
                bridge.tools_api.handle_request(self, request_path)
                return

            payload = self._read_json()
            if payload is None:
                return
            path = request_path.strip("/")
            try:
                if path == "api/setup":
                    self._handle_setup(payload)
                elif path in {"api/release", "release"}:
                    self._handle_release()
                else:
                    self._send_error(404, "not_found", "unknown endpoint")
            except NoBridgeEnvAvailableError as exc:
                self._send_error(429, "no_bridge_env_available", str(exc))
            except ADBCommandError as exc:
                self._send_error(500, "adb_error", str(exc))
            except ValueError as exc:
                self._send_error(400, "bad_request", str(exc))
            except Exception as exc:
                self._send_error(500, "bridge_error", str(exc))

        def log_message(self, format: str, *args: Any) -> None:
            return

        def _handle_health(self) -> None:
            try:
                device_info = bridge.state.device.check_device()
            except ADBCommandError as exc:
                self._send_error(503, "device_unavailable", str(exc))
                return
            self._send_json(200, bridge_ok(_health_payload(bridge, device_info)))

        def _handle_setup(self, payload: dict[str, Any]) -> None:
            task_id = benchmark_task_id_from_headers(self.headers)
            episode_id, newly_acquired = bridge.state.acquire(task_id)
            try:
                bridge.state.device.check_device()
                bridge.state.device.reset_home()
            except Exception:
                # Roll back ownership taken by THIS call so a failed setup does
                # not leave the device 429-locked for every other task. Keep
                # ownership held before this call (idempotent re-setup): a
                # transient adb error must not let another task steal the
                # device from a task that is still running.
                if newly_acquired:
                    bridge.state.release(task_id)
                raise
            self._send_json(
                200,
                bridge_ok({"episode_id": episode_id, "reset": True, "task_id": task_id}),
            )

        def _handle_release(self) -> None:
            task_id = benchmark_task_id_from_headers(self.headers)
            released = bridge.state.release(task_id)
            self._send_json(200, bridge_ok({"released": released}))

        def _handle_api_screen(self) -> None:
            # Screen capture works with or without an active episode / task id
            # so the runner can grab pre/post screenshots at any point.
            with bridge.state.lock:
                jpeg, width, height = bridge.state.device.screenshot_jpeg()
                action_log = list(bridge.state.action_log)
                active_episode_id = bridge.state.active_episode_id
            screenshot = encode_screenshot(jpeg, "image/jpeg", width, height)
            self._send_json(
                200,
                bridge_ok(
                    _screen_snapshot_payload(
                        active_episode_id=active_episode_id or None,
                        screenshot=screenshot,
                        action_log=action_log,
                    )
                ),
            )

        def _read_json(self) -> dict[str, Any] | None:
            try:
                length = int(self.headers.get("Content-Length", "0") or "0")
            except ValueError:
                self._send_error(400, "bad_header", "invalid Content-Length")
                return None
            if length < 0:
                self._send_error(400, "bad_header", "Content-Length must be non-negative")
                return None
            if length > MAX_REQUEST_BODY_BYTES:
                self._send_error(413, "request_too_large", f"request body exceeds {MAX_REQUEST_BODY_BYTES} bytes")
                return None
            raw = self.rfile.read(length) if length else b"{}"
            try:
                payload = json.loads(raw.decode("utf-8"))
            except json.JSONDecodeError:
                self._send_error(400, "bad_json", "invalid JSON body")
                return None
            if not isinstance(payload, dict):
                self._send_error(400, "bad_json", "JSON body must be an object")
                return None
            return payload

        def _send_error(self, status: int, code: str, message: str) -> None:
            self._send_json(status, bridge_error(code, message, status=status))

        def _send_json(self, status: int, payload: dict[str, Any]) -> None:
            data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    return ADBBridgeRequestHandler


def _screen_snapshot_payload(
    *,
    active_episode_id: str | None = None,
    screenshot: dict[str, Any] | None = None,
    action_log: list[dict[str, Any]] | None = None,
) -> dict[str, Any]:
    actions = [_screen_action_payload(entry) for entry in (action_log or [])[-10:]]
    return {
        "status": "running" if active_episode_id else "waiting",
        "active_episode_id": active_episode_id,
        "screenshot": screenshot,
        "action_count": len(action_log or []),
        "actions": actions,
    }


def _screen_action_payload(entry: dict[str, Any]) -> dict[str, Any]:
    return {
        "action_id": entry.get("action_id"),
        "ts": entry.get("ts"),
        "tool": entry.get("tool"),
        "adb": entry.get("adb"),
        "duration_ms": entry.get("duration_ms"),
    }


def _health_payload(bridge: ADBBridgeServer, device_info: dict[str, Any]) -> dict[str, Any]:
    return {
        "status": "ok",
        "bridge_type": "adb_android",
        "serial": device_info.get("serial"),
        "device_state": device_info.get("state"),
        "env_count": 1,
        "concurrent": 1,
        "active_episode_id": bridge.state.active_episode_id,
        "active_task_id": bridge.state.active_task_id,
        "interfaces": ["/api/tools", "/api/screen", "/api/setup", "/api/release", "/api/concurrent"],
    }


def _concurrent_payload(bridge: ADBBridgeServer) -> dict[str, Any]:
    return {
        "bridge_type": "adb_android",
        "concurrent": 1,
        "env_count": 1,
        "active_task_id": bridge.state.active_task_id,
    }
