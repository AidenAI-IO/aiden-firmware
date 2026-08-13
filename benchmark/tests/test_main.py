import json
import time
import urllib.parse

import pytest

from runner.agent_client import ToolInvokeResult
from runner.analysis import AnalysisResult
from runner.models import HardAssertionFailure, HardAssertionResults, RubricVerdict, TaskResult
import runner.main as main
import runner.webui as webui


class FakeClockClient:
    def __init__(self, years):
        self.years = list(years)
        self.calls = []

    def invoke_tool(self, name, args):
        self.calls.append((name, args))
        year = self.years.pop(0)
        return ToolInvokeResult(output=f"{year}\n", is_error=False, duration_ms=1)


def test_wait_for_agent_clock_retries_until_board_clock_is_current(monkeypatch):
    sleeps = []
    monkeypatch.setattr(time, "sleep", lambda seconds: sleeps.append(seconds))
    client = FakeClockClient([2021, 2021, 2026])

    assert hasattr(main, "wait_for_agent_clock")
    assert main.wait_for_agent_clock(client, min_year=2026, timeout_sec=10, poll_sec=2) is True

    assert [call[0] for call in client.calls] == ["shell", "shell", "shell"]
    assert all(call[1]["command"] == "date +%Y" for call in client.calls)
    assert sleeps == [2, 2]


def test_resolve_target_platform_infers_adb_android(monkeypatch):
    args = type("Args", (), {"target_platform": "auto", "environment_url": "http://127.0.0.1:18899"})()
    monkeypatch.setattr(main, "_read_environment_health", lambda environment_url: {"bridge_type": "adb_android"})

    assert main._resolve_target_platform(args) == "android"


def _task_result_with_details():
    return TaskResult(
        suite="suite",
        run_id="run-1",
        task_id="task-1",
        category="diagnostic",
        attempt=1,
        status="failed",
        rubric=[RubricVerdict(id="r1", verdict="no", reason="missing evidence")],
        rubric_pass_count=0,
        rubric_total=1,
        hard_assertions=HardAssertionResults(
            min_tool_calls=False,
            response_exists=False,
            required_tools=False,
            forbidden_tools=False,
        ),
        hard_assertion_failures=[
            HardAssertionFailure(
                id="required_tools",
                label="Required Tools",
                requirement="Must call: screenshot.",
                actual="Missing: screenshot. Used: none.",
            ),
            HardAssertionFailure(
                id="forbidden_tools",
                label="Forbidden Tools",
                requirement="Must not call: shell.",
                actual="Forbidden calls: shell at step 1. Used: shell.",
            ),
        ],
        metrics={"error": "boom", "agent_error": "agent boom", "judge_error": "judge boom"},
    )


def test_log_task_result_keeps_default_output_concise(capsys):
    main._log_task_result("task-1", 1, _task_result_with_details(), verbose=False)

    out = capsys.readouterr().out
    assert "FAILED" in out
    assert "Hard assertion failures" not in out
    assert "Error: boom" not in out
    assert "Agent Error" not in out
    assert "Judge Error" not in out
    assert "Rubric Details" not in out


def test_log_task_result_shows_details_in_verbose_mode(capsys):
    main._log_task_result("task-1", 1, _task_result_with_details(), verbose=True)

    out = capsys.readouterr().out
    assert "FAILED" in out
    assert "Hard assertion failures" in out
    assert "Required Tools" in out
    assert "Requirement: Must call: screenshot." in out
    assert "Actual: Missing: screenshot. Used: none." in out
    assert "Forbidden Tools" in out
    assert "Requirement: Must not call: shell." in out
    assert "Actual: Forbidden calls: shell at step 1. Used: shell." in out
    assert "Error: boom" in out
    assert "Agent Error" in out
    assert "Judge Error" in out
    assert "Rubric Details" in out


