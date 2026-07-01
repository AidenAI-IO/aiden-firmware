"""Unit tests for optimizer_client.py JSON extraction."""
import io
import json
from pathlib import Path
import urllib.error

import pytest
from skillopt.optimizer_client import (
    OptimizerConfig,
    OptimizerError,
    chat_optimizer,
    chat_optimizer_json,
    extract_json,
)


def test_extract_json_bare():
    raw = '{"foo": 1, "bar": "x"}'
    assert extract_json(raw) == {"foo": 1, "bar": "x"}


def test_extract_json_with_prose():
    raw = 'Here is my analysis:\n{"patch": {"edits": []}}\nDone.'
    assert extract_json(raw) == {"patch": {"edits": []}}


def test_extract_json_with_code_fence():
    raw = '```json\n{"a": 1}\n```'
    assert extract_json(raw) == {"a": 1}


def test_extract_json_with_unlabeled_fence():
    raw = '```\n{"a": 2}\n```'
    assert extract_json(raw) == {"a": 2}


def test_extract_json_no_object():
    with pytest.raises(OptimizerError):
        extract_json("just plain text, no JSON here")


def test_extract_json_invalid():
    with pytest.raises(OptimizerError):
        extract_json('{"broken": ')


def test_extract_json_nested():
    raw = 'prose {"outer": {"inner": [1, 2, 3]}} more prose'
    assert extract_json(raw) == {"outer": {"inner": [1, 2, 3]}}


def test_chat_optimizer_uses_agent_toml_api_key_when_env_missing(monkeypatch, tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text('[model]\napi_key = "sk-agent"\n', encoding="utf-8")
    seen = {}

    class FakeResponse:
        status = 200

        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return json.dumps({"choices": [{"message": {"content": "{}"}}]}).encode("utf-8")

    def fake_urlopen(req, timeout):
        seen["authorization"] = req.headers.get("Authorization")
        seen["timeout"] = timeout
        return FakeResponse()

    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    assert chat_optimizer(OptimizerConfig(agent_config_path=str(config)), "system", "user") == "{}"
    assert seen == {"authorization": "Bearer sk-agent", "timeout": 180}


def test_chat_optimizer_non_json_body_raises_optimizer_error(monkeypatch):
    class FakeResponse:
        status = 200

        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return b"not json"

    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-env")
    monkeypatch.setattr("urllib.request.urlopen", lambda req, timeout: FakeResponse())

    with pytest.raises(OptimizerError, match="optimizer returned non-JSON body"):
        chat_optimizer(OptimizerConfig(), "system", "user")


def test_chat_optimizer_non_string_content_raises_optimizer_error(monkeypatch):
    class FakeResponse:
        status = 200

        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return json.dumps({"choices": [{"message": {"content": {"not": "string"}}}]}).encode("utf-8")

    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-env")
    monkeypatch.setattr("urllib.request.urlopen", lambda req, timeout: FakeResponse())

    with pytest.raises(OptimizerError, match="unexpected optimizer content type"):
        chat_optimizer(OptimizerConfig(), "system", "user")


def test_chat_optimizer_tries_comma_separated_model_fallbacks(monkeypatch):
    class FakeResponse:
        status = 200

        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return json.dumps({"choices": [{"message": {"content": "{}"}}]}).encode("utf-8")

    seen_models: list[str] = []

    def fake_urlopen(req, timeout):
        payload = json.loads(req.data.decode("utf-8"))
        seen_models.append(payload["model"])
        if payload["model"] == "region-blocked-model":
            raise urllib.error.HTTPError(
                req.full_url,
                403,
                "Forbidden",
                hdrs=None,
                fp=io.BytesIO(b'{"error":{"message":"region blocked"}}'),
            )
        return FakeResponse()

    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-env")
    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    raw = chat_optimizer(OptimizerConfig(model="region-blocked-model, usable-model"), "system", "user")

    assert raw == "{}"
    assert seen_models == ["region-blocked-model", "usable-model"]


def test_chat_optimizer_retries_transient_http_500_once(monkeypatch):
    class FakeResponse:
        status = 200

        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return json.dumps({"choices": [{"message": {"content": "{}"}}]}).encode("utf-8")

    attempts = 0

    def fake_urlopen(req, timeout):
        nonlocal attempts
        attempts += 1
        if attempts == 1:
            raise urllib.error.HTTPError(
                req.full_url,
                500,
                "Internal Server Error",
                hdrs=None,
                fp=io.BytesIO(b"planner/executor role model call timed out after 2m0s"),
            )
        return FakeResponse()

    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-env")
    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    raw = chat_optimizer(OptimizerConfig(), "system", "user")

    assert raw == "{}"
    assert attempts == 2


def test_extract_json_truncated_flags_incomplete():
    truncated = '{\n  "batch_size": 2,\n  "success_patterns": ["For cross-app'
    with pytest.raises(OptimizerError) as exc:
        extract_json(truncated)
    assert getattr(exc.value, "incomplete", False) is True
    assert "truncated" in str(exc.value)


def test_extract_json_invalid_flags_not_incomplete():
    invalid = '{"a": 1, "b": ,}'
    with pytest.raises(OptimizerError) as exc:
        extract_json(invalid)
    assert getattr(exc.value, "incomplete", False) is False


def test_chat_optimizer_json_retries_truncated_and_bumps_max_tokens(monkeypatch):
    call_log: list[dict] = []

    def fake_chat(cfg, system, user):
        call_log.append({"max_tokens": cfg.max_tokens, "user": user})
        if len(call_log) == 1:
            return '{"batch_size": 2, "success_patterns": ["For cross-app'
        return '{"batch_size": 2, "patch": {"edits": []}}'

    monkeypatch.setattr("skillopt.optimizer_client.chat_optimizer", fake_chat)

    result = chat_optimizer_json(
        OptimizerConfig(max_tokens=4096, json_parse_attempts=2),
        system="sys",
        user="user",
    )
    assert result == {"batch_size": 2, "patch": {"edits": []}}
    assert len(call_log) == 2
    assert call_log[1]["max_tokens"] == 8192
    assert "previous response was cut off" in call_log[1]["user"]


def test_chat_optimizer_json_gives_up_after_max_parse_attempts(monkeypatch):
    monkeypatch.setattr(
        "skillopt.optimizer_client.chat_optimizer",
        lambda cfg, system, user: '{"broken": ',
    )
    with pytest.raises(OptimizerError):
        chat_optimizer_json(
            OptimizerConfig(json_parse_attempts=2),
            system="sys",
            user="user",
        )
