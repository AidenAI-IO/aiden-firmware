import json
import pytest
from pathlib import Path
from runner.suite import load_suite, SuiteValidationError

FIXTURE = {
    "name": "test_suite",
    "global_reset": {"tool_sequence": [{"tool": "keyboard_tap", "args": {"keys": ["escape"]}}]},
    "tasks": [
        {
            "id": "open_settings",
            "category": "single_step",
            "description_for_judge": "Agent should open Settings.",
            "prompt": "请打开系统设置。",
            "rubric": [{"id": "in_settings", "check": "Post-screenshot shows Settings."}],
            "hard_assertions": {"min_tool_calls": 1, "max_tool_calls": 8, "must_complete_within_sec": 90},
        }
    ],
}

def test_load_suite_returns_parsed(tmp_path: Path):
    p = tmp_path / "s.json"
    p.write_text(json.dumps(FIXTURE), encoding="utf-8")
    suite = load_suite(p)
    assert suite.name == "test_suite"
    assert len(suite.tasks) == 1
    assert suite.tasks[0].id == "open_settings"
    assert suite.tasks[0].category == "single_step"
    assert suite.tasks[0].rubric[0].id == "in_settings"

def test_load_suite_parses_expected_option_answer(tmp_path: Path):
    fixture = {
        **FIXTURE,
        "tasks": [
            {
                **FIXTURE["tasks"][0],
                "expected_answer": "(c)",
                "answer_format": "option_letter",
            }
        ],
    }
    p = tmp_path / "s.json"
    p.write_text(json.dumps(fixture), encoding="utf-8")

    suite = load_suite(p)

    assert suite.tasks[0].expected_answer == "(c)"
    assert suite.tasks[0].answer_format == "option_letter"

def test_load_suite_parses_expected_recalled_memory_ids(tmp_path: Path):
    fixture = {
        **FIXTURE,
        "tasks": [
            {
                **FIXTURE["tasks"][0],
                "expected_recalled_memory_ids": ["mem_expected"],
            }
        ],
    }
    p = tmp_path / "s.json"
    p.write_text(json.dumps(fixture), encoding="utf-8")

    suite = load_suite(p)

    assert suite.tasks[0].expected_recalled_memory_ids == ["mem_expected"]

def test_load_suite_parses_required_and_forbidden_tools(tmp_path: Path):
    fixture = {
        **FIXTURE,
        "tasks": [
            {
                **FIXTURE["tasks"][0],
                "hard_assertions": {
                    "required_tools": ["enter_plan_mode", "commit_plan", "commit_plan"],
                    "forbidden_tools": ["screenshot"],
                },
            }
        ],
    }
    p = tmp_path / "s.json"
    p.write_text(json.dumps(fixture), encoding="utf-8")

    suite = load_suite(p)

    assert suite.tasks[0].hard_assertions.required_tools == ["enter_plan_mode", "commit_plan"]
    assert suite.tasks[0].hard_assertions.forbidden_tools == ["screenshot"]

def test_load_suite_rejects_invalid_required_tools(tmp_path: Path):
    fixture = {
        **FIXTURE,
        "tasks": [
            {
                **FIXTURE["tasks"][0],
                "hard_assertions": {"required_tools": ["enter_plan_mode", ""]},
            }
        ],
    }
    p = tmp_path / "s.json"
    p.write_text(json.dumps(fixture), encoding="utf-8")

    with pytest.raises(SuiteValidationError):
        load_suite(p)

def test_load_suite_rejects_invalid_expected_recalled_memory_ids(tmp_path: Path):
    fixture = {
        **FIXTURE,
        "tasks": [
            {
                **FIXTURE["tasks"][0],
                "expected_recalled_memory_ids": [""],
            }
        ],
    }
    p = tmp_path / "s.json"
    p.write_text(json.dumps(fixture), encoding="utf-8")

    with pytest.raises(SuiteValidationError):
        load_suite(p)

def test_load_suite_rejects_falsy_invalid_expected_recalled_memory_ids(tmp_path: Path):
    fixture = {
        **FIXTURE,
        "tasks": [
            {
                **FIXTURE["tasks"][0],
                "expected_recalled_memory_ids": "",
            }
        ],
    }
    p = tmp_path / "s.json"
    p.write_text(json.dumps(fixture), encoding="utf-8")

    with pytest.raises(SuiteValidationError):
        load_suite(p)

