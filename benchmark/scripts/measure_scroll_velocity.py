"""Check whether the MobileGym simulator models velocity-dependent fling.

On a real phone the swipe duration changes the fling distance: a slow drag barely
coasts while a fast flick flies past. This script holds the finger travel fixed
and sweeps the duration, which isolates whether the simulator's inertia responds
to velocity at all.

    python scripts/measure_scroll_velocity.py --env-url http://localhost:4173
"""

from __future__ import annotations

import argparse
import asyncio
import sys
from pathlib import Path

BENCHMARK_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(BENCHMARK_ROOT / "mobilegym" / "scripts"))

from start_simulator import create_mobilegym_env, prepare_import_paths, resolve_mobilegym_root  # noqa: E402

DURATIONS_MS = [120, 300, 600, 1200, 2400]
FINGER_START_Y = 800
FINGER_END_Y = 400  # fixed 400 normalized units of travel in every trial


async def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--env-url", default="http://localhost:4173")
    args = parser.parse_args()

    root, _ = resolve_mobilegym_root(None)
    prepare_import_paths(root)

    from bench_env.env.base import Action, ActionType

    env, _ = await create_mobilegym_env(args.env_url, headless=True)
    try:
        await env.reset(app_ids=["scroll_lab"])
        await env.page.evaluate("() => window.__OS__?.openApp('scroll_lab', '/')")
        await asyncio.sleep(1.5)

        scroll_js = """() => {
            const c = document.querySelector('[data-scroll-container="main"]');
            return c ? c.scrollTop : null;
        }"""

        print(f"fixed finger travel: y {FINGER_START_Y} -> {FINGER_END_Y} (400 normalized units)\n")
        print(f"{'duration_ms':>12} | {'scrollTop after':>16} | {'scroll delta':>12}")
        print("-" * 46)
        for duration in DURATIONS_MS:
            # Return to the top without going through the swipe path.
            await env.page.evaluate(
                """() => {
                    const c = document.querySelector('[data-scroll-container="main"]');
                    if (c) c.scrollTop = 0;
                }"""
            )
            await asyncio.sleep(0.4)
            before = await env.page.evaluate(scroll_js)
            action = Action(
                ActionType.SWIPE,
                {
                    "point1": [500, FINGER_START_Y],
                    "point2": [500, FINGER_END_Y],
                    "duration": duration,
                },
            )
            await env.step(action)
            await asyncio.sleep(1.2)
            after = await env.page.evaluate(scroll_js)
            delta = None if (before is None or after is None) else after - before
            print(f"{duration:>12} | {str(after):>16} | {str(delta):>12}")
    finally:
        close = getattr(env, "close", None)
        if close is not None:
            await close()
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
