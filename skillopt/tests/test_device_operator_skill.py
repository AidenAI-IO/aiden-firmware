import json
from pathlib import Path


def _device_operator_skill() -> str:
    repo_root = Path(__file__).resolve().parents[2]
    return (repo_root / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md").read_text(encoding="utf-8")


def _mobilegym_profile() -> str:
    repo_root = Path(__file__).resolve().parents[2]
    return (repo_root / "skillopt" / "profiles" / "mobilegym" / "device-operator" / "SKILL.md").read_text(encoding="utf-8")


def _suite(name: str) -> dict:
    repo_root = Path(__file__).resolve().parents[2]
    path = repo_root / "skillopt" / "suites" / "device-operator" / name
    return json.loads(path.read_text(encoding="utf-8"))


def test_device_operator_keeps_mobilegym_rules_out_of_base_skill():
    skill = _device_operator_skill()

    assert "There is no `open_app` tool" not in skill
    assert "MobileGym" not in skill


def test_device_operator_keeps_loop_routing_policy_out_of_skill():
    skill = _device_operator_skill()

    assert "plan mode" not in skill.lower()
    assert "routing" not in skill.lower()
    assert "route phase" not in skill.lower()
    assert "task timeouts" not in skill


def test_device_operator_failed_attempt_rules_are_consistent():
    from skillopt.skill_lint import lint_skill_text

    assert lint_skill_text(_device_operator_skill()) == []


def test_device_operator_stops_after_repeated_unlock_failures():
    skill = _device_operator_skill()

    assert "device is detected to be locked" in skill
    assert "standard unlock gestures" in skill
    assert "switch to diagnosis or report the locked device as a blocker" in skill
    assert "Do not keep repeating unlock gestures" in skill


def test_mobilegym_profile_documents_text_entry_fallback():
    profile = _mobilegym_profile()

    assert "enter_text_in_field" in profile
    assert "keyboard_text" in profile
    assert "not in the tool catalog" in profile


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


def test_commerce_ebay_rubrics_allow_honest_no_result_blockers():
    required = {
        "crossapp_train.json": {
            "crossapp_commerce_ebay_lowest_price_notes": ["ebay_search_performed", "price_compared"],
        },
        "crossapp_verification.json": {
            "crossapp_commerce_ebay_balance_diff_notes": ["ebay_price_found"],
            "crossapp_commerce_price_budget_stop": ["budget_price_checked"],
        },
    }

    for suite_name, expected in required.items():
        tasks = {task["id"]: task for task in _suite(suite_name)["tasks"]}
        for task_id, rubric_ids in expected.items():
            rubrics = {rubric["id"]: rubric["check"] for rubric in tasks[task_id]["rubric"]}
            for rubric_id in rubric_ids:
                check = rubrics[rubric_id].lower()
                assert "unavailable" in check or "no visible" in check or "no result" in check


def test_device_operator_stops_when_hid_text_entry_is_unavailable():
    skill = _device_operator_skill()

    assert "/dev/hidg" in skill
    assert "Do not fall back to `keyboard_text` for Chinese/CJK" in skill
    assert "at most one fresh screenshot" in skill
    assert "report the blocker" in skill


def test_device_operator_never_probes_sensitive_permission_toggles():
    skill = _device_operator_skill()

    assert "Do not tap a privacy permission switch" in skill
    assert "just to inspect" in skill
    assert "ask before touching the switch" in skill


def test_device_operator_uses_handoff_when_waiting_for_sensitive_confirmation():
    skill = _device_operator_skill()

    assert "call `request_human_handoff` immediately" in skill
    assert "Do not ask in prose and then continue using tools" in skill
