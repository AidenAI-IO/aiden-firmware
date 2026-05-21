from __future__ import annotations
import dataclasses as dc
import json
from typing import Any
import httpx

class AgentTimeoutError(TimeoutError):
    pass

class AgentRequestError(RuntimeError):
    pass

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
        transport: httpx.BaseTransport | None = None,
        default_timeout_sec: int = 180,
    ):
        self._client = httpx.Client(
            base_url=base_url, transport=transport, timeout=default_timeout_sec
        )
        self._default_timeout = default_timeout_sec

    def health(self) -> bool:
        try:
            r = self._client.get("/api/tools", timeout=5)
            return r.status_code == 200
        except httpx.HTTPError:
            return False

    def clear_history(self) -> None:
        r = self._client.post("/api/clear", timeout=10)
        r.raise_for_status()

    def chat(self, message: str, timeout_sec: int | None = None) -> ChatResponse:
        try:
            r = self._client.post(
                "/api/chat",
                json={"message": message},
                timeout=timeout_sec or self._default_timeout,
            )
        except httpx.ReadTimeout as e:
            raise AgentTimeoutError(str(e)) from e
        except httpx.HTTPError as e:
            raise AgentRequestError(str(e)) from e
        if r.status_code != 200:
            raise AgentRequestError(f"chat returned {r.status_code}: {r.text}")
        body = r.json()
        return ChatResponse(response=body.get("response", ""), history=body.get("history", []))

    def invoke_tool(self, name: str, args: dict[str, Any]) -> ToolInvokeResult:
        r = self._client.post(
            f"/api/tools/{name}",
            json={"input": args},
            timeout=30,
        )
        if r.status_code != 200:
            raise AgentRequestError(f"invoke {name} returned {r.status_code}: {r.text}")
        body = r.json()
        return ToolInvokeResult(
            output=body.get("output", ""),
            is_error=bool(body.get("is_error")),
            duration_ms=int(body.get("duration_ms", 0)),
        )

    def close(self) -> None:
        self._client.close()
