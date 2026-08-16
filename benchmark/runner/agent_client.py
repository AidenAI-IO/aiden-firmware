from __future__ import annotations
import dataclasses as dc
import json
import socket
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from typing import Any


class AgentTimeoutError(TimeoutError):
    def __init__(self, message: str = "", *, request_id: str | None = None):
        super().__init__(message)
        self.request_id = request_id


class AgentRequestError(RuntimeError):
    def __init__(self, message: str = "", *, request_id: str | None = None):
        super().__init__(message)
        self.request_id = request_id


@dc.dataclass
class ChatResponse:
    response: str
    history: list[dict[str, Any]]


@dc.dataclass
class ToolInvokeResult:
    output: str
    is_error: bool
    duration_ms: int


class AgentClient:
    def __init__(
        self,
        base_url: str = "http://localhost:8080",
        default_timeout_sec: int = 180,
        benchmark_token: str = "",
    ):
        self.base_url = base_url.rstrip("/")
        self._default_timeout = default_timeout_sec
        self._benchmark_token = str(benchmark_token or "").strip()
        self._pending_chat_request_ids: set[str] = set()

    def _post(
        self,
        path: str,
        payload: dict[str, Any] | None = None,
        timeout: int = 30,
        headers: dict[str, str] | None = None,
    ) -> tuple[int, bytes]:
        data = json.dumps(payload or {}, ensure_ascii=False).encode("utf-8")
        request_headers = {"Content-Type": "application/json"}
        if headers:
            request_headers.update(headers)
        req = urllib.request.Request(
            f"{self.base_url}{path}",
            data=data,
            headers=request_headers,
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return resp.status, resp.read()
        except socket.timeout as e:
            raise AgentTimeoutError(str(e)) from e
        except urllib.error.HTTPError as e:
            body = b""
            try:
                body = e.read()
            except Exception:
                pass
            raise AgentRequestError(f"HTTP {e.code}: {body[:200]!r}") from e
        except urllib.error.URLError as e:
            if isinstance(e.reason, socket.timeout):
                raise AgentTimeoutError(str(e)) from e
            raise AgentRequestError(str(e)) from e
        except (ConnectionResetError, ConnectionError, OSError) as e:
            raise AgentRequestError(str(e)) from e

    def _get(self, path: str, timeout: int = 5) -> tuple[int, bytes]:
        req = urllib.request.Request(f"{self.base_url}{path}", method="GET")
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return resp.status, resp.read()
        except socket.timeout as e:
            raise AgentTimeoutError(str(e)) from e
        except urllib.error.HTTPError as e:
            raise AgentRequestError(f"HTTP {e.code}") from e
        except urllib.error.URLError as e:
            if isinstance(e.reason, socket.timeout):
                raise AgentTimeoutError(str(e)) from e
            raise AgentRequestError(str(e)) from e
        except (ConnectionResetError, ConnectionError, OSError) as e:
            raise AgentRequestError(str(e)) from e

    def health(self) -> bool:
        try:
            status, _ = self._get("/api/tools", timeout=5)
            return status == 200
        except (AgentRequestError, AgentTimeoutError):
            return False

    def device_type(self) -> str:
        status, body_bytes = self._get("/api/phone-bridge/status", timeout=5)
        if status != 200:
            raise AgentRequestError(f"phone bridge status returned {status}")
        body = json.loads(body_bytes)
        return str(body.get("device_type") or "").strip() if isinstance(body, dict) else ""

    def clear_history(self, timeout: int = 30) -> None:
        self._post("/api/clear", timeout=timeout)

    def seed_memory(self, memory: dict[str, Any], timeout: int = 30) -> dict[str, Any]:
        headers = {}
        if self._benchmark_token:
            headers["Authorization"] = f"Bearer {self._benchmark_token}"
        status, body_bytes = self._post(
            "/api/benchmark/seed_memory", memory, timeout=timeout, headers=headers
        )
        if status != 200:
            raise AgentRequestError(f"seed_memory returned {status}")
        body = json.loads(body_bytes)
        return body if isinstance(body, dict) else {}

    def set_phone_bridge_state(
        self, state: dict[str, Any], timeout: int = 30
    ) -> dict[str, Any]:
        headers = {}
        if self._benchmark_token:
            headers["Authorization"] = f"Bearer {self._benchmark_token}"
        status, body_bytes = self._post(
            "/api/benchmark/phone_bridge_state",
            state,
            timeout=timeout,
            headers=headers,
        )
        if status != 200:
            raise AgentRequestError(f"phone_bridge_state returned {status}")
        try:
            body = json.loads(body_bytes)
        except json.JSONDecodeError as exc:
            raise AgentRequestError(f"phone_bridge_state returned invalid JSON: {exc}") from exc
        return body if isinstance(body, dict) else {}

    def get_history(self) -> list[dict[str, Any]]:
        status, body_bytes = self._get("/api/history", timeout=5)
        if status != 200:
            raise AgentRequestError(f"history returned {status}")
        body = json.loads(body_bytes)
        return body if isinstance(body, list) else []

    def get_episode(self, episode_id: str) -> dict[str, Any]:
        encoded_id = urllib.parse.quote(str(episode_id), safe="")
        status, body_bytes = self._get(f"/api/episodes/{encoded_id}", timeout=5)
        if status != 200:
            raise AgentRequestError(f"episode returned {status}")
        body = json.loads(body_bytes)
        episode = body.get("episode") if isinstance(body, dict) else None
        if not isinstance(episode, dict):
            raise AgentRequestError("episode response did not contain an episode object")
        return episode

    def chat(
        self,
        message: str,
        timeout_sec: int | None = None,
        attachments: list[dict[str, Any]] | None = None,
        skills: list[str] | None = None,
    ) -> ChatResponse:
        request_id = f"benchmark-{uuid.uuid4().hex}"
        payload: dict[str, Any] = {"message": message, "request_id": request_id}
        if attachments:
            payload["attachments"] = attachments
        if skills:
            payload["skills"] = skills
        try:
            status, body_bytes = self._post(
                "/api/chat", payload, timeout=timeout_sec or self._default_timeout
            )
        except (AgentTimeoutError, AgentRequestError) as exc:
            exc.request_id = request_id
            self._pending_chat_request_ids.add(request_id)
            raise
        if status != 200:
            raise AgentRequestError(f"chat returned {status}")
        body = json.loads(body_bytes)

        # Async mode: agent returns request_id, long poll for completion.
        response_request_id = str(body.get("request_id") or "").strip()
        if response_request_id:
            return self._wait_for_chat_result(response_request_id, timeout_sec)

        return ChatResponse(
            response=body.get("response", ""),
            history=body.get("history", []),
        )

    def _wait_for_chat_result(
        self, request_id: str, timeout_sec: int | None = None
    ) -> ChatResponse:
        """Long poll /api/chat/result?wait=true until the task completes."""
        timeout = timeout_sec or self._default_timeout
        encoded_id = urllib.parse.quote(request_id, safe="")
        try:
            status, body_bytes = self._get(
                f"/api/chat/result?request_id={encoded_id}&wait=true",
                timeout=timeout,
            )
        except (AgentTimeoutError, AgentRequestError) as exc:
            exc.request_id = request_id
            self._pending_chat_request_ids.add(request_id)
            raise
        if status != 200:
            raise AgentRequestError(
                f"chat/result returned {status}", request_id=request_id
            )
        body = json.loads(body_bytes)

        result_status = body.get("status")
        if result_status == "error":
            raise AgentRequestError(f"chat failed: {body.get('error', 'unknown')}")
        if result_status == "not_found":
            raise AgentRequestError(f"chat result not found for {request_id}")
        if result_status != "complete":
            raise AgentRequestError(
                f"unexpected status from wait=true: {result_status}"
            )

        return ChatResponse(
            response=body.get("response", ""),
            history=body.get("history", []),
        )

    def cancel_chat(self, request_id: str, timeout: int = 15) -> str:
        status, body_bytes = self._post(
            "/api/chat/cancel",
            {"request_id": request_id},
            timeout=timeout,
        )
        if status != 200:
            raise AgentRequestError(
                f"chat/cancel returned {status}", request_id=request_id
            )
        body = json.loads(body_bytes)
        return str(body.get("status") or "") if isinstance(body, dict) else ""

    def chat_result_status(self, request_id: str, timeout: int = 5) -> str:
        encoded_id = urllib.parse.quote(request_id, safe="")
        status, body_bytes = self._get(
            f"/api/chat/result?request_id={encoded_id}", timeout=timeout
        )
        if status != 200:
            raise AgentRequestError(
                f"chat/result returned {status}", request_id=request_id
            )
        body = json.loads(body_bytes)
        return str(body.get("status") or "") if isinstance(body, dict) else ""

    def invoke_tool(
        self,
        name: str,
        args: dict[str, Any],
        timeout: int = 90,
        benchmark_task_id: str | None = None,
    ) -> ToolInvokeResult:
        headers = {}
        if str(benchmark_task_id or "").strip():
            headers["benchmark-task-id"] = str(benchmark_task_id).strip()
        status, body_bytes = self._post(
            f"/api/tools/{name}", {"input": args}, timeout=timeout, headers=headers
        )
        if status != 200:
            raise AgentRequestError(f"invoke {name} returned {status}")
        body = json.loads(body_bytes)
        return ToolInvokeResult(
            output=body.get("output", ""),
            is_error=bool(body.get("is_error")),
            duration_ms=int(body.get("duration_ms", 0)),
        )

    def recover_after_timeout(
        self,
        timeout_sec: int = 90,
        poll_sec: float = 3.0,
    ) -> bool:
        """Cancel timed-out chats, wait for terminal state, then clear history."""
        deadline = time.monotonic() + max(0, timeout_sec)
        pending_request_ids = list(
            getattr(self, "_pending_chat_request_ids", set())
        )
        for request_id in pending_request_ids:
            cancel_sent = False
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    return False
                if not cancel_sent:
                    try:
                        self.cancel_chat(
                            request_id,
                            timeout=min(15, max(1, int(remaining))),
                        )
                        cancel_sent = True
                    except (AgentTimeoutError, AgentRequestError):
                        time.sleep(min(poll_sec, max(0, deadline - time.monotonic())))
                        continue
                try:
                    result_status = self.chat_result_status(
                        request_id,
                        timeout=min(5, max(1, int(remaining))),
                    )
                except (AgentTimeoutError, AgentRequestError):
                    if time.monotonic() >= deadline:
                        return False
                    time.sleep(min(poll_sec, max(0, deadline - time.monotonic())))
                    continue
                if result_status in {"complete", "error", "canceled", "not_found"}:
                    self._pending_chat_request_ids.discard(request_id)
                    break
                if time.monotonic() >= deadline:
                    return False
                time.sleep(min(poll_sec, max(0, deadline - time.monotonic())))

        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return False
            try:
                self.clear_history(timeout=min(15, max(1, int(remaining))))
                return True
            except (AgentTimeoutError, AgentRequestError):
                if time.monotonic() >= deadline:
                    return False
                time.sleep(min(poll_sec, max(0, deadline - time.monotonic())))

    def close(self) -> None:
        # urllib doesn't keep persistent connections by default; nothing to clean up.
        pass
