"""Measure Scroll Lab swipe overshoot directly against a running simulator.

Drives the MobileGym env itself (no agent, no bridge) so the swipe-distance to
scroll-distance relationship can be measured deterministically:

    python scripts/measure_scroll_overshoot.py --env-url http://localhost:4173

Reports the list viewport height, the per-swipe scroll delta, and how many rows
each swipe advances, which is what makes "scroll one screen" overshoot.
"""

from __future__ import annotations

import argparse
import asyncio
import sys
from pathlib import Path

BENCHMARK_ROOT = Path(__file__).resolve().parents[1]
SCRIPTS_DIR = BENCHMARK_ROOT / "mobilegym" / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))

from start_simulator import create_mobilegym_env, prepare_import_paths, resolve_mobilegym_root  # noqa: E402

# Finger travel in normalized 0-1000 units, top-to-bottom pairs.
# A finger moving up (start_y > end_y) scrolls content down, and vice versa.
SWIPES: list[tuple[int, int, int]] = [
    (850, 150, 300),  # a natural "one screen" swipe
    (850, 150, 300),  # repeat: does the second screen behave the same?
    (700, 400, 300),  # short swipe
    (900, 100, 300),  # long swipe
    (400, 700, 300),  # finger down: scroll back up, must not lower the high-water mark
]

SCROLL_LAB_STATE_JS = """() => {
  const c = document.querySelector('[data-scroll-container="main"]');
  const rows = [...document.querySelectorAll('[data-scroll-lab-item]')];
  return {
    viewportHeight: c ? c.clientHeight : null,
    scrollHeight: c ? c.scrollHeight : null,
    rowCount: rows.length,
    firstRowTop: rows.length ? rows[0].getBoundingClientRect().top : null,
    rowHeightSample: rows.slice(0, 6).map(r => Math.round(r.getBoundingClientRect().height)),
  };
}"""


async def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--env-url", default="http://localhost:4173")
    parser.add_argument("--mobilegym-root", default=None)
    args = parser.parse_args()

    root, source = resolve_mobilegym_root(args.mobilegym_root)
    prepare_import_paths(root)
    print(f"mobilegym root: {root} ({source})")

    from bench_env.env.base import Action, ActionType

    env, _config = await create_mobilegym_env(args.env_url, headless=True)
    try:
        await env.reset(app_ids=["scroll_lab"])
        # env.open_app does not navigate the simulator; the OS bridge does.
        await env.page.evaluate("() => window.__OS__?.openApp('scroll_lab', '/')")
        await asyncio.sleep(2.0)

        layout = await env.page.evaluate(SCROLL_LAB_STATE_JS)
        state = await env.get_state()
        scroll = (state.get("apps") or {}).get("scroll_lab") or {}
        print(f"\nlayout: {layout}")
        print(f"initial scroll_lab state: {scroll}\n")

        print(f"{'swipe':>14} | {'scrollTop':>9} | {'first':>5} | {'max':>4} | {'up':>3}")
        print("-" * 52)
        for start_y, end_y, duration in SWIPES:
            action = Action(
                ActionType.SWIPE,
                {"point1": [500, start_y], "point2": [500, end_y], "duration": duration},
            )
            await env.step(action)
            await asyncio.sleep(1.2)
            state = await env.get_state()
            scroll = (state.get("apps") or {}).get("scroll_lab") or {}
            print(
                f"{start_y:>5}->{end_y:<5} | {scroll.get('scrollTop'):>9} | "
                f"{scroll.get('firstVisibleOrdinal'):>5} | {scroll.get('maxFirstVisibleOrdinal'):>4} | "
                f"{scroll.get('upwardReversals'):>3}"
            )
    finally:
        close = getattr(env, "close", None)
        if close is not None:
            await close()
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
