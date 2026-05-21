from __future__ import annotations
import json
from pathlib import Path

def compare_runs(a: Path, b: Path) -> int:
    rows_a = _load(a)
    rows_b = _load(b)
    keys = set(rows_a) | set(rows_b)
    print(f"=== {a.name}  vs  {b.name} ===")
    flips = 0
    for k in sorted(keys):
        ra = rows_a.get(k)
        rb = rows_b.get(k)
        if not ra:
            print(f"+ {k}  added in B  status={rb['status']}")
            continue
        if not rb:
            print(f"- {k}  removed in B  status={ra['status']}")
            continue
        if ra["status"] != rb["status"]:
            flips += 1
            print(f"~ {k}  {ra['status']} -> {rb['status']}")
        wa = ra.get("metrics", {}).get("wall_ms")
        wb = rb.get("metrics", {}).get("wall_ms")
        if wa and wb and abs(wb - wa) > 1000:
            print(f"   wall {wa}ms -> {wb}ms")
    print(f"flips: {flips}")
    return 0

def _load(run_dir: Path) -> dict[str, dict]:
    out: dict[str, dict] = {}
    for line in (run_dir / "results.jsonl").read_text("utf-8").splitlines():
        r = json.loads(line)
        out[f"{r['task_id']}#{r['attempt']}"] = r
    return out
