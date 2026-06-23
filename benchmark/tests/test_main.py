import time
import json

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
        def __init__(self, base_url):
            self.base_url = base_url

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


def test_run_triggers_llm_analysis_when_enabled(monkeypatch, tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(json.dumps({"name": "empty_suite", "tasks": []}), encoding="utf-8")
    calls = []

    class FakeClient:
        def __init__(self, base_url):
            self.base_url = base_url

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
        def __init__(self, base_url):
            self.base_url = base_url

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

    class FakeClient:
        def __init__(self, base_url):
            self.base_url = base_url

        def close(self):
            pass

    captured = {}
    monkeypatch.setattr(main, "AgentClient", FakeClient)
    monkeypatch.setattr(main, "wait_for_agent_ready", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "wait_for_agent_clock", lambda *args, **kwargs: True)
    monkeypatch.setattr(main, "generate_report_html", lambda run_dir: "<html></html>")
    monkeypatch.setattr(main, "call_environment_release", lambda *args, **kwargs: None)
    monkeypatch.setattr(webui, "ensure_daemon_image", lambda *args, **kwargs: None)
    monkeypatch.setattr(webui, "read_environment_bridge_concurrency", lambda *args, **kwargs: 1)
    monkeypatch.setattr(webui, "prepare_run_config", lambda *args, **kwargs: None)
    monkeypatch.setattr(webui, "reserve_free_port", lambda: 18081)
    monkeypatch.setattr(webui, "start_daemon_logs", lambda *args, **kwargs: None)
    monkeypatch.setattr(webui, "stop_daemon_compose", lambda *args, **kwargs: None)

    def fake_start_daemon_compose(job, **kwargs):
        captured["job"] = job
        captured["kwargs"] = kwargs
        return "container-id"

    monkeypatch.setattr(webui, "start_daemon_compose", fake_start_daemon_compose)

    def fake_run_one_task(client, suite, task, attempt, artifact_dir, *args, **kwargs):
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
            "--auto-agent-setup",
            "--no-judge",
        ]
    )

    assert rc == 0
    assert captured["job"].endpoint == "http://127.0.0.1:19090"
    assert captured["job"].docker_endpoint == "http://host.docker.internal:19090"
    assert captured["kwargs"]["environment_bridge_endpoint"] == "http://host.docker.internal:19090"
    assert captured["kwargs"]["environment_bridge_mode"] is True
