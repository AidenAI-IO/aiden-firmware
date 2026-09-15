from pathlib import Path

from runner.suite import load_suite


def test_mobilegym_list_search_suite_has_fixed_depth_targets():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "mobilegym_list_search.json"
    suite = load_suite(suite_path)

    assert suite.name == "mobilegym_list_search"
    assert [task.id for task in suite.tasks] == [
        "find_mid_list_target_024",
        "find_deep_list_target_083",
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

    for task, ordinal in zip(suite.tasks, (24, 83), strict=True):
        padded = f"{ordinal:03d}"
        assert f"Aiden 滚动目标 {padded}" in task.prompt
        assert f"position {ordinal} of 100" in task.description_for_judge
        assert f"SL-{padded}" in task.rubric[0].check
