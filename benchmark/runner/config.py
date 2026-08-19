"""Shared agent configuration management for Benchmark and SkillOpt WebUIs.

This module provides agent.toml configuration management functionality that can be
used by both benchmark/runner/webui.py and skillopt/webui.py.
"""

from __future__ import annotations

import json
import os
import re
from pathlib import Path
from typing import Mapping


VOICE_SIDE_EFFECT_DEFAULTS = (
    "voice_streaming_tts_enabled",
    "voice_tool_call_speech",
    "voice_progress_speech_enabled",
)

BENCHMARK_AGENT_PROVIDER_ENV = "AIDEN_BENCHMARK_AGENT_PROVIDER"
BENCHMARK_AGENT_MODEL_ENV = "AIDEN_BENCHMARK_AGENT_MODEL"
BENCHMARK_AGENT_BASE_URL_ENV = "AIDEN_BENCHMARK_AGENT_BASE_URL"
BENCHMARK_AGENT_API_KEY_ENV = "AIDEN_BENCHMARK_AGENT_API_KEY"

AGENT_CREDENTIAL_KEYS = ("api_key", "relay_api_key")
_AGENT_CREDENTIAL_ENV_REFERENCE = re.compile(
    rf"(?m)^(?P<prefix>\s*(?:{'|'.join(AGENT_CREDENTIAL_KEYS)})\s*=\s*)"
    r"(?P<quote>['\"])(?P<reference>\$[A-Za-z_][A-Za-z0-9_]*)"
    r"(?P=quote)(?P<suffix>\s*(?:#.*)?)$"
)


class AgentConfigManager:
    """Manages agent.toml configuration for WebUI applications.

    This class provides configuration loading, saving, validation, and template
    rendering for agent.toml files.
    """

    def __init__(
        self,
        base_config_dir: Path,
        config_path: Path | None = None,
    ):
        self.base_config_dir = base_config_dir
        self.config_path = config_path or (base_config_dir / "agent.toml")

    def get_config(self) -> tuple[str, str]:
        """Get agent config content.

        Returns:
            Tuple of (content, source) where source is 'saved' or 'generated'.
        """
        if self.config_path.exists():
            saved_content = self.config_path.read_text(encoding="utf-8")
            content = apply_agent_toml_runtime_defaults(saved_content)
            if content != saved_content:
                validate_agent_toml(content)
                write_text_atomic(self.config_path, content)
            return content, "saved"
        content = self._generate_validated_initial_config()
        write_text_atomic(self.config_path, content)
        return content, "generated"

    def save_config(self, content: str) -> tuple[str, str]:
        """Save agent config content.

        Args:
            content: TOML content to save

        Returns:
            Tuple of (content, source='saved')

        Raises:
            ValueError: If content is invalid TOML
        """
        content = apply_agent_toml_runtime_defaults(content)
        validate_agent_toml(content)
        write_text_atomic(self.config_path, content)
        return content, "saved"

    def reset_config(self) -> tuple[str, str]:
        """Reset config to initial state.

        Returns:
            Tuple of (content, source)
        """
        content = self._generate_validated_initial_config(exclude_config_path=True)
        write_text_atomic(self.config_path, content)
        return content, "generated"

    def _generate_validated_initial_config(
        self, *, exclude_config_path: bool = False
    ) -> str:
        content = apply_agent_toml_runtime_defaults(
            self._generate_initial_config(exclude_config_path=exclude_config_path)
        )
        validate_agent_toml(content)
        return content

    def _generate_initial_config(self, *, exclude_config_path: bool = False) -> str:
        """Load the initial config from an explicit file or template."""
        config = self.base_config_dir / "agent.toml"
        config_is_target = config.resolve() == self.config_path.resolve()
        if config.exists() and not (exclude_config_path and config_is_target):
            return config.read_text(encoding="utf-8")

        template = self.base_config_dir / "agent.toml.template"
        if template.exists():
            return render_agent_template(template.read_text(encoding="utf-8"))

        raise missing_agent_config_error(self.base_config_dir)


