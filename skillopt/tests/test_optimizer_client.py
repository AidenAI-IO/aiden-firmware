"""Unit tests for optimizer_client.py JSON extraction."""
import io
import json
from pathlib import Path
import urllib.error

import pytest
from skillopt.optimizer_client import OptimizerConfig, OptimizerError, chat_optimizer, extract_json


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