def test_load_suite_rejects_non_string_prompt_prefix(tmp_path: Path):
    fixture = {**FIXTURE, "prompt_prefix": ["recall_memory"]}
    p = tmp_path / "s.json"
    p.write_text(json.dumps(fixture), encoding="utf-8")

    with pytest.raises(SuiteValidationError):
        load_suite(p)

def test_load_suite_rejects_invalid_expected_option_answer(tmp_path: Path):
    fixture = {
        **FIXTURE,
        "tasks": [
            {
                **FIXTURE["tasks"][0],
                "expected_answer": "z",
                "answer_format": "option_letter",
            }
        ],
    }
    p = tmp_path / "s.json"
    p.write_text(json.dumps(fixture), encoding="utf-8")

    with pytest.raises(SuiteValidationError):
        load_suite(p)

def test_load_suite_rejects_ambiguous_expected_option_answer(tmp_path: Path):
    fixture = {
        **FIXTURE,
        "tasks": [
            {
                **FIXTURE["tasks"][0],
                "expected_answer": "(a) or (b)",
                "answer_format": "option_letter",
            }
        ],
    }
    p = tmp_path / "s.json"
    p.write_text(json.dumps(fixture), encoding="utf-8")

    with pytest.raises(SuiteValidationError):
        load_suite(p)

def test_load_suite_rejects_answer_format_without_expected_answer(tmp_path: Path):
    fixture = {
        **FIXTURE,
        "tasks": [
            {
                **FIXTURE["tasks"][0],
                "answer_format": "option_letter",
            }
        ],
    }
    p = tmp_path / "s.json"
    p.write_text(json.dumps(fixture), encoding="utf-8")

    with pytest.raises(SuiteValidationError):
        load_suite(p)


def test_load_suite_missing_tasks_raises(tmp_path: Path):
    p = tmp_path / "s.json"
    p.write_text(json.dumps({"name": "x"}), encoding="utf-8")
    with pytest.raises(SuiteValidationError):
        load_suite(p)

def test_load_suite_invalid_category_raises(tmp_path: Path):
    bad = {**FIXTURE, "tasks": [{**FIXTURE["tasks"][0], "category": "weird"}]}
    p = tmp_path / "s.json"
    p.write_text(json.dumps(bad), encoding="utf-8")
    with pytest.raises(SuiteValidationError):
        load_suite(p)

def test_load_suite_duplicate_ids_raise(tmp_path: Path):
    bad = {**FIXTURE, "tasks": [FIXTURE["tasks"][0], FIXTURE["tasks"][0]]}
    p = tmp_path / "s.json"
    p.write_text(json.dumps(bad), encoding="utf-8")
    with pytest.raises(SuiteValidationError):
        load_suite(p)


def test_phone_control_suite_uses_cmd_h_for_global_reset():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "phone_control_v1.json"
    suite = load_suite(suite_path)
    sequence = suite.global_reset["tool_sequence"]

    cmd_h_steps = [
        step
        for step in sequence
        if step.get("tool") == "keyboard_tap" and step.get("args", {}).get("keys") == ["meta", "h"]
    ]
    assert len(cmd_h_steps) == 2
    assert all(
        not (step.get("tool") == "touch_gesture" and step.get("args", {}).get("type") == "home")
        for step in sequence
    )


def test_phone_control_suite_constrains_agent_to_iphone_ui():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "phone_control_v1.json"
    suite = load_suite(suite_path)

    assert "iPhone" in suite.prompt_prefix
    assert "macOS" in suite.prompt_prefix
    assert "shell" in suite.prompt_prefix
    assert "osascript" in suite.prompt_prefix


def test_phone_control_tap_back_setup_is_iphone_specific():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "phone_control_v1.json"
    suite = load_suite(suite_path)
    task_by_id = {task.id: task for task in suite.tasks}
    setup = task_by_id["tap_back"].setup

    assert setup.get("tool_sequence")
    tools = [step.get("tool") for step in setup["tool_sequence"]]
    assert "touch_gesture" in tools
    assert "keyboard_text" in tools
    assert any(
        step.get("args", {}).get("text") == "设置"
        for step in setup["tool_sequence"]
        if step.get("tool") == "keyboard_text"
    )


