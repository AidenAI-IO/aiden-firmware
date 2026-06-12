import json
import re
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


def read_report_tasks(path):
    html = path.read_text()
    match = re.search(r"const TASKS = (.*?);\nconst rows", html, re.S)
    assert match, "report TASKS payload missing"
    return json.loads(match.group(1))


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
    assert suite_summary["batch_id"] == "batch-x"
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
    # Suites with non-zero unknown or cleanup_failed must NOT show as passed at
    # the batch level — that would hide incomplete or partially-broken runs.
    batch_html = (batch / "index.html").read_text()
    assert "Task Records" in batch_html
    assert "Fail" in batch_html
    assert read_json(batch / "summary.json")["pass_rate"] < 1
    assert "task.unknown" in batch_html


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


def test_generate_reports_handles_direct_mobilegym_run_directory(tmp_path):
    from mobilegym import report

    run_dir = tmp_path / "20260610_100501"
    write_json(
        run_dir / "meta.json",
        {
            "suite": ["wechat"],
            "task_max_steps": {
                "wechat.BlacklistContact": 45,
                "wechat.ConditionalReplyToBoss": 30,
            },
        },
    )
    write_jsonl(
        run_dir / "results.jsonl",
        [
            {"id": "wechat.BlacklistContact", "is_success": True, "execution": {"stop_reason": "complete"}},
            {"id": "wechat.ConditionalReplyToBoss", "is_success": False, "execution": {"stop_reason": "false_complete"}},
        ],
    )
    write_jsonl(
        run_dir / "errors.jsonl",
        [
            {
                "id": "wechat.ConditionalReplyToBoss",
                "error": "AidenAdapterTimeout: Aiden /api/chat timed out",
            }
        ],
    )
    (run_dir / "console.log").write_text("console output")

    summary = report.generate_reports(run_dir)

    assert summary["tasks"] == 2
    assert summary["passed"] == 1
    assert summary["error"] == 1
    assert summary["suites"][0]["suite"] == "wechat"
    assert (run_dir / "index.html").exists()
    assert read_json(run_dir / "summary.json")["pass_rate"] == 0.5
    html = (run_dir / "index.html").read_text()
    assert "wechat.BlacklistContact" in html
    assert "results.jsonl" in html
    assert "errors.jsonl" in html
    assert "console.log" in html


def test_generate_reports_uses_benchmark_drawer_style(tmp_path):
    from mobilegym import report

    run_dir = tmp_path / "20260611_120000"
    write_json(run_dir / "meta.json", {"suite": ["clock"]})
    write_jsonl(
        run_dir / "results.jsonl",
        [{"id": "clock.CountAlarms", "is_success": True, "execution": {"stop_reason": "complete"}}],
    )

    report.generate_reports(run_dir)

    html = (run_dir / "index.html").read_text()
    assert "Agent Benchmark" in html
    assert "Task Records" in html
    assert "drawer-backdrop" in html
    assert "function openDrawer" in html


def test_batch_report_lists_individual_task_rows(tmp_path):
    from mobilegym import report

    batch = tmp_path / "batch-task-rows"
    for idx, task_id in enumerate(["clock.AddAlarm", "clock.CountAlarms"]):
        shard = batch / "clock" / f"shard-{idx}"
        write_json(
            shard / "shard.json",
            {
                "batch_id": "batch-task-rows",
                "suite": "clock",
                "shard_index": idx,
                "shard_count": 2,
                "selected_task_count": 1,
                "selected_task_ids": [task_id],
                "exit_code": 0,
            },
        )
        write_jsonl(shard / "raw" / "run" / "results.jsonl", [{"id": task_id, "is_success": True}])

    report.generate_reports(batch)

    html = (batch / "index.html").read_text()
    assert "clock.AddAlarm" in html
    assert "clock.CountAlarms" in html
    assert "2 shards" not in html


def test_report_maps_mobilegym_task_fields_into_drawer_payload(tmp_path):
    from mobilegym import report

    batch = tmp_path / "batch-fields"
    shard = batch / "account" / "shard-0"
    write_json(
        shard / "shard.json",
        {
            "batch_id": "batch-fields",
            "suite": "account",
            "shard_index": 0,
            "shard_count": 1,
            "selected_task_count": 1,
            "selected_task_ids": ["account.WechatAccountCancellation"],
            "exit_code": 0,
        },
    )
    write_jsonl(
        shard / "raw" / "run" / "results.jsonl",
        [
            {
                "id": "account.WechatAccountCancellation",
                "task_name": "帮我把微信账号注销掉",
                "suite": "account",
                "is_success": False,
                "execution": {
                    "runtime_s": 1.2,
                    "steps": 3,
                    "agent_answer": "done",
                    "error": "timeout",
                },
            }
        ],
    )
    write_json(
        shard / "raw" / "run" / "trajectory" / "account_WechatAccountCancellation" / "meta.json",
        {"task_id": "account.WechatAccountCancellation"},
    )
    write_json(
        shard / "raw" / "run" / "trajectory" / "account_WechatAccountCancellation" / "aiden_bridge_actions.json",
        [
            {
                "action_id": "ep1:0001",
                "tool_name": "touch_gesture",
                "tool_input": {"type": "tap", "point": {"x": 500, "y": 900}},
                "mobilegym_action": {"action_type": "CLICK", "data": {"x": 500, "y": 900}},
                "duration_ms": 42,
                "error": None,
            }
        ],
    )

    report.generate_reports(batch)

    task = read_report_tasks(batch / "account" / "index.html")[0]
    assert task["prompt"] == "帮我把微信账号注销掉"
    assert task["description"] == "帮我把微信账号注销掉"
    assert task["wall_ms"] == 1200
    assert task["tool_calls_count"] == 1
    assert "[touch_gesture]" in task["tool_calls_detail"]
    assert "mobilegym_action" in task["tool_calls_detail"]
    assert task["response"] == "done"
    assert ["execution_error", "timeout"] in task["errors"]
    assert task["errors"].count(["execution_error", "timeout"]) == 1


