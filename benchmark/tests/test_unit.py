import json
from pathlib import Path

from runner.agent_client import ToolInvokeResult
from runner.unit import _check_expectation, _collect_suites, _run_suite, is_unit_suite


class FakeClient:
    def __init__(self, outputs):
        self.outputs = list(outputs)
        self.calls = []

    def invoke_tool(self, name, args, timeout=90):
        self.calls.append((name, args, timeout))
        return self.outputs.pop(0)


def test_check_expectation_validates_json_rules():
    error = _check_expectation(
        {"ok": True, "json": {"actions": {"type": "array", "min_len": 1}}},
        '{"actions":["home"]}',
        {"actions": ["home"]},
        is_error=False,
    )

    assert error == ""


def test_check_expectation_reports_missing_text():
    error = _check_expectation(
        {"ok": False, "contains": ["invalid"]},
        "unsupported platform",
        None,
        is_error=True,
    )

    assert "missing text" in error


def test_run_suite_invokes_tool_with_object_payload(tmp_path: Path):
    suite = tmp_path / "quick_action.json"
    suite.write_text(
        json.dumps({
            "kind": "unit",
            "name": "quick_action",
            "target": {"type": "tool", "name": "quick_action"},
            "tests": [{
                "id": "list_ios",
                "input": {"list": True, "platform": "ios"},
                "expect": {"ok": True, "json": {"actions": {"type": "array"}}},
            }],
        }),
        encoding="utf-8",
    )
    client = FakeClient([
        ToolInvokeResult(output='{"actions":["home"]}', is_error=False, duration_ms=12)
    ])

    results = _run_suite(client, suite)

    assert results[0].status == "passed"
    assert client.calls == [("quick_action", {"list": True, "platform": "ios"}, 10)]


def test_collect_suites_filters_unit_kind(tmp_path: Path):
    unit = tmp_path / "unit.json"
    regular = tmp_path / "regular.json"
    unit.write_text('{"kind":"unit"}', encoding="utf-8")
    regular.write_text('{"name":"regular"}', encoding="utf-8")

    class Args:
        suite = None
        suite_dir = str(tmp_path)

    assert is_unit_suite(unit) is True
    assert _collect_suites(Args()) == [unit]
