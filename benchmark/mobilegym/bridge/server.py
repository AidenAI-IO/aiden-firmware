from __future__ import annotations

import asyncio
import json
import socket
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from .actions import action_to_dict, build_action
from .episode import BridgeEpisodeState, StaleEpisodeError
from .protocol import bridge_error, bridge_ok, encode_screenshot
from .tools_api import ToolsAPIHandler


ACTION_ENDPOINTS = {"tap", "swipe", "drag", "type_text", "key", "back", "home", "wait"}


class BridgeServer:
    def __init__(
        self,
        state: BridgeEpisodeState,
        host: str = "127.0.0.1",
        port: int = 0,
        public_host: str | None = None,
        request_timeout_sec: float = 30,
    ):
        self.state = state
        self.host = host
        self.port = port
        self.public_host = public_host
        self.request_timeout_sec = request_timeout_sec
        self._httpd: ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None
        self.base_url = ""
        self.tools_api = ToolsAPIHandler(state, request_timeout_sec)

    def start(self) -> str:
        if self._httpd is not None:
            return self.base_url
        handler = _handler_for(self)
        self._httpd = ThreadingHTTPServer((self.host, self.port), handler)
        self._httpd.daemon_threads = True
        host, port = self._httpd.server_address[:2]
        self.base_url = f"http://{self._public_host(host)}:{port}"
        self._thread = threading.Thread(target=self._httpd.serve_forever, name="mobilegym-bridge-http", daemon=True)
        self._thread.start()
        return self.base_url

    def _public_host(self, bound_host: str) -> str:
        if self.public_host:
            return self.public_host
        if bound_host in {"", "0.0.0.0", "::"}:
            return _get_container_ip() or socket.gethostname()
        return bound_host

    def stop(self) -> None:
        if self._httpd is None:
            return
        self._httpd.shutdown()
        self._httpd.server_close()
        if self._thread is not None:
            self._thread.join(timeout=2)
        self._httpd = None
        self._thread = None

    def submit(self, coro: Any) -> Any:
        future = asyncio.run_coroutine_threadsafe(coro, self.state.owner_loop)
        return future.result(timeout=self.request_timeout_sec)


