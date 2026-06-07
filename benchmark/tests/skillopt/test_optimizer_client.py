"""Unit tests for optimizer_client.py JSON extraction."""
import json
from pathlib import Path

import pytest
from runner.skillopt.optimizer_client import OptimizerConfig, OptimizerError, chat_optimizer, extract_json


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
