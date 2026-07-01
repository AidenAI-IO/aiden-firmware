from __future__ import annotations
import json
import statistics
from pathlib import Path
from runner.metrics import aggregate_trace_observation_metrics

def compare_runs(a: Path, b: Path) -> int:
    rows_a = _load(a)
    rows_b = _load(b)
    keys = set(rows_a) | set(rows_b)
    print(f"=== {a.name}  vs  {b.name} ===")

    summary_a = _summary(rows_a)
    summary_b = _summary(rows_b)
    print(
        f"Passed: {summary_a['passed']}/{summary_a['total']} -> "
        f"{summary_b['passed']}/{summary_b['total']} "
        f"(delta {_signed(summary_b['passed'] - summary_a['passed'])})"
    )
    print(
        f"Tool calls median: {_number(summary_a['tool_calls_median'])} -> "
        f"{_number(summary_b['tool_calls_median'])} "
        f"(delta {_signed_number(_delta(summary_b['tool_calls_median'], summary_a['tool_calls_median']))})"
    )
    print(
        f"Wall median: {_ms(summary_a['wall_ms_median'])} -> "
        f"{_ms(summary_b['wall_ms_median'])} "
        f"(delta {_signed_ms(_delta(summary_b['wall_ms_median'], summary_a['wall_ms_median']))})"
    )

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
        if wa is not None and wb is not None and abs(wb - wa) > 1000:
            print(f"   wall {wa}ms -> {wb}ms")
    print(f"flips: {flips}")

    obs_ids = set(summary_a["trace_observations"]) | set(summary_b["trace_observations"])
    if obs_ids:
        print("Trace observations:")
    for obs_id in sorted(obs_ids):
        oa = summary_a["trace_observations"].get(
            obs_id, {"tasks_with_observation": 0, "tasks_observed": 0}
        )
        ob = summary_b["trace_observations"].get(
            obs_id, {"tasks_with_observation": 0, "tasks_observed": 0}
        )
        print(
            f"  {obs_id}: {oa['tasks_with_observation']}/{oa['tasks_observed']} -> "
            f"{ob['tasks_with_observation']}/{ob['tasks_observed']} "
            f"(delta {_signed(ob['tasks_with_observation'] - oa['tasks_with_observation'])})"
        )
    return 0

def _load(run_dir: Path) -> dict[str, dict]:
    out: dict[str, dict] = {}
    for line in (run_dir / "results.jsonl").read_text("utf-8").splitlines():
        r = json.loads(line)
        out[f"{r['task_id']}#{r['attempt']}"] = r
    return out


def _summary(rows: dict[str, dict]) -> dict:
    values = list(rows.values())
    return {
        "total": len(values),
        "passed": sum(1 for row in values if row.get("status") == "passed"),
        "tool_calls_median": _median_metric(values, "tool_calls"),
        "wall_ms_median": _median_metric(values, "wall_ms"),
        "trace_observations": aggregate_trace_observation_metrics(
            row.get("metrics", {}) for row in values
        ),
    }


def _median_metric(rows: list[dict], key: str) -> float | None:
    numbers = [row.get("metrics", {}).get(key) for row in rows]
    numbers = [value for value in numbers if isinstance(value, int | float)]
    if not numbers:
        return None
    return float(statistics.median(numbers))


def _delta(new: float | None, old: float | None) -> float | None:
    if new is None or old is None:
        return None
    return new - old


def _number(value: float | None) -> str:
    if value is None:
        return "n/a"
    return f"{value:.1f}"


def _ms(value: float | None) -> str:
    if value is None:
        return "n/a"
    return f"{value:.0f}ms"


def _signed(value: int) -> str:
    return f"{value:+d}"


def _signed_number(value: float | None) -> str:
    if value is None:
        return "n/a"
    return f"{value:+.1f}"


def _signed_ms(value: float | None) -> str:
    if value is None:
        return "n/a"
    return f"{value:+.0f}ms"
