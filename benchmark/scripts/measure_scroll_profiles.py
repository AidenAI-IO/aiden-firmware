"""Compare swipe profiles through the same MNK HTTP route used by the Agent.

MOBILEGYM_ROOT=/path/to/mobilegym python scripts/measure_scroll_profiles.py \
    --env-url http://127.0.0.1:4173 --output /tmp/scroll-profiles.json

Requires MobileGym with ScrollLab, swipeProfiles and the recent-sample velocity
estimator. This measures the simulator, not phone physics or LLM task success.
"""
from __future__ import annotations

import argparse
import asyncio
import json
import sys
import urllib.request
from pathlib import Path

BENCHMARK_ROOT = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(BENCHMARK_ROOT), str(BENCHMARK_ROOT / "mobilegym/scripts")]

from start_simulator import create_mobilegym_env, prepare_import_paths, resolve_mobilegym_root
from mobilegym.bridge.episode import BridgeEpisodeState
from mobilegym.bridge.server import BridgeServer


async def measure(args: argparse.Namespace) -> list[dict]:
    root, _ = resolve_mobilegym_root(None)
    prepare_import_paths(root)
    env, _ = await create_mobilegym_env(args.env_url, headless=True)
    state = BridgeEpisodeState(env, asyncio.get_running_loop())
    bridge = BridgeServer(state)
    url = bridge.start()

    async def post(path: str, payload: dict) -> dict:
        def send():
            request = urllib.request.Request(url + path, data=json.dumps(payload).encode(),
                headers={"Content-Type": "application/json", "benchmark-task-id": "scroll-profiles"}, method="POST")
            with urllib.request.urlopen(request, timeout=90) as response:
                return json.load(response)
        return await asyncio.to_thread(send)

    records = []
    try:
        for repeat in range(args.repeat):
            for duration in args.durations_ms:
                for profile in ("linear", "decelerate"):
                    await post("/api/setup", {"app_ids": ["scroll_lab"], "foreground_app_id": "scroll_lab"})
                    await env.page.wait_for_selector("[data-scroll-lab-item]")
                    await env.page.evaluate("""() => {
                        if (!window.__SIM_INPUT__?.swipeProfiles?.includes('decelerate')) throw Error('update MobileGym');
                        window.__scrollProfileTrace = {samples: []};
                        for (const type of ['pointerdown', 'pointermove', 'pointerup']) {
                            document.addEventListener(type, e => {
                                const trace = window.__scrollProfileTrace;
                                trace.samples.push({type, x:e.clientX, y:e.clientY, t:performance.now()});
                                if (type === 'pointerup') trace.atRelease = document.querySelector('[data-scroll-container="main"]').scrollTop;
                            }, true);
                        }
                    }""")
                    result = await post("/api/providers/mnk", {"operation": "swipe", "swipe": {
                        "path": [[500, args.start_y], [500, args.end_y]], "button": "left",
                        "duration_ms": duration, "profile": profile,
                    }})
                    if result != {"success": True}:
                        raise RuntimeError(result)
                    action = state.action_log[-1]["mobilegym_action"]["data"]
                    if action["duration"] != duration or action["profile"] != profile:
                        raise RuntimeError(f"MNK options lost: {action}")
                    snapshot = (await post("/state", {}))["data"]["apps"]["scroll_lab"]
                    trace = await env.page.evaluate("() => window.__scrollProfileTrace")
                    record = {"repeat": repeat, "profile": profile, "duration_ms": duration,
                        "path": [[500, args.start_y], [500, args.end_y]],
                        "at_release_px": trace["atRelease"], "final_px": snapshot["scrollTop"],
                        "coast_px": snapshot["scrollTop"] - trace["atRelease"],
                        "max_first_visible": snapshot["maxFirstVisibleOrdinal"], "trace": trace["samples"]}
                    records.append(record)
                    print(json.dumps({k:v for k,v in record.items() if k != "trace"}), flush=True)
    finally:
        await asyncio.to_thread(bridge.stop)
        await env.close()
    return records


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-url", default="http://127.0.0.1:4173")
    parser.add_argument("--durations-ms", nargs="+", type=int, default=[120, 300, 600, 900])
    parser.add_argument("--repeat", type=int, default=1)
    parser.add_argument("--start-y", type=float, default=880)
    parser.add_argument("--end-y", type=float, default=180)
    parser.add_argument("--output", type=Path, required=True)
    arguments = parser.parse_args()
    if arguments.repeat < 1 or any(not 1 <= d <= 10000 for d in arguments.durations_ms):
        parser.error("repeat must be positive; durations must be in [1,10000]")
    results = asyncio.run(measure(arguments))
    arguments.output.write_text(json.dumps(results, indent=2) + "\n")
