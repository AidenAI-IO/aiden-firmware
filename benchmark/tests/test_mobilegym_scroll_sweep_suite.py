import importlib.util
import json
from pathlib import Path

from runner.suite import load_suite

BENCHMARK_ROOT = Path(__file__).resolve().parents[1]
SUITE_PATH = BENCHMARK_ROOT / "suites" / "mobilegym_scroll_sweep.json"
REGRESSION_PATH = BENCHMARK_ROOT / "suites" / "mobilegym_scroll_regression.json"
GENERATOR_PATH = BENCHMARK_ROOT / "scripts" / "generate_mobilegym_scroll_sweep.py"


def _load_generator():
    spec = importlib.util.spec_from_file_location("scroll_sweep_generator", GENERATOR_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_scroll_regression_covers_medium_and_deep_targets():
    suite = load_suite(REGRESSION_PATH)

    assert suite.name == "mobilegym_scroll_regression"
    assert [task.id for task in suite.tasks] == ["find_item_024", "find_item_083"]
    assert all(task.app_ids == ["scroll_lab"] for task in suite.tasks)
    assert all(task.foreground_app_id == "scroll_lab" for task in suite.tasks)
    assert all(task.hard_assertions.required_tools == ["touch_gesture"] for task in suite.tasks)
    assert suite.global_reset == {}


def test_scroll_regression_depth_bound_matches_each_target():
    suite = load_suite(REGRESSION_PATH)

    for ordinal, task in zip((24, 83), suite.tasks, strict=True):
        padded = f"{ordinal:03d}"
        assert task.environment_assertions == {
            "route.app": "scroll_lab",
            "route.path": f"/item/scroll-item-{padded}",
            "apps.scroll_lab.selectedItemId": f"scroll-item-{padded}",
            "apps.scroll_lab.maxFirstVisibleOrdinal": {"max": ordinal},
        }
        assert f"第 {ordinal} 项" in task.prompt
        assert f"SL-{padded}" not in task.prompt
        assert "报告记录编号" not in task.prompt


def test_generated_suites_match_committed_files():
    generator = _load_generator()
    assert json.loads(REGRESSION_PATH.read_text(encoding="utf-8")) == generator.build_suite()
    assert json.loads(SUITE_PATH.read_text(encoding="utf-8")) == generator.build_suite(calibration=True)


def test_optional_sweep_covers_every_ordinal_without_requiring_scroll_for_visible_rows():
    suite = load_suite(SUITE_PATH)
    assert [task.id for task in suite.tasks] == [f"find_item_{i:03d}" for i in range(1, 101)]
    assert all(task.foreground_app_id == "scroll_lab" for task in suite.tasks)
    assert all(not task.hard_assertions.required_tools for task in suite.tasks)
