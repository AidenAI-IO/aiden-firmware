"""Export the comparison as a standalone research figure."""
from __future__ import annotations

import argparse
import json
from pathlib import Path

import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from matplotlib.ticker import PercentFormatter
import numpy as np


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('directory',type=Path)
    args=parser.parse_args()
    root=args.directory.resolve()
    data=json.loads((root/'comparison.json').read_text())
    coverage=json.loads((root/'coverage.json').read_text())['trials']
    colors={'before':'#8D9BAD','after':'#157C78'}
    labels={'before':'Before #715','after':'PR #715'}
    plt.rcParams.update({'font.family':'DejaVu Sans','font.size':10,'axes.spines.top':False,
                         'axes.spines.right':False,'axes.spines.left':False,'axes.edgecolor':'#CBD2D8',
                         'axes.labelcolor':'#23333D','text.color':'#23333D','xtick.color':'#23333D','ytick.color':'#23333D'})
    fig,axes=plt.subplots(1,3,figsize=(13,4.4),gridspec_kw={'width_ratios':[1.3,1.05,1]})
    for arm,shift in [('before',-.19),('after',.19)]:
        values=[data['groups'][f'{arm}-{target}'] for target in (24,83,'all')]
        rates=np.array([g['rate'] for g in values])
        errors=np.array([[g['rate']-g['wilson_95'][0] for g in values],
                         [g['wilson_95'][1]-g['rate'] for g in values]])
        axes[0].bar(np.arange(3)+shift,rates,width=.34,color=colors[arm],label=labels[arm])
        axes[0].errorbar(np.arange(3)+shift,rates,yerr=np.maximum(errors,0),fmt='none',color='#33444F',capsize=3,lw=1)
        for i,g in enumerate(values):
            axes[0].text(i+shift,g['wilson_95'][1]+.045,f'{g["successes"]}/{g["n"]}',ha='center',fontsize=9)
    axes[0].set(xticks=range(3),xticklabels=['Item 24','Item 83','Combined'],ylim=(0,1.22),
                yticks=[0,.25,.5,.75,1],title='Original task success')
    axes[0].yaxis.set_major_formatter(PercentFormatter(1))
    handles, legend_labels = axes[0].get_legend_handles_labels()
    fig.legend(handles, legend_labels, loc='upper left', bbox_to_anchor=(.055,.89),
               ncol=2, frameon=False, fontsize=9)

    for i,arm in enumerate(('before','after')):
        rows=[r for r in coverage if r['arm']==arm]
        values=[r['wholly_unseen_count'] for r in rows]
        x=i+np.linspace(-.11,.11,len(values))
        axes[1].scatter(x,values,color=colors[arm],s=28,alpha=.8)
        axes[1].plot([i-.23,i+.23],[np.median(values)]*2,color='#23333D',lw=2)
    axes[1].set(xticks=[0,1],xticklabels=['Before','After'],ylabel='Wholly unseen rows per trial',
                title='Information missed (exploratory)',xlim=(-.6,1.6),ylim=(-1,None))

    for i,arm in enumerate(('before','after')):
        rows=[r for r in data['trials'] if r['arm']==arm]
        values=[r['wall_sec'] for r in rows]
        x=i+np.linspace(-.11,.11,len(values))
        axes[2].scatter(x,values,color=colors[arm],s=28,alpha=.8)
        axes[2].plot([i-.23,i+.23],[np.median(values)]*2,color='#23333D',lw=2)
    axes[2].axhline(240,color='#AD6570',ls='--',lw=1)
    axes[2].text(1.48,243,'240 s limit',ha='right',fontsize=8,color='#AD6570')
    axes[2].set(xticks=[0,1],xticklabels=['Before','After'],ylabel='Seconds (timeouts included)',
                title='Task duration',xlim=(-.6,1.6),ylim=(0,270))
    for ax in axes:
        ax.set_axisbelow(True)
        ax.grid(axis='y',color='#E7ECEF',lw=.7)
        ax.tick_params(axis='both',length=0)
    fig.suptitle('DeepSeek Flash · Scroll Lab · Before / after PR #715',x=.06,ha='left',fontsize=16,fontweight='bold')
    fig.text(.06,.035,'Simulated production HID-report replay; 10 repeats × 2 targets per arm. '
             'Error bars: 95% Wilson intervals. Dots: trials; bars: medians.\n'
             'Any visible pixel counts toward row coverage. Missing-row analysis is post-hoc; '
             'it does not change the original task score.',fontsize=8,color='#5A6A74')
    fig.subplots_adjust(left=.065,right=.98,bottom=.24,top=.78,wspace=.42)
    fig.savefig(root/'comparison.png',dpi=200)
    fig.savefig(root/'comparison.svg')
    svg = root/'comparison.svg'
    svg.write_text('\n'.join(line.rstrip() for line in svg.read_text().splitlines())+'\n')
    plt.close(fig)


if __name__=='__main__':
    main()
