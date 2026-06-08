"""Skill override via temporary file replacement.

Context manager that swaps a SKILL.md file, triggers agent reload, runs
code under the swap, and restores the original on exit. Used by the
orchestrator to evaluate candidate skills without permanently modifying
the skill index.
"""
from __future__ import annotations
import os
import shutil
import tempfile
from contextlib import contextmanager
from pathlib import Path

from runner.agent_client import AgentClient, AgentRequestError


def _reload_skills(client: AgentClient) -> None:
    try:
        client._post("/api/skills/reload", timeout=5)
    except AgentRequestError as exc:
        if "HTTP 404" not in str(exc):
            raise
        # Older board agents do not expose /api/skills/reload, but clearing the
        # run state makes the next skill_read observe the updated on-disk skill.
        client._post("/api/clear", timeout=30)


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
    if not skill_path.exists():
        raise FileNotFoundError(f"skill file not found: {skill_path}")

    backup_fd, backup_name = tempfile.mkstemp(
        prefix=f"{skill_path.stem}.",
        suffix=".backup",
        dir=str(skill_path.parent),
    )
    os.close(backup_fd)
    backup_path = Path(backup_name)

    # Backup original
    shutil.copy2(skill_path, backup_path)
    try:
        # Write candidate
        skill_path.write_text(candidate_content, encoding="utf-8")
        # Trigger reload so the next chat sees the candidate
        _reload_skills(client)
        yield
    finally:
        # Restore original
        shutil.move(str(backup_path), str(skill_path))
        # Reload again so future calls see the restored skill
        try:
            _reload_skills(client)
        except Exception as exc:
            raise RuntimeError("skill reload failed after disk restore") from exc
