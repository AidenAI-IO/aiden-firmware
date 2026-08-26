from __future__ import annotations
import json
import statistics
from pathlib import Path
from typing import Any
from runner.metrics import aggregate_trace_observation_metrics

def compare_runs(a: Path, b: Path, *, json_output: bool = False) -> int:
    rows_a = _load(a)
    rows_b = _load(b)
    comparison = build_comparison(a, b, rows_a=rows_a, rows_b=rows_b)
    if json_output:
        print(json.dumps(comparison, ensure_ascii=False, indent=2, sort_keys=True))
        return 0

    keys = set(rows_a) | set(rows_b)
    print(f"=== {a.name}  vs  {b.name} ===")

    summary_a = comparison["baseline"]
    summary_b = comparison["candidate"]
    quality = comparison["quality"]
    efficiency = comparison["efficiency"]
    _print_rate("Success rate", quality["success_rate"])
    _print_rate("Rubric completion", quality["rubric_completion"])
    _print_rate("Answer accuracy", quality["answer_accuracy"])
    _print_metric_change(
        "Wall median", efficiency["wall_ms_median"], suffix="ms", integer=True
    )
    _print_metric_change(
        "Wall p95", efficiency["wall_ms_p95"], suffix="ms", integer=True
    )
    _print_metric_change(
        "Agent time median", efficiency["agent_wall_ms_median"], suffix="ms", integer=True
    )
    _print_metric_change(
        "Agent time p95", efficiency["agent_wall_ms_p95"], suffix="ms", integer=True
    )
    _print_metric_change(
        "Total tokens median", efficiency["total_tokens_median"], suffix="", integer=True
    )
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
    print(
        f"Wall median relative change: "
        f"{_signed_pct(_relative_delta(summary_b['wall_ms_median'], summary_a['wall_ms_median']))}"
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


def build_comparison(
    a: Path,
    b: Path,
    *,
    rows_a: dict[str, dict] | None = None,
    rows_b: dict[str, dict] | None = None,
) -> dict[str, Any]:
    """Return a machine-readable paired comparison of two benchmark runs.

    The intended Thinking experiment order is ``off`` as run A and ``on`` as
    run B. Rates are reported both as percentage-point deltas and relative
    uplift; efficiency deltas are relative to run A. Missing judge/token data
    is represented as ``null`` rather than silently treating it as zero.
    """
    rows_a = rows_a if rows_a is not None else _load(a)
    rows_b = rows_b if rows_b is not None else _load(b)
    summary_a = _summary(rows_a)
    summary_b = _summary(rows_b)

    def metric_change(key: str) -> dict[str, float | None]:
        old = summary_a.get(key)
        new = summary_b.get(key)
        return {
            "baseline": old,
            "candidate": new,
            "delta": _delta(new, old),
            "relative_pct": _relative_delta(new, old),
        }

    return {
        "baseline_run": str(a),
        "candidate_run": str(b),
        "baseline": summary_a,
        "candidate": summary_b,
        "quality": {
            "success_rate": _rate_change(summary_a, summary_b, "success_rate"),
            "rubric_completion": _rate_change(summary_a, summary_b, "rubric_completion"),
            "answer_accuracy": _rate_change(summary_a, summary_b, "answer_accuracy"),
        },
        "efficiency": {
            "wall_ms_median": metric_change("wall_ms_median"),
            "wall_ms_p95": metric_change("wall_ms_p95"),
            "agent_wall_ms_median": metric_change("agent_wall_ms_median"),
            "agent_wall_ms_p95": metric_change("agent_wall_ms_p95"),
            "tool_calls_median": metric_change("tool_calls_median"),
            "total_tokens_median": metric_change("total_tokens_median"),
        },
        "task_flips": _task_flips(rows_a, rows_b),
    }

def _load(run_dir: Path) -> dict[str, dict]:
    out: dict[str, dict] = {}
    for line in (run_dir / "results.jsonl").read_text("utf-8").splitlines():
        r = json.loads(line)
        out[f"{r['task_id']}#{r['attempt']}"] = r
    return out


def _summary(rows: dict[str, dict]) -> dict:
    values = list(rows.values())
    passed = sum(1 for row in values if row.get("status") == "passed")
    rubric_rows = [
        row for row in values
        if isinstance(row.get("rubric"), list) and row.get("rubric")
    ]
    rubric_total = sum(
        sum(1 for verdict in row.get("rubric", []) if isinstance(verdict, dict))
        for row in rubric_rows
    )
    rubric_pass = sum(
        sum(1 for verdict in row.get("rubric", [])
            if isinstance(verdict, dict) and verdict.get("verdict") == "yes")
        for row in rubric_rows
    )
    answer_rows = [
        row for row in values
        if isinstance(row.get("metrics", {}).get("expected_answer_match"), bool)
    ]
    return {
        "total": len(values),
        "passed": passed,
        "success_rate": _fraction(passed, len(values)),
        "rubric_pass": rubric_pass,
        "rubric_total": rubric_total,
        "rubric_completion": _fraction(rubric_pass, rubric_total),
        "answer_correct": sum(
            1 for row in answer_rows
            if row.get("metrics", {}).get("expected_answer_match") is True
        ),
        "answer_total": len(answer_rows),
        "answer_accuracy": _fraction(
            sum(1 for row in answer_rows
                if row.get("metrics", {}).get("expected_answer_match") is True),
            len(answer_rows),
        ),
        "tool_calls_median": _median_metric(values, "tool_calls"),
        "wall_ms_median": _median_metric(values, "wall_ms"),
        "wall_ms_p95": _percentile_metric(values, "wall_ms", 95),
        "agent_wall_ms_median": _median_metric(values, "agent_wall_ms"),
        "agent_wall_ms_p95": _percentile_metric(values, "agent_wall_ms", 95),
        "total_tokens_median": _median_metric(values, "total_tokens"),
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


def _percentile_metric(rows: list[dict], key: str, pct: float) -> float | None:
    numbers = [row.get("metrics", {}).get(key) for row in rows]
    numbers = sorted(float(value) for value in numbers if isinstance(value, int | float))
    if not numbers:
        return None
    rank = (len(numbers) - 1) * pct / 100
    lower = int(rank)
    upper = min(lower + 1, len(numbers) - 1)
    return numbers[lower] + (numbers[upper] - numbers[lower]) * (rank - lower)


def _fraction(numerator: int, denominator: int) -> float | None:
    if denominator <= 0:
        return None
    return numerator / denominator


def _rate_change(
    baseline: dict[str, Any], candidate: dict[str, Any], key: str
) -> dict[str, float | None]:
    old = baseline.get(key)
    new = candidate.get(key)
    return {
        "baseline": old,
        "candidate": new,
        "delta_pp": (new - old) * 100 if old is not None and new is not None else None,
        "relative_pct": _relative_delta(new, old),
    }


def _relative_delta(new: float | None, old: float | None) -> float | None:
    if new is None or old is None or old == 0:
        return None
    return (new - old) / old * 100


def _task_flips(rows_a: dict[str, dict], rows_b: dict[str, dict]) -> dict[str, int]:
    common = set(rows_a) & set(rows_b)
    return {
        "common": len(common),
        "improved": sum(
            1 for key in common
            if rows_a[key].get("status") != "passed" and rows_b[key].get("status") == "passed"
        ),
        "regressed": sum(
            1 for key in common
            if rows_a[key].get("status") == "passed" and rows_b[key].get("status") != "passed"
        ),
    }


def _print_rate(label: str, change: dict[str, float | None]) -> None:
    old = change.get("baseline")
    new = change.get("candidate")
    if old is None or new is None:
        print(f"{label}: n/a")
        return
    delta_pp = change.get("delta_pp")
    relative = change.get("relative_pct")
    print(
        f"{label}: {old * 100:.1f}% -> {new * 100:.1f}% "
        f"(delta {_signed_number(delta_pp)}pp, {_signed_pct(relative)})"
    )


def _print_metric_change(
    label: str, change: dict[str, float | None], *, suffix: str, integer: bool
) -> None:
    old = change.get("baseline")
    new = change.get("candidate")
    if old is None or new is None:
        print(f"{label}: n/a")
        return
    formatter = (lambda value: f"{value:.0f}") if integer else (lambda value: f"{value:.1f}")
    print(
        f"{label}: {formatter(old)}{suffix} -> {formatter(new)}{suffix} "
        f"(delta {_signed_number(change.get('delta'))}{suffix}, "
        f"{_signed_pct(change.get('relative_pct'))})"
    )


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


def _signed_pct(value: float | None) -> str:
    if value is None:
        return "n/a"
    return f"{value:+.1f}%"
