from __future__ import annotations

import hashlib
import json
from pathlib import Path
from types import SimpleNamespace

import pytest

from runner.langfuse_reporter import (
    LangfusePublishError,
    _fetch_dataset_item_via_public_api,
    publish_run,
)


class FakeAPIError(RuntimeError):
    def __init__(self, status_code: int, message: str):
        super().__init__(message)
        self.status_code = status_code


class FakeScores:
    def __init__(self, owner):
        self.owner = owner

    def create(self, **kwargs):
        if self.owner.score_error is not None:
            raise self.owner.score_error
        self.owner.score_calls.append(kwargs)
        return SimpleNamespace(id=kwargs["id"])


class FakeDatasets:
    def __init__(self, owner):
        self.owner = owner

    def get(self, name):
        if name not in self.owner.datasets:
            raise FakeAPIError(404, "not found")
        return self.owner.datasets[name]


class FakeTrace:
    def __init__(self, owner):
        self.owner = owner

    def get(self, trace_id, **kwargs):
        self.owner.trace_calls.append(trace_id)
        if self.owner.trace_error is not None:
            raise self.owner.trace_error
        return SimpleNamespace(id=trace_id)


class FakeLangfuse:
    def __init__(self, existing_run=None, score_error=None, trace_error=None):
        self.datasets = {}
        self.items = []
        self.spans = []
        self.experiment = None
        self.flushed = False
        self.existing_run = existing_run
        self.score_error = score_error
        self.score_calls = []
        self.trace_error = trace_error
        self.trace_calls = []
        self.api = SimpleNamespace(
            datasets=FakeDatasets(self),
            scores=FakeScores(self),
            trace=FakeTrace(self),
        )

    def auth_check(self):
        return True

    def create_dataset(self, **kwargs):
        dataset = SimpleNamespace(**kwargs)
        self.datasets[kwargs["name"]] = dataset
        return dataset

    def get_dataset_run(self, **kwargs):
        if self.existing_run is not None:
            return self.existing_run
        raise FakeAPIError(404, "not found")

    def create_dataset_item(self, **kwargs):
        item = SimpleNamespace(**kwargs)
        self.items.append(item)
        return item

    def update_current_span(self, **kwargs):
        self.spans.append(kwargs)

    def run_experiment(self, **kwargs):
        item_results = []
        for item in kwargs["data"]:
            output = kwargs["task"](item=item)
            evaluations = []
            for evaluator in kwargs["evaluators"]:
                evaluations.extend(
                    evaluator(
                        input=item.input,
                        output=output,
                        expected_output=item.expected_output,
                        metadata=item.metadata,
                    )
                )
            item_results.append(SimpleNamespace(
                item=item,
                output=output,
                evaluations=evaluations,
                trace_id=f"trace-{item.id}",
            ))
        run_evaluations = []
        for evaluator in kwargs["run_evaluators"]:
            run_evaluations.extend(evaluator(item_results=item_results))
        self.experiment = {**kwargs, "item_results": item_results, "run_evaluations": run_evaluations}
        existing_items = (
            list(self.existing_run.dataset_run_items)
            if self.existing_run is not None
            else []
        )
        existing_items.extend(
            SimpleNamespace(dataset_item_id=result.item.id, trace_id=result.trace_id)
            for result in item_results
        )
        self.existing_run = SimpleNamespace(
            id="dataset-run-1",
            metadata=kwargs["metadata"],
            dataset_run_items=existing_items,
        )
        return SimpleNamespace(
            dataset_run_id="dataset-run-1",
            dataset_run_url="http://langfuse.local/dataset-run-1",
            item_results=[
                SimpleNamespace(
                    **vars(item_result),
                    dataset_run_id="dataset-run-1",
                )
                for item_result in item_results
            ],
            run_evaluations=run_evaluations,
        )

    def flush(self):
        self.flushed = True


