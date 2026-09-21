"""Run paired, isolated #715 trials with the board's model configuration."""
from __future__ import annotations

import argparse
import concurrent.futures
import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import tomllib

import requests
import tomli_w

HERE = Path(__file__).resolve().parent
BENCHMARK = HERE.parents[1]
ROOT = BENCHMARK.parent
BASE = 'b4281646d4a02acc6a169760096fd44448e26a23'
FIXED = '3e9eac9ecd425b4f5e2f1279e0f527d0ec6a85d2'


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def ready(url, process, timeout=120):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f'process exited with {process.returncode} while waiting for {url}')
        try:
            if requests.get(url, timeout=2).ok:
                return
        except requests.RequestException:
            pass
        time.sleep(0.3)
    raise TimeoutError(url)


def stop(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--mobilegym-root', type=Path, required=True)
    parser.add_argument('--fixed-root', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--repeats', type=int, default=10)
    parser.add_argument('--probe-only', action='store_true')
    args = parser.parse_args()
    args.out = args.out.resolve()
    args.out.mkdir(parents=True, exist_ok=False)
    board = tomllib.loads(subprocess.check_output(
        ['ssh', 'luckfox', 'cat /userdata/agent/agent.toml'], text=True))
    model = board['model_settings']['model'].copy()
    provider_name = model['provider']
    provider = board['model_settings']['providers'][provider_name].copy()
    if provider.get('type') != 'deepseek':
        raise RuntimeError('Board active model is not DeepSeek')
    model['log_raw_http'] = False
    config = {
        'model_settings':{'model':model, 'providers':{provider_name:provider}},
        'conversation_settings':board.get('conversation_settings', {}),
        'basic_settings':{'language_timezone':{'locale':'zh-CN'}, 'device':{'device_type':'Android'}},
        'voice_settings':{'classic':{'runtime':{
            'voice_streaming_tts_enabled':False, 'voice_tool_call_speech':False,
            'voice_progress_speech_enabled':False}}},
    }
    config['conversation_settings'].get('agent', {}).pop('input_mode', None)
    public = {'base':BASE, 'fixed':FIXED, 'mobilegym':subprocess.check_output(
        ['git','rev-parse','HEAD'], cwd=args.mobilegym_root, text=True).strip(),
        'model':{k:v for k,v in model.items() if k != 'api_key'},
        'provider_type':provider['type'], 'provider_url':provider.get('base_url','https://api.deepseek.com'),
        'repeats_per_target_per_arm':args.repeats, 'targets':[24,83],
        'suite_sha256':hashlib.sha256((BENCHMARK/'suites/mobilegym_scroll_regression.json').read_bytes()).hexdigest(),
        'replay':'Production touchscreen HID reports; identical 100ms release velocity window; original MobileGym Android fling functions.',
        'limitations':['Host HID timing, not board USB timing.', 'Finite-window velocity estimate, not full Android VelocityTracker.',
                       'Simulation results do not establish real-device success rates.'],
        'process_config_changes':['Disable raw model HTTP logging.', 'Remove obsolete input_mode=text; HTTP chat with no voice side effects.', 'Fresh runtime directory for every trial.']}
    public['started_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    public['conversation_settings'] = config['conversation_settings']
    public['sha256'] = {str(path):hashlib.sha256(path.read_bytes()).hexdigest() for path in
        [HERE/'run.py',HERE/'bridge.py',HERE/'replay.js',
         *(Path(f'/tmp/pr715-{arm}-{binary}') for arm in ('before','after') for binary in ('agent','hid'))]}
    (args.out/'manifest.json').write_text(json.dumps(public, ensure_ascii=False, indent=2))
    processes = []
    logs = []

    def spawn(command, log, cwd=None):
        handle = log.open('w')
        logs.append(handle)
        process = subprocess.Popen(command, cwd=cwd, stdout=handle, stderr=subprocess.STDOUT)
        processes.append(process)
        return process

    try:
        web_port = port()
        web_url = f'http://127.0.0.1:{web_port}'
        web = spawn(['node','node_modules/vite/bin/vite.js','--host','127.0.0.1','--port',str(web_port),'--strictPort'],
                    args.out/'vite.log', args.mobilegym_root)
        ready(web_url, web)
        urls = {}
        for arm in ('before','after'):
            bridge_port = port()
            bridge = spawn([sys.executable,str(HERE/'bridge.py'),'--mobilegym-root',str(args.mobilegym_root),
                '--env-url',web_url,'--capture-bin',f'/tmp/pr715-{arm}-hid','--port',str(bridge_port),
                '--artifacts',str(args.out/arm/'hid')],args.out/f'{arm}-bridge.log')
            urls[arm] = f'http://127.0.0.1:{bridge_port}'
            ready(urls[arm]+'/health',bridge)

        # Fixed-path sanity checks precede model trials and are not counted.
        probes = []
        for arm in ('before','after'):
            for duration in (120,300,600):
                headers = {'benchmark-task-id':f'probe-{arm}'}
                response = requests.post(urls[arm]+'/api/setup', headers=headers,
                    json={'app_ids':['scroll_lab'],'foreground_app_id':'scroll_lab'}, timeout=120)
                response.raise_for_status()
                response = requests.post(urls[arm]+'/api/providers/mnk', headers=headers,
                    json={'operation':'swipe','swipe':{'path':[[500,800],[500,200]],'button':'left','duration_ms':duration}},timeout=30)
                response.raise_for_status()
                records = sorted((args.out/arm/'hid').glob('*.json'),key=lambda p:p.stat().st_mtime)
                data = json.loads(records[-1].read_text())
                probes.append({'arm':arm,'duration_ms':duration,**data['metrics']})
                requests.post(urls[arm]+'/api/release',headers=headers,json={},timeout=20).raise_for_status()
        (args.out/'probes.json').write_text(json.dumps(probes,indent=2))
        print(json.dumps({'probes':probes}),flush=True)
        if args.probe_only:
            return

        def trial(arm, target, repeat):
            task_id = f'find_item_{target:03d}'
            run_id = f'{arm}-{target:03d}-{repeat:02d}'
            with tempfile.TemporaryDirectory(prefix=f'pr715-{arm}-', dir='/tmp') as private:
                runtime = Path(private)
                runtime.chmod(0o700)
                (runtime/'agent.toml').write_text(tomli_w.dumps(config))
                (runtime/'agent.toml').chmod(0o600)
                source = ROOT if arm == 'before' else args.fixed_root
                shutil.copytree(source/'src/agent/config/skills',runtime/'skills')
                agent_port = port()
                agent_url = f'http://127.0.0.1:{agent_port}'
                agent = spawn([f'/tmp/pr715-{arm}-agent','-dir',str(runtime),'-addr',f'127.0.0.1:{agent_port}',
                    '-environment-bridge-mode','-environment-bridge-endpoint',urls[arm],'-benchmark-task-id',run_id],
                    args.out/f'{run_id}-agent.log')
                try:
                    ready(agent_url+'/health',agent)
                    command = [sys.executable,'-m','runner','run','--suite','suites/mobilegym_scroll_regression.json',
                        '--agent-url',agent_url,'--environment-url',urls[arm],'--benchmark-task-id',run_id,
                        '--task-id',task_id,'--no-judge','--repeats','1','--out',str(args.out/'runs'),
                        '--run-id',run_id,'--skip-clock-wait','--agent-model',model['model']]
                    with (args.out/f'{run_id}-runner.log').open('w') as logfile:
                        completed = subprocess.run(command,cwd=BENCHMARK,stdout=logfile,stderr=subprocess.STDOUT,timeout=420)
                    results = list((args.out/'runs'/run_id).glob('results.jsonl'))
                    if not results:
                        raise RuntimeError(f'{run_id}: no results (exit {completed.returncode})')
                    rows = [json.loads(line) for line in results[0].read_text().splitlines() if line.strip()]
                    print(json.dumps({'trial':run_id,'results':[{'status':r.get('status'),'success':r.get('metrics',{}).get('success'),
                        'wall_ms':r.get('metrics',{}).get('wall_ms'),'tool_calls':r.get('metrics',{}).get('tool_calls')} for r in rows]}),flush=True)
                    return rows
                finally:
                    stop(agent)

        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            for repeat in range(1,args.repeats+1):
                for target in ((24,83) if repeat % 2 else (83,24)):
                    futures = [pool.submit(trial,arm,target,repeat) for arm in ('before','after')]
                    for future in futures:
                        future.result()
    finally:
        for process in reversed(processes):
            stop(process)
        for handle in logs:
            handle.close()


if __name__ == '__main__':
    main()
