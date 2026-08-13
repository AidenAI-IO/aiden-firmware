from pathlib import Path
import tomllib

from runner import agent_config
from runner.agent_config import (
    default_agent_config_path,
    load_agent_model_config,
    resolve_agent_model_api_key,
    set_agent_device_type,
)


def test_set_agent_device_type_ignores_device_header_in_multiline_string():
    content = '''notes = """
[device]
description = "not a table"
"""
'''

    rendered = set_agent_device_type(content, "Android")

    config = tomllib.loads(rendered)
    assert config["notes"] == '[device]\ndescription = "not a table"\n'
    assert config["device"]["device_type"] == "Android"


def test_set_agent_device_type_ignores_device_key_in_multiline_string():
    content = """[device]\nnotes = '''
device_type = "not a key"
'''\n"""

    rendered = set_agent_device_type(content, "iOS")

    config = tomllib.loads(rendered)
    assert config["device"]["notes"] == 'device_type = "not a key"\n'
    assert config["device"]["device_type"] == "iOS"


def test_set_agent_device_type_replaces_multiline_device_type_value():
    content = '''[device]
device_type = """
iOS
"""
backend = "external"
'''

    rendered = set_agent_device_type(content, "Android")

    config = tomllib.loads(rendered)
    assert config["device"]["device_type"] == "Android"
    assert config["device"]["backend"] == "external"


def test_load_agent_model_config_reads_model_section(tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text(
        """
[model]
provider = "openrouter"
model = "google/gemini-3.5-flash"
api_key = "sk-direct"
""".strip(),
        encoding="utf-8",
    )

    assert load_agent_model_config(config) == {
        "provider": "openrouter",
        "model": "google/gemini-3.5-flash",
        "api_key": "sk-direct",
    }


def test_load_agent_model_config_resolves_canonical_named_provider(tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text(
        """
[model_providers.work]
type = "openrouter"
api_key = "sk-record"
base_url = "https://openrouter.ai/api/v1"

[model]
provider = "work"
model = "google/gemini-3.5-flash"
""".strip(),
        encoding="utf-8",
    )

    assert load_agent_model_config(config) == {
        "provider": "openrouter",
        "model": "google/gemini-3.5-flash",
        "api_key": "sk-record",
        "base_url": "https://openrouter.ai/api/v1",
    }


def test_load_agent_model_config_prefers_canonical_namespace_and_type(tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text(
        """
[providers.work]
provider = "openai"
api_key = "sk-legacy"

[model_providers.work]
provider = "ollama"
type = "openrouter"
api_key = "sk-canonical"

[model]
provider = "work"
model = "google/gemini-3.5-flash"
""".strip(),
        encoding="utf-8",
    )

    resolved = load_agent_model_config(config)

    assert resolved["provider"] == "openrouter"
    assert resolved["api_key"] == "sk-canonical"


def test_load_agent_model_config_does_not_fall_back_from_empty_canonical_type(tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text(
        """
[model_providers.work]
type = ""
provider = "openai"

[model]
provider = "work"
model = "gpt-4o"
""".strip(),
        encoding="utf-8",
    )

    assert load_agent_model_config(config)["provider"] == ""


def test_load_agent_model_config_reads_legacy_named_provider(tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text(
        """
[providers.work]
provider = "openai"
api_key = "sk-legacy"

[model]
provider = "work"
model = "gpt-4o"
""".strip(),
        encoding="utf-8",
    )

    resolved = load_agent_model_config(config)

    assert resolved["provider"] == "openai"
    assert resolved["api_key"] == "sk-legacy"


def test_resolve_agent_model_api_key_uses_api_key_environment_reference(tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text(
        """
[model_providers.work]
type = "openrouter"
api_key = "$OPENROUTER_API_KEY"

[model]
provider = "work"
model = "google/gemini-3.5-flash"
""".strip(),
        encoding="utf-8",
    )

    assert resolve_agent_model_api_key(config, env={"OPENROUTER_API_KEY": "sk-env"}) == "sk-env"


def test_resolve_agent_model_api_key_uses_direct_agent_toml_value(tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text('[model]\napi_key = "sk-direct"\n', encoding="utf-8")

    assert resolve_agent_model_api_key(config, env={}) == "sk-direct"


def test_resolve_agent_model_api_key_treats_unprefixed_value_as_literal(tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text('[model]\napi_key = "OPENROUTER_API_KEY"\n', encoding="utf-8")

    assert resolve_agent_model_api_key(config, env={"OPENROUTER_API_KEY": "sk-env"}) == "OPENROUTER_API_KEY"


def test_resolve_agent_model_api_key_keeps_unprefixed_value_without_matching_env(tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text('[model]\napi_key = "OPENROUTER_API_KEY"\n', encoding="utf-8")

    assert resolve_agent_model_api_key(config, env={}) == "OPENROUTER_API_KEY"


def test_default_agent_config_path_ignores_directory_env_path(monkeypatch, tmp_path: Path):
    config_dir = tmp_path / "agent.toml"
    config_dir.mkdir()
    monkeypatch.setenv("AIDEN_AGENT_CONFIG", str(config_dir))
    monkeypatch.setattr(agent_config, "REPO_ROOT", tmp_path / "missing-repo")

    assert default_agent_config_path() != config_dir


def test_resolve_agent_model_api_key_ignores_directory_path(tmp_path: Path):
    config_dir = tmp_path / "agent.toml"
    config_dir.mkdir()

    assert resolve_agent_model_api_key(config_dir, env={}) is None
