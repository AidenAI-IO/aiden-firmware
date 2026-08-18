"""HTTP Environment Bridge server for a single VPhone iOS VM."""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from mnk_provider import execute_mnk_request

from .client import VPhoneSocketError
from .protocol import bridge_error, bridge_ok, encode_provider_frame
from .state import NoBridgeEnvAvailableError, VPhoneBridgeState, benchmark_task_id_from_headers
from .tools_api import MAX_REQUEST_BODY_BYTES, VPhoneToolsAPIHandler


# Matches the agent screenshot provider HTTP timeout (30s).
DEFAULT_BRIDGE_REQUEST_TIMEOUT_SEC = 30


class VPhoneBridgeServer:
    def __init__(
        self,
        device: Any,
        host: str = "127.0.0.1",
        port: int = 0,
        request_timeout_sec: float = DEFAULT_BRIDGE_REQUEST_TIMEOUT_SEC,
        action_settle_sec: float | None = None,
    ):
        self.state = VPhoneBridgeState(device=device)
        self.host = host
        self.port = int(port)
        self.request_timeout_sec = max(0.1, float(request_timeout_sec))
        tools_kwargs = {} if action_settle_sec is None else {"action_settle_sec": action_settle_sec}
        self.tools_api = VPhoneToolsAPIHandler(self.state, self.request_timeout_sec, **tools_kwargs)
        self._httpd: ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None
        self.base_url = ""

    def start(self) -> str:
        if self._httpd is not None:
            return self.base_url
        self._httpd = ThreadingHTTPServer((self.host, self.port), _handler_for(self))
        self._httpd.daemon_threads = True
        host, port = self._httpd.server_address[:2]
        self.base_url = f"http://{host}:{port}"
        self._thread = threading.Thread(
            target=self._httpd.serve_forever,
            name="vphone-ios-bridge-http",
            daemon=True,
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


def _handler_for(bridge: VPhoneBridgeServer):
    class VPhoneBridgeRequestHandler(BaseHTTPRequestHandler):
        timeout = bridge.request_timeout_sec

        def do_GET(self) -> None:
            path = self.path.split("?", 1)[0]
            if path == "/api/tools":
                bridge.tools_api.handle_request(self, path)
                return
            if path == "/api/concurrent":
                self._send_json(200, bridge_ok(_concurrent_payload(bridge)))
                return
            if path == "/health":
                self._handle_health()
                return
            self._send_error(404, "not_found", "unknown endpoint")

        def do_POST(self) -> None:
            path = self.path.split("?", 1)[0]
            if path.startswith("/api/tools/"):
                bridge.tools_api.handle_request(self, path)
                return
            payload = self._read_json()
            if payload is None:
                return
            try:
                if path == "/api/setup":
                    self._handle_setup()
                elif path == "/api/providers/screenshot":
                    self._handle_provider_screenshot()
                elif path == "/api/providers/mnk":
                    self._handle_provider_mnk(payload)
                elif path in {"/api/release", "/release"}:
                    self._handle_release()
                else:
                    self._send_error(404, "not_found", "unknown endpoint")
            except NoBridgeEnvAvailableError as exc:
                self._send_error(429, "no_bridge_env_available", str(exc))
            except VPhoneSocketError as exc:
                self._send_vphone_error(exc)
            except ValueError as exc:
                self._send_error(400, "bad_request", str(exc))
            except Exception as exc:
                self._send_error(500, "bridge_error", str(exc))

        def log_message(self, format: str, *args: Any) -> None:
            return

        def _handle_health(self) -> None:
            try:
                status = bridge.state.device.check_device()
            except VPhoneSocketError as exc:
                self._send_error(503, exc.code, str(exc))
                return
            self._send_json(200, bridge_ok(_health_payload(bridge, status)))

        def _handle_setup(self) -> None:
            task_id = benchmark_task_id_from_headers(self.headers)
            episode_id, newly_acquired = bridge.state.acquire(task_id)
            try:
                bridge.state.device.check_device()
                bridge.state.device.reset_home()
            except Exception:
                if newly_acquired:
                    bridge.state.release(task_id)
                raise
            self._send_json(
                200,
                bridge_ok({"episode_id": episode_id, "reset": True, "task_id": task_id}),
            )

        def _handle_release(self) -> None:
            task_id = benchmark_task_id_from_headers(self.headers)
            self._send_json(200, bridge_ok({"released": bridge.state.release(task_id)}))

        def _handle_provider_screenshot(self) -> None:
            task_id = benchmark_task_id_from_headers(self.headers)
            bridge.state.check_task_access(task_id)
            with bridge.state.lock:
                payload, width, height, source_width, source_height = bridge.state.device.screenshot_jpeg()
                seq = int(getattr(bridge.state, "_screenshot_seq", 0)) + 1
                bridge.state._screenshot_seq = seq
            self._send_json(
                200,
                bridge_ok(
                    encode_provider_frame(
                        payload,
                        width=width,
                        height=height,
                        backend="vphone",
                        seq=seq,
                        source_width=source_width,
                        source_height=source_height,
                    )
                ),
            )

        def _handle_provider_mnk(self, payload: dict[str, Any]) -> None:
            task_id = benchmark_task_id_from_headers(self.headers)
            bridge.state.check_task_access(task_id)
            if not bridge.state.active_episode_id:
                self._send_json(409, {"error": "no active episode; call /api/setup first"})
                return
            status, response = execute_mnk_request(payload, bridge.tools_api._submit_tool_call)
            self._send_json(status, response)

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
                # Deliberately do not read the oversized body; close instead so the
                # unread bytes cannot be parsed as the next request under keep-alive.
                self._send_error(
                    413, "request_too_large",
                    f"request body exceeds {MAX_REQUEST_BODY_BYTES} bytes",
                    close=True,
                )
                return None
            raw = self.rfile.read(length) if length else b"{}"
            try:
                payload = json.loads(raw.decode("utf-8"))
            except (UnicodeDecodeError, json.JSONDecodeError):
                self._send_error(400, "bad_json", "invalid JSON body")
                return None
            if not isinstance(payload, dict):
                self._send_error(400, "bad_json", "JSON body must be an object")
                return None
            return payload

        def _send_vphone_error(self, exc: VPhoneSocketError) -> None:
            status = 503 if exc.code in {
                "socket_not_found", "socket_refused", "socket_timeout", "socket_io",
                "display_unavailable", "guest_unavailable", "guest_ssh_failed",
            } else 500
            self._send_error(status, exc.code, str(exc))

        def _send_error(self, status: int, code: str, message: str, *, close: bool = False) -> None:
            self._send_json(status, bridge_error(code, message, status=status), close=close)

        def _send_json(self, status: int, payload: dict[str, Any], *, close: bool = False) -> None:
            data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(data)))
            if close:
                self.close_connection = True
                self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(data)

    return VPhoneBridgeRequestHandler


def _health_payload(bridge: VPhoneBridgeServer, status: dict[str, Any]) -> dict[str, Any]:
    return {
        "status": "ok",
        "bridge_type": "vphone_ios",
        "platform": "ios",
        "env_count": 1,
        "concurrent": 1,
        "screen_width": status.get("screen_width"),
        "screen_height": status.get("screen_height"),
        "display_ready": status.get("display_ready"),
        "vphoned_connected": status.get("vphoned_connected"),
        "capabilities": status.get("capabilities") or [],
        "legacy_host_control": bool(status.get("legacy_host_control")),
        "active_episode_id": bridge.state.active_episode_id,
        "active_task_id": bridge.state.active_task_id,
        "interfaces": ["/api/tools", "/api/providers/screenshot", "/api/providers/mnk", "/api/setup", "/api/release", "/api/concurrent"],
    }


def _concurrent_payload(bridge: VPhoneBridgeServer) -> dict[str, Any]:
    return {
        "bridge_type": "vphone_ios",
        "concurrent": 1,
        "env_count": 1,
        "active_task_id": bridge.state.active_task_id,
    }
