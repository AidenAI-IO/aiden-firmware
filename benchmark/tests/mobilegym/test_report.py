import json
import subprocess
import sys
from pathlib import Path


BENCHMARK_ROOT = Path(__file__).resolve().parents[2]


def write_jsonl(path, rows):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(json.dumps(row) for row in rows) + "\n")


def write_json(path, payload):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, ensure_ascii=False, indent=2))


def read_json(path):
    return json.loads(path.read_text())


def test_generate_reports_normalizes_mobilegym_results_and_missing_tasks(tmp_path):
    from mobilegym import report

    batch = tmp_path / "batch-x"
    shard = batch / "clock" / "shard-0"
    raw = shard / "raw" / "run-1"
    write_json(
        shard / "shard.json",
        {
            "batch_id": "batch-x",
            "suite": "clock",
            "shard_index": 0,
            "shard_count": 1,
            "selected_task_count": 4,
            "selected_task_ids": ["task.pass", "task.fail", "task.error", "task.missing"],
            "exit_code": 1,
            "cleanup_failed": 0,
        },
    )
    (shard / "runner.log").write_text("runner output")
    (shard / "compose.log").write_text("compose output")
    (raw / "console.log").parent.mkdir(parents=True, exist_ok=True)
    (raw / "console.log").write_text("mobilegym console")
    write_jsonl(
        raw / "results.jsonl",
        [
            {"id": "task.pass", "is_success": True, "is_error": False},
            {"id": "task.fail", "is_success": False, "is_error": False, "execution": {"stop_reason": "false_complete"}},
            {"id": "task.error", "is_success": True, "is_error": False},
        ],
    )
    write_jsonl(raw / "errors.jsonl", [{"id": "task.error", "error": "boom"}])

    summary = report.generate_reports(batch)

    assert summary["tasks"] == 4
    assert summary["passed"] == 1
    assert summary["failed"] == 1
    assert summary["error"] == 1
    assert summary["worker_failed"] == 1
    assert summary["cleanup_failed"] == 0
    suite_summary = read_json(batch / "clock" / "summary.json")
    assert suite_summary["pass_rate"] == 0.25
    html = (batch / "clock" / "index.html").read_text()
    assert "results.jsonl" in html
    assert "errors.jsonl" in html
    assert "console.log" in html
    assert "runner.log" in html
    assert "compose.log" in html
    assert (batch / "index.html").exists()


def test_generate_reports_handles_fallback_fields_unknown_and_empty_shards(tmp_path):
    from mobilegym import report

    batch = tmp_path / "batch-y"
    fallback = batch / "phone" / "shard-0"
    write_json(
        fallback / "shard.json",
        {
            "batch_id": "batch-y",
            "suite": "phone",
            "shard_index": 0,
            "shard_count": 2,
            "selected_task_count": 4,
            "selected_task_ids": ["task.status-pass", "task.success-false", "task.passed-false", "task.unknown"],
            "exit_code": 0,
            "cleanup_failed": 1,
        },
    )
    write_jsonl(
        fallback / "raw" / "run" / "results.jsonl",
        [
            {"id": "task.status-pass", "status": "passed"},
            {"id": "task.success-false", "success": False},
            {"id": "task.passed-false", "passed": False},
        ],
    )
    empty = batch / "phone" / "shard-1"
    write_json(
        empty / "shard.json",
        {
            "batch_id": "batch-y",
            "suite": "phone",
            "shard_index": 1,
            "shard_count": 2,
            "selected_task_count": 0,
            "selected_task_ids": [],
            "exit_code": 0,
            "cleanup_failed": 0,
        },
    )

    summary = report.generate_reports(batch)

    assert summary["tasks"] == 4
    assert summary["passed"] == 1
    assert summary["failed"] == 2
    assert summary["unknown"] == 1
    assert summary["empty"] == 1
    assert summary["cleanup_failed"] == 1
    assert read_json(batch / "summary.json")["suites"][0]["suite"] == "phone"


def test_generate_reports_groups_positional_tasks_under_tasks_suite(tmp_path):
    from mobilegym import report

    batch = tmp_path / "batch-tasks"
    task_dir = batch / "tasks" / "clock-countalarms-a1b2c3d4"
    write_json(
        task_dir / "shard.json",
        {
            "batch_id": "batch-tasks",
            "suite": "tasks",
            "task_id": "clock.CountAlarms",
            "task_slug": "clock-countalarms-a1b2c3d4",
            "shard_index": 0,
            "shard_count": 1,
            "selected_task_count": 1,
            "selected_task_ids": ["clock.CountAlarms"],
            "exit_code": 0,
        },
    )
    write_jsonl(task_dir / "raw" / "run" / "results.jsonl", [{"id": "clock.CountAlarms", "is_success": True}])

    summary = report.generate_reports(batch)

    assert summary["suites"][0]["suite"] == "tasks"
    assert read_json(batch / "tasks" / "summary.json")["passed"] == 1
    assert (batch / "tasks" / "index.html").exists()


def test_report_module_cli_rejects_missing_batch_dir(tmp_path):
    missing = tmp_path / "missing"
    result = subprocess.run(
        [sys.executable, "-m", "mobilegym.report", str(missing)],
        cwd=BENCHMARK_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )

    assert result.returncode == 2
    assert "not found" in result.stderr.lower()
