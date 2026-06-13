import time

from runner.agent_client import ToolInvokeResult
from runner.models import HardAssertionResults, RubricVerdict, TaskResult
import runner.main as main


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
    assert "required_tools" in out
    assert "forbidden_tools" in out
    assert "Error: boom" in out
    assert "Agent Error" in out
    assert "Judge Error" in out
    assert "Rubric Details" in out
