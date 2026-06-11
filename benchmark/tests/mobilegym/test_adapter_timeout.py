import importlib

import pytest


class DirtyDaemon:
    base_url = "http://daemon.local"
    control_token = "daemon-control"
    bridge_device_token = "bridge-device"

    def __init__(self, fail_cleanup=False):
        self.stop_calls = 0
        self.kill_calls = 0
        self.dirty = False
        self.fail_cleanup = fail_cleanup

    def stop(self):
        self.stop_calls += 1
        if self.fail_cleanup:
            raise OSError("stop failed")

    def kill(self):
        self.kill_calls += 1
        if self.fail_cleanup:
            raise OSError("kill failed")

    def mark_dirty(self):
        self.dirty = True


class TimeoutHTTPClient:
    def __init__(self):
        self.calls = []
        self.active_bridge_episode = None

    def post_json(self, url, payload, *, token=None, timeout=None):
        self.calls.append((url, payload, token, timeout))
        if url.endswith("/episode/start"):
            self.active_bridge_episode = payload["episode_id"]
            return {"ok": True}
        if url.endswith("/api/chat"):
            raise TimeoutError("chat timed out")
        if url.endswith("/episode/end"):
            self.active_bridge_episode = None
            return {"ok": True, "data": {"action_log": []}}
        return {"ok": True}

    def fake_tool_call_after_cleanup(self, episode_id):
        if episode_id != self.active_bridge_episode:
            raise RuntimeError("stale episode_id")


def test_timeout_ends_bridge_and_daemon_binding_then_stops_dirty_worker():
    module = importlib.import_module("mobilegym.adapter.aiden_go_agent")
    daemon = DirtyDaemon()
    client = TimeoutHTTPClient()
    agent = module.AidenGoAgent(
        bridge_url="http://bridge.local",
        bridge_control_token="bridge-control",
        daemon=daemon,
        http_client=client,
        episode_id_factory=lambda: "ep-timeout",
        chat_timeout_sec=0.01,
    )

    agent.reset("do a task")
    with pytest.raises(module.AidenAdapterTimeout) as excinfo:
        agent.act(obs=None)

    assert excinfo.value.worker_dirty is True
    assert daemon.dirty is True
    assert daemon.stop_calls == 1
    assert daemon.kill_calls == 1
    assert [call[0] for call in client.calls] == [
        "http://bridge.local/episode/start",
        "http://daemon.local/api/mobilegym/episode/start",
        "http://daemon.local/api/clear",
        "http://daemon.local/api/chat",
        "http://daemon.local/api/mobilegym/episode/end",
        "http://bridge.local/episode/end",
    ]
    with pytest.raises(RuntimeError, match="stale episode_id"):
        client.fake_tool_call_after_cleanup("ep-timeout")


def test_cleanup_failure_keeps_timeout_surfaced_and_marks_worker_dirty():
    module = importlib.import_module("mobilegym.adapter.aiden_go_agent")
    daemon = DirtyDaemon(fail_cleanup=True)
    client = TimeoutHTTPClient()
    agent = module.AidenGoAgent(
        bridge_url="http://bridge.local",
        bridge_control_token="bridge-control",
        daemon=daemon,
        http_client=client,
        episode_id_factory=lambda: "ep-dirty",
    )

    agent.reset("do a task")
    with pytest.raises(module.AidenAdapterTimeout) as excinfo:
        agent.act(obs=None)

    assert "cleanup failed" in str(excinfo.value)
    assert excinfo.value.worker_dirty is True
    assert daemon.dirty is True
    assert client.calls[-1][0] == "http://bridge.local/episode/end"
