from runner.metrics import aggregate
from runner.models import TaskResult


def test_aggregate_counts_all_trace_observation_ids():
    results = [
        TaskResult(
            suite="suite",
            run_id="run",
            task_id="plain",
            category="single_step",
            attempt=1,
            status="passed",
            rubric=[],
            metrics={
                "trace_observations": [
                    {"id": "used_enter_text_in_field", "passed": False},
                    {"id": "used_search_launch_app", "passed": False},
                ]
            },
        ),
        TaskResult(
            suite="suite",
            run_id="run",
            task_id="text",
            category="multi_step",
            attempt=1,
            status="passed",
            rubric=[],
            metrics={
                "trace_observations": [
                    {"id": "used_enter_text_in_field", "passed": True},
                    {"id": "used_search_launch_app", "passed": True},
                ]
            },
        ),
    ]

    agg = aggregate(results)

    assert agg["trace_observations"] == {
        "used_enter_text_in_field": {"tasks_with_observation": 1, "tasks_observed": 2},
        "used_search_launch_app": {"tasks_with_observation": 1, "tasks_observed": 2},
    }
