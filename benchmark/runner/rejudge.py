from __future__ import annotations
import dataclasses as dc
import json
from pathlib import Path
from runner.judge import JudgeConfig, judge_task
from runner.suite import RubricItem
from runner.models import RubricVerdict
from runner.report import now_iso

def rejudge_run(run_dir: Path, judge_model: str) -> int:
    cfg = JudgeConfig(model=judge_model)
    cache = run_dir / "_judge_cache"
    new_results = []
    for line in (run_dir / "results.jsonl").read_text("utf-8").splitlines():
        row = json.loads(line)
        td = run_dir / "tasks" / row["task_id"]
        attempt_dir = td / f"attempt_{row['attempt']}" if (td / f"attempt_{row['attempt']}").exists() else td
        pre = attempt_dir / "pre.jpg"
        steps = sorted((attempt_dir / "steps").glob("*.jpg")) if (attempt_dir / "steps").exists() else []
        if not pre.exists() or not steps:
            row["status"] = "judge_error"
            row["metrics"] = {**row.get("metrics", {}), "rejudge_error": "missing artifacts"}
            new_results.append(row)
            continue
        post = steps[-1]
        trace = json.loads((attempt_dir / "trace.json").read_text("utf-8"))
        rubric = [RubricItem(id=r["id"], check=r["check"]) for r in row.get("rubric_spec", [])]
        if not rubric:
            row["status"] = "judge_error"
            row["metrics"] = {**row.get("metrics", {}), "rejudge_error": "missing rubric_spec"}
            new_results.append(row)
            continue
        verdict = judge_task(
            description=row.get("description_for_judge", ""),
            rubric=rubric, pre_screenshot=pre, post_screenshot=post,
            trace=trace, final_response=trace.get("final_response", ""),
            cfg=cfg, cache_dir=cache,
        )
        row["rubric"] = [dc.asdict(v) for v in verdict.verdicts]
        row["rubric_pass_count"] = sum(1 for v in verdict.verdicts if v.verdict == "yes")
        row["status"] = "passed" if row["rubric_pass_count"] == row["rubric_total"] else "failed"
        row["finished_at"] = now_iso()
        new_results.append(row)
    out = run_dir / "results.rejudged.jsonl"
    out.write_text("\n".join(json.dumps(r, ensure_ascii=False, sort_keys=True) for r in new_results) + "\n",
                   encoding="utf-8")
    print(f"wrote {out}")
    return 0