def test_phone_control_bluetooth_task_uses_chinese_keyword():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "phone_control_v1.json"
    suite = load_suite(suite_path)
    task = next(task for task in suite.tasks if task.id == "settings_search_bluetooth")

    assert "蓝牙" in task.prompt
    assert "Bluetooth" not in task.prompt
    assert any("蓝牙" in item.check for item in task.rubric)

def test_loop_planning_suite_uses_tool_hard_assertions():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "loop_planning_v1.json"
    suite = load_suite(suite_path)
    task_by_id = {task.id: task for task in suite.tasks}

    assert suite.name == "loop_planning_v1"
    assert "screenshot" in suite.prompt_prefix
    assert task_by_id["direct_answer_no_plan"].hard_assertions.forbidden_tools
    assert "enter_plan_mode" in task_by_id["single_calculation_no_plan"].hard_assertions.forbidden_tools
    assert "commit_plan" in task_by_id["two_calculation_compare_no_plan"].hard_assertions.forbidden_tools
    assert task_by_id["invoice_reconciliation_requires_plan"].hard_assertions.required_tools == [
        "enter_plan_mode",
        "commit_plan",
        "calculator",
    ]
    assert task_by_id["expense_summary_requires_plan"].hard_assertions.required_tools == [
        "enter_plan_mode",
        "commit_plan",
    ]
    for task in suite.tasks:
        forbidden = set(task.hard_assertions.forbidden_tools)
        assert "screenshot" in forbidden
        assert "touch_gesture" in forbidden
        assert "keyboard_text" in forbidden


def test_device_operator_skillopt_suite_is_ready_for_optimization():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "skillopt" / "device_operator_skillopt_v1.json"
    suite = load_suite(suite_path)
    task_by_id = {task.id: task for task in suite.tasks}
    expected_tasks = {
        "open_settings_from_home",
        "open_clock_from_home",
        "spotlight_type_exact_query",
        "settings_search_bluetooth_zh",
        "navigate_back_from_settings_detail",
        "scroll_settings_until_battery_visible",
        "recover_unknown_state_to_home",
        "open_notification_shade",
        "enter_note_text_after_focus",
        "clear_existing_note_text",
        "avoid_repeating_failed_target",
        "ask_before_privacy_permission",
    }

    assert suite.name == "device_operator_skillopt_v1"
    assert "device-operator" in suite.prompt_prefix
    assert "screenshot" in suite.prompt_prefix
    assert "shell" in suite.prompt_prefix
    assert expected_tasks <= set(task_by_id)
    assert len(suite.tasks) >= 12
    assert all(task.category in {"single_step", "multi_step"} for task in suite.tasks)

    n_train = int(len(suite.tasks) * 0.7)
    selection_tasks = suite.tasks[n_train:]
    assert n_train > 0
    assert selection_tasks

    assert any(obs.skill_name == "device-operator" for obs in suite.trace_observations)
    assert any(
        "same coordinate" in item.check or "相同坐标" in item.check
        for item in task_by_id["avoid_repeating_failed_target"].rubric
    )
    assert any(
        "permission" in item.check or "隐私" in item.check
        for item in task_by_id["ask_before_privacy_permission"].rubric
    )
    for task in suite.tasks:
        for step in (task.setup or {}).get("tool_sequence", []):
            if step.get("tool") == "keyboard_text":
                text = step.get("args", {}).get("text", "")
                assert text.isascii(), f"{task.id} setup keyboard_text must be ASCII: {text!r}"


def test_device_operator_skillopt_validation_suite_is_held_out():
    suites_root = Path(__file__).resolve().parents[1] / "suites" / "skillopt"
    train_suite = load_suite(suites_root / "device_operator_skillopt_v1.json")
    validation_suite = load_suite(suites_root / "device_operator_skillopt_validation_v1.json")
    train_ids = {task.id for task in train_suite.tasks}
    validation_ids = {task.id for task in validation_suite.tasks}

    assert validation_suite.name == "device_operator_skillopt_validation_v1"
    assert "device-operator" in validation_suite.prompt_prefix
    assert len(validation_suite.tasks) >= 4
    assert validation_ids.isdisjoint(train_ids)
    assert any(obs.skill_name == "device-operator" for obs in validation_suite.trace_observations)
    assert all(task.category in {"single_step", "multi_step"} for task in validation_suite.tasks)
    for task in validation_suite.tasks:
        for step in (task.setup or {}).get("tool_sequence", []):
            if step.get("tool") == "keyboard_text":
                text = step.get("args", {}).get("text", "")
                assert text.isascii(), f"{task.id} setup keyboard_text must be ASCII: {text!r}"

