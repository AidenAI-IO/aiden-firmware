from __future__ import annotations

import asyncio
import json
import socket
import threading
import time
import urllib.parse
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from .actions import action_to_dict, build_action
from .episode import (
    BridgeEpisodeState,
    BridgeTaskRouter,
    MissingBenchmarkTaskIDError,
    NoBridgeEnvAvailableError,
    StaleEpisodeError,
    benchmark_task_id_from_headers,
)
from .protocol import bridge_error, bridge_ok, encode_screenshot
from .tools_api import ToolsAPIHandler


ACTION_ENDPOINTS = {"tap", "swipe", "drag", "type_text", "key", "back", "home", "wait"}


class BridgeServer:
    def __init__(
        self,
        state: BridgeEpisodeState | BridgeTaskRouter,
        host: str = "127.0.0.1",
        port: int = 0,
        public_host: str | None = None,
        request_timeout_sec: float = 30,
    ):
        self.router = BridgeTaskRouter.from_state(state)
        self.state = self.router.default_state
        self.host = host
        self.port = port
        self.public_host = public_host
        self.request_timeout_sec = request_timeout_sec
        self._httpd: ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None
        self.base_url = ""
        self.tools_api = ToolsAPIHandler(self.router, request_timeout_sec)

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

    def submit_to_state(self, state: BridgeEpisodeState, coro: Any) -> Any:
        future = asyncio.run_coroutine_threadsafe(coro, state.owner_loop)
        return future.result(timeout=self.request_timeout_sec)


