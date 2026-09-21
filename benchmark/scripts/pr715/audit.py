"""Check frozen inputs, complete trial coverage, traces, and credential hygiene."""
from __future__ import annotations

import argparse
import base64
import hashlib
import json
from pathlib import Path
import struct
import subprocess
import tomllib


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('directory', type=Path)
    parser.add_argument('--check-board-credentials', action='store_true')
    args = parser.parse_args()
    root = args.directory.resolve()
    manifest = json.loads((root/'manifest.json').read_text())
    for name, expected in manifest['sha256'].items():
        assert hashlib.sha256(Path(name).read_bytes()).hexdigest() == expected, name
    suite = Path(__file__).resolve().parents[2]/'suites/mobilegym_scroll_regression.json'
    assert hashlib.sha256(suite.read_bytes()).hexdigest() == manifest['suite_sha256']
    expected_ids = {f'{arm}-{target:03d}-{repeat:02d}' for arm in ('before','after')
                    for target in manifest['targets']
                    for repeat in range(1,manifest['repeats_per_target_per_arm']+1)}
    actual_ids = {p.parent.name for p in (root/'runs').glob('*/results.jsonl')}
    assert actual_ids == expected_ids, {'missing':sorted(expected_ids-actual_ids),'extra':sorted(actual_ids-expected_ids)}
    samples_checked = 0
    swipes_checked = 0
    geometry = json.loads((root/'row-geometry.json').read_text())
    shell_commands = []
    tools = set()
    for run_id in sorted(expected_ids):
        arm, target, _ = run_id.split('-')
        result = json.loads((root/'runs'/run_id/'results.jsonl').read_text())
        task = root/'runs'/run_id/'tasks'/f'find_item_{target}'
        snapshot = json.loads((task/'environment_state.json').read_text())
        trace = json.loads((task/'trace.json').read_text())
        lab = snapshot['apps']['scroll_lab']
        highwater = 1
        records = sorted((root/arm/'hid').glob(f'{run_id}-*.json'))
        for index, path in enumerate(records):
            record = json.loads(path.read_text())
            assert record['task_id'] == run_id
            if index == 0:
                assert record['metrics']['before'] == 0, f'{run_id}: nonzero reset'
            previous_time = -1
            contact_seen = False
            release_seen = False
            for sample in record['samples']:
                raw = base64.b64decode(sample['report'])
                assert len(raw) == 6 and sample['time_ms'] >= previous_time
                x,y = struct.unpack('<HH',raw[2:6])
                assert abs(sample['x']-x*1000/32767) < 1e-9
                assert abs(sample['y']-y*1000/32767) < 1e-9
                assert sample['down'] == bool(raw[0]&1)
                if sample['down']:
                    assert not release_seen and raw[0] == 3 and raw[1] == 1
                    contact_seen = True
                elif contact_seen:
                    release_seen = True
                previous_time = sample['time_ms']
                samples_checked += 1
            assert contact_seen and release_seen
            current = record['state']['apps']['scroll_lab']['maxFirstVisibleOrdinal']
            offset = record['metrics']['after']
            first_visible = next(row['ordinal'] for row in geometry['rows']
                if row['bottom'] > offset and row['top'] < offset+geometry['container_height'])
            assert first_visible == record['state']['apps']['scroll_lab']['firstVisibleOrdinal']
            assert current >= highwater, f'{run_id}: high-water decreased'
            highwater = current
            swipes_checked += 1
        assert lab['maxFirstVisibleOrdinal'] >= highwater
        if result['metrics'].get('success'):
            assert result['status'] == 'passed'
            assert lab['maxFirstVisibleOrdinal'] <= int(target)
            assert lab['selectedItemId'] == f'scroll-item-{target}'
            assert snapshot['route']['path'] == f'/item/scroll-item-{target}'
            assert result['metrics']['tool_calls'] <= 40
            assert result['metrics']['wall_ms'] <= 240_000
        for call in trace['tool_calls']:
            tools.add(call['tool'])
            if call['tool'] == 'shell':
                shell_commands.append({'trial':run_id,'command':call['input'].get('command','')})
    suspicious = [entry for entry in shell_commands if '/tool-results/' not in entry['command']]
    secret_matches = []
    if args.check_board_credentials:
        board = tomllib.loads(subprocess.check_output(['ssh','luckfox','cat /userdata/agent/agent.toml'],text=True))
        secrets = [provider.get('api_key','').encode() for provider in board['model_settings']['providers'].values()
                   if provider.get('api_key') and not provider['api_key'].startswith('$')]
        for path in root.rglob('*'):
            if path.is_file() and path.suffix in {'.json','.jsonl','.log','.md','.csv','.html','.txt'}:
                data = path.read_bytes()
                if any(secret in data for secret in secrets):
                    secret_matches.append(str(path.relative_to(root)))
        assert not secret_matches, {'credential_match_files':secret_matches}
    output = {'trials_checked':len(expected_ids),'swipes_checked':swipes_checked,
              'hid_reports_checked':samples_checked,'tools':sorted(tools),
              'geometry_first_visible_checks':swipes_checked,
              'shell_commands_checked':len(shell_commands),
              'shell_commands_outside_tool_result_reads':suspicious,
              'credential_scan_performed':args.check_board_credentials,
              'credential_match_files':secret_matches}
    (root/'audit.json').write_text(json.dumps(output,indent=2))
    print(json.dumps(output,indent=2))


if __name__ == '__main__':
    main()
