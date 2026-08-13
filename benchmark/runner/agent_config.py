from __future__ import annotations

import os
import re
from pathlib import Path
from typing import Mapping


REPO_ROOT = Path(__file__).resolve().parents[2]
VALID_DEVICE_TYPES = {"iOS", "Android", "macOS", "windows", "linux"}


def set_agent_device_type(content: str, device_type: str) -> str:
    """Set device.device_type in a runtime TOML copy while preserving other text."""
    canonical = str(device_type or "").strip()
    if canonical not in VALID_DEVICE_TYPES:
        raise ValueError(f"unsupported device type: {device_type!r}")

    lines = content.splitlines()
    setting = f'device_type = "{canonical}"'
    dotted_key = re.compile(
        r'''^(?P<indent>\s*)(?:device|"device"|'device')\s*\.\s*'''
        r'''(?:device_type|"device_type"|'device_type')\s*=.*$'''
    )
    first_table = next(
        (index for index, line in enumerate(lines) if line.lstrip().startswith("[")),
        len(lines),
    )
    dotted_index = next(
        (index for index, line in enumerate(lines[:first_table]) if dotted_key.match(line)),
        None,
    )
    if dotted_index is not None:
        match = dotted_key.match(lines[dotted_index])
        indent = match.group("indent") if match is not None else ""
        lines[dotted_index] = f'{indent}device.device_type = "{canonical}"'
        return "\n".join(lines) + "\n"

    device_table = re.compile(
        r'''^\s*\[\s*(?:device|"device"|'device')\s*\]\s*(?:#.*)?$'''
    )
    device_header = next(
        (index for index, line in enumerate(lines) if device_table.match(line)),
        None,
    )
    if device_header is None:
        if lines and lines[-1].strip():
            lines.append("")
        lines.extend(["[device]", setting])
    else:
        section_end = next(
            (
                index
                for index in range(device_header + 1, len(lines))
                if lines[index].lstrip().startswith("[")
            ),
            len(lines),
        )
        existing = next(
            (
                index
                for index in range(device_header + 1, section_end)
                if re.match(
                    r'''^\s*(?:device_type|"device_type"|'device_type')\s*=''',
                    lines[index],
                )
            ),
            None,
        )
        if existing is None:
            lines.insert(device_header + 1, setting)
        else:
            lines[existing] = setting
    return "\n".join(lines) + "\n"


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
    sections: dict[str, dict[str, str]] = {}
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = _strip_toml_comment(raw_line)
        if not line:
            continue
        if line.startswith("[") and line.endswith("]"):
            section = line[1:-1].strip()
            continue
        if not section or "=" not in line:
            continue
        key, value = line.split("=", 1)
        sections.setdefault(section, {})[key.strip()] = _parse_toml_string(value)

    model = dict(sections.get("model", {}))
    provider_ref = model.get("provider", "").strip()
    if not provider_ref:
        return model

    canonical = sections.get(f"model_providers.{provider_ref}")
    legacy = sections.get(f"providers.{provider_ref}")
    record = canonical if canonical is not None else legacy
    if record is None:
        return model

    if "type" in record:
        model["provider"] = record["type"].strip()
    elif "provider" in record:
        model["provider"] = record["provider"].strip()
    for key in ("api_key", "base_url"):
        if not model.get(key, "").strip() and record.get(key, "").strip():
            model[key] = record[key]
    return model


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
        if path.is_file():
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
    if path is None or not path.is_file():
        return None

    model = load_agent_model_config(path)
    configured = model.get("api_key", "").strip()
    if not configured:
        return None
    if configured.startswith("$"):
        return env.get(configured[1:].strip()) or None
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
