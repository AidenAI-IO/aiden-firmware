"""Bounded JSON-line client for the VPhone host-control Unix socket."""

from __future__ import annotations

import json
import socket
import threading
from pathlib import Path
from typing import Any


DEFAULT_MAX_RESPONSE_BYTES = 32 * 1024 * 1024
DEFAULT_MAX_REQUEST_BYTES = 64 * 1024


class VPhoneSocketError(RuntimeError):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


class VPhoneSocketClient:
    def __init__(
        self,
        socket_path: str | Path,
        *,
        timeout_sec: float = 30,
        max_response_bytes: int = DEFAULT_MAX_RESPONSE_BYTES,
        max_request_bytes: int = DEFAULT_MAX_REQUEST_BYTES,
    ):
        self.socket_path = Path(socket_path).expanduser()
        self.timeout_sec = max(0.1, float(timeout_sec))
        self.max_response_bytes = max(1024, int(max_response_bytes))
        self.max_request_bytes = max(1024, int(max_request_bytes))
        self._request_lock = threading.Lock()

    def request(self, payload: dict[str, Any], *, timeout_sec: float | None = None) -> dict[str, Any]:
        # The host-control accept loop and VM are single-device resources. Keep
        # health checks and tool calls ordered even when the HTTP layer is threaded.
        with self._request_lock:
            return self._request(payload, timeout_sec=timeout_sec)

    def _request(self, payload: dict[str, Any], *, timeout_sec: float | None = None) -> dict[str, Any]:
        if not isinstance(payload, dict):
            raise TypeError("payload must be an object")
        try:
            encoded = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8") + b"\n"
        except (TypeError, ValueError) as exc:
            raise VPhoneSocketError("invalid_request", f"cannot encode socket request: {exc}") from exc
        if len(encoded) > self.max_request_bytes:
            raise VPhoneSocketError(
                "request_too_large", f"socket request exceeds {self.max_request_bytes} bytes"
            )

        timeout = self.timeout_sec if timeout_sec is None else max(0.1, float(timeout_sec))
        client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        client.settimeout(timeout)
        try:
            client.connect(str(self.socket_path))
            client.sendall(encoded)
            raw = self._read_line(client)
        except FileNotFoundError as exc:
            raise VPhoneSocketError(
                "socket_not_found", f"VPhone socket does not exist: {self.socket_path}"
            ) from exc
        except ConnectionRefusedError as exc:
            raise VPhoneSocketError(
                "socket_refused", f"VPhone socket refused the connection: {self.socket_path}"
            ) from exc
        except socket.timeout as exc:
            raise VPhoneSocketError("socket_timeout", f"VPhone socket timed out after {timeout:.1f}s") from exc
        except OSError as exc:
            raise VPhoneSocketError("socket_io", f"VPhone socket I/O failed: {exc}") from exc
        finally:
            client.close()

        if not raw:
            raise VPhoneSocketError("empty_response", "VPhone socket returned an empty response")
        try:
            response = json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise VPhoneSocketError("invalid_response", "VPhone socket returned invalid JSON") from exc
        if not isinstance(response, dict):
            raise VPhoneSocketError("invalid_response", "VPhone socket response must be an object")
        if response.get("ok") is not True:
            error = response.get("error")
            if isinstance(error, dict):
                code = str(error.get("code") or "command_failed")
                message = str(error.get("message") or error)
            else:
                code = str(response.get("code") or "command_failed")
                message = str(error or "VPhone command failed")
            raise VPhoneSocketError(code, message)
        return response

    def _read_line(self, client: socket.socket) -> bytes:
        chunks: list[bytes] = []
        total = 0
        while True:
            chunk = client.recv(min(65536, self.max_response_bytes - total + 1))
            if not chunk:
                break
            newline = chunk.find(b"\n")
            if newline >= 0:
                chunk = chunk[:newline]
                chunks.append(chunk)
                total += len(chunk)
                break
            chunks.append(chunk)
            total += len(chunk)
            if total > self.max_response_bytes:
                raise VPhoneSocketError(
                    "response_too_large", f"VPhone socket response exceeds {self.max_response_bytes} bytes"
                )
        if total > self.max_response_bytes:
            raise VPhoneSocketError(
                "response_too_large", f"VPhone socket response exceeds {self.max_response_bytes} bytes"
            )
        return b"".join(chunks)
