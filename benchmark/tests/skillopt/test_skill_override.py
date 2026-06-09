"""Unit tests for skill_override.py."""
import pytest

from runner.agent_client import AgentRequestError
from runner.skillopt.skill_override import with_skill_override


class RecordingClient:
    def __init__(self):
        self.calls = 0

    def _post(self, path: str, timeout: int):
        self.calls += 1
        return {"path": path, "timeout": timeout}


class RestoreFailingClient(RecordingClient):
    def _post(self, path: str, timeout: int):
        self.calls += 1
        if self.calls == 2:
            raise RuntimeError("reload unavailable")
        return {"path": path, "timeout": timeout}


class ReloadNotFoundClient:
    def __init__(self):
        self.paths: list[str] = []

    def _post(self, path: str, timeout: int):
        self.paths.append(path)
        if path == "/api/skills/reload":
            raise AgentRequestError("HTTP 404: endpoint not found")
        return {"path": path, "timeout": timeout}


def test_nested_skill_overrides_restore_each_invocation(tmp_path):
    skill_path = tmp_path / "SKILL.md"
    skill_path.write_text("original", encoding="utf-8")
    client = RecordingClient()

    with with_skill_override(client, skill_path, "outer"):
        assert skill_path.read_text(encoding="utf-8") == "outer"
        with with_skill_override(client, skill_path, "inner"):
            assert skill_path.read_text(encoding="utf-8") == "inner"
        assert skill_path.read_text(encoding="utf-8") == "outer"

    assert skill_path.read_text(encoding="utf-8") == "original"


def test_restore_reload_failure_is_reported_after_disk_restore(tmp_path):
    skill_path = tmp_path / "SKILL.md"
    skill_path.write_text("original", encoding="utf-8")
    client = RestoreFailingClient()

    with pytest.raises(RuntimeError, match="skill reload failed after disk restore"):
        with with_skill_override(client, skill_path, "candidate"):
            assert skill_path.read_text(encoding="utf-8") == "candidate"

    assert skill_path.read_text(encoding="utf-8") == "original"


def test_reload_falls_back_to_clear_when_reload_endpoint_missing(tmp_path):
    skill_path = tmp_path / "SKILL.md"
    skill_path.write_text("original", encoding="utf-8")
    client = ReloadNotFoundClient()

    with with_skill_override(client, skill_path, "candidate"):
        assert skill_path.read_text(encoding="utf-8") == "candidate"

    assert skill_path.read_text(encoding="utf-8") == "original"
    assert client.paths == [
        "/api/skills/reload",
        "/api/clear",
        "/api/skills/reload",
        "/api/clear",
    ]