def test_drawer_task_deduplicates_mobilegym_errors():
    from mobilegym import report

    task = report._drawer_task(
        {
            "task_id": "account.WechatAccountCancellation",
            "suite": "account",
            "status": "error",
            "reason": "AidenAdapterTimeout: timed out",
            "result": {"execution": {"error": "AidenAdapterTimeout: timed out"}},
            "error": {"error": "AidenAdapterTimeout: timed out"},
        }
    )

    assert task["errors"] == [["error", "AidenAdapterTimeout: timed out"]]


def test_direct_run_report_reads_bridge_actions_from_trajectory(tmp_path):
    from mobilegym import report


    run = tmp_path / "run"
    write_json(run / "meta.json", {"task_id": "account.WechatAccountCancellation"})
    write_jsonl(
        run / "results.jsonl",
        [
            {
                "id": "account.WechatAccountCancellation",
                "task_name": "帮我把微信账号注销掉",
                "is_success": True,
                "execution": {"agent_answer": "done", "steps": 5},
            }
        ],
    )
    write_json(
        run / "trajectory" / "account_WechatAccountCancellation" / "meta.json",
        {"task_id": "account.WechatAccountCancellation"},
    )
    write_json(
        run / "trajectory" / "account_WechatAccountCancellation" / "aiden_bridge_actions.json",
        [
            {
                "tool_name": "touch_gesture",
                "tool_input": {"type": "tap"},
                "mobilegym_action": {"action_type": "CLICK"},
                "duration_ms": 12,
                "error": None,
            }
        ],
    )

    report.generate_reports(run)

    task = read_report_tasks(run / "index.html")[0]
    assert task["response"] == "done"
    assert task["tool_calls_count"] == 1
    assert "[touch_gesture]" in task["tool_calls_detail"]


def test_direct_run_summary_uses_actual_model_name_from_meta(tmp_path):
    from mobilegym import report

    run = tmp_path / "run"
    write_json(run / "meta.json", {"task_id": "clock.CountAlarms", "model_name": "qwen3.6-35b"})
    write_jsonl(run / "results.jsonl", [{"id": "clock.CountAlarms", "is_success": True}])

    report.generate_reports(run)

    assert read_json(run / "summary.json")["model"] == "qwen3.6-35b"


def test_batch_summary_uses_shard_model_name(tmp_path):
    from mobilegym import report

    batch = tmp_path / "batch-model"
    shard = batch / "clock" / "shard-0"
    write_json(
        shard / "shard.json",
        {
            "batch_id": "batch-model",
            "suite": "clock",
            "shard_index": 0,
            "shard_count": 1,
            "selected_task_count": 1,
            "selected_task_ids": ["clock.CountAlarms"],
            "model": "qwen3.6-35b",
            "exit_code": 0,
        },
    )
    write_jsonl(shard / "raw" / "run" / "results.jsonl", [{"id": "clock.CountAlarms", "is_success": True}])

    report.generate_reports(batch)

    assert read_json(batch / "summary.json")["model"] == "qwen3.6-35b"


def test_failed_shard_without_selected_tasks_still_generates_report_row(tmp_path):
    from mobilegym import report

    batch = tmp_path / "batch-worker-failed"
    shard = batch / "personamem_lt_recall_v1" / "shard-0"
    write_json(
        shard / "shard.json",
        {
            "batch_id": "batch-worker-failed",
            "suite": "personamem_lt_recall_v1",
            "shard_index": 0,
            "shard_count": 4,
            "exit_code": 2,
        },
    )
    (shard / "runner.log").parent.mkdir(parents=True, exist_ok=True)
    (shard / "runner.log").write_text("run_aiden.py: error: unrecognized arguments: --aiden-suite\n", encoding="utf-8")

    report.generate_reports(batch)

    summary = read_json(batch / "summary.json")
    assert summary["tasks"] == 1
    assert summary["worker_failed"] == 1
    assert read_report_tasks(batch / "index.html")[0]["status"] == "worker_failed"


def test_nested_aiden_suite_shards_are_included_in_batch_report(tmp_path):
    from mobilegym import report

    batch = tmp_path / "batch-nested"
    shard = batch / "perception" / "perception_v1" / "shard-0"
    write_json(
        shard / "shard.json",
        {
            "batch_id": "batch-nested",
            "suite": "perception/perception_v1",
            "shard_index": 0,
            "shard_count": 1,
            "exit_code": 2,
            "model": "qwen3.6-35b",
        },
    )

    report.generate_reports(batch)

    summary = read_json(batch / "summary.json")
    assert summary["tasks"] == 1
    assert summary["worker_failed"] == 1
    assert summary["suites"][0]["suite"] == "perception/perception_v1"
    assert read_report_tasks(batch / "index.html")[0]["id"] == "perception/perception_v1.shard-0"


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
