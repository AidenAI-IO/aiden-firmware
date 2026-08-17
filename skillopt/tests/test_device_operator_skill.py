import json
from pathlib import Path


def _device_operator_skill() -> str:
    repo_root = Path(__file__).resolve().parents[2]
    return (repo_root / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md").read_text(encoding="utf-8")


def _suite(name: str) -> dict:
    repo_root = Path(__file__).resolve().parents[2]
    path = repo_root / "skillopt" / "suites" / "device-operator" / name
    return json.loads(path.read_text(encoding="utf-8"))


def test_device_operator_failed_attempt_rules_are_consistent():
    from skillopt.skill_lint import lint_skill_text

    assert lint_skill_text(_device_operator_skill()) == []


def test_crossapp_suites_are_separate_from_legacy_device_operator_suites():
    legacy_train_ids = {task["id"] for task in _suite("device_operator_train.json")["tasks"]}
    legacy_verification_ids = {task["id"] for task in _suite("device_operator_verification.json")["tasks"]}
    crossapp_train_ids = {task["id"] for task in _suite("crossapp_train.json")["tasks"]}
    crossapp_verification_ids = {task["id"] for task in _suite("crossapp_verification.json")["tasks"]}

    assert "open_settings_from_home" in legacy_train_ids
    assert "validation_open_settings_via_search" in legacy_verification_ids
    assert all(not task_id.startswith("crossapp_") for task_id in legacy_train_ids | legacy_verification_ids)
    assert "crossapp_commerce_ebay_lowest_price_notes" in crossapp_train_ids
    assert "crossapp_commerce_ebay_balance_diff_notes" in crossapp_verification_ids