def _handler_for(bridge: BridgeServer):
    class BridgeRequestHandler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            # Handle /api/tools catalog
            if self.path == "/api/tools":
                bridge.tools_api.handle_request(self, self.path)
                return
            if self.path != "/health":
                self._send_error(404, "not_found", "unknown endpoint")
                return
            self._send_json(
                200,
                bridge_ok({"status": "ok", "active_episode_id": bridge.state.active_episode_id}),
            )

        def do_POST(self) -> None:
            # Handle /api/tools/{tool_name} invocation
            if self.path.startswith("/api/tools/"):
                bridge.tools_api.handle_request(self, self.path)
                return

            payload = self._read_json()
            if payload is None:
                return
            path = self.path.strip("/")
            try:
                if path == "episode/start":
                    self._handle_episode_start(payload)
                elif path == "episode/end":
                    self._handle_episode_end(payload)
                elif path in {"api/reset", "reset"}:
                    self._handle_reset(payload)
                elif path == "state":
                    self._handle_state(payload)
                elif path == "route":
                    self._handle_route()
                elif path == "screenshot":
                    self._handle_screenshot(payload)
                elif path in ACTION_ENDPOINTS:
                    self._handle_action(path, payload)
                else:
                    self._send_error(404, "not_found", "unknown endpoint")
            except StaleEpisodeError:
                self._send_error(409, "stale_episode", "stale episode_id")
            except ValueError as exc:
                self._send_error(400, "bad_request", str(exc))
            except TimeoutError:
                self._send_error(504, "timeout", "bridge request timed out")
            except Exception as exc:
                self._send_error(500, "bridge_error", str(exc))

        def log_message(self, format: str, *args: Any) -> None:
            return

        def _handle_episode_start(self, payload: dict[str, Any]) -> None:
            result = bridge.submit(bridge.state.start_episode(str(payload.get("episode_id", ""))))
            self._send_json(200, bridge_ok(result))

        def _handle_episode_end(self, payload: dict[str, Any]) -> None:
            result = bridge.submit(bridge.state.end_episode(str(payload.get("episode_id", ""))))
            self._send_json(200, bridge_ok(result))

        def _handle_reset(self, payload: dict[str, Any]) -> None:
            episode_id = str(payload.get("episode_id") or "").strip()
            if not episode_id:
                episode_id = f"reset-{uuid.uuid4().hex}"
            result = bridge.submit(bridge.state.reset_episode(episode_id))
            self._send_json(200, bridge_ok(result))

        def _handle_state(self, payload: dict[str, Any]) -> None:
            result = bridge.submit(bridge.state.run_env(lambda env: env.get_state(**payload)))
            self._send_json(200, bridge_ok(result))

        def _handle_route(self) -> None:
            result = bridge.submit(bridge.state.run_env(lambda env: env.get_route()))
            self._send_json(200, bridge_ok(result))

        def _handle_screenshot(self, payload: dict[str, Any]) -> None:
            episode_id = str(payload.get("episode_id", ""))
            bridge.state.require_active(episode_id)

            async def get_screenshot(env: Any) -> dict[str, Any]:
                bridge.state.require_active(str(payload.get("episode_id", "")))
                observation = await _maybe_await(env.get_observation())
                return _encode_observation_screenshot(observation)

            result = bridge.submit(bridge.state.run_env(get_screenshot))
            self._send_json(200, result)

        def _handle_action(self, name: str, payload: dict[str, Any]) -> None:
            episode_id = str(payload.get("episode_id", ""))
            bridge.state.require_active(episode_id)
            tool_input = {key: value for key, value in payload.items() if key != "episode_id"}
            action = build_action(name, tool_input)
            action_payload = action_to_dict(action)

            async def step_env(env: Any) -> dict[str, Any]:
                bridge.state.require_active(episode_id)
                started = time.monotonic()
                step_result = await _maybe_await(env.step(action))
                duration_ms = int((time.monotonic() - started) * 1000)
                observation = _observation_value(step_result, "observation")
                if observation is None:
                    observation = await _maybe_await(env.get_observation())
                screenshot = _encode_observation_screenshot(observation)
                log_entry = bridge.state.log_action(
                    tool_name=name,
                    tool_input=tool_input,
                    mobilegym_action=action_payload,
                    duration_ms=duration_ms,
                    error=None,
                    episode_id=episode_id,
                    screenshot=screenshot,
                )
                return {
                    "ok": True,
                    "message": "ok",
                    "action_id": log_entry["action_id"],
                    "screenshot": screenshot,
                }

            result = bridge.submit(bridge.state.run_env(step_env))
            self._send_json(200, result)

        def _read_json(self) -> dict[str, Any] | None:
            try:
                length = int(self.headers.get("Content-Length", "0") or "0")
            except ValueError:
                self._send_error(400, "bad_header", "invalid Content-Length")
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
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    return BridgeRequestHandler


async def _maybe_await(value: Any) -> Any:
    if asyncio.iscoroutine(value):
        return await value
    return value


def _encode_observation_screenshot(observation: Any) -> dict[str, Any]:
    payload = _observation_value(observation, "screenshot")
    if payload is None:
        payload = _observation_value(observation, "screenshot_bytes")
    if isinstance(payload, str):
        payload = payload.encode("utf-8")
    if not isinstance(payload, bytes):
        raise ValueError("observation does not contain screenshot bytes")
    mime_type = _observation_value(observation, "mime_type") or _infer_screenshot_mime_type(payload)
    width = _observation_int(observation, "width") or _observation_int(observation, "screenshot_width")
    height = _observation_int(observation, "height") or _observation_int(observation, "screenshot_height")
    return encode_screenshot(payload, mime_type=str(mime_type), width=width, height=height)


def _infer_screenshot_mime_type(payload: bytes) -> str:
    if payload.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    if payload.startswith(b"\xff\xd8"):
        return "image/jpeg"
    return "application/octet-stream"


def _observation_int(observation: Any, name: str) -> int | None:
    value = _observation_value(observation, name)
    if value is None:
        return None
    return int(value)


def _observation_value(observation: Any, name: str) -> Any:
    if isinstance(observation, dict):
        value = observation.get(name)
    else:
        value = getattr(observation, name, None)
    if callable(value):
        return value()
    return value


def _get_container_ip() -> str | None:
    """Return the container's outbound IP on the docker bridge network.

    Parallel workers run in isolated docker-compose projects with `bind=0.0.0.0`.
    `socket.gethostname()` returns the random container ID, which other services
    on the user-defined network may not always resolve. The UDP-connect trick
    forces the OS to pick the interface that would carry outbound traffic and
    surfaces its address. No packets are sent — `connect` on a SOCK_DGRAM only
    binds the local address.
    """
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.connect(("8.8.8.8", 80))
        ip = s.getsockname()[0]
        s.close()
        return ip
    except OSError:
        return None
