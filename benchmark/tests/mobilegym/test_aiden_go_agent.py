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


def test_aiden_go_agent_runs_setup_after_mobilegym_episode_is_active():
    module = importlib.import_module("mobilegym.adapter.aiden_go_agent")

    class BridgeAwareHTTPClient(RecordingHTTPClient):
        def __init__(self):
            super().__init__()
            self.episode_active = False

        def post_json(self, url, payload, *, token=None, timeout=None):
            self.calls.append((url, payload, token, timeout))
            if url.endswith("/api/mobilegym/episode/start"):
                self.episode_active = True
                return {"ok": True}
            if url.endswith("/api/mobilegym/episode/end"):
                self.episode_active = False
                return {"ok": True}
            if url.endswith("/api/tools/keyboard_tap"):
                if not self.episode_active:
                    return {
                        "is_error": True,
                        "output": "error: keyboard_tap failed: mobilegym bridge episode is not active",
                    }
                return {"is_error": False, "output": "ok"}
            if url.endswith("/api/chat"):
                return {"response": "done"}
            if url.endswith("/episode/end"):
                return {"ok": True, "data": {"action_log": []}}
            return {"ok": True}

    class Task:
        id = "suite.case_one"
        instruction = "Do it."
        metadata = {
            "aiden_suite_name": "suite",
            "global_reset": {
                "tool_sequence": [
                    {"tool": "keyboard_tap", "args": {"keys": ["meta", "h"]}},
                ],
            },
        }

    client = BridgeAwareHTTPClient()
    agent = module.AidenGoAgent(
        bridge_url="http://bridge.local",
        bridge_control_token="bridge-control",
        daemon=FakeDaemon(),
        http_client=client,
        episode_id_factory=lambda: "ep-setup",
    )

    agent.reset(Task())
    agent.act(obs=None)

    assert [call[0] for call in client.calls] == [
        "http://bridge.local/episode/start",
        "http://daemon.local/api/mobilegym/episode/start",
        "http://daemon.local/api/clear",
        "http://daemon.local/api/tools/keyboard_tap",
        "http://daemon.local/api/chat",
        "http://daemon.local/api/mobilegym/episode/end",
        "http://bridge.local/episode/end",
    ]


def test_aiden_go_agent_act_runs_aiden_suite_setup_with_runtime_memory_dir(monkeypatch):
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
        instruction = "Recall the music preference."
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

    assert client.calls == []

    agent.act(obs=None)

    assert [call[0] for call in client.calls] == [
        "http://bridge.local/episode/start",
        "http://daemon.local/api/mobilegym/episode/start",
        "http://daemon.local/api/clear",
        "http://daemon.local/api/mobilegym/setup/shell",
        "http://daemon.local/api/mobilegym/setup/shell",
        "http://daemon.local/api/chat",
        "http://daemon.local/api/mobilegym/episode/end",
        "http://bridge.local/episode/end",
    ]
    commands = [call[1]["input"]["command"] for call in client.calls[3:5]]
    assert commands == [
        "rm -rf /tmp/aiden-config/memory/long_term",
        "mkdir -p /tmp/aiden-config/memory/long_term/memories",
    ]


def test_aiden_go_agent_marks_daemon_dirty_when_suite_setup_fails():
    module = importlib.import_module("mobilegym.adapter.aiden_go_agent")

    class SetupFailHTTPClient(RecordingHTTPClient):
        def post_json(self, url, payload, *, token=None, timeout=None):
            self.calls.append((url, payload, token, timeout))
            if url.endswith("/api/mobilegym/setup/shell"):
                return {"is_error": True, "output": "setup exploded"}
            if url.endswith("/episode/end"):
                return {"ok": True, "data": {"action_log": []}}
            return {"ok": True}

    class Task:
        id = "suite.case_one"
        instruction = "Do it."
        metadata = {
            "aiden_suite_name": "suite",
            "setup": {
                "tool_sequence": [
                    {"tool": "shell", "args": {"command": "false"}},
                ],
            },
        }

    daemon = FakeDaemon()
    agent = module.AidenGoAgent(
        bridge_url="http://bridge.local",
        bridge_control_token="bridge-control",
        daemon=daemon,
        http_client=SetupFailHTTPClient(),
        episode_id_factory=lambda: "ep-setup-fail",
    )

    agent.reset(Task())
    with pytest.raises(module.AidenAdapterError, match="setup exploded") as exc:
        agent.act(obs=None)

    assert exc.value.worker_dirty is True
    assert daemon.dirty is True
    assert daemon.stop_calls == 1
    assert daemon.kill_calls == 1


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