def missing_agent_config_error(base_config_dir: Path) -> FileNotFoundError:
    return FileNotFoundError(
        "agent.toml is required: provide an explicit agent config or add "
        f"{base_config_dir / 'agent.toml'} or "
        f"{base_config_dir / 'agent.toml.template'}"
    )


def write_text_atomic(path: Path, content: str) -> None:
    """Write text file atomically using temp file."""
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(content, encoding="utf-8")
    tmp.replace(path)


def validate_agent_toml(content: str) -> None:
    """Validate agent.toml content.

    Args:
        content: TOML content to validate

    Raises:
        ValueError: If content is invalid
    """
    if not content.strip():
        raise ValueError("agent.toml cannot be empty")
    try:
        import tomllib
    except ModuleNotFoundError:
        import tomli as tomllib
    try:
        tomllib.loads(content)
    except tomllib.TOMLDecodeError as exc:
        raise ValueError(f"invalid agent.toml: {exc}") from exc


def apply_agent_toml_runtime_defaults(content: str) -> str:
    """Add safe benchmark runtime defaults missing from older saved configs."""
    if not content.strip():
        return content

    lines = content.splitlines()
    insert_at = len(lines)
    for index, line in enumerate(lines):
        if line.lstrip().startswith("["):
            insert_at = index
            break

    preamble = "\n".join(lines[:insert_at])
    present = {
        match.group("quoted") or match.group("bare")
        for match in re.finditer(
            r'(?m)^\s*(?:"(?P<quoted>[^"]+)"|(?P<bare>[A-Za-z_][A-Za-z0-9_]*))\s*=',
            preamble,
        )
    }
    missing = [key for key in VOICE_SIDE_EFFECT_DEFAULTS if key not in present]
    if not missing:
        return content

    insert_lines = [f"{key} = false" for key in missing]
    if insert_at < len(lines):
        insert_lines.append("")
    lines[insert_at:insert_at] = insert_lines
    return "\n".join(lines) + "\n"


def render_agent_template(text: str) -> str:
    """Render agent.toml template with environment variables.

    Args:
        text: Template text with {{VAR}} placeholders

    Returns:
        Rendered text with variables substituted
    """
    replacements = {
        BENCHMARK_AGENT_PROVIDER_ENV: os.getenv(BENCHMARK_AGENT_PROVIDER_ENV) or "fake",
        BENCHMARK_AGENT_MODEL_ENV: os.getenv(BENCHMARK_AGENT_MODEL_ENV) or "",
        BENCHMARK_AGENT_BASE_URL_ENV: os.getenv(BENCHMARK_AGENT_BASE_URL_ENV) or "",
        BENCHMARK_AGENT_API_KEY_ENV: os.getenv(BENCHMARK_AGENT_API_KEY_ENV) or "",
        "CONTROL_TOKEN_FILE": "/config/control_token",
    }
    rendered = text
    for key, value in replacements.items():
        escaped = json.dumps(value, ensure_ascii=False)[1:-1]
        rendered = rendered.replace("{{" + key + "}}", escaped)
    return rendered


def materialize_agent_config_credentials(
    content: str,
    *,
    env: Mapping[str, str] | None = None,
) -> str:
    """Replace credential environment references before mounting config in Docker."""
    source_env = os.environ if env is None else env

    def replace(match: re.Match[str]) -> str:
        env_name = match.group("reference")[1:]
        value = source_env.get(env_name)
        if value is None or not value.strip():
            raise ValueError(
                "agent.toml credential references missing environment variable "
                f"{env_name}"
            )
        return (
            match.group("prefix")
            + json.dumps(value, ensure_ascii=False)
            + match.group("suffix")
        )

    return _AGENT_CREDENTIAL_ENV_REFERENCE.sub(replace, content)
