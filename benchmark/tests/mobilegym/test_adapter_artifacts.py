import importlib
import json


def test_exports_bridge_action_logs_to_trajectory_artifacts(tmp_path):
    artifacts = importlib.import_module("mobilegym.adapter.artifacts")
    logs = [
        {
            "episode_id": "ep1",
            "action_id": "ep1:0001",
            "tool_name": "touch_gesture",
            "tool_input": {"type": "tap", "point": {"x": 0.5, "y": 0.5}},
            "mobilegym_action": {"action_type": "CLICK", "data": {"point": [540, 1200]}},
            "screenshot": {"format": "jpeg", "data": "/9j/..."},
            "duration_ms": 123,
            "error": None,
        }
    ]

    output = artifacts.export_bridge_actions(tmp_path, logs)

    assert output == tmp_path / "aiden_bridge_actions.json"
    payload = json.loads(output.read_text())
    assert payload[0]["action_id"] == "ep1:0001"
    assert payload[0]["tool_name"] == "touch_gesture"
    assert payload[0]["tool_input"] == {"type": "tap", "point": {"x": 0.5, "y": 0.5}}
    assert payload[0]["mobilegym_action"] == {"action_type": "CLICK", "data": {"point": [540, 1200]}}
    assert payload[0]["screenshot"]["format"] == "jpeg"
    assert payload[0]["screenshot"]["data"] == "/9j/..."
    assert payload[0]["duration_ms"] == 123
    assert payload[0]["error"] is None


def test_agent_exports_bridge_end_action_logs_when_artifact_dir_is_set(tmp_path):
    aiden = importlib.import_module("mobilegym.adapter.aiden_go_agent")

    class FakeDaemon:
        base_url = "http://daemon.local"
        control_token = "daemon-control"
        bridge_device_token = "bridge-device"

        def mark_dirty(self):
            raise AssertionError("successful run should not mark dirty")

    class FakeClient:
        def post_json(self, url, payload, *, token=None, timeout=None):
            if url.endswith("/api/chat"):
                return {"response": "done"}
            if url.endswith("/episode/end"):
                return {
                    "ok": True,
                    "data": {
                        "action_log": [
                            {
                                "episode_id": payload["episode_id"],
                                "action_id": f"{payload['episode_id']}:0001",
                                "tool_name": "tap",
                                "tool_input": {"x": 0.1, "y": 0.2},
                                "mobilegym_action": {"action_type": "CLICK", "data": {}},
                                "screenshot": {"format": "png", "data": "abc"},
                                "duration_ms": 5,
                                "error": None,
                            }
                        ]
                    },
                }
            return {"ok": True}

    agent = aiden.AidenGoAgent(
        bridge_url="http://bridge.local",
        bridge_control_token="bridge-control",
        daemon=FakeDaemon(),
        http_client=FakeClient(),
        episode_id_factory=lambda: "ep-artifact",
        artifact_dir=tmp_path,
    )

    agent.reset("task")
    action = agent.act(obs=None)

    assert action.data["response"] == "done"
    payload = json.loads((tmp_path / "aiden_bridge_actions.json").read_text())
    assert payload[0]["action_id"] == "ep-artifact:0001"
    assert payload[0]["screenshot"] == {"format": "png", "data": "abc"}
