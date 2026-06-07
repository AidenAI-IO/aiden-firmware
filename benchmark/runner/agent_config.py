from __future__ import annotations

import os
import re
from pathlib import Path
from typing import Mapping


REPO_ROOT = Path(__file__).resolve().parents[2]
_ENV_NAME_RE = re.compile(r"^[A-Z_][A-Z0-9_]*$")


def _strip_toml_comment(line: str) -> str:
    in_quote = False
    escaped = False
    out: list[str] = []
    for ch in line:
        if ch == '"' and not escaped:
            in_quote = not in_quote
        if ch == "#" and not in_quote:
            break
        out.append(ch)
        escaped = ch == "\\" and not escaped
        if ch != "\\":
            escaped = False
    return "".join(out).strip()


def _parse_toml_string(value: str) -> str:
    value = value.strip()
    if len(value) >= 2 and value[0] == '"' and value[-1] == '"':
        return bytes(value[1:-1], "utf-8").decode("unicode_escape")
    return value


def load_agent_model_config(path: Path) -> dict[str, str]:
    section = ""
    values: dict[str, str] = {}
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = _strip_toml_comment(raw_line)
        if not line:
            continue
        if line.startswith("[") and line.endswith("]"):
            section = line[1:-1].strip()
            continue
        if section != "model" or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = _parse_toml_string(value)
    return values


def default_agent_config_path() -> Path | None:
    candidates = []
    if env_path := os.environ.get("AIDEN_AGENT_CONFIG"):
        candidates.append(Path(env_path))
    candidates.extend([
        Path("/userdata/agent/agent.toml"),
        REPO_ROOT / "agent.toml",
        REPO_ROOT / "src" / "agent" / "config" / "agent.toml",
    ])
    for path in candidates:
        if path.exists():
            return path
    return None


def resolve_agent_model_api_key(
    path: Path | None = None,
    *,
    env: Mapping[str, str] | None = None,
) -> str | None:
    if env is None:
        env = os.environ
    if path is None:
        path = default_agent_config_path()
    if path is None or not path.exists():
        return None

    configured = load_agent_model_config(path).get("api_key", "").strip()
    if not configured:
        return None
    if configured in env and env[configured]:
        return env[configured]
    if _ENV_NAME_RE.fullmatch(configured):
        return None
    return configured


def resolve_api_key(
    api_key_env: str,
    *,
    agent_config_path: str | Path | None = None,
    env: Mapping[str, str] | None = None,
) -> str | None:
    if env is None:
        env = os.environ
    if env.get(api_key_env):
        return env[api_key_env]
    path = Path(agent_config_path) if agent_config_path else None
    return resolve_agent_model_api_key(path, env=env)
