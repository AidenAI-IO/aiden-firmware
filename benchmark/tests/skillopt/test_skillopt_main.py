from pathlib import Path

from runner.skillopt import main


def test_resolve_skill_path_uses_packaged_board_skills(monkeypatch, tmp_path: Path):
    skill_path = tmp_path / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("---\nname: device-operator\n---\n", encoding="utf-8")
    monkeypatch.setattr(main, "REPO_ROOT", tmp_path)

    assert main._resolve_skill_path("device-operator") == skill_path


def test_resolve_skill_path_prefers_env_skills_dir(monkeypatch, tmp_path: Path):
    repo_skill = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    repo_skill.parent.mkdir(parents=True)
    repo_skill.write_text("repo", encoding="utf-8")
    env_root = tmp_path / "custom-skills"
    env_skill = env_root / "device-operator" / "SKILL.md"
    env_skill.parent.mkdir(parents=True)
    env_skill.write_text("env", encoding="utf-8")
    monkeypatch.setattr(main, "REPO_ROOT", tmp_path)
    monkeypatch.setenv("AIDEN_SKILLS_DIR", str(env_root))

    assert main._resolve_skill_path("device-operator") == env_skill