def test_memory_suite_covers_representative_memory_behaviors():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "memory_v1.json"
    suite = load_suite(suite_path)
    task_by_id = {task.id: task for task in suite.tasks}

    expected_tasks = {
        "save_explicit_preference",
        "save_user_fact",
        "save_user_rule",
        "save_user_procedure",
        "save_correct_tags",
        "use_preference_brevity",
        "use_preference_language",
        "use_rule_to_block_action",
        "use_procedure_steps",
        "recall_saved_fact_after_setup",
        "recall_session_chunk_details",
        "update_changed_preference",
        "use_filtered_subset",
        "forget_on_request",
        "avoid_saving_ephemeral_fact",
        "no_save_for_ephemeral_chat",
        "no_recall_when_in_context",
    }
    assert expected_tasks <= set(task_by_id)
    assert len(suite.tasks) >= 17
    assert all(task.category == "memory" for task in suite.tasks)

    assert task_by_id["use_preference_brevity"].setup["type"] == "agent_prompt"
    assert task_by_id["use_preference_language"].setup["tool_sequence"]
    assert task_by_id["use_rule_to_block_action"].setup["tool_sequence"]
    assert task_by_id["use_procedure_steps"].setup["tool_sequence"]
    assert task_by_id["recall_saved_fact_after_setup"].setup["type"] == "agent_prompt"
    assert task_by_id["recall_session_chunk_details"].setup["tool_sequence"]
    assert task_by_id["use_filtered_subset"].setup["tool_sequence"]
    assert any(
        "recall_session_chunks" in item.check
        for item in task_by_id["recall_session_chunk_details"].rubric
    )
    assert task_by_id["forget_on_request"].setup["type"] == "agent_prompt"

def test_memory_suite_global_reset_tolerates_missing_memory_dir():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "memory_v1.json"
    suite = load_suite(suite_path)
    command = suite.global_reset["tool_sequence"][0]["args"]["command"]

    assert "mkdir -p /userdata/agent/memory" in command
    assert "ls /userdata/agent/memory/ 2>/dev/null || true" in command


def test_personamem_lt_recall_suite_uses_deterministic_answers():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "personamem_lt_recall_v1.json"
    suite = load_suite(suite_path)
    task_by_id = {task.id: task for task in suite.tasks}
    expected_tasks = {
        "personamem_music_recall_fact",
        "personamem_music_update_reason",
        "personamem_music_preference_evolution",
        "personamem_cooking_generalization",
        "personamem_music_creative_getaway",
        "personamem_music_new_expression",
        "personamem_food_fusion_cuisine",
        "personamem_therapy_yoga_acknowledge",
        "personamem_therapy_games_dislike",
        "personamem_writing_style_discrimination",
        "personamem_book_recommendation_preference",
        "personamem_travel_planning_preference",
        "personamem_online_shopping_preference",
        "personamem_sports_recommendation_preference",
        "personamem_study_consultation_generalization",
        "personamem_family_relations_update_reason",
    }

    assert suite.name == "personamem_lt_recall_v1"
    assert "recall_memory" in suite.prompt_prefix
    assert expected_tasks <= set(task_by_id)
    assert len(suite.tasks) >= 16
    assert all(task.category == "memory" for task in suite.tasks)
    assert all(task.setup and task.setup["tool_sequence"] for task in suite.tasks)
    assert all(task.expected_answer for task in suite.tasks)
    assert all(task.expected_recalled_memory_ids for task in suite.tasks)
    assert all(task.answer_format == "option_letter" for task in suite.tasks)
    for task in suite.tasks:
        for step in task.setup["tool_sequence"]:
            if step.get("tool") != "shell":
                continue
            command = step["args"]["command"]
            assert "<<'EOF'" in command
            assert "\nEOF" in command


def test_episode_memory_suite_guards_against_setup_context_leakage():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "episode_memory_v1.json"
    suite = load_suite(suite_path)
    task_by_id = {task.id: task for task in suite.tasks}
    task = task_by_id["reuse_success_episode_for_planning"]

    assert task.setup["type"] == "agent_prompt"
    assert task.setup["clear_history_after"] is True
    assert any(
        "recall_device_memory" in item.check and "inspect_episode" in item.check
        for item in task.rubric
    )
