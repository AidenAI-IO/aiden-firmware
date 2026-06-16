import abc
import importlib
import json
import socket
import sys
import types
import urllib.error
from enum import Enum

import pytest


class FakeDaemon:
    base_url = "http://daemon.local"
    control_token = "daemon-control"
    bridge_device_token = "bridge-device"

    def __init__(self):
        self.stop_calls = 0
        self.kill_calls = 0
        self.dirty = False

    def stop(self):
        self.stop_calls += 1

    def kill(self):
        self.kill_calls += 1

    def mark_dirty(self):
        self.dirty = True


class RecordingHTTPClient:
    def __init__(self):
        self.calls = []

    def post_json(self, url, payload, *, token=None, timeout=None):
        self.calls.append((url, payload, token, timeout))
        if url.endswith("/api/chat"):
            return {"response": "finished the task"}
        if url.endswith("/episode/end"):
            return {"ok": True, "data": {"action_log": []}}
        return {"ok": True}


def test_aiden_go_agent_act_runs_one_episode_and_returns_complete_action():
    module = importlib.import_module("mobilegym.adapter.aiden_go_agent")
    daemon = FakeDaemon()
    client = RecordingHTTPClient()
    agent = module.AidenGoAgent(
        bridge_url="http://bridge.local",
        bridge_control_token="bridge-control",
        daemon=daemon,
        http_client=client,
        episode_id_factory=lambda: "ep-test",
    )
    task = {"instruction": "count alarms"}

    agent.reset(task)
    action = agent.act(obs={"ignored": True})

    assert agent.task is task
    assert getattr(action.action_type, "name", action.action_type) == "COMPLETE"
    assert action.data["response"] == "finished the task"
    assert [call[0] for call in client.calls] == [
        "http://bridge.local/episode/start",
        "http://daemon.local/api/mobilegym/episode/start",
        "http://daemon.local/api/clear",
        "http://daemon.local/api/chat",
        "http://daemon.local/api/mobilegym/episode/end",
        "http://bridge.local/episode/end",
    ]
    assert client.calls[0][1] == {"episode_id": "ep-test"}
    assert client.calls[0][2] == "bridge-control"
    assert client.calls[1][1] == {
        "episode_id": "ep-test",
        "bridge_url": "http://bridge.local",
        "bridge_token": "bridge-device",
    }
    assert client.calls[1][2] == "daemon-control"
    assert client.calls[2][1] == {}
    assert client.calls[3][1] == {"message": "count alarms", "episode_id": "ep-test"}


def test_aiden_go_agent_reset_runs_aiden_suite_setup_with_runtime_memory_dir(monkeypatch):
    module = importlib.import_module("mobilegym.adapter.aiden_go_agent")
    daemon = FakeDaemon()
    client = RecordingHTTPClient()
    agent = module.AidenGoAgent(
        bridge_url="http://bridge.local",
        bridge_control_token="bridge-control",
        daemon=daemon,
        http_client=client,
    )
    monkeypatch.setenv("AIDEN_RUNTIME_CONFIG_DIR", "/tmp/aiden-config")

    class Task:
        id = "personamem_lt_recall_v1.personamem_music"
        metadata = {
            "aiden_suite_name": "personamem_lt_recall_v1",
            "global_reset": {
                "tool_sequence": [
                    {
                        "tool": "shell",
                        "args": {
                            "command": "rm -rf /userdata/agent/memory/long_term",
                        },
                    }
                ],
            },
            "setup": {
                "tool_sequence": [
                    {
                        "tool": "shell",
                        "args": {
                            "command": "mkdir -p /userdata/agent/memory/long_term/memories",
                        },
                    }
                ],
            },
        }

    agent.reset(Task())

    assert [call[0] for call in client.calls] == [
        "http://daemon.local/api/clear",
        "http://daemon.local/api/tools/shell",
        "http://daemon.local/api/tools/shell",
    ]
    commands = [call[1]["input"]["command"] for call in client.calls[1:]]
    assert commands == [
        "rm -rf /tmp/aiden-config/memory/long_term",
        "mkdir -p /tmp/aiden-config/memory/long_term/memories",
    ]


