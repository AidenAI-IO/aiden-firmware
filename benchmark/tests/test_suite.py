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


def test_phone_control_suite_uses_touch_home_for_global_reset():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "phone_control_v1.json"
    suite = load_suite(suite_path)
    sequence = suite.global_reset["tool_sequence"]

    home_steps = [
        step
        for step in sequence
        if step.get("tool") == "touch_gesture" and step.get("args", {}).get("type") == "home"
    ]
    assert len(home_steps) == 1
    assert any(
        step.get("tool") == "touch_gesture" and step.get("args", {}).get("type") == "home"
        for step in sequence
    )
    assert any(
        step.get("tool") == "touch_gesture"
        and step.get("args", {}).get("type") == "tap"
        and step.get("args", {}).get("point", {}).get("x", 0) > 0.75
        and step.get("args", {}).get("point", {}).get("y", 1) < 0.08
        for step in sequence
    )
    assert all(step.get("tool") != "keyboard_tap" for step in sequence)


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
    setup_prompt = task_by_id["tap_back"].setup["prompt"]

    assert "iPhone" in setup_prompt
    assert "显示与亮度" in setup_prompt
    assert "shell" in setup_prompt

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
