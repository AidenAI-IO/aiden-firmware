"""Skill override via temporary file replacement.

Context manager that swaps a SKILL.md file, triggers agent reload, runs
code under the swap, and restores the original on exit. Used by the
orchestrator to evaluate candidate skills without permanently modifying
the skill index.
"""
from __future__ import annotations
import shutil
from contextlib import contextmanager
from pathlib import Path

from runner.agent_client import AgentClient


@contextmanager
def with_skill_override(
    client: AgentClient,
    skill_path: Path,
    candidate_content: str,
):
    """Temporarily replace a SKILL.md file and reload the agent's skill index.

    Usage:
        with with_skill_override(client, Path(".../<skill>/SKILL.md"), candidate):
            # Agent now sees candidate_content as the skill.
            result = client.chat("...")
        # Agent skill index restored to original.

    The agent's /api/skills/reload endpoint is called after writing the
    candidate, forcing the next Run to load fresh from disk.
    """
    backup_path = skill_path.with_suffix(".md.backup")
    if not skill_path.exists():
        raise FileNotFoundError(f"skill file not found: {skill_path}")

    # Backup original
    shutil.copy2(skill_path, backup_path)
    try:
        # Write candidate
        skill_path.write_text(candidate_content, encoding="utf-8")
        # Trigger reload so the next chat sees the candidate
        client._post("/api/skills/reload", timeout=5)
        yield
    finally:
        # Restore original
        shutil.move(str(backup_path), str(skill_path))
        # Reload again so future calls see the restored skill
        try:
            client._post("/api/skills/reload", timeout=5)
        except Exception:
            pass  # Best-effort; original is already restored on disk