def test_aiden_go_agent_resolves_prompt_string_to_aiden_suite_task(tmp_path):
    module = importlib.import_module("mobilegym.adapter.aiden_go_agent")

    instruction = (
        "You are completing text-only tasks. End with <final_answer>(b)</final_answer>.\n\n"
        "No computation is needed. Select option (b)."
    )

    class TaggedHTTPClient(RecordingHTTPClient):
        def post_json(self, url, payload, *, token=None, timeout=None):
            self.calls.append((url, payload, token, timeout))
            if url.endswith("/api/chat"):
                return {"response": "<final_answer>(b)</final_answer>"}
            if url.endswith("/episode/end"):
                return {"ok": True, "data": {"action_log": []}}
            return {"ok": True}

    task = types.SimpleNamespace(
        id="loop_planning_v1.direct_answer_no_plan",
        instruction=instruction,
        metadata={
            "aiden_suite_name": "loop_planning_v1",
            "aiden_task_id": "direct_answer_no_plan",
        },
    )
    agent = module.AidenGoAgent(
        bridge_url="http://bridge.local",
        bridge_control_token="bridge-control",
        daemon=FakeDaemon(),
        http_client=TaggedHTTPClient(),
        episode_id_factory=lambda: "ep-prompt-lookup",
        artifact_dir=tmp_path / "raw" / "run",
        task_lookup={instruction: task},
    )

    agent.reset(instruction)
    action = agent.act(obs=None)

    assert agent.task is task
    assert task.metadata["aiden_last_response"] == "<final_answer>(b)</final_answer>"
    assert action.data["aiden_last_response"] == "<final_answer>(b)</final_answer>"
    meta_path = tmp_path / "raw" / "run" / "trajectory" / "loop_planning_v1_direct_answer_no_plan" / "meta.json"
    assert json.loads(meta_path.read_text(encoding="utf-8"))["task_id"] == "loop_planning_v1.direct_answer_no_plan"


def test_aiden_go_agent_fails_on_unknown_string_task_when_lookup_is_configured():
    module = importlib.import_module("mobilegym.adapter.aiden_go_agent")
    agent = module.AidenGoAgent(
        bridge_url="http://bridge.local",
        bridge_control_token="bridge-control",
        daemon=FakeDaemon(),
        http_client=RecordingHTTPClient(),
        task_lookup={"known instruction": object()},
    )

    with pytest.raises(module.AidenAdapterError, match="task lookup miss"):
        agent.reset("unknown instruction")


def test_aiden_go_agent_writes_task_meta_with_aiden_evidence(tmp_path):
    module = importlib.import_module("mobilegym.adapter.aiden_go_agent")

    task = types.SimpleNamespace(
        task_id="suite.case_one",
        metadata={
            "aiden_suite_name": "suite",
            "aiden_task_id": "case_one",
            "description_for_judge": "Judge it.",
            "rubric": [{"id": "ok", "check": "ok"}],
            "hard_assertions": {"min_tool_calls": 0, "max_tool_calls": 5},
            "aiden_last_response": "done",
            "aiden_last_chat_history": [{"type": "assistant", "content": "done"}],
        },
    )

    artifact_dir = module._task_artifact_dir(tmp_path, task)
    module._write_task_meta(artifact_dir, task)

    meta = json.loads((artifact_dir / "meta.json").read_text(encoding="utf-8"))
    assert meta["task_id"] == "suite.case_one"
    assert meta["aiden_last_response"] == "done"
    assert meta["aiden_last_chat_history"] == [{"type": "assistant", "content": "done"}]
    assert meta["description_for_judge"] == "Judge it."
    assert meta["hard_assertions"] == {"min_tool_calls": 0, "max_tool_calls": 5}


def test_aiden_go_agent_act_writes_task_meta_without_action_log(tmp_path):
    module = importlib.import_module("mobilegym.adapter.aiden_go_agent")

    class NoActionLogHTTPClient(RecordingHTTPClient):
        def post_json(self, url, payload, *, token=None, timeout=None):
            self.calls.append((url, payload, token, timeout))
            if url.endswith("/api/chat"):
                return {
                    "response": "done",
                    "history": [{"type": "assistant", "content": "done"}],
                }
            if url.endswith("/episode/end"):
                return {"ok": True, "data": {}}
            return {"ok": True}

    class Task:
        id = "suite.case_one"
        instruction = "Do it."
        metadata = {
            "aiden_suite_name": "suite",
            "aiden_task_id": "case_one",
            "description_for_judge": "Judge it.",
            "rubric": [{"id": "ok", "check": "ok"}],
            "hard_assertions": {"min_tool_calls": 0, "max_tool_calls": 5},
        }

    task = Task()
    agent = module.AidenGoAgent(
        bridge_url="http://bridge.local",
        bridge_control_token="bridge-control",
        daemon=FakeDaemon(),
        http_client=NoActionLogHTTPClient(),
        episode_id_factory=lambda: "ep-no-actions",
        artifact_dir=tmp_path / "raw" / "run",
    )

    agent.reset(task)
    agent.act(obs=None)

    meta_path = tmp_path / "raw" / "run" / "trajectory" / "suite_case_one" / "meta.json"
    meta = json.loads(meta_path.read_text(encoding="utf-8"))
    assert meta["aiden_last_response"] == "done"
    assert meta["aiden_last_chat_history"] == [{"type": "assistant", "content": "done"}]
    assert not (meta_path.parent / "aiden_bridge_actions.json").exists()


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
