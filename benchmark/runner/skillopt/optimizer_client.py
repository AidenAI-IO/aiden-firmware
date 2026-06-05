"""Optimizer LLM client.

Thin wrapper around OpenRouter's OpenAI-compatible chat completions API,
mirroring the style of runner/judge.py so we have one HTTP/auth pattern
in this repo. Used by reflect.py to invoke the analyst (optimizer) model.
"""
from __future__ import annotations
import dataclasses as dc
import json
import os
import socket
import urllib.error
import urllib.request


@dc.dataclass
class OptimizerConfig:
    provider: str = "openrouter"
    model: str = "anthropic/claude-opus-4-7"
    api_key_env: str = "OPENROUTER_API_KEY"
    max_tokens: int = 4096
    timeout_sec: int = 180


class OptimizerError(RuntimeError):
    pass


def chat_optimizer(
    cfg: OptimizerConfig,
    system: str,
    user: str,
) -> str:
    """Run one optimizer chat call. Returns raw response text.

    Raises OptimizerError on transport / status failures. JSON parsing of
    the response body is the caller's responsibility (analyst prompts
    request a JSON object directly).
    """
    api_key = os.environ.get(cfg.api_key_env)
    if not api_key:
        raise OptimizerError(f"missing env var {cfg.api_key_env}")

    payload = json.dumps({
        "model": cfg.model,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "max_tokens": cfg.max_tokens,
    }).encode("utf-8")

    req = urllib.request.Request(
        "https://openrouter.ai/api/v1/chat/completions",
        data=payload,
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=cfg.timeout_sec) as resp:
            if resp.status != 200:
                raise OptimizerError(f"optimizer HTTP {resp.status}")
            body = json.loads(resp.read())
    except urllib.error.HTTPError as e:
        raise OptimizerError(f"optimizer HTTP {e.code}: {e.read()[:200]!r}") from e
    except (socket.timeout, urllib.error.URLError) as e:
        raise OptimizerError(f"optimizer network error: {e}") from e

    try:
        return body["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError) as e:
        raise OptimizerError(f"unexpected optimizer response shape: {e}") from e


def extract_json(raw: str) -> dict:
    """Best-effort JSON extraction from an analyst response.

    Handles common formats: bare JSON, JSON inside ```json fences, JSON
    embedded in surrounding prose. Raises OptimizerError if nothing
    parseable is found.
    """
    s = raw.strip()
    # Strip code fences if present
    if s.startswith("```"):
        s = s.split("```", 2)
        # ['', 'json\n{...}', '...'] or ['', '{...}', '...']
        if len(s) >= 2:
            inner = s[1]
            if inner.startswith("json\n"):
                inner = inner[5:]
            elif inner.startswith("\n"):
                inner = inner[1:]
            s = inner
        else:
            s = "".join(s)
    if not isinstance(s, str):
        s = str(s)
    start = s.find("{")
    end = s.rfind("}")
    if start == -1 or end == -1 or end <= start:
        raise OptimizerError(f"no JSON object found in optimizer response: {raw[:200]!r}")
    try:
        return json.loads(s[start:end + 1])
    except json.JSONDecodeError as e:
        raise OptimizerError(f"failed to parse optimizer JSON: {e}: {raw[:200]!r}") from e
