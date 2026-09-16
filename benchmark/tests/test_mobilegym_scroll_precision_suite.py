from pathlib import Path

from runner.suite import load_suite


def _load_scroll_precision_suite():
    suite_path = Path(__file__).resolve().parents[1] / "suites" / "mobilegym_scroll_precision.json"
    return load_suite(suite_path)


def test_mobilegym_scroll_precision_suite_targets_one_screen_band():
    suite = _load_scroll_precision_suite()

    assert suite.name == "mobilegym_scroll_precision"
    assert [task.id for task in suite.tasks] == [
        "mouse_scroll_exactly_one_screen",
        "swipe_exactly_one_screen",
    ]
    assert [task.app_ids for task in suite.tasks] == [["scroll_lab"], ["scroll_lab"]]
    assert [task.hard_assertions.required_tools for task in suite.tasks] == [
        ["mouse_scroll"],
        ["touch_gesture"],
    ]
    for task in suite.tasks:
        assert task.environment_assertions == {
            "route.app": "scroll_lab",
            "route.path": "/",
            "apps.scroll_lab.firstVisibleOrdinal": {"min": 4, "max": 10},
        }
    assert "列表顶部" in suite.global_reset["prompt"]


def test_mobilegym_scroll_precision_prompts_forbid_correction_and_item_taps():
    suite = _load_scroll_precision_suite()

    for task in suite.tasks:
        assert "正好一屏" in task.prompt
        assert "不要点开任何条目" in task.prompt
        assert "只滚动一次" in task.prompt or "只滑动一次" in task.prompt
        # The wheel task must exercise the overshooting tool under test.
        assert task.hard_assertions.max_tool_calls <= 3
        assert task.rubric[0].id == "scrolled_one_screen_no_overshoot"
