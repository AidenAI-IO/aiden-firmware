from pathlib import Path

from runner import agent_config
from runner.agent_config import default_agent_config_path, load_agent_model_config, resolve_agent_model_api_key


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


def test_resolve_agent_model_api_key_uses_direct_agent_toml_value(tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text('[model]\napi_key = "sk-direct"\n', encoding="utf-8")

    assert resolve_agent_model_api_key(config, env={}) == "sk-direct"


def test_resolve_agent_model_api_key_resolves_env_name_from_agent_toml(tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text('[model]\napi_key = "OPENROUTER_API_KEY"\n', encoding="utf-8")

    assert resolve_agent_model_api_key(config, env={"OPENROUTER_API_KEY": "sk-env"}) == "sk-env"


def test_resolve_agent_model_api_key_does_not_treat_missing_env_name_as_secret(tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text('[model]\napi_key = "OPENROUTER_API_KEY"\n', encoding="utf-8")

    assert resolve_agent_model_api_key(config, env={}) is None


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
