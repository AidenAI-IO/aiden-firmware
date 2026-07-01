"""Optimizer LLM client.

Thin wrapper around OpenRouter's OpenAI-compatible chat completions API,
mirroring the style of runner/judge.py so we have one HTTP/auth pattern
in this repo. Used by reflect.py to invoke the analyst (optimizer) model.
"""
from __future__ import annotations
import dataclasses as dc
import json
import socket
import urllib.error
import urllib.request

from runner.agent_config import resolve_api_key


DEFAULT_OPTIMIZER_MODEL = "anthropic/claude-opus-4-7"


@dc.dataclass
class OptimizerConfig:
    provider: str = "openrouter"
    model: str = DEFAULT_OPTIMIZER_MODEL
    api_key_env: str = "OPENROUTER_API_KEY"
    agent_config_path: str | None = None
    max_tokens: int = 8192
    timeout_sec: int = 180
    request_attempts: int = 2
    # How many extra attempts to make when the model returns text that
    # parses as truncated/invalid JSON (separate from transport retries).
    json_parse_attempts: int = 2


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
    api_key = resolve_api_key(cfg.api_key_env, agent_config_path=cfg.agent_config_path)
    if not api_key:
        raise OptimizerError(f"missing env var {cfg.api_key_env}")

    models = optimizer_model_candidates(cfg.model)
    failures: list[str] = []
    last_error: OptimizerError | None = None
    for model in models:
        try:
            return _chat_optimizer_once(cfg, model, api_key, system, user)
        except OptimizerError as e:
            last_error = e
            failures.append(f"{model}: {e}")

    if len(models) == 1 and last_error is not None:
        raise last_error
    raise OptimizerError("optimizer failed for all configured models: " + "; ".join(failures))


def optimizer_model_candidates(model: str) -> list[str]:
    models = [part.strip() for part in str(model or "").split(",") if part.strip()]
    return models or [DEFAULT_OPTIMIZER_MODEL]


def _chat_optimizer_once(
    cfg: OptimizerConfig,
    model: str,
    api_key: str,
    system: str,
    user: str,
) -> str:
    payload = json.dumps({
        "model": model,
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
    attempts = max(1, int(cfg.request_attempts or 1))
    last_error: OptimizerError | None = None
    for attempt in range(attempts):
        try:
            with urllib.request.urlopen(req, timeout=cfg.timeout_sec) as resp:
                if resp.status != 200:
                    err = OptimizerError(f"optimizer HTTP {resp.status}")
                    if resp.status >= 500 and attempt + 1 < attempts:
                        last_error = err
                        continue
                    raise err
                try:
                    body = json.loads(resp.read())
                except json.JSONDecodeError as e:
                    raise OptimizerError(f"optimizer returned non-JSON body: {e}") from e
                break
        except urllib.error.HTTPError as e:
            err = OptimizerError(f"optimizer HTTP {e.code}: {e.read()[:200]!r}")
            if e.code >= 500 and attempt + 1 < attempts:
                last_error = err
                continue
            raise err from e
        except (socket.timeout, urllib.error.URLError) as e:
            err = OptimizerError(f"optimizer network error: {e}")
            if attempt + 1 < attempts:
                last_error = err
                continue
            raise err from e
    else:
        raise last_error or OptimizerError("optimizer request failed")

    try:
        content = body["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError) as e:
        raise OptimizerError(f"unexpected optimizer response shape: {e}") from e
    if not isinstance(content, str):
        raise OptimizerError("unexpected optimizer content type (expected string)")
    return content


def extract_json(raw: str) -> dict:
    """Best-effort JSON extraction from an analyst response.

    Handles common formats: bare JSON, JSON inside ```json fences, JSON
    embedded in surrounding prose. Raises OptimizerError if nothing
    parseable is found. The error carries an ``incomplete`` flag when the
    failure looks like the response was truncated mid-object (vs. a
    schema/structural error), so callers can ask the model to retry with
    more headroom.
    """
    s = raw.strip()
    if s.startswith("```"):
        s = s.split("```", 2)
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
    if start == -1:
        raise OptimizerError(f"no JSON object found in optimizer response: {raw[:200]!r}")

    decoder = json.JSONDecoder()
    try:
        obj, _ = decoder.raw_decode(s[start:])
        if isinstance(obj, dict):
            return obj
    except json.JSONDecodeError as e:
        # If the response ends without closing the top-level object,
        # raw_decode reports the error at end-of-input. Flag it so the
        # caller can retry with a larger token budget instead of giving up.
        tail = s[start:].rstrip()
        incomplete = e.pos >= len(tail) or not tail.endswith("}")
        err = OptimizerError(
            f"failed to parse optimizer JSON ({'truncated' if incomplete else 'invalid'}): "
            f"{e}: {raw[:200]!r}"
        )
        err.incomplete = incomplete  # type: ignore[attr-defined]
        raise err from e
    raise OptimizerError(f"optimizer JSON was not an object: {raw[:200]!r}")


def chat_optimizer_json(
    cfg: OptimizerConfig,
    system: str,
    user: str,
) -> dict:
    """Run an optimizer call and parse the response as a JSON object.

    Retries when the model returns text that fails to parse — typically
    a truncated JSON when the response hit ``max_tokens``. The retry
    appends a stricter instruction and doubles the token budget once,
    which is enough to recover for both Anthropic and Bytedance models.
    """
    attempts = max(1, int(cfg.json_parse_attempts or 1))
    bumped_cfg = cfg
    last_error: OptimizerError | None = None
    extra_instruction = ""
    for attempt in range(attempts):
        raw = chat_optimizer(
            bumped_cfg,
            system=system,
            user=user + extra_instruction,
        )
        try:
            return extract_json(raw)
        except OptimizerError as exc:
            last_error = exc
            if attempt + 1 >= attempts:
                break
            incomplete = bool(getattr(exc, "incomplete", False))
            if incomplete:
                bumped_cfg = dc.replace(
                    bumped_cfg,
                    max_tokens=min(bumped_cfg.max_tokens * 2, 32768),
                )
                extra_instruction = (
                    "\n\nIMPORTANT: your previous response was cut off before the "
                    "closing brace. Return a single complete JSON object — keep "
                    "patches short if needed, but the object must end with `}`. "
                    "No prose, no code fences."
                )
            else:
                extra_instruction = (
                    "\n\nIMPORTANT: return a single JSON object, no prose, "
                    "no code fences. Make sure the JSON is syntactically valid."
                )
    assert last_error is not None
    raise last_error