def test_aiden_go_agent_records_chat_history_on_task_for_suite_evaluate():
    module = importlib.import_module("mobilegym.adapter.aiden_go_agent")

    class HistoryHTTPClient(RecordingHTTPClient):
        def post_json(self, url, payload, *, token=None, timeout=None):
            self.calls.append((url, payload, token, timeout))
            if url.endswith("/api/chat"):
                return {
                    "response": "I recall the stored preference.\n<final_answer>(c)</final_answer>",
                    "history": [
                        {
                            "type": "tool_result",
                            "tool_name": "recall_memory",
                            "content": json.dumps({"results": [{"id": "mem_expected"}]}),
                        }
                    ],
                }
            if url.endswith("/episode/end"):
                return {"ok": True, "data": {"action_log": []}}
            return {"ok": True}

    class Task:
        id = "personamem_lt_recall_v1.personamem_music"
        instruction = "Choose one option."
        metadata = {"aiden_suite_name": "personamem_lt_recall_v1"}

    task = Task()
    agent = module.AidenGoAgent(
        bridge_url="http://bridge.local",
        bridge_control_token="bridge-control",
        daemon=FakeDaemon(),
        http_client=HistoryHTTPClient(),
        episode_id_factory=lambda: "ep-history",
    )

    agent.reset(task)
    action = agent.act(obs=None)

    assert task.metadata["aiden_last_response"].endswith("<final_answer>(c)</final_answer>")
    assert task.metadata["aiden_last_chat_history"][0]["tool_name"] == "recall_memory"
    assert action.data["aiden_last_response"].endswith("<final_answer>(c)</final_answer>")
    assert action.data["aiden_last_chat_history"][0]["tool_name"] == "recall_memory"


def test_json_http_client_classifies_urlerror_wrapped_socket_timeout(monkeypatch):
    module = importlib.import_module("mobilegym.adapter.aiden_go_agent")

    def raise_timeout(request, *, timeout=None):
        raise urllib.error.URLError(socket.timeout("timed out"))

    monkeypatch.setattr(module.urllib.request, "urlopen", raise_timeout)

    with pytest.raises(module.AidenAdapterTimeout):
        module.JsonHTTPClient().post_json(
            "http://daemon.local/api/chat",
            {"message": "count alarms"},
            timeout=0.01,
        )


def test_register_shim_registers_aiden_go_without_mobilegym_llm(monkeypatch):
    registered = {}
    bench_env = types.ModuleType("bench_env")
    agent_module = types.ModuleType("bench_env.agent")

    def register_agent(name, agent_cls):
        registered[name] = agent_cls

    agent_module.register_agent = register_agent
    bench_env.agent = agent_module
    monkeypatch.setitem(sys.modules, "bench_env", bench_env)
    monkeypatch.setitem(sys.modules, "bench_env.agent", agent_module)

    register = importlib.import_module("mobilegym.adapter.register")
    aiden = importlib.import_module("mobilegym.adapter.aiden_go_agent")

    register.register_with_mobilegym()

    assert registered == {"aiden_go": aiden.AidenGoAgent}


def test_aiden_go_agent_uses_mobilegym_action_classes_when_available(monkeypatch):
    class FakeActionType(str, Enum):
        COMPLETE = "COMPLETE"

    class FakeAction:
        def __init__(self, action_type, data):
            self.action_type = action_type
            self.data = data

    bench_env = types.ModuleType("bench_env")
    env_module = types.ModuleType("bench_env.env")
    base_module = types.ModuleType("bench_env.env.base")
    base_module.Action = FakeAction
    base_module.ActionType = FakeActionType
    bench_env.env = env_module
    env_module.base = base_module
    monkeypatch.setitem(sys.modules, "bench_env", bench_env)
    monkeypatch.setitem(sys.modules, "bench_env.env", env_module)
    monkeypatch.setitem(sys.modules, "bench_env.env.base", base_module)

    module = importlib.import_module("mobilegym.adapter.aiden_go_agent")
    action = module.complete_action("done")

    assert isinstance(action, FakeAction)
    assert action.action_type == FakeActionType.COMPLETE
    assert action.data == {"response": "done"}


def test_aiden_go_agent_satisfies_mobilegym_base_agent_contract(monkeypatch):
    class FakeBaseAgent(abc.ABC):
        @property
        @abc.abstractmethod
        def name(self):
            raise NotImplementedError

        @abc.abstractmethod
        def parse_response(self, response_text):
            raise NotImplementedError

        @abc.abstractmethod
        def build_messages(self, obs):
            raise NotImplementedError

        @abc.abstractmethod
        def reset(self, task):
            raise NotImplementedError

        @abc.abstractmethod
        def act(self, obs):
            raise NotImplementedError

    bench_env = types.ModuleType("bench_env")
    agent_module = types.ModuleType("bench_env.agent")
    agent_module.BaseAgent = FakeBaseAgent
    bench_env.agent = agent_module
    monkeypatch.setitem(sys.modules, "bench_env", bench_env)
    monkeypatch.setitem(sys.modules, "bench_env.agent", agent_module)

    old_module = sys.modules.pop("mobilegym.adapter.aiden_go_agent", None)
    try:
        module = importlib.import_module("mobilegym.adapter.aiden_go_agent")

        agent = module.AidenGoAgent(
            bridge_url="http://bridge.local",
            bridge_control_token="bridge-control",
            daemon=FakeDaemon(),
        )

        assert agent.name == "aiden_go"
        assert agent.build_messages(obs=None) == []
        assert agent.parse_response("done").data["response"] == "done"
    finally:
        sys.modules.pop("mobilegym.adapter.aiden_go_agent", None)
        if old_module is not None:
            sys.modules["mobilegym.adapter.aiden_go_agent"] = old_module
