"""Measure wholly unseen rows from settled screenshots; does not alter scoring."""
from __future__ import annotations

import argparse
import asyncio
import json
from pathlib import Path
import sys

HERE = Path(__file__).resolve().parent
sys.path.insert(0,str(HERE.parents[1]/'mobilegym/scripts'))
from start_simulator import create_mobilegym_env, prepare_import_paths


async def geometry(mobilegym_root, env_url):
    prepare_import_paths(mobilegym_root)
    env, _ = await create_mobilegym_env(env_url,headless=True)
    try:
        await env.reset(app_ids=['scroll_lab'])
        await env.page.evaluate("() => window.__OS__.openApp('scroll_lab','/')")
        await env.page.wait_for_selector('[data-scroll-lab-item]')
        return await env.page.evaluate('''() => {
            const c=document.querySelector('[data-scroll-container="main"]');
            const r=c.getBoundingClientRect();
            return {viewport_width:innerWidth, viewport_height:innerHeight, dpr:devicePixelRatio,
                container_top:r.top, container_height:c.clientHeight,
                rows:[...c.querySelectorAll('[data-scroll-lab-item]')].map((el,index)=>{
                    const b=el.getBoundingClientRect();
                    return {ordinal:index+1,id:el.getAttribute('data-scroll-lab-item'),
                        top:b.top-r.top+c.scrollTop,bottom:b.bottom-r.top+c.scrollTop};
                })};
        }''')
    finally:
        await env.close()


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('directory',type=Path)
    parser.add_argument('--mobilegym-root')
    parser.add_argument('--env-url')
    args=parser.parse_args()
    root=args.directory.resolve()
    geometry_path=root/'row-geometry.json'
    if not geometry_path.exists():
        if not args.mobilegym_root or not args.env_url:
            parser.error('first run requires --mobilegym-root and --env-url')
        data=asyncio.run(geometry(args.mobilegym_root,args.env_url))
        geometry_path.write_text(json.dumps(data,indent=2))
    geom=json.loads(geometry_path.read_text())
    output=[]
    for result_path in sorted((root/'runs').glob('*/results.jsonl')):
        run_id=result_path.parent.name
        arm,target,_=run_id.split('-')
        target=int(target)
        result=json.loads(result_path.read_text())
        trace=json.loads((result_path.parent/'tasks'/f'find_item_{target:03d}'/'trace.json').read_text())
        unexpected=[c['tool'] for c in trace['tool_calls'] if c['tool'] not in
                    {'shell','skill_read','screenshot','touch_gesture','wait_for_stable_screen','recall_memory'}]
        records=[json.loads(p.read_text()) for p in sorted((root/arm/'hid').glob(f'{run_id}-*.json'))]
        offsets=[0,*[r['metrics']['after'] for r in records]]
        # Conservative coverage: even one pixel of the row within the list
        # viewport counts as seen. This says nothing about actual model reading.
        seen={row['ordinal'] for row in geom['rows']
              if any(row['bottom']>offset and row['top']<offset+geom['container_height'] for offset in offsets)}
        missed=sorted(set(range(1,target+1))-seen)
        output.append({'trial':run_id,'arm':arm,'target':target,'original_success':result['metrics'].get('success'),
            'settled_viewports':len(offsets),'wholly_unseen_rows_through_target':missed,
            'wholly_unseen_count':len(missed),'prefix_coverage':(target-len(missed))/target,
            'has_unseen_rows':bool(missed),'unsupported_tools_requiring_manual_review':unexpected})
    data={'definition':'Rows 1 through target with no intersection at all in the initial or any saved post-swipe settled viewport. '
                       'Transient motion frames do not count; any 1px intersection counts as observed. '
                       'Geometric availability does not establish that the model read a row. Supplemental, not the original suite score.',
          'trials':output}
    (root/'coverage.json').write_text(json.dumps(data,indent=2))
    for arm in ('before','after'):
        rows=[r for r in output if r['arm']==arm]
        print(json.dumps({'arm':arm,'n':len(rows),'trials_with_unseen_rows':sum(r['has_unseen_rows'] for r in rows),
                          'total_unseen_rows':sum(r['wholly_unseen_count'] for r in rows)}))


if __name__=='__main__':
    main()
