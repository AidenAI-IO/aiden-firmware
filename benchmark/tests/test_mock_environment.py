from __future__ import annotations

import json
import urllib.request
from pathlib import Path

from runner.mock_environment import MockEnvironmentServer
from runner.suite import load_suite


def _json_request(url: str, method: str = "GET", payload: dict | None = None) -> dict:
    data = None if payload is None else json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=data,
        headers={"Content-Type": "application/json"},
        method=method,
    )
    with urllib.request.urlopen(req, timeout=5) as response:
        return json.loads(response.read().decode("utf-8"))


def test_mock_environment_returns_scripted_tool_result_and_updates_screen():
    suite_path = (
        Path(__file__).resolve().parents[1]
        / "suites"
        / "aiden_app"
        / "ios_pip_background_v1.json"
    )
    suite = load_suite(suite_path)
    assert suite.mock_environment is not None
    server = MockEnvironmentServer(suite.mock_environment, suite_path.parent)
    base_url = server.start()
    try:
        setup = _json_request(f"{base_url}/api/setup", "POST", {})
        assert setup["ok"] is True

        contacts = _json_request(
            f"{base_url}/api/tools/bridge_contacts",
            "POST",
            {"input": json.dumps({"action": "query", "query": "Biden"})},
        )
        output = json.loads(contacts["output"])
        assert output["contacts"][0]["phone_numbers"] == ["+1 202-555-0147"]

        blocked = _json_request(
            f"{base_url}/api/tools/enter_text_via_bridge",
            "POST",
            {
                "input": json.dumps(
                    {
                        "text": "+1 202-555-0147",
                        "platform": "ios",
                        "focus": {"x": 500, "y": 360, "coord_space": "normalized"},
                    }
                )
            },
        )
        assert blocked["is_error"] is True

        opened = _json_request(
            f"{base_url}/api/tools/search_launch_app",
            "POST",
            {"input": json.dumps({"app": "备忘录"})},
        )
        assert json.loads(opened["output"])["ok"] is True

        entered = _json_request(
            f"{base_url}/api/tools/enter_text_via_bridge",
            "POST",
            {
                "input": json.dumps(
                    {
                        "text": "+1 202-555-0147",
                        "platform": "ios",
                        "focus": {
                            "x": 500,
                            "y": 360,
                            "coord_space": "normalized",
                        },
                    }
                )
            },
        )
        assert json.loads(entered["output"])["committed"] is True

        state = _json_request(f"{base_url}/api/state")
        assert state["data"]["calls"][-1]["tool"] == "enter_text_via_bridge"
        assert "+1 202-555-0147" in state["data"]["screen_text"]

        screen = _json_request(f"{base_url}/api/screen")
        screenshot = screen["data"]["screenshot"]
        assert screenshot["width"] == 1170
        assert screenshot["data"]
    finally:
        server.stop()


def test_mock_environment_requires_visible_icon_click_before_text_entry():
    suite_path = (
        Path(__file__).resolve().parents[1]
        / "suites"
        / "aiden_app"
        / "ios_pip_notes_icon_visible_v1.json"
    )
    suite = load_suite(suite_path)
    assert suite.mock_environment is not None
    server = MockEnvironmentServer(suite.mock_environment, suite_path.parent)
    base_url = server.start()
    try:
        blocked = _json_request(
            f"{base_url}/api/tools/enter_text_via_bridge",
            "POST",
            {
                "input": json.dumps(
                    {
                        "text": "+1 202-555-0147",
                        "platform": "ios",
                        "focus": {"x": 500, "y": 360, "coord_space": "normalized"},
                    }
                )
            },
        )
        assert blocked["is_error"] is True

        clicked = _json_request(
            f"{base_url}/api/tools/mouse_click",
            "POST",
            {
                "input": json.dumps(
                    {"x": 180, "y": 310, "coord_space": "normalized"}
                )
            },
        )
        assert json.loads(clicked["output"])["clicked"] is True

        entered = _json_request(
            f"{base_url}/api/tools/enter_text_via_bridge",
            "POST",
            {
                "input": json.dumps(
                    {
                        "text": "+1 202-555-0147",
                        "platform": "ios",
                        "focus": {"x": 500, "y": 360, "coord_space": "normalized"},
                    }
                )
            },
        )
        assert json.loads(entered["output"])["committed"] is True
    finally:
        server.stop()


def test_mock_environment_allows_direct_entry_when_notes_is_already_open():
    suite_path = (
        Path(__file__).resolve().parents[1]
        / "suites"
        / "aiden_app"
        / "ios_pip_notes_open_v1.json"
    )
    suite = load_suite(suite_path)
    assert suite.mock_environment is not None
    server = MockEnvironmentServer(suite.mock_environment, suite_path.parent)
    base_url = server.start()
    try:
        entered = _json_request(
            f"{base_url}/api/tools/enter_text_via_bridge",
            "POST",
            {
                "input": json.dumps(
                    {
                        "text": "+1 202-555-0147",
                        "platform": "ios",
                        "focus": {"x": 500, "y": 360, "coord_space": "normalized"},
                    }
                )
            },
        )
        assert json.loads(entered["output"])["committed"] is True
    finally:
        server.stop()


def test_mock_environment_rejects_unconfigured_input_when_no_default_matches(tmp_path):
    suite_path = tmp_path / "suite.json"
    suite_path.write_text(
        json.dumps(
            {
                "name": "mock",
                "mock_environment": {
                    "phone_bridge": {"platform": "ios"},
                    "tools": {
                        "bridge_contacts": {
                            "input_contains": {"query": "Biden"},
                            "output": {"ok": True},
                        }
                    },
                },
                "tasks": [
                    {
                        "id": "t1",
                        "category": "diagnostic",
                        "description_for_judge": "test",
                        "prompt": "test",
                        "rubric": [{"id": "done", "check": "done"}],
                    }
                ],
            }
        ),
        encoding="utf-8",
    )
    suite = load_suite(suite_path)
    assert suite.mock_environment is not None
    server = MockEnvironmentServer(suite.mock_environment, tmp_path)
    base_url = server.start()
    try:
        result = _json_request(
            f"{base_url}/api/tools/bridge_contacts",
            "POST",
            {"input": json.dumps({"query": "Alice"})},
        )
        assert result["is_error"] is True
        assert "no mock response" in result["error"]
    finally:
        server.stop()