def _write_run(
    tmp_path: Path,
    *,
    metrics_run_id: str = "run-a",
    suite_prompt: str = "Do task A.",
    attempt_count: int = 1,
) -> Path:
    tmp_path.mkdir(parents=True, exist_ok=True)
    suite_path = tmp_path / "suite.json"
    suite_payload = {
        "name": "smoke_suite",
        "tasks": [
            {
                "id": "task-a",
                "category": "diagnostic",
                "description_for_judge": "Complete task A.",
                "prompt": suite_prompt,
                "rubric": [{"id": "done", "check": "Task is done."}],
                "hard_assertions": {"min_tool_calls": 0, "max_tool_calls": 3},
            }
        ],
    }
    suite_bytes = json.dumps(suite_payload).encode()
    suite_path.write_bytes(suite_bytes)
    suite_sha = hashlib.sha256(suite_bytes).hexdigest()

    run_dir = tmp_path / "run-a"
    run_dir.mkdir()
    manifest = {
        "run_id": "run-a",
        "suite_path": str(suite_path),
        "suite_snapshot_path": "suite.json",
        "suite_sha256": suite_sha,
        "git_sha": "abc123",
        "git_dirty": False,
        "agent_model": "test-model",
        "metrics_schema_version": "p0-v1",
        "metrics_k": attempt_count,
        "selected_task_ids": [],
        "active_skills": [],
    }
    aggregate = {
        "tasks": 1,
        "passed": 1,
        "by_status": {"passed": 1},
        "by_category": {
            "diagnostic": {
                "passed": 1,
                "total": 1,
                "rubric_pass": 1,
                "rubric_total": 1,
            }
        },
        "unique_tasks": 1,
        "attempts": attempt_count,
        "metrics_k": attempt_count,
        "agent_eligible_attempts": attempt_count,
        "invalid_attempts": 0,
        "pass_at_1": {
            "value": 1.0, "coverage": 1.0, "successes": 1,
            "eligible_tasks": 1, "total_tasks": 1,
            "ci95": {"lower": 0.2, "upper": 1.0},
        },
        "pass_at_k": {
            "value": 1.0, "coverage": 1.0, "successes": 1,
            "eligible_tasks": 1, "total_tasks": 1,
            "ci95": {"lower": 0.2, "upper": 1.0},
        },
        "pass_pow_k": {
            "value": 1.0, "coverage": 1.0, "successes": 1,
            "eligible_tasks": 1, "total_tasks": 1,
            "ci95": {"lower": 0.2, "upper": 1.0},
        },
        "attempt_success_rate": {
            "value": 1.0, "coverage": 1.0, "successes": 1,
            "eligible_attempts": 1, "total_attempts": 1,
            "ci95": {"lower": 0.2, "upper": 1.0},
        },
        "oracle_best_score_at_k": {
            "value": 1.0, "count": 1, "eligible_tasks": 1,
        },
        "task_wall_ms": {
            "count": 1, "sum": 123.0, "mean": 123.0,
            "p50": 123.0, "p90": 123.0, "p95": 123.0,
        },
        "llm_time_ms": {
            "count": 1, "sum": 50.0, "mean": 50.0,
            "p50": 50.0, "p90": 50.0, "p95": 50.0,
        },
        "vision_llm_time_ms": {
            "count": 1, "sum": 40.0, "mean": 40.0,
            "p50": 40.0, "p90": 40.0, "p95": 40.0,
        },
        "time_to_first_token_ms": {
            "count": 1, "sum": 10.0, "mean": 10.0,
            "p50": 10.0, "p90": 10.0, "p95": 10.0,
        },
        "device_execution_ms": {
            "count": 1, "sum": 20.0, "mean": 20.0,
            "p50": 20.0, "p90": 20.0, "p95": 20.0,
        },
        "screenshot_capture_ms": {
            "count": 1, "sum": 5.0, "mean": 5.0,
            "p50": 5.0, "p90": 5.0, "p95": 5.0,
        },
        "retry_count": {
            "count": 1, "sum": 1.0, "mean": 1.0,
            "p50": 1.0, "p90": 1.0, "p95": 1.0,
        },
        "tool_calls": {
            "count": 1, "sum": 2.0, "mean": 2.0,
            "p50": 2.0, "p90": 2.0, "p95": 2.0,
        },
        "first_success_attempt": {"count": 1, "mean": 1.0, "p50": 1.0},
        "failure_classes": {},
        "failure_stages": {},
        "failure_stage_coverage": {
            "classified": 0, "eligible_failures": 0, "value": None,
        },
        "trace_observations": {
            "used_memory": {
                "tasks_with_observation": 1,
                "tasks_observed": 1,
            }
        },
    }
    result = {
        "suite": "smoke_suite",
        "run_id": "run-a",
        "task_id": "task-a",
        "category": "diagnostic",
        "attempt": 1,
        "status": "passed",
        "rubric": [{"id": "done", "verdict": "yes", "reason": "done"}],
        "rubric_pass_count": 1,
        "rubric_total": 1,
        "hard_assertions": {"min_tool_calls": True},
        "hard_assertion_failures": [],
        "metrics": {
            "success": True,
            "agent_eligible": True,
            "quality_score": 1.0,
            "task_wall_ms": 123,
            "llm_time_ms": 50,
            "vision_llm_time_ms": 40,
            "time_to_first_token_ms": 10,
            "device_execution_ms": 20,
            "screenshot_capture_ms": 5,
            "retry_count": 1,
            "tool_calls": 2,
            "screenshots_taken": 1,
            "expected_answer_match": True,
            "expected_recalled_memory_match": False,
            "trace_observations": [
                {"id": "used_memory", "passed": True, "reason": "found"}
            ],
            "episode_id": "episode-a",
        },
        "artifact_dir": str(run_dir / "tasks" / "task-a"),
        "started_at": "2026-09-20T00:00:00+00:00",
        "finished_at": "2026-09-20T00:00:01+00:00",
    }
    (run_dir / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    (run_dir / "suite.json").write_bytes(suite_bytes)
    (run_dir / "metrics.json").write_text(
        json.dumps({
            "schema_version": "p0-v1",
            "suite": "smoke_suite",
            "run_id": metrics_run_id,
            "suite_sha256": suite_sha,
            "metrics_k": attempt_count,
            "aggregate": aggregate,
        }),
        encoding="utf-8",
    )
    results = [
        {
            **result,
            "attempt": attempt,
            "metrics": {
                **result["metrics"],
                "episode_id": (
                    "episode-a"
                    if attempt_count == 1
                    else f"episode-a-{attempt}"
                ),
            },
        }
        for attempt in range(1, attempt_count + 1)
    ]
    (run_dir / "results.jsonl").write_text(
        "".join(json.dumps(row) + "\n" for row in results),
        encoding="utf-8",
    )
    return run_dir


def test_publish_run_maps_attempts_and_aggregate_metrics(tmp_path: Path):
    run_dir = _write_run(tmp_path)
    client = FakeLangfuse()

    published = publish_run(run_dir, client=client)

    assert published.run_name == "run-a"
    assert published.item_count == 1
    assert published.dataset_run_url == "http://langfuse.local/dataset-run-1"
    assert published.dataset_name == "aiden-benchmark:smoke_suite"
    assert client.items[0].input == {
        "task_id": "task-a",
        "attempt": 1,
        "category": "diagnostic",
        "prompt": "Do task A.",
    }
    item_scores = {
        score["name"]: score["value"]
        for score in client.score_calls
        if score.get("trace_id")
    }
    assert item_scores["benchmark.success"] == 1
    assert item_scores["benchmark.task_wall_ms"] == 123
    assert item_scores["benchmark.time_to_first_token_ms"] == 10
    assert item_scores["benchmark.device_execution_ms"] == 20
    assert item_scores["benchmark.retry_count"] == 1
    assert item_scores["benchmark.screenshots_taken"] == 1
    assert item_scores["benchmark.expected_answer_match"] == 1
    assert item_scores["benchmark.expected_recalled_memory_match"] == 0
    assert item_scores["benchmark.trace_observation.used_memory"] == 1
    run_scores = {
        score["name"]: score["value"]
        for score in client.score_calls
        if score.get("dataset_run_id")
    }
    assert run_scores["capability.pass_at_1"] == 1.0
    assert run_scores["capability.pass_at_1.successes"] == 1
    assert run_scores["capability.pass_at_1.ci95_lower"] == 0.2
    assert run_scores["capability.attempt_success_rate.total_attempts"] == 1
    assert run_scores["efficiency.task_wall_ms.count"] == 1
    assert run_scores["efficiency.task_wall_ms.mean"] == 123.0
    assert run_scores["efficiency.task_wall_ms.p50"] == 123.0
    assert run_scores["efficiency.task_wall_ms.p95"] == 123.0
    assert run_scores["efficiency.llm_time_ms.p50"] == 50.0
    assert run_scores["efficiency.time_to_first_token_ms.p90"] == 10.0
    assert run_scores["reliability.retry_count.sum"] == 1.0
    assert run_scores["capability.first_success_attempt.p50"] == 1.0
    assert run_scores["status.passed"] == 1
    assert run_scores["category.diagnostic.pass_rate"] == 1.0
    assert run_scores["observations.used_memory.pass_rate"] == 1.0
    assert client.experiment["metadata"]["git_sha"] == "abc123"
    assert len(client.experiment["metadata"]["workload_sha256"]) == 64
    assert client.spans[0]["metadata"]["aiden_episode_id"] == "episode-a"
    assert client.flushed is True
    assert client.trace_calls


def test_publish_run_uses_stable_dataset_and_item_ids(tmp_path: Path):
    first_run = _write_run(tmp_path / "first")
    second_run = _write_run(
        tmp_path / "second",
        suite_prompt="Do the revised task A.",
    )
    first = FakeLangfuse()
    second = FakeLangfuse()

    first_result = publish_run(first_run, client=first)
    second_result = publish_run(second_run, client=second)

    assert first_result.dataset_name == second_result.dataset_name
    assert first.items[0].id == second.items[0].id
    assert first.items[0].input != second.items[0].input
    assert (
        first.experiment["metadata"]["suite_sha256"]
        != second.experiment["metadata"]["suite_sha256"]
    )


def test_publish_run_serializes_dataset_run_item_creation(tmp_path: Path):
    run_dir = _write_run(tmp_path, attempt_count=3)
    client = FakeLangfuse()

    published = publish_run(run_dir, client=client)

    assert published.item_count == 3
    assert client.experiment["max_concurrency"] == 1


def test_publish_run_reports_publication_progress(tmp_path: Path):
    progress = []

    publish_run(_write_run(tmp_path), client=FakeLangfuse(), progress=progress.append)

    assert any(
        "dataset=aiden-benchmark:smoke_suite run=run-a items=1" in message
        for message in progress
    )
    assert any("experiment replay start" in message for message in progress)
    assert any("verification complete" in message for message in progress)


def test_publish_run_uses_suite_snapshot_when_source_changes(tmp_path: Path):
    run_dir = _write_run(tmp_path)
    manifest = json.loads((run_dir / "manifest.json").read_text(encoding="utf-8"))
    Path(manifest["suite_path"]).write_text("{}", encoding="utf-8")
    client = FakeLangfuse()

    publish_run(run_dir, client=client)

    assert client.items[0].input["prompt"] == "Do task A."


def test_publish_run_rejects_mismatched_artifacts_before_network(tmp_path: Path):
    run_dir = _write_run(tmp_path, metrics_run_id="different")

    with pytest.raises(LangfusePublishError, match="run_id values differ"):
        publish_run(run_dir, client=FakeLangfuse())


def test_publish_run_wraps_invalid_suite_as_publish_error(tmp_path: Path):
    run_dir = _write_run(tmp_path)
    (run_dir / "suite.json").write_text("{}", encoding="utf-8")
    manifest = json.loads((run_dir / "manifest.json").read_text(encoding="utf-8"))
    manifest["suite_sha256"] = hashlib.sha256(b"{}").hexdigest()
    (run_dir / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")

    with pytest.raises(LangfusePublishError, match="Langfuse publish failed"):
        publish_run(run_dir, client=FakeLangfuse())


def test_publish_run_rejects_incomplete_experiment_result(tmp_path: Path):
    class IncompleteExperimentLangfuse(FakeLangfuse):
        def run_experiment(self, **kwargs):
            return SimpleNamespace(
                dataset_run_id=None,
                dataset_run_url=None,
                item_results=[],
                run_evaluations=[],
            )

    with pytest.raises(LangfusePublishError, match="did not link all dataset items"):
        publish_run(_write_run(tmp_path), client=IncompleteExperimentLangfuse())


def test_publish_run_fails_when_existing_run_lookup_fails(tmp_path: Path):
    class UnavailableLangfuse(FakeLangfuse):
        def get_dataset_run(self, **kwargs):
            raise FakeAPIError(503, "temporarily unavailable")

    with pytest.raises(LangfusePublishError, match="temporarily unavailable"):
        publish_run(_write_run(tmp_path), client=UnavailableLangfuse())


def test_publish_run_repairs_incomplete_existing_run(tmp_path: Path):
    run_dir = _write_run(tmp_path)
    seed = FakeLangfuse()
    publish_run(run_dir, client=seed)
    existing_run = SimpleNamespace(
        id="dataset-run-1",
        metadata=seed.experiment["metadata"],
        dataset_run_items=[],
    )
    client = FakeLangfuse(existing_run=existing_run)

    published = publish_run(run_dir, client=client)

    assert published.already_exists is False
    assert len(client.items) == 1
    assert client.experiment is not None


def test_publish_run_repairs_scores_for_complete_existing_run(tmp_path: Path):
    run_dir = _write_run(tmp_path)
    seed = FakeLangfuse()
    publish_run(run_dir, client=seed)
    item_result = seed.experiment["item_results"][0]
    existing_run = SimpleNamespace(
        id="dataset-run-1",
        metadata=seed.experiment["metadata"],
        dataset_run_items=[SimpleNamespace(
            dataset_item_id=item_result.item.id,
            trace_id="existing-trace-1",
        )],
    )
    client = FakeLangfuse(existing_run=existing_run)

    published = publish_run(run_dir, client=client)

    assert published.already_exists is True
    assert client.experiment is None
    assert any(score.get("trace_id") == "existing-trace-1" for score in client.score_calls)
    assert any(score.get("dataset_run_id") == "dataset-run-1" for score in client.score_calls)


def test_publish_run_rejects_reused_run_id_with_different_results(tmp_path: Path):
    run_dir = _write_run(tmp_path)
    first = FakeLangfuse()
    publish_run(run_dir, client=first)
    item_result = first.experiment["item_results"][0]
    existing_run = SimpleNamespace(
        id="dataset-run-1",
        metadata=first.experiment["metadata"],
        dataset_run_items=[SimpleNamespace(
            dataset_item_id=item_result.item.id,
            trace_id=item_result.trace_id,
        )],
    )
    row = json.loads((run_dir / "results.jsonl").read_text(encoding="utf-8"))
    row["metrics"]["task_wall_ms"] = 999
    (run_dir / "results.jsonl").write_text(json.dumps(row) + "\n", encoding="utf-8")

    with pytest.raises(LangfusePublishError, match="conflicts with benchmark artifacts"):
        publish_run(run_dir, client=FakeLangfuse(existing_run=existing_run))


def test_publish_run_fails_when_score_write_fails(tmp_path: Path):
    client = FakeLangfuse(score_error=FakeAPIError(503, "score storage unavailable"))

    with pytest.raises(LangfusePublishError, match="score storage unavailable"):
        publish_run(_write_run(tmp_path), client=client)


def test_publish_run_fails_when_trace_is_not_readable_after_flush(tmp_path: Path):
    client = FakeLangfuse(trace_error=FakeAPIError(503, "trace storage unavailable"))

    with pytest.raises(LangfusePublishError, match="trace storage unavailable"):
        publish_run(_write_run(tmp_path), client=client)


def test_publish_run_retries_trace_readback_on_not_found(monkeypatch, tmp_path: Path):
    class EventuallyReadableTrace(FakeTrace):
        def get(self, trace_id, **kwargs):
            self.owner.trace_calls.append(trace_id)
            if len(self.owner.trace_calls) == 1:
                raise FakeAPIError(404, "not found yet")
            return SimpleNamespace(id=trace_id)

    now = 0.0
    sleep_calls = []

    def monotonic():
        return now

    def sleep(seconds):
        nonlocal now
        sleep_calls.append(seconds)
        now += seconds

    monkeypatch.setattr("runner.langfuse_reporter.time.monotonic", monotonic)
    monkeypatch.setattr("runner.langfuse_reporter.time.sleep", sleep)
    client = FakeLangfuse()
    client.api.trace = EventuallyReadableTrace(client)

    publish_run(_write_run(tmp_path), client=client)

    assert len(client.trace_calls) == 2
    assert sleep_calls == [1.0]


def test_publish_run_uses_exponential_backoff_until_deadline(monkeypatch, tmp_path: Path):
    now = 0.0
    sleep_calls = []

    def monotonic():
        return now

    def sleep(seconds):
        nonlocal now
        sleep_calls.append(seconds)
        now += seconds

    monkeypatch.setenv("LANGFUSE_PUBLISH_VERIFY_TIMEOUT_SECONDS", "7")
    monkeypatch.setattr("runner.langfuse_reporter.time.monotonic", monotonic)
    monkeypatch.setattr("runner.langfuse_reporter.time.sleep", sleep)
    client = FakeLangfuse(trace_error=FakeAPIError(404, "not found yet"))

    with pytest.raises(LangfusePublishError, match="traces were not readable"):
        publish_run(_write_run(tmp_path), client=client)

    assert sleep_calls == [1.0, 2.0, 4.0]
    assert len(client.trace_calls) == 4


@pytest.mark.parametrize("value", ["invalid", "0", "inf"])
def test_publish_run_rejects_invalid_verify_timeout(monkeypatch, tmp_path: Path, value: str):
    monkeypatch.setenv("LANGFUSE_PUBLISH_VERIFY_TIMEOUT_SECONDS", value)

    with pytest.raises(
        LangfusePublishError,
        match="LANGFUSE_PUBLISH_VERIFY_TIMEOUT_SECONDS must be a positive number",
    ):
        publish_run(_write_run(tmp_path), client=FakeLangfuse())


def test_publish_run_does_not_create_dataset_after_lookup_failure(tmp_path: Path):
    class UnavailableDatasets(FakeDatasets):
        def get(self, name):
            raise FakeAPIError(503, "dataset storage unavailable")

    client = FakeLangfuse()
    client.api.datasets = UnavailableDatasets(client)

    with pytest.raises(LangfusePublishError, match="dataset storage unavailable"):
        publish_run(_write_run(tmp_path), client=client)

    assert client.datasets == {}


def test_langfuse_v3_dataset_item_compatibility(monkeypatch):
    monkeypatch.setattr(
        "runner.langfuse_reporter._public_api_get",
        lambda path: {
            "id": "item-1",
            "status": "ACTIVE",
            "input": {"task_id": "task-a", "attempt": 1},
            "expectedOutput": {"ok": True},
            "metadata": {},
            "sourceTraceId": None,
            "sourceObservationId": None,
            "datasetId": "dataset-1",
            "datasetName": "benchmark",
            "createdAt": "2026-09-20T00:00:00Z",
            "updatedAt": "2026-09-20T00:00:00Z",
        },
    )

    item = _fetch_dataset_item_via_public_api("item-1")

    assert item.id == "item-1"
    assert item.dataset_id == "dataset-1"
    assert item.input == {"task_id": "task-a", "attempt": 1}
