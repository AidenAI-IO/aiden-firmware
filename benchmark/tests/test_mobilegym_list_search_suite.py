from pathlib import Path

from runner.suite import load_suite


def test_mobilegym_list_search_suite_has_fixed_depth_targets():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "mobilegym_list_search.json"
    suite = load_suite(suite_path)

    assert suite.name == "mobilegym_list_search"
    assert [task.id for task in suite.tasks] == [
        "find_mid_content_blue_umbrella",
        "find_deep_content_silver_cold_case",
    ]
    assert [task.app_ids for task in suite.tasks] == [["scroll_lab"], ["scroll_lab"]]
    assert all(task.category == "multi_step" for task in suite.tasks)
    assert all(task.hard_assertions.required_tools == ["touch_gesture"] for task in suite.tasks)
    assert suite.tasks[0].environment_assertions == {
        "route.app": "scroll_lab",
        "route.path": "/item/scroll-item-024",
        "apps.scroll_lab.selectedItemId": "scroll-item-024",
    }
    assert suite.tasks[1].environment_assertions == {
        "route.app": "scroll_lab",
        "route.path": "/item/scroll-item-083",
        "apps.scroll_lab.selectedItemId": "scroll-item-083",
    }
    assert "列表顶部" in suite.global_reset["prompt"]


def test_mobilegym_list_search_prompts_match_judged_targets():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "mobilegym_list_search.json"
    suite = load_suite(suite_path)

    expected_prompts = (
        ("蓝色雨伞", "7312", 24, "SL-024"),
        ("银色保温箱", "K4M7", 83, "SL-083"),
    )
    for task, (content, token, ordinal, code) in zip(suite.tasks, expected_prompts, strict=True):
        padded = f"{ordinal:03d}"
        assert content in task.prompt
        assert token in task.prompt
        assert padded not in task.prompt
        assert f"SL-{padded}" not in task.prompt
        assert f"position {ordinal} of 100" in task.description_for_judge
        assert content in task.rubric[0].check
        assert code in task.rubric[0].check
