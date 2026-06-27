from pathlib import Path


def _device_operator_skill() -> str:
    repo_root = Path(__file__).resolve().parents[2]
    return (repo_root / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md").read_text(encoding="utf-8")


def test_device_operator_keeps_mobilegym_rules_out_of_base_skill():
    skill = _device_operator_skill()

    assert "There is no `open_app` tool" not in skill
    assert "MobileGym" not in skill
