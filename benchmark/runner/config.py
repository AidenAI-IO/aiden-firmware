"""Shared agent configuration management for Benchmark and SkillOpt WebUIs.

This module provides agent.toml configuration management functionality that can be
used by both benchmark/runner/webui.py and skillopt/webui.py.
"""

from __future__ import annotations

import os
import re
from pathlib import Path


VOICE_SIDE_EFFECT_DEFAULTS = (
    "voice_streaming_tts_enabled",
    "voice_tool_call_speech",
    "voice_progress_speech_enabled",
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
        content = apply_agent_toml_runtime_defaults(self._generate_initial_config())
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
        if self.config_path.exists():
            self.config_path.unlink()
        return self.get_config()

    def _generate_initial_config(self) -> str:
        """Generate initial config from template or default."""
        config = self.base_config_dir / "agent.toml"
        if config.exists():
            return config.read_text(encoding="utf-8")

        template = self.base_config_dir / "agent.toml.template"
        if template.exists():
            return render_agent_template(template.read_text(encoding="utf-8"))

        return default_agent_toml()


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
        return  # Skip validation if tomllib not available
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
        "MODEL_PROVIDER": (
            os.getenv("MODEL_PROVIDER")
            or os.getenv("AIDEN_MODEL_PROVIDER")
            or "fake"
        ),
        "MODEL_NAME": (
            os.getenv("MODEL_NAME")
            or os.getenv("AIDEN_MODEL")
            or os.getenv("OPENAI_MODEL")
            or ""
        ),
        "MODEL_BASE_URL": (
            os.getenv("MODEL_BASE_URL") or os.getenv("AIDEN_MODEL_BASE_URL") or ""
        ),
        "MODEL_API_KEY": (
            os.getenv("MODEL_API_KEY")
            or os.getenv("OPENROUTER_API_KEY")
            or os.getenv("AIDEN_MODEL_API_KEY")
            or ""
        ),
        "CONTROL_TOKEN_FILE": "/config/control_token",
    }
    rendered = text
    for key, value in replacements.items():
        rendered = rendered.replace("{{" + key + "}}", value.replace('"', '\\"'))
    return rendered


def default_agent_toml() -> str:
    """Generate default agent.toml content."""
    return "\n".join(
        [
            'instruction = ""',
            'input_mode = "text"',
            'trigger_mode = "manual"',
            "max_iterations = -1",
            "voice_streaming_tts_enabled = false",
            "voice_tool_call_speech = false",
            "voice_progress_speech_enabled = false",
            "screenshot_keep_n = 3",
            "screenshot_prune_interval = 25",
            "screen_stable_timeout_ms = 3500",
            "screen_stable_ms = 500",
            "screen_stable_diff_threshold = 2",
            "",
            "[model_providers.benchmark]",
            'type = "openrouter"',
            'base_url = ""',
            'api_key = ""',
            "",
            "[model]",
            'provider = "benchmark"',
            'model = "qwen3.6-35b"',
            "temperature = 0.2",
            "max_response_tokens = 1000",
            "",
        ]
    )