def _handler_for(bridge: BridgeServer):
    class BridgeRequestHandler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            path = urllib.parse.urlparse(self.path).path
            # Handle /api/tools catalog
            if path == "/api/tools":
                bridge.tools_api.handle_request(self, path)
                return
            if path in {"/screen", "/screen/"}:
                self._handle_screen_page()
                return
            if path == "/screen/snapshot":
                try:
                    self._handle_screen_snapshot()
                except MissingBenchmarkTaskIDError as exc:
                    self._send_error(400, "missing_benchmark_task_id", str(exc))
                except NoBridgeEnvAvailableError as exc:
                    self._send_error(429, "no_bridge_env_available", str(exc))
                return
            if path != "/health":
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
                elif path in {"api/release", "release"}:
                    self._handle_release()
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
            except MissingBenchmarkTaskIDError as exc:
                self._send_error(400, "missing_benchmark_task_id", str(exc))
            except NoBridgeEnvAvailableError as exc:
                self._send_error(429, "no_bridge_env_available", str(exc))
            except ValueError as exc:
                self._send_error(400, "bad_request", str(exc))
            except TimeoutError:
                self._send_error(504, "timeout", "bridge request timed out")
            except Exception as exc:
                self._send_error(500, "bridge_error", str(exc))

        def log_message(self, format: str, *args: Any) -> None:
            return

        def _handle_episode_start(self, payload: dict[str, Any]) -> None:
            state = self._request_state()
            result = bridge.submit_to_state(state, state.start_episode(str(payload.get("episode_id", ""))))
            self._send_json(200, bridge_ok(result))

        def _handle_episode_end(self, payload: dict[str, Any]) -> None:
            state = self._request_state()
            result = bridge.submit_to_state(state, state.end_episode(str(payload.get("episode_id", ""))))
            self._send_json(200, bridge_ok(result))

        def _handle_reset(self, payload: dict[str, Any]) -> None:
            state = self._request_state()
            episode_id = str(payload.get("episode_id") or "").strip()
            if not episode_id:
                episode_id = f"reset-{uuid.uuid4().hex}"
            result = bridge.submit_to_state(state, state.reset_episode(episode_id))
            self._send_json(200, bridge_ok(result))

        def _handle_release(self) -> None:
            task_id = benchmark_task_id_from_headers(self.headers)
            released = bridge.router.release_task_id(task_id)
            self._send_json(200, bridge_ok({"released": released}))

        def _handle_state(self, payload: dict[str, Any]) -> None:
            state = self._request_state()
            result = bridge.submit_to_state(state, state.run_env(lambda env: env.get_state(**payload)))
            self._send_json(200, bridge_ok(result))

        def _handle_route(self) -> None:
            state = self._request_state()
            result = bridge.submit_to_state(state, state.run_env(lambda env: env.get_route()))
            self._send_json(200, bridge_ok(result))

        def _handle_screenshot(self, payload: dict[str, Any]) -> None:
            state = self._request_state()
            episode_id = str(payload.get("episode_id", ""))
            state.require_active(episode_id)

            async def get_screenshot(env: Any) -> dict[str, Any]:
                state.require_active(str(payload.get("episode_id", "")))
                observation = await _maybe_await(env.get_observation())
                return _encode_observation_screenshot(observation)

            result = bridge.submit_to_state(state, state.run_env(get_screenshot))
            self._send_json(200, result)

        def _handle_screen_page(self) -> None:
            self._send_text(200, "text/html; charset=utf-8", SCREEN_HTML)

        def _handle_screen_snapshot(self) -> None:
            state = self._request_state()
            if not state.active_episode_id:
                self._send_json(200, bridge_ok(_screen_snapshot_payload()))
                return

            async def get_snapshot(env: Any) -> dict[str, Any]:
                episode_id = state.active_episode_id
                if not episode_id:
                    return _screen_snapshot_payload()
                observation = await _maybe_await(env.get_observation())
                return _screen_snapshot_payload(
                    active_episode_id=episode_id,
                    screenshot=_encode_observation_screenshot(observation),
                    action_log=state.action_log,
                )

            result = bridge.submit_to_state(state, state.run_env(get_snapshot))
            self._send_json(200, bridge_ok(result))

        def _handle_action(self, name: str, payload: dict[str, Any]) -> None:
            state = self._request_state()
            episode_id = str(payload.get("episode_id", ""))
            state.require_active(episode_id)
            tool_input = {key: value for key, value in payload.items() if key != "episode_id"}
            action = build_action(name, tool_input)
            action_payload = action_to_dict(action)

            async def step_env(env: Any) -> dict[str, Any]:
                state.require_active(episode_id)
                started = time.monotonic()
                step_result = await _maybe_await(env.step(action))
                duration_ms = int((time.monotonic() - started) * 1000)
                observation = _observation_value(step_result, "observation")
                if observation is None:
                    observation = await _maybe_await(env.get_observation())
                screenshot = _encode_observation_screenshot(observation)
                log_entry = state.log_action(
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

            result = bridge.submit_to_state(state, state.run_env(step_env))
            self._send_json(200, result)

        def _request_state(self) -> BridgeEpisodeState:
            return bridge.router.state_for_headers(self.headers)

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
            self._send_bytes(status, "application/json", data)

        def _send_text(self, status: int, content_type: str, text: str) -> None:
            self._send_bytes(status, content_type, text.encode("utf-8"))

        def _send_bytes(self, status: int, content_type: str, data: bytes) -> None:
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    return BridgeRequestHandler


SCREEN_HTML = """<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>MobileGym Screen</title>
  <style>
    :root { color-scheme: light dark; font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    body { margin: 0; background: #111; color: #f6f7f9; }
    header { display: flex; align-items: center; gap: 16px; padding: 12px 16px; background: #1b1d22; border-bottom: 1px solid #333842; }
    header strong { font-size: 14px; }
    header span { color: #b8c0cc; font-size: 13px; }
    main { display: grid; grid-template-columns: minmax(0, 1fr) 280px; min-height: calc(100vh - 46px); }
    .screen { display: grid; place-items: center; padding: 16px; overflow: auto; }
    img { max-width: min(100%, 420px); width: auto; height: auto; background: #000; box-shadow: 0 12px 40px rgba(0, 0, 0, .45); }
    .placeholder { color: #9aa3af; border: 1px dashed #46515f; border-radius: 8px; padding: 24px; }
    aside { border-left: 1px solid #333842; background: #17191f; padding: 14px; overflow: auto; }
    h2 { margin: 0 0 10px; font-size: 13px; color: #dfe4ea; }
    dl { display: grid; grid-template-columns: 88px 1fr; gap: 6px 10px; margin: 0 0 18px; font-size: 12px; }
    dt { color: #8d98a7; }
    dd { margin: 0; color: #f3f5f8; overflow-wrap: anywhere; }
    .action { border-top: 1px solid #2c313a; padding: 8px 0; font-size: 12px; }
    .action strong { display: block; color: #f3f5f8; }
    .action span { color: #98a2b3; overflow-wrap: anywhere; }
    .error { color: #ffb4ab; }
    @media (max-width: 760px) {
      main { grid-template-columns: 1fr; }
      aside { border-left: 0; border-top: 1px solid #333842; }
    }
  </style>
</head>
<body>
  <header>
    <strong>MobileGym Screen</strong>
    <span id="status">connecting</span>
    <span id="updated"></span>
  </header>
  <main>
    <section class="screen">
      <img id="shot" alt="Current MobileGym screenshot" hidden>
      <div id="placeholder" class="placeholder">Waiting for an active benchmark episode.</div>
    </section>
    <aside>
      <h2>Task State</h2>
      <dl>
        <dt>Status</dt><dd id="stateStatus">unknown</dd>
        <dt>Episode</dt><dd id="episode">none</dd>
        <dt>Actions</dt><dd id="actionCount">0</dd>
        <dt>Size</dt><dd id="size">none</dd>
      </dl>
      <h2>Recent Actions</h2>
      <div id="actions"></div>
    </aside>
  </main>
  <script>
    const statusEl = document.getElementById('status');
    const updatedEl = document.getElementById('updated');
    const stateStatusEl = document.getElementById('stateStatus');
    const episodeEl = document.getElementById('episode');
    const actionCountEl = document.getElementById('actionCount');
    const sizeEl = document.getElementById('size');
    const shotEl = document.getElementById('shot');
    const placeholderEl = document.getElementById('placeholder');
    const actionsEl = document.getElementById('actions');

    function renderActions(actions) {
      actionsEl.replaceChildren();
      if (!actions.length) {
        const empty = document.createElement('div');
        empty.className = 'action';
        empty.textContent = 'No actions yet.';
        actionsEl.appendChild(empty);
        return;
      }
      for (const action of actions) {
        const row = document.createElement('div');
        row.className = 'action';
        const title = document.createElement('strong');
        title.textContent = `${action.action_id || ''} ${action.tool_name || ''}`.trim();
        const detail = document.createElement('span');
        detail.textContent = JSON.stringify(action.tool_input || {});
        row.append(title, detail);
        actionsEl.appendChild(row);
      }
    }

    async function refresh() {
      try {
        const res = await fetch('/screen/snapshot', {cache: 'no-store'});
        const body = await res.json();
        if (!res.ok || !body.ok) throw new Error(body.error?.message || res.statusText);
        const data = body.data || {};
        statusEl.textContent = data.status || 'unknown';
        statusEl.className = '';
        stateStatusEl.textContent = data.status || 'unknown';
        episodeEl.textContent = data.active_episode_id || 'none';
        actionCountEl.textContent = String(data.action_count || 0);
        updatedEl.textContent = new Date().toLocaleTimeString();
        const shot = data.screenshot;
        if (shot && shot.data) {
          const format = shot.format === 'jpeg' ? 'jpeg' : 'png';
          shotEl.src = `data:image/${format};base64,${shot.data}`;
          shotEl.hidden = false;
          placeholderEl.hidden = true;
          sizeEl.textContent = `${shot.width || '?'} x ${shot.height || '?'}`;
        } else {
          shotEl.hidden = true;
          placeholderEl.hidden = false;
          placeholderEl.textContent = 'Waiting for an active benchmark episode.';
          sizeEl.textContent = 'none';
        }
        renderActions(data.actions || []);
      } catch (err) {
        statusEl.textContent = String(err.message || err);
        statusEl.className = 'error';
      }
    }

    refresh();
    setInterval(refresh, 1000);
  </script>
</body>
</html>
"""


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
        "episode_id": entry.get("episode_id"),
        "action_id": entry.get("action_id"),
        "tool_name": entry.get("tool_name"),
        "tool_input": entry.get("tool_input") or {},
        "duration_ms": entry.get("duration_ms"),
        "error": entry.get("error"),
        "has_screenshot": bool(entry.get("screenshot")),
    }


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
