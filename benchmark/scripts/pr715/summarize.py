"""Derive comparison tables from saved runner and HID artifacts."""
from __future__ import annotations

import argparse
import csv
import json
import math
from pathlib import Path
import statistics


def wilson(successes, count):
    if not count:
        return [0, 1]
    z = 1.959963984540054
    p = successes / count
    denominator = 1 + z*z/count
    center = (p + z*z/(2*count))/denominator
    radius = z*math.sqrt(p*(1-p)/count + z*z/(4*count*count))/denominator
    return [center-radius, center+radius]


def fisher(a, b, c, d):
    n1, n2, successes = a+b, c+d, a+c
    denominator = math.comb(n1+n2, successes)
    probability = lambda x: math.comb(n1,x)*math.comb(n2,successes-x)/denominator
    observed = probability(a)
    return min(1, sum(probability(x) for x in range(max(0,successes-n2), min(n1,successes)+1)
                      if probability(x) <= observed+1e-12))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('directory', type=Path)
    args = parser.parse_args()
    root = args.directory.resolve()
    manifest = json.loads((root/'manifest.json').read_text())
    targets = manifest['targets']
    geometry_path = root/'row-geometry.json'
    geometry = json.loads(geometry_path.read_text()) if geometry_path.exists() else None
    rows = []
    for path in sorted((root/'runs').glob('*/results.jsonl')):
        run_id = path.parent.name
        arm, target_text, repeat = run_id.split('-')
        target = int(target_text)
        results = [json.loads(line) for line in path.read_text().splitlines() if line]
        assert len(results) == 1, path
        result = results[0]
        metrics = result['metrics']
        artifact = path.parent/'tasks'/result['task_id']
        state = json.loads((artifact/'environment_state.json').read_text())
        lab = state['apps']['scroll_lab']
        trace = json.loads((artifact/'trace.json').read_text())
        hid = [json.loads(p.read_text()) for p in sorted((root/arm/'hid').glob(f'{run_id}-*.json'))]
        if not hid:
            # Early pilot artifacts were named by environment episode instead
            # of run ID. Do not misreport absent matching files as zero swipes.
            print(f'Warning: no named HID records for {run_id}; swipe metrics unavailable')
        coasts = [abs(record['metrics']['coast_px']) for record in hid]
        opened = lab.get('selectedItemId') == f'scroll-item-{target:03d}' and state['route']['path'] == f'/item/scroll-item-{target:03d}'
        overshoot = lab['maxFirstVisibleOrdinal'] > target
        terminal_path = root/arm/'hid'/f'{run_id}.terminal.json'
        terminal = json.loads(terminal_path.read_text()) if terminal_path.exists() else None
        success = (terminal is not None and terminal['reason'] == 'target_visible') if manifest.get('visibility_goal') else (metrics.get('success') is True and terminal is None)
        overshoot_phase = ''
        if overshoot and hid and geometry:
            target_bottom = geometry['rows'][target-1]['bottom']
            overshoot_phase = 'inertia' if hid[-1]['metrics']['at_release'] < target_bottom else 'contact'
        rows.append({'run_id':run_id,'arm':arm,'target':target,'repeat':int(repeat),
            'status':('passed' if success else 'failed') if manifest.get('visibility_goal') else result['status'],
            'success':success, 'runner_status':result['status'],
            'terminal_reason':terminal['reason'] if terminal else '',
            'first_visible':lab['firstVisibleOrdinal'],
            'last_visible':terminal['evidence'].get('last') if terminal else None,
            'target_visible_px':terminal['evidence'].get('target_visible_px') if terminal else None,
            'overshoot_phase':overshoot_phase,
            'opened_target':opened,'overshoot':overshoot,'opened_after_overshoot':opened and overshoot,
            'max_first_visible':lab['maxFirstVisibleOrdinal'], 'selected_item':lab.get('selectedItemId'),
            'wall_sec':metrics['wall_ms']/1000, 'tool_calls':metrics['tool_calls'],
            'llm_calls':metrics.get('llm_calls'), 'input_tokens':metrics.get('input_tokens'),
            'output_tokens':metrics.get('output_tokens'), 'swipes':len(hid) if hid else None,
            'reverse_swipes':sum(r['request']['swipe']['path'][-1][1] > r['request']['swipe']['path'][0][1] for r in hid) if hid else None,
            'mean_coast_px':statistics.mean(coasts) if coasts else None,
            'max_coast_px':max(coasts) if coasts else None,
            'tool_errors':metrics.get('tool_errors'),
            'hard_failure_ids':';'.join(f['id'] for f in result.get('hard_assertion_failures',[])),
            'report':str(path.parent.relative_to(root)/'report.html')})
    summary = {}
    for arm in ('before','after'):
        for target in [*targets,None]:
            selected = [r for r in rows if r['arm'] == arm and (target is None or r['target'] == target)]
            if not selected:
                continue
            count = len(selected)
            successes = sum(r['success'] for r in selected)
            summary[f'{arm}-{target or "all"}'] = {'n':count,'successes':successes,
                'rate':successes/count,'wilson_95':wilson(successes,count),
                'overshoots':sum(r['overshoot'] for r in selected),
                'opened_targets':sum(r['opened_target'] for r in selected),
                'opened_after_overshoot':sum(r['opened_after_overshoot'] for r in selected),
                'median_wall_sec':statistics.median(r['wall_sec'] for r in selected),
                'median_tool_calls':statistics.median(r['tool_calls'] for r in selected),
                'median_swipes':statistics.median(r['swipes'] for r in selected if r['swipes'] is not None)
                    if any(r['swipes'] is not None for r in selected) else None}
    comparisons = {}
    for target in [*targets,'all']:
        before, after = summary.get(f'before-{target}'), summary.get(f'after-{target}')
        if before and after:
            comparisons[str(target)] = {'difference_percentage_points':100*(after['rate']-before['rate']),
                'fisher_two_sided_p':fisher(before['successes'],before['n']-before['successes'],
                                          after['successes'],after['n']-after['successes'])}
    mechanical = {}
    for arm in ('before','after'):
        valid_ids = {r['run_id'] for r in rows if r['arm'] == arm}
        records = [json.loads(p.read_text()) for p in (root/arm/'hid').glob('*.json')]
        records = [r for r in records if r.get('task_id') in valid_ids and 'metrics' in r]
        coasts = [abs(r['metrics']['coast_px']) for r in records]
        if coasts:
            mechanical[arm] = {'swipes':len(coasts),'nonzero_coasts':sum(c>0 for c in coasts),
                'median_coast_px':statistics.median(coasts),'mean_coast_px':statistics.mean(coasts),
                'min_coast_px':min(coasts),'max_coast_px':max(coasts)}
    output = {'groups':summary,'comparisons':comparisons,'mechanical':mechanical,'trials':rows}
    (root/'comparison.json').write_text(json.dumps(output,ensure_ascii=False,indent=2))
    if rows:
        with (root/'comparison.csv').open('w',newline='') as handle:
            writer = csv.DictWriter(handle,fieldnames=list(rows[0]),lineterminator='\n')
            writer.writeheader()
            writer.writerows(rows)
    lines = ['# PR #715 Scroll Comparison','',
        '| Target | Before | After | Difference | Fisher two-sided p |',
        '| --- | ---: | ---: | ---: | ---: |']
    for target in [*targets,'all']:
        before, after = summary.get(f'before-{target}'), summary.get(f'after-{target}')
        if before and after:
            value = comparisons[str(target)]
            lines.append(f'| {target} | {before["successes"]}/{before["n"]} ({before["rate"]:.0%}) | '
                         f'{after["successes"]}/{after["n"]} ({after["rate"]:.0%}) | '
                         f'{value["difference_percentage_points"]:+.1f} pp | {value["fisher_two_sided_p"]:.4f} |')
    for arm in manifest.get('arms', ['before','after']):
        total = summary.get(f'{arm}-all')
        if total:
            lines.extend(['',f'{arm}: {total["successes"]}/{total["n"]} ({total["rate"]:.0%}).'])
    criterion = ('Primary success: target intersects the viewport after a fully settled swipe. Stop immediately on success or overshoot; no detail click required. Raw runner scores still evaluate the original detail-page task and are secondary diagnostics.' if manifest.get('visibility_goal') else
        'Success uses the unchanged suite: correct selected item and route, no target overshoot, '
                  'at most 40 tool calls, and at most 240 seconds. Returning to a passed target cannot undo failure.'
    )
    lines.extend(['',criterion,
                  '', 'These are simulated HID-report replay results with DeepSeek, not real-device success rates.',
                  '', '| Trial | Success | Target Opened | Overshoot | Maximum First Row | Seconds | Tool Calls |',
                  '| --- | --- | --- | --- | ---: | ---: | ---: |'])
    for row in rows:
        lines.append(f'| [{row["run_id"]}]({row["report"]}) | {row["success"]} | {row["opened_target"]} | '
                     f'{row["overshoot"]} | {row["max_first_visible"]} | {row["wall_sec"]:.1f} | {row["tool_calls"]} |')
    (root/'comparison.md').write_text('\n'.join(lines)+'\n')
    print(json.dumps({'groups':summary,'comparisons':comparisons},indent=2))


if __name__ == '__main__':
    main()
