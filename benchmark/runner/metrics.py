from __future__ import annotations
import statistics
from collections import Counter
from benchmark.runner.models import TaskResult

def aggregate(results: list[TaskResult]) -> dict[str, object]:
    if not results:
        return {"tasks": 0}
    by_status: Counter[str] = Counter(r.status for r in results)
    by_category: dict[str, dict[str, int]] = {}
    for r in results:
        cat = by_category.setdefault(r.category, {"passed": 0, "total": 0,
                                                    "rubric_pass": 0, "rubric_total": 0})
        cat["total"] += 1
        if r.status == "passed":
            cat["passed"] += 1
        cat["rubric_pass"] += r.rubric_pass_count
        cat["rubric_total"] += r.rubric_total
    judge_eligible = [r for r in results if r.status not in {"judge_error", "skipped"}]
    pass_count = sum(1 for r in judge_eligible if r.status == "passed")
    walls = [r.metrics.get("wall_ms", 0) for r in results if r.metrics.get("wall_ms")]
    tool_counts = [r.metrics.get("tool_calls", 0) for r in results
                   if r.metrics.get("tool_calls") is not None]
    return {
        "tasks": len(results),
        "passed": pass_count,
        "by_status": dict(by_status),
        "by_category": by_category,
        "wall_ms_median": int(statistics.median(walls)) if walls else None,
        "wall_ms_p95": int(_percentile(walls, 95)) if walls else None,
        "tool_calls_median": int(statistics.median(tool_counts)) if tool_counts else None,
        "tool_calls_p95": int(_percentile(tool_counts, 95)) if tool_counts else None,
    }

def _percentile(values: list[float], pct: float) -> float:
    if not values:
        return 0
    s = sorted(values)
    k = (len(s) - 1) * pct / 100
    f = int(k)
    c = min(f + 1, len(s) - 1)
    return s[f] + (s[c] - s[f]) * (k - f)
