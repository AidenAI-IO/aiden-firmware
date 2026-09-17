import importlib.util
import json
from pathlib import Path

from runner.suite import load_suite

BENCHMARK_ROOT = Path(__file__).resolve().parents[1]
SUITE_PATH = BENCHMARK_ROOT / "suites" / "mobilegym_scroll_sweep.json"
GENERATOR_PATH = BENCHMARK_ROOT / "scripts" / "generate_mobilegym_scroll_sweep.py"


def _load_generator():
    spec = importlib.util.spec_from_file_location("scroll_sweep_generator", GENERATOR_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_scroll_sweep_covers_every_ordinal_with_the_scroll_tool():
    suite = load_suite(SUITE_PATH)

    assert suite.name == "mobilegym_scroll_sweep"
    assert [task.id for task in suite.tasks] == [f"find_item_{i:03d}" for i in range(1, 101)]
    assert all(task.app_ids == ["scroll_lab"] for task in suite.tasks)
    # Android derives pointer_mode=touchscreen, where mouse_scroll is rejected,
    # so the real scroll path is the touch gesture.
    assert all(task.hard_assertions.required_tools == ["touch_gesture"] for task in suite.tasks)
    assert "列表顶部" in suite.global_reset["prompt"]


def test_scroll_sweep_depth_bound_matches_each_target():
    suite = load_suite(SUITE_PATH)

    for ordinal, task in enumerate(suite.tasks, start=1):
        padded = f"{ordinal:03d}"
        assert task.environment_assertions == {
            "route.app": "scroll_lab",
            "route.path": f"/item/scroll-item-{padded}",
            "apps.scroll_lab.selectedItemId": f"scroll-item-{padded}",
            "apps.scroll_lab.maxFirstVisibleOrdinal": {"max": ordinal},
            "apps.scroll_lab.upwardReversals": 0,
        }
        assert f"第 {ordinal} 项" in task.prompt
        assert f"SL-{padded}" not in task.prompt


def test_scroll_sweep_file_matches_generator_output():
    generator = _load_generator()
    on_disk = json.loads(SUITE_PATH.read_text(encoding="utf-8"))

    assert on_disk == generator.build_suite()
