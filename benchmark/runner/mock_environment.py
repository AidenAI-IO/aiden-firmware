from __future__ import annotations

import base64
import io
import json
import threading
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

from PIL import Image, ImageDraw

from runner.matching import dict_contains
from runner.suite import MockEnvironmentSpec, MockToolResponseSpec


class MockEnvironmentServer:
    """Scripted environment bridge used by deterministic agent policy suites."""

    def __init__(
        self,
        spec: MockEnvironmentSpec,
        suite_dir: Path,
        host: str = "127.0.0.1",
        port: int = 0,
    ):
        self.spec = spec
        self.suite_dir = Path(suite_dir)
        self.host = host
        self.port = port
        self.base_url = ""
        self.screen_text = spec.screen_text or "Mock phone environment ready."
        self.calls: list[dict[str, Any]] = []
        self._lock = threading.Lock()
        self._httpd: ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None

    def start(self) -> str:
        if self._httpd is not None:
            return self.base_url
        self._httpd = ThreadingHTTPServer((self.host, self.port), _handler_for(self))
        self._httpd.daemon_threads = True
        bound_host, bound_port = self._httpd.server_address[:2]
        self.base_url = f"http://{bound_host}:{bound_port}"
        self._thread = threading.Thread(
            target=self._httpd.serve_forever,
            name="benchmark-mock-environment",
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

    def activate(self, spec: MockEnvironmentSpec) -> None:
        """Switch the scripted fixture before starting an isolated task worker."""
        with self._lock:
            self.spec = spec
            self.calls.clear()
            self.screen_text = spec.screen_text or "Mock phone environment ready."

    def reset(self) -> None:
        with self._lock:
            self.calls.clear()
            self.screen_text = self.spec.screen_text or "Mock phone environment ready."

    def invoke(self, tool_name: str, tool_input: dict[str, Any]) -> MockToolResponseSpec:
        with self._lock:
            self.calls.append({"tool": tool_name, "input": tool_input})
            if tool_name == "screenshot":
                return MockToolResponseSpec(output=self.screenshot_payload())
            for response in self.spec.tools.get(tool_name, []):
                screen_matches = (
                    not response.screen_contains
                    or response.screen_contains in self.screen_text
                )
                if screen_matches and dict_contains(
                    tool_input, response.input_contains
                ):
                    if response.screen_text:
                        self.screen_text = response.screen_text
                    return response
            if self.spec.default_tool_response is not None:
                response = self.spec.default_tool_response
                if response.screen_text:
                    self.screen_text = response.screen_text
                return response
        return MockToolResponseSpec(
            output={"ok": False, "error": f"no mock response configured for {tool_name}"},
            is_error=True,
            error=f"no mock response configured for {tool_name}",
        )

    def screenshot_payload(self) -> dict[str, Any]:
        raw = self._screen_bytes()
        return {
            "data": base64.b64encode(raw).decode("ascii"),
            "width": 1170,
            "height": 2532,
            "format": "jpeg",
            "description": self.screen_text,
        }

    def snapshot(self) -> dict[str, Any]:
        with self._lock:
            calls = list(self.calls)
            screen_text = self.screen_text
        return {
            "phone_bridge": dict(self.spec.phone_bridge),
            "screen_text": screen_text,
            "calls": calls,
        }

    def _screen_bytes(self) -> bytes:
        if self.spec.screen:
            screen_path = self.suite_dir / self.spec.screen
            return screen_path.read_bytes()
        image = Image.new("RGB", (1170, 2532), "#f5f5f7")
        draw = ImageDraw.Draw(image)
        draw.rounded_rectangle(
            (45, 55, 1125, 2470),
            radius=55,
            fill="white",
            outline="#c7c7cc",
            width=4,
        )
        draw.text((95, 105), "Aiden Benchmark Mock Phone", fill="#111111")
        y = 180
        for line in _wrap_text(self.screen_text, width=72):
            draw.text((95, y), line, fill="#222222")
            y += 38
        output = io.BytesIO()
        image.save(output, format="JPEG", quality=88)
        return output.getvalue()


def _handler_for(server: MockEnvironmentServer):
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            path = urllib.parse.urlparse(self.path).path
            if path == "/health":
                self._json(200, {"ok": True, "data": {"bridge_type": "mock"}})
                return
            if path == "/api/concurrent":
                self._json(
                    200,
                    {
                        "ok": True,
                        "data": {"bridge_type": "mock", "concurrent": 1},
                    },
                )
                return
            if path == "/api/screen":
                self._json(
                    200,
                    {
                        "ok": True,
                        "data": {
                            "status": "running",
                            "screenshot": server.screenshot_payload(),
                        },
                    },
                )
                return
            if path == "/api/tools":
                names = sorted(set(server.spec.tools) | {"screenshot"})
                self._json(
                    200,
                    {
                        "tools": [
                            {
                                "name": name,
                                "category": "mock",
                                "description": f"Scripted mock response for {name}.",
                                "input_mode": "json",
                                "example_input": "{}",
                                "args_schema": {"type": "object"},
                                "http": {"method": "POST", "path": f"/api/tools/{name}"},
                            }
                            for name in names
                        ]
                    },
                )
                return
            if path == "/api/state":
                self._json(200, {"ok": True, "data": server.snapshot()})
                return
            self._json(
                404,
                {"ok": False, "error": {"code": "not_found", "message": path}},
            )

        def do_POST(self) -> None:
            path = urllib.parse.urlparse(self.path).path
            if path == "/api/setup":
                server.reset()
                self._json(200, {"ok": True, "data": {"setup": True}})
                return
            if path == "/api/release":
                self._json(200, {"ok": True, "data": {"released": True}})
                return
            if path.startswith("/api/tools/"):
                tool_name = path.removeprefix("/api/tools/").strip()
                payload = self._read_json()
                if payload is None:
                    return
                tool_input = _decode_tool_input(payload.get("input"))
                response = server.invoke(tool_name, tool_input)
                output = (
                    response.output
                    if isinstance(response.output, str)
                    else json.dumps(response.output, ensure_ascii=False)
                )
                self._json(
                    200,
                    {
                        "output": output,
                        "is_error": response.is_error,
                        "error": response.error,
                        "duration_ms": 0,
                    },
                )
                return
            self._json(
                404,
                {"ok": False, "error": {"code": "not_found", "message": path}},
            )

        def _read_json(self) -> dict[str, Any] | None:
            try:
                length = int(self.headers.get("Content-Length", "0"))
                raw = self.rfile.read(length) if length else b"{}"
                payload = json.loads(raw.decode("utf-8"))
            except (ValueError, UnicodeDecodeError, json.JSONDecodeError) as exc:
                self._json(
                    400,
                    {
                        "ok": False,
                        "error": {"code": "bad_request", "message": str(exc)},
                    },
                )
                return None
            if not isinstance(payload, dict):
                self._json(
                    400,
                    {
                        "ok": False,
                        "error": {
                            "code": "bad_request",
                            "message": "object required",
                        },
                    },
                )
                return None
            return payload

        def _json(self, status: int, payload: dict[str, Any]) -> None:
            data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def log_message(self, format: str, *args: Any) -> None:
            return

    return Handler


def _decode_tool_input(raw: Any) -> dict[str, Any]:
    if isinstance(raw, dict):
        return raw
    if not isinstance(raw, str) or not raw.strip():
        return {}
    try:
        decoded = json.loads(raw)
    except json.JSONDecodeError:
        return {"raw_input": raw}
    return decoded if isinstance(decoded, dict) else {"value": decoded}


def _wrap_text(text: str, width: int) -> list[str]:
    lines: list[str] = []
    for paragraph in str(text or "").splitlines() or [""]:
        remaining = paragraph
        if not remaining:
            lines.append("")
            continue
        while len(remaining) > width:
            lines.append(remaining[:width])
            remaining = remaining[width:]
        lines.append(remaining)
    return lines
