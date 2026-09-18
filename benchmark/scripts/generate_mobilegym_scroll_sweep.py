"""Generate the Scroll Lab regression and optional depth-calibration suites.

The monotonic maxFirstVisibleOrdinal records an overshoot even if the agent
scrolls back. Run this script after editing the deterministic fixture.
"""

from __future__ import annotations

import json
from pathlib import Path

BENCHMARK_ROOT = Path(__file__).resolve().parents[1]
FIXTURE = BENCHMARK_ROOT / "mobilegym/vendor/mobilegym/apps/ScrollLab/data/defaults.json"
SUITE_DIR = BENCHMARK_ROOT / "suites"
REGRESSION_ORDINALS = (24, 83)


def build_task(ordinal: int, item: dict, *, calibration: bool = False) -> dict:
    padded = f"{ordinal:03d}"
    title = item["title"]["zh"]
    preview = item["preview"]["zh"]
    return {
        "id": f"find_item_{padded}",
        "category": "multi_step",
        "prompt": (
            f"在“列表实验室”的长列表中，必要时向下滚动，找到第 {ordinal} 项（标题为“{title}”），"
            "打开它的详情页。不要使用搜索。"
        ),
        "description_for_judge": (
            f"Open Scroll Lab item {ordinal} of 100, titled {title}, with preview {preview} "
            f"and record code SL-{padded}. Scrolling past it and back still counts as an overshoot."
        ),
        "rubric": [{
            "id": "opened_target_item",
            "check": f"Post-screenshot shows the Scroll Lab detail page for item {ordinal} / 100, code SL-{padded}.",
        }],
        "hard_assertions": {
            "min_tool_calls": 1,
            "max_tool_calls": 40,
            "must_complete_within_sec": 240,
            **({} if calibration else {"required_tools": ["touch_gesture"]}),
        },
        "app_ids": ["scroll_lab"],
        "foreground_app_id": "scroll_lab",
        "environment_assertions": {
            "route.app": "scroll_lab",
            "route.path": f"/item/scroll-item-{padded}",
            "apps.scroll_lab.selectedItemId": f"scroll-item-{padded}",
            "apps.scroll_lab.maxFirstVisibleOrdinal": {"max": ordinal},
        },
        "repeats": 1,
    }


def build_suite(*, calibration: bool = False) -> dict:
    items = json.loads(FIXTURE.read_text(encoding="utf-8"))["items"]
    if len(items) != 100:
        raise ValueError("Scroll Lab fixture must have 100 items")
    ordinals = range(1, 101) if calibration else REGRESSION_ORDINALS
    return {
        "name": "mobilegym_scroll_sweep" if calibration else "mobilegym_scroll_regression",
        "suite_category": "MobileGym",
        "version": "1.0",
        "description": (
            "Optional 100-depth calibration of scroll overshoot."
            if calibration else "Scroll overshoot regression at medium and deep list positions."
        ),
        "prompt_prefix": "",
        "global_reset": {},
        "trace_observations": [],
        "tasks": [build_task(ordinal, items[ordinal - 1], calibration=calibration) for ordinal in ordinals],
    }


def main() -> None:
    for calibration, filename in (
        (False, "mobilegym_scroll_regression.json"),
        (True, "mobilegym_scroll_sweep.json"),
    ):
        suite = build_suite(calibration=calibration)
        output = SUITE_DIR / filename
        output.write_text(json.dumps(suite, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(f"wrote {output} with {len(suite['tasks'])} tasks")


if __name__ == "__main__":
    main()
