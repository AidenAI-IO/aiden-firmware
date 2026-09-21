"""Experiment-only bridge: replay reports from either production HID revision."""
from __future__ import annotations

import argparse
import asyncio
import json
from pathlib import Path
import signal
import sys
import tempfile

HERE = Path(__file__).resolve().parent
BENCHMARK = HERE.parents[1]
sys.path.insert(0, str(BENCHMARK / 'mobilegym' / 'scripts'))
from start_simulator import create_mobilegym_env, prepare_import_paths


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--mobilegym-root', required=True)
    parser.add_argument('--env-url', required=True)
    parser.add_argument('--capture-bin', required=True)
    parser.add_argument('--port', type=int, required=True)
    parser.add_argument('--artifacts', type=Path, required=True)
    args = parser.parse_args()
    args.artifacts.mkdir(parents=True, exist_ok=True)
    prepare_import_paths(args.mobilegym_root)
    from mobilegym.bridge.episode import BridgeEpisodeState
    from mobilegym.bridge import server
    env, _ = await create_mobilegym_env(args.env_url, headless=True)
    state = BridgeEpisodeState(env, asyncio.get_running_loop())
    bridge = server.BridgeServer(state, port=args.port)
    original_handler_for = server._handler_for
    replay = (HERE / 'replay.js').read_text()
    counter = 0

    async def perform(current, payload, task_id):
        nonlocal counter
        counter += 1
        label = f'{task_id}-{counter:04d}'
        with tempfile.NamedTemporaryFile(suffix='.json') as output:
            process = await asyncio.create_subprocess_exec(
                args.capture_bin, output.name, stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
            stdout, stderr = await process.communicate(json.dumps(payload).encode())
            if process.returncode:
                raise RuntimeError(stderr.decode())
            samples = json.loads(Path(output.name).read_text())
        metrics = await current.page.evaluate(replay, samples)
        await asyncio.sleep(0.1)
        snapshot = await current.get_state(required_apps=['scroll_lab'])
        record = {'episode_id':state.active_episode_id, 'task_id':task_id, 'request':payload,
                  'samples':samples, 'metrics':metrics, 'state':snapshot}
        (args.artifacts / f'{label}.json').write_text(json.dumps(record, ensure_ascii=False, indent=2))
        await current.page.screenshot(path=str(args.artifacts / f'{label}.jpg'), type='jpeg', quality=80)
        print(json.dumps({'episode':state.active_episode_id, **metrics}), flush=True)
        return metrics

    def handler_for(instance):
        base = original_handler_for(instance)
        class Handler(base):
            def _handle_provider_mnk(self, payload):
                if payload.get('operation') != 'swipe':
                    return super()._handle_provider_mnk(payload)
                selected = self._request_state()
                if not selected.active_episode_id:
                    return self._send_json(409, {'error':'no active episode'})
                try:
                    task_id = self.headers.get('benchmark-task-id', 'unknown')
                    instance.submit_to_state(selected, selected.run_env(lambda current: perform(current, payload, task_id)))
                    self._send_json(200, {'success':True})
                except Exception as error:
                    self._send_json(500, {'error':str(error)})
        return Handler
    server._handler_for = handler_for
    print(bridge.start(), flush=True)
    stop = asyncio.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        asyncio.get_running_loop().add_signal_handler(sig, stop.set)
    try:
        await stop.wait()
    finally:
        bridge.stop()
        await env.close()


if __name__ == '__main__':
    asyncio.run(main())