def test_run_manifest_records_agent_model(monkeypatch, tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(
        json.dumps(
            {
                "name": "empty_suite",
                "tasks": [],
            }
        ),
        encoding="utf-8",
    )

    class FakeClient:
        def __init__(self, base_url, benchmark_token=""):
            self.base_url = base_url
            self.benchmark_token = benchmark_token

        def health(self):
            return True

        def close(self):
            pass

    monkeypatch.setattr(main, "AgentClient", FakeClient)
    monkeypatch.setattr(main, "wait_for_agent_clock", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "upload_report", lambda *args, **kwargs: False)
    monkeypatch.setattr(main, "generate_report_html", lambda run_dir: "<html></html>")

    rc = main.cli(
        [
            "run",
            "--suite",
            str(suite_path),
            "--out",
            str(tmp_path / "runs"),
            "--agent-model",
            "qwen3.6-35b",
            "--no-judge",
        ]
    )

    assert rc == 0
    manifest_path = next((tmp_path / "runs").glob("*/manifest.json"))
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    assert manifest["agent_model"] == "qwen3.6-35b"
    assert manifest["judge_config"] is None


def test_run_state_file_records_incremental_totals(monkeypatch, tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(
        json.dumps(
            {
                "name": "progress_suite",
                "tasks": [
                    {
                        "id": "first",
                        "category": "diagnostic",
                        "prompt": "first",
                        "description_for_judge": "first",
                        "rubric": [{"id": "done", "check": "done"}],
                    },
                    {
                        "id": "second",
                        "category": "diagnostic",
                        "prompt": "second",
                        "description_for_judge": "second",
                        "rubric": [{"id": "done", "check": "done"}],
                    },
                ],
            }
        ),
        encoding="utf-8",
    )
    state_file = tmp_path / "state.json"
    observed_before_second_task = {}

    class FakeClient:
        def __init__(self, base_url, benchmark_token=""):
            self.base_url = base_url
            self.benchmark_token = benchmark_token

        def health(self):
            return True

        def close(self):
            pass

    def fake_run_one_task(client, suite, task, attempt, artifact_dir, *args, **kwargs):
        if task.id == "second":
            observed_before_second_task.update(json.loads(state_file.read_text(encoding="utf-8")))
        status = "passed" if task.id == "first" else "failed"
        return TaskResult(
            suite=suite.name,
            run_id="progress-run",
            task_id=task.id,
            category=task.category,
            attempt=attempt,
            status=status,
            rubric=[],
            rubric_pass_count=0,
            rubric_total=0,
            artifact_dir=str(artifact_dir),
        )

    monkeypatch.setattr(main, "AgentClient", FakeClient)
    monkeypatch.setattr(main, "wait_for_agent_ready", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "wait_for_agent_clock", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "recover_agent_after_timeout", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "run_one_task", fake_run_one_task)
    monkeypatch.setattr(main, "generate_report_html", lambda run_dir: "<html></html>")
    monkeypatch.setattr(main, "upload_report", lambda *args, **kwargs: False)

    rc = main.cli(
        [
            "run",
            "--suite",
            str(suite_path),
            "--out",
            str(tmp_path / "runs"),
            "--run-id",
            "progress-run",
            "--state-file",
            str(state_file),
            "--no-judge",
        ]
    )

    assert rc == 1
    assert observed_before_second_task["completed"] == 1
    assert observed_before_second_task["totals"] == {
        "tasks": 2,
        "passed": 1,
        "failed": 0,
        "skipped": 0,
        "judge_error": 0,
        "timeout": 0,
    }
    final_state = json.loads(state_file.read_text(encoding="utf-8"))
    assert final_state["status"] == "done"
    assert final_state["totals"] == {
        "tasks": 2,
        "passed": 1,
        "failed": 1,
        "skipped": 0,
        "judge_error": 0,
        "timeout": 0,
    }


def test_result_totals_keep_timeout_separate_from_failed():
    results = [
        TaskResult(suite="s", run_id="r", task_id="pass", category="c", attempt=1, status="passed", rubric=[]),
        TaskResult(suite="s", run_id="r", task_id="fail", category="c", attempt=1, status="failed", rubric=[]),
        TaskResult(suite="s", run_id="r", task_id="timeout", category="c", attempt=1, status="timeout", rubric=[]),
    ]

    totals = main._result_totals(results, total_tasks=3)

    assert totals == {
        "tasks": 3,
        "passed": 1,
        "failed": 1,
        "skipped": 0,
        "judge_error": 0,
        "timeout": 1,
    }
    assert sum(totals[key] for key in ("passed", "failed", "skipped", "judge_error", "timeout")) == totals["tasks"]


def test_run_skips_tasks_outside_target_platform(monkeypatch, tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(
        json.dumps(
            {
                "name": "platform_suite",
                "tasks": [
                    {
                        "id": "android_task",
                        "platforms": ["android"],
                        "category": "diagnostic",
                        "prompt": "android",
                        "description_for_judge": "android",
                        "rubric": [{"id": "done", "check": "done"}],
                    },
                    {
                        "id": "ios_task",
                        "platforms": ["ios"],
                        "category": "diagnostic",
                        "prompt": "ios",
                        "description_for_judge": "ios",
                        "rubric": [{"id": "done", "check": "done"}],
                    },
                ],
            }
        ),
        encoding="utf-8",
    )
    called_tasks = []

    class FakeClient:
        def __init__(self, base_url, benchmark_token=""):
            self.base_url = base_url

        def health(self):
            return True

        def close(self):
            pass

    def fake_run_one_task(client, suite, task, attempt, artifact_dir, *args, **kwargs):
        called_tasks.append(task.id)
        return TaskResult(
            suite=suite.name,
            run_id="platform-run",
            task_id=task.id,
            category=task.category,
            attempt=attempt,
            status="passed",
            rubric=[],
            rubric_pass_count=0,
            rubric_total=0,
            artifact_dir=str(artifact_dir),
        )

    monkeypatch.setattr(main, "AgentClient", FakeClient)
    monkeypatch.setattr(main, "wait_for_agent_ready", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "wait_for_agent_clock", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "run_one_task", fake_run_one_task)
    monkeypatch.setattr(main, "generate_report_html", lambda run_dir: "<html></html>")
    monkeypatch.setattr(main, "upload_report", lambda *args, **kwargs: False)

    rc = main.cli(
        [
            "run",
            "--suite",
            str(suite_path),
            "--out",
            str(tmp_path / "runs"),
            "--run-id",
            "platform-run",
            "--target-platform",
            "android",
            "--no-judge",
            "--inter-task-cooldown-sec",
            "0",
        ]
    )

    assert rc == 0
    assert called_tasks == ["android_task"]
    manifest = json.loads((tmp_path / "runs" / "platform-run" / "manifest.json").read_text(encoding="utf-8"))
    assert manifest["target_platform"] == "android"
    assert manifest["totals"] == {
        "tasks": 2,
        "passed": 1,
        "failed": 0,
        "skipped": 1,
        "judge_error": 0,
        "timeout": 0,
    }


def test_run_all_platform_skipped_tasks_does_not_require_agent(monkeypatch, tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(
        json.dumps(
            {
                "name": "platform_suite",
                "tasks": [
                    {
                        "id": "ios_task",
                        "platforms": ["ios"],
                        "category": "diagnostic",
                        "prompt": "ios",
                        "description_for_judge": "ios",
                        "rubric": [{"id": "done", "check": "done"}],
                    }
                ],
            }
        ),
        encoding="utf-8",
    )

    def fail_client(*args, **kwargs):
        raise AssertionError("AgentClient should not be constructed when every task is platform-skipped")

    monkeypatch.setattr(main, "AgentClient", fail_client)
    monkeypatch.setattr(main, "generate_report_html", lambda run_dir: "<html></html>")

    rc = main.cli(
        [
            "run",
            "--suite",
            str(suite_path),
            "--out",
            str(tmp_path / "runs"),
            "--run-id",
            "all-skipped-run",
            "--target-platform",
            "android",
            "--no-judge",
        ]
    )

    assert rc == 0
    manifest = json.loads((tmp_path / "runs" / "all-skipped-run" / "manifest.json").read_text(encoding="utf-8"))
    assert manifest["totals"]["skipped"] == 1


def test_run_triggers_llm_analysis_when_enabled(monkeypatch, tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(json.dumps({"name": "empty_suite", "tasks": []}), encoding="utf-8")
    calls = []

    class FakeClient:
        def __init__(self, base_url, benchmark_token=""):
            self.base_url = base_url
            self.benchmark_token = benchmark_token

        def health(self):
            return True

        def close(self):
            pass

    def fake_analyze(run_dir, repo_root, cfg):
        calls.append((run_dir, repo_root, cfg))
        (run_dir / "llm_analysis.md").write_text("analysis", encoding="utf-8")
        return AnalysisResult(ok=True, markdown_path=run_dir / "llm_analysis.md")

    monkeypatch.setattr(main, "AgentClient", FakeClient)
    monkeypatch.setattr(main, "wait_for_agent_clock", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "upload_report", lambda *args, **kwargs: False)
    monkeypatch.setattr(main, "analyze_run", fake_analyze)

    rc = main.cli(
        [
            "run",
            "--suite",
            str(suite_path),
            "--out",
            str(tmp_path / "runs"),
            "--no-judge",
            "--judge-model",
            "bytedance-seed/seed-2.0-lite",
            "--llm-analysis",
        ]
    )

    assert rc == 0
    assert calls and calls[0][2].enabled is True
    assert calls[0][2].model == "bytedance-seed/seed-2.0-lite"


def test_run_llm_analysis_env_limits_fall_back_on_invalid_values(monkeypatch, tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(json.dumps({"name": "empty_suite", "tasks": []}), encoding="utf-8")
    calls = []

    class FakeClient:
        def __init__(self, base_url, benchmark_token=""):
            self.base_url = base_url
            self.benchmark_token = benchmark_token

        def health(self):
            return True

        def close(self):
            pass

    def fake_analyze(run_dir, repo_root, cfg):
        calls.append(cfg)
        return AnalysisResult(ok=True, markdown_path=run_dir / "llm_analysis.md")

    monkeypatch.setenv("AIDEN_BENCHMARK_ANALYSIS_MAX_LOG_BYTES", "not-an-int")
    monkeypatch.setenv("AIDEN_BENCHMARK_ANALYSIS_MAX_CODE_BYTES", "not-an-int")
    monkeypatch.setenv("AIDEN_BENCHMARK_ANALYSIS_TIMEOUT_SEC", "not-an-int")
    monkeypatch.setattr(main, "AgentClient", FakeClient)
    monkeypatch.setattr(main, "wait_for_agent_clock", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "upload_report", lambda *args, **kwargs: False)
    monkeypatch.setattr(main, "analyze_run", fake_analyze)

    rc = main.cli(
        ["run", "--suite", str(suite_path), "--out", str(tmp_path / "runs"), "--no-judge", "--llm-analysis"]
    )

    assert rc == 0
    assert calls[0].max_log_bytes == 64 * 1024
    assert calls[0].max_code_bytes == 128 * 1024
    assert calls[0].timeout_sec == 180


def test_run_keeps_exit_code_when_analysis_fails(monkeypatch, tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(json.dumps({"name": "empty_suite", "tasks": []}), encoding="utf-8")

    class FakeClient:
        def __init__(self, base_url):
            pass

        def health(self):
            return True

        def close(self):
            pass

    monkeypatch.setattr(main, "AgentClient", FakeClient)
    monkeypatch.setattr(main, "wait_for_agent_clock", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "upload_report", lambda *args, **kwargs: False)
    monkeypatch.setattr(
        main,
        "analyze_run",
        lambda run_dir, repo_root, cfg: AnalysisResult(ok=False, warning="boom"),
    )

    rc = main.cli(
        ["run", "--suite", str(suite_path), "--out", str(tmp_path / "runs"), "--no-judge", "--llm-analysis"]
    )

    assert rc == 0


def test_read_optional_token_fails_fast_for_missing_file(tmp_path):
    with pytest.raises(ValueError, match="unable to read benchmark token file"):
        main._read_optional_token(tmp_path / "missing-token")


def test_read_optional_token_fails_fast_for_empty_file(tmp_path):
    token_file = tmp_path / "control_token"
    token_file.write_text("  \n", encoding="utf-8")

    with pytest.raises(ValueError, match=r"benchmark token file .* is empty"):
        main._read_optional_token(token_file)


def test_auto_agent_setup_injects_environment_url_as_bridge_endpoint(monkeypatch, tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(
        json.dumps(
            {
                "name": "mobile_suite",
                "tasks": [
                    {
                        "id": "open_clock",
                        "category": "diagnostic",
                        "prompt": "open clock",
                        "description_for_judge": "open clock",
                        "rubric": [{"id": "done", "check": "done"}],
                    }
                ],
            }
        ),
        encoding="utf-8",
    )

    captured = {}
    stale_clears = []

    class FakeClient:
        def __init__(self, base_url, benchmark_token=""):
            self.base_url = base_url
            captured["client_base_url"] = base_url
            self.benchmark_token = benchmark_token
            captured["client_benchmark_token"] = benchmark_token

        def close(self):
            pass

    monkeypatch.setattr(main, "AgentClient", FakeClient)
    monkeypatch.setattr(main, "wait_for_agent_ready", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "wait_for_agent_clock", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "generate_report_html", lambda run_dir: "<html></html>")
    monkeypatch.setattr(main, "call_environment_release", lambda *args, **kwargs: None)
    monkeypatch.setattr(main, "clear_stale_adb_android_owner", lambda url: stale_clears.append(url))
    monkeypatch.setattr(webui, "ensure_daemon_image", lambda *args, **kwargs: None)
    monkeypatch.setattr(webui, "read_environment_bridge_concurrency", lambda *args, **kwargs: 1)
    def fake_prepare_run_config(base_config_dir, config_dir, **kwargs):
        captured["prepare_config_kwargs"] = kwargs
        config_dir.mkdir(parents=True, exist_ok=True)
        (config_dir / "control_token").write_text("test-token", encoding="utf-8")

    monkeypatch.setattr(webui, "prepare_run_config", fake_prepare_run_config)
    monkeypatch.setattr(webui, "docker_published_port", lambda container_id, container_port: 18081)
    monkeypatch.setattr(webui, "start_daemon_logs", lambda *args, **kwargs: None)
    monkeypatch.setattr(webui, "stop_daemon_compose", lambda *args, **kwargs: None)

    def fake_start_daemon_compose(job, **kwargs):
        captured["job"] = job
        captured["kwargs"] = kwargs
        return "container-id"

    monkeypatch.setattr(webui, "start_daemon_compose", fake_start_daemon_compose)

    def fake_run_one_task(client, suite, task, attempt, artifact_dir, *args, **kwargs):
        captured["task_kwargs"] = kwargs
        return TaskResult(
            suite=suite.name,
            run_id="auto-run",
            task_id=task.id,
            category=task.category,
            attempt=attempt,
            status="passed",
            rubric=[],
            rubric_pass_count=0,
            rubric_total=0,
            artifact_dir=str(artifact_dir),
        )

    monkeypatch.setattr(main, "run_one_task", fake_run_one_task)

    rc = main.cli(
        [
            "run",
            "--suite",
            str(suite_path),
            "--out",
            str(tmp_path / "runs"),
            "--run-id",
            "auto-run",
            "--environment-url",
            "http://127.0.0.1:19090",
            "--target-platform",
            "android",
            "--auto-agent-setup",
            "--skill",
            "device-operator",
            "--no-judge",
        ]
    )

    assert rc == 0
    assert captured["job"].endpoint == "http://127.0.0.1:19090"
    assert captured["job"].docker_endpoint == "http://host.docker.internal:19090"
    assert captured["job"].agent_url == "http://127.0.0.1:18081"
    assert captured["client_base_url"] == "http://127.0.0.1:18081"
    assert captured["client_benchmark_token"] == "test-token"
    assert captured["kwargs"]["host_port"] == 0
    assert captured["kwargs"]["environment_bridge_endpoint"] == "http://host.docker.internal:19090"
    assert captured["kwargs"]["environment_bridge_mode"] is True
    assert captured["prepare_config_kwargs"]["target_platform"] == "android"
    assert captured["task_kwargs"]["active_skills"] == ["device-operator"]
    assert stale_clears == ["http://127.0.0.1:19090"]


def test_mock_environment_suite_requires_auto_agent_setup(tmp_path, capsys):
    suite_path = tmp_path / "mock-suite.json"
    suite_path.write_text(
        json.dumps(
            {
                "name": "mock_suite",
                "mock_environment": {
                    "phone_bridge": {"platform": "ios"},
                    "tools": {},
                },
                "tasks": [],
            }
        ),
        encoding="utf-8",
    )

    rc = main.cli(
        [
            "run",
            "--suite",
            str(suite_path),
            "--out",
            str(tmp_path / "runs"),
            "--no-judge",
        ]
    )

    assert rc == 2
    assert "mock_environment require --auto-agent-setup" in capsys.readouterr().err


def test_auto_agent_setup_starts_mock_environment_and_injects_phone_state(
    monkeypatch, tmp_path
):
    suite_path = tmp_path / "mock-suite.json"
    phone_state = {
        "connected": False,
        "platform": "ios",
        "app_state": "background",
        "pip_bridge_enabled": True,
    }
    suite_path.write_text(
        json.dumps(
            {
                "name": "mock_suite",
                "tasks": [
                    {
                        "id": "policy",
                        "category": "diagnostic",
                        "prompt": "test policy",
                        "description_for_judge": "test policy",
                        "rubric": [{"id": "done", "check": "done"}],
                        "mock_environment": {
                            "phone_bridge": phone_state,
                            "screen_text": "mock home screen",
                            "tools": {"screenshot": {"output": {"ok": True}}},
                        },
                    }
                ],
            }
        ),
        encoding="utf-8",
    )
    captured = {}

    class FakeClient:
        def __init__(self, base_url, benchmark_token=""):
            captured["client_base_url"] = base_url
            captured["benchmark_token"] = benchmark_token

        def set_phone_bridge_state(self, state):
            captured["phone_state"] = state
            return {"ok": True}

        def close(self):
            pass

    def fake_prepare_run_config(base_config_dir, config_dir, **kwargs):
        config_dir.mkdir(parents=True, exist_ok=True)
        (config_dir / "control_token").write_text("mock-token", encoding="utf-8")

    def fake_start_daemon_compose(job, **kwargs):
        captured["job"] = job
        captured["daemon_kwargs"] = kwargs
        return "container-id"

    monkeypatch.setattr(main, "AgentClient", FakeClient)
    monkeypatch.setattr(main, "wait_for_agent_ready", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "wait_for_agent_clock", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "generate_report_html", lambda run_dir: "<html></html>")
    monkeypatch.setattr(webui, "ensure_daemon_image", lambda *args, **kwargs: None)
    monkeypatch.setattr(webui, "prepare_run_config", fake_prepare_run_config)
    monkeypatch.setattr(
        webui, "docker_published_port", lambda container_id, container_port: 18081
    )
    monkeypatch.setattr(webui, "start_daemon_compose", fake_start_daemon_compose)
    monkeypatch.setattr(webui, "start_daemon_logs", lambda *args, **kwargs: None)
    monkeypatch.setattr(webui, "stop_daemon_compose", lambda *args, **kwargs: None)
    monkeypatch.setattr(
        main,
        "run_one_task",
        lambda client, suite, task, attempt, artifact_dir, *args, **kwargs: TaskResult(
            suite=suite.name,
            run_id="mock-run",
            task_id=task.id,
            category=task.category,
            attempt=attempt,
            status="passed",
            rubric=[],
            rubric_pass_count=0,
            rubric_total=0,
            artifact_dir=str(artifact_dir),
        ),
    )

    rc = main.cli(
        [
            "run",
            "--suite",
            str(suite_path),
            "--out",
            str(tmp_path / "runs"),
            "--run-id",
            "mock-run",
            "--auto-agent-setup",
            "--no-judge",
        ]
    )

    assert rc == 0
    assert captured["phone_state"] == phone_state
    assert captured["benchmark_token"] == "mock-token"
    assert captured["job"].endpoint.startswith("http://127.0.0.1:")
    assert captured["job"].docker_endpoint.startswith(
        "http://host.docker.internal:"
    )
    public_endpoint = urllib.parse.urlparse(captured["job"].endpoint)
    docker_endpoint = urllib.parse.urlparse(captured["job"].docker_endpoint)
    assert public_endpoint.path.startswith("/_aiden_mock/")
    assert docker_endpoint.path == public_endpoint.path
    assert captured["daemon_kwargs"]["environment_bridge_endpoint"] == captured[
        "job"
    ].docker_endpoint
    manifest = json.loads(
        (tmp_path / "runs" / "mock-run" / "manifest.json").read_text(
            encoding="utf-8"
        )
    )
    assert manifest["mock_environment"]["default"] is None
    assert manifest["mock_environment"]["tasks"]["policy"]["phone_bridge"] == phone_state
    assert manifest["environment_url"].endswith("/_aiden_mock/REDACTED")


def test_auto_agent_setup_caps_environment_concurrency(monkeypatch, tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(
        json.dumps(
            {
                "name": "mobile_suite",
                "tasks": [
                    {
                        "id": f"task_{i}",
                        "category": "diagnostic",
                        "prompt": "open",
                        "description_for_judge": "open",
                        "rubric": [{"id": "done", "check": "done"}],
                    }
                    for i in range(3)
                ],
            }
        ),
        encoding="utf-8",
    )

    class FakeClient:
        def __init__(self, base_url, benchmark_token=""):
            self.base_url = base_url
            self.benchmark_token = benchmark_token

        def close(self):
            pass

    monkeypatch.setattr(main, "AgentClient", FakeClient)
    monkeypatch.setattr(main, "wait_for_agent_ready", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "wait_for_agent_clock", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "generate_report_html", lambda run_dir: "<html></html>")
    monkeypatch.setattr(main, "call_environment_release", lambda *args, **kwargs: None)
    monkeypatch.setattr(webui, "ensure_daemon_image", lambda *args, **kwargs: None)
    monkeypatch.setattr(webui, "read_environment_bridge_concurrency", lambda *args, **kwargs: 5)
    def fake_prepare_run_config(base_config_dir, config_dir, **kwargs):
        config_dir.mkdir(parents=True, exist_ok=True)
        (config_dir / "control_token").write_text("test-token", encoding="utf-8")

    monkeypatch.setattr(webui, "prepare_run_config", fake_prepare_run_config)
    monkeypatch.setattr(webui, "docker_published_port", lambda container_id, container_port: 18081)
    monkeypatch.setattr(webui, "start_daemon_compose", lambda *args, **kwargs: "container-id")
    monkeypatch.setattr(webui, "start_daemon_logs", lambda *args, **kwargs: None)
    monkeypatch.setattr(webui, "stop_daemon_compose", lambda *args, **kwargs: None)
    monkeypatch.setattr(
        main,
        "run_one_task",
        lambda client, suite, task, attempt, artifact_dir, *args, **kwargs: TaskResult(
            suite=suite.name,
            run_id="capped-run",
            task_id=task.id,
            category=task.category,
            attempt=attempt,
            status="passed",
            rubric=[],
            rubric_pass_count=0,
            rubric_total=0,
            artifact_dir=str(artifact_dir),
        ),
    )

    rc = main.cli(
        [
            "run",
            "--suite",
            str(suite_path),
            "--out",
            str(tmp_path / "runs"),
            "--run-id",
            "capped-run",
            "--environment-url",
            "http://127.0.0.1:19090",
            "--auto-agent-setup",
            "--max-concurrency",
            "2",
            "--no-judge",
        ]
    )

    assert rc == 0
    manifest = json.loads((tmp_path / "runs" / "capped-run" / "manifest.json").read_text(encoding="utf-8"))
    assert manifest["concurrency"] == 2


def test_auto_agent_setup_rejects_negative_max_concurrency_before_setup(monkeypatch, tmp_path, capsys):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(
        json.dumps(
            {
                "name": "mobile_suite",
                "tasks": [
                    {
                        "id": "task_1",
                        "category": "diagnostic",
                        "prompt": "open",
                        "description_for_judge": "open",
                        "rubric": [{"id": "done", "check": "done"}],
                    }
                ],
            }
        ),
        encoding="utf-8",
    )

    def fail_if_setup_runs(*args, **kwargs):
        raise AssertionError("daemon setup should not run for invalid max-concurrency")

    monkeypatch.setattr(webui, "ensure_daemon_image", fail_if_setup_runs)
    monkeypatch.setattr(webui, "read_environment_bridge_concurrency", fail_if_setup_runs)

    rc = main.cli(
        [
            "run",
            "--suite",
            str(suite_path),
            "--out",
            str(tmp_path / "runs"),
            "--run-id",
            "bad-concurrency-run",
            "--environment-url",
            "http://127.0.0.1:19090",
            "--auto-agent-setup",
            "--max-concurrency",
            "-1",
            "--no-judge",
        ]
    )

    assert rc == 2
    assert "max-concurrency must be non-negative" in capsys.readouterr().err


def test_task_route_id_keeps_explicit_id_verbatim_across_attempts(tmp_path):
    import argparse
    from pathlib import Path

    from runner.suite import Suite

    suite = Suite(
        name="suite",
        global_reset={},
        tasks=[],
        sha256="",
        source_path=Path(tmp_path / "suite.json"),
    )
    derived = argparse.Namespace(benchmark_task_id="")
    explicit = argparse.Namespace(benchmark_task_id="webui:job-test")

    # Without an explicit id every attempt gets its own route, so parallel
    # environments can hand each attempt a separate device.
    assert main._task_route_id(derived, suite, "t1", 1, 2) == "suite.json:t1:attempt-1"
    assert main._task_route_id(derived, suite, "t1", 2, 2) == "suite.json:t1:attempt-2"
    # With an explicit id the caller already started its daemon under that id;
    # suffixing attempts here would break ownership on the second repeat.
    assert main._task_route_id(explicit, suite, "t1", 1, 2) == "webui:job-test"
    assert main._task_route_id(explicit, suite, "t1", 2, 2) == "webui:job-test"


def test_run_releases_environment_route_per_non_auto_attempt(monkeypatch, tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(
        json.dumps(
            {
                "name": "mobile_suite",
                "tasks": [
                    {
                        "id": "open_clock",
                        "category": "diagnostic",
                        "prompt": "open clock",
                        "description_for_judge": "open clock",
                        "rubric": [{"id": "done", "check": "done"}],
                    }
                ],
            }
        ),
        encoding="utf-8",
    )
    route_ids = []
    releases = []
    stale_clears = []

    class FakeClient:
        def __init__(self, base_url):
            self.base_url = base_url

        def health(self):
            return True

        def close(self):
            pass

    def fake_run_one_task(client, suite, task, attempt, artifact_dir, *args, **kwargs):
        route_ids.append(kwargs["benchmark_task_id"])
        return TaskResult(
            suite=suite.name,
            run_id="route-run",
            task_id=task.id,
            category=task.category,
            attempt=attempt,
            status="passed",
            rubric=[],
            rubric_pass_count=0,
            rubric_total=0,
            artifact_dir=str(artifact_dir),
        )

    monkeypatch.setattr(main, "AgentClient", FakeClient)
    monkeypatch.setattr(main, "wait_for_agent_ready", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "wait_for_agent_clock", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "run_one_task", fake_run_one_task)
    monkeypatch.setattr(main, "generate_report_html", lambda run_dir: "<html></html>")
    monkeypatch.setattr(main, "upload_report", lambda *args, **kwargs: False)
    monkeypatch.setattr(
        main,
        "call_environment_release",
        lambda environment_url, task_id=None, **kwargs: releases.append((environment_url, task_id)),
    )
    monkeypatch.setattr(main, "clear_stale_adb_android_owner", lambda url: stale_clears.append(url))

    rc = main.cli(
        [
            "run",
            "--suite",
            str(suite_path),
            "--out",
            str(tmp_path / "runs"),
            "--run-id",
            "route-run",
            "--environment-url",
            "http://127.0.0.1:19090",
            "--repeats",
            "2",
            "--inter-task-cooldown-sec",
            "0",
            "--no-judge",
        ]
    )

    assert rc == 0
    assert route_ids == ["suite.json:open_clock:attempt-1", "suite.json:open_clock:attempt-2"]
    assert releases == [
        ("http://127.0.0.1:19090", "suite.json:open_clock:attempt-1"),
        ("http://127.0.0.1:19090", "suite.json:open_clock:attempt-2"),
    ]
    assert stale_clears == ["http://127.0.0.1:19090"]
