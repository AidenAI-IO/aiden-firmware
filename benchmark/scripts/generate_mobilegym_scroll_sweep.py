"""Generate the MobileGym Scroll Lab depth sweep suite.

One task per list ordinal: the agent scrolls to a target at a known depth and
opens it, and the run fails if the target was ever pushed off the top of the
screen on the way there.

Why ``maxFirstVisibleOrdinal``: Scroll Lab fixes the target at ordinal N, and
counting items as the list scrolls is the actual skill under test. Asserting
``maxFirstVisibleOrdinal <= N`` makes an overshoot irreversible in the record
even when the agent recovers by scrolling back, which is exactly the "scrolled
past it and lost the information" failure this suite measures.

The list does not render ordinals, so the prompt identifies the target by its
record content while the assertions use the ordinal the fixture fixes.

Usage:
    python scripts/generate_mobilegym_scroll_sweep.py
"""

from __future__ import annotations

import json
from pathlib import Path

BENCHMARK_ROOT = Path(__file__).resolve().parents[1]
FIXTURE = BENCHMARK_ROOT / "mobilegym/vendor/mobilegym/apps/ScrollLab/data/defaults.json"
OUTPUT = BENCHMARK_ROOT / "suites/mobilegym_scroll_sweep.json"

SCROLL_TOOL = "touch_gesture"
MAX_TOOL_CALLS = 40
TIMEOUT_SEC = 240

GLOBAL_RESET_PROMPT = (
    "请把设备准备到“列表实验室”的列表顶部：如果不在该应用，先回到主屏幕并打开“列表实验室”；"
    "如果在详情页，先返回列表；最后滚动到列表最顶部。准备好后简短回复。"
)


def build_task(ordinal: int, item: dict[str, object]) -> dict[str, object]:
    padded = f"{ordinal:03d}"
    title = str(item["title"]["zh"])  # type: ignore[index]
    preview = str(item["preview"]["zh"])  # type: ignore[index]
    return {
        "id": f"find_item_{padded}",
        "category": "multi_step",
        "prompt": (
            f"在“列表实验室”的长列表中向下滚动，找到第 {ordinal} 项（标题为“{title}”），"
            "点开它的详情页并报告它的记录编号。不要使用搜索。"
        ),
        "description_for_judge": (
            f"Agent must scroll the Scroll Lab list down to item {ordinal} of 100 and open "
            f"its detail page. The target record is titled {title} with preview {preview}, "
            f"and sits at the fixed position {ordinal} of 100 with record code SL-{padded}. "
            "Scrolling past it and coming back still counts as an overshoot."
        ),
        "rubric": [
            {
                "id": "opened_target_item",
                "check": (
                    f"Post-screenshot shows the Scroll Lab detail page for the record at position "
                    f"{ordinal} / 100 with record code SL-{padded}."
                ),
            }
        ],
        "hard_assertions": {
            "min_tool_calls": 1,
            "max_tool_calls": MAX_TOOL_CALLS,
            "must_complete_within_sec": TIMEOUT_SEC,
            "required_tools": [SCROLL_TOOL],
        },
        "app_ids": ["scroll_lab"],
        "environment_assertions": {
            "route.app": "scroll_lab",
            "route.path": f"/item/scroll-item-{padded}",
            "apps.scroll_lab.selectedItemId": f"scroll-item-{padded}",
            "apps.scroll_lab.maxFirstVisibleOrdinal": {"max": ordinal},
            # Guard: while AIDEN_MOBILEGYM_BLOCK_SCROLLBACK is set the bridge
            # refuses finger-down swipes, so a non-zero count means a rollback
            # slipped through and the one-way premise did not actually hold.
            "apps.scroll_lab.upwardReversals": 0,
        },
        "repeats": 1,
    }


def build_suite() -> dict[str, object]:
    fixture = json.loads(FIXTURE.read_text(encoding="utf-8"))
    items = fixture["items"]
    return {
        "name": "mobilegym_scroll_sweep",
        "suite_category": "MobileGym",
        "version": "1.0",
        "description": (
            "Depth sweep over all 100 Scroll Lab rows. Every task asks the agent to scroll to one "
            "fixed ordinal and open it; environment_assertions require the target to be reached "
            "and never pushed off the top of the screen. The per-depth pass rate shows where "
            "swipe momentum starts overshooting the target."
        ),
        "prompt_prefix": "",
        "global_reset": {
            "type": "agent_prompt",
            "prompt": GLOBAL_RESET_PROMPT,
            "timeout_sec": 120,
            "clear_history_after": True,
        },
        "trace_observations": [],
        "tasks": [build_task(index + 1, item) for index, item in enumerate(items)],
    }


def main() -> None:
    if not FIXTURE.exists():
        raise SystemExit(f"Scroll Lab fixture not found: {FIXTURE}")
    suite = build_suite()
    OUTPUT.write_text(json.dumps(suite, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"wrote {OUTPUT} with {len(suite['tasks'])} tasks")


if __name__ == "__main__":
    main()
