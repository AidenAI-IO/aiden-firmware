import time

import pytest

from runner.agent_client import AgentRequestError, AgentTimeoutError
from runner.agent_client import ChatResponse
from runner.recovery import prepare_task_isolation, wait_for_agent_ready
from runner.reset import ResetError
from runner.suite import HardAssertions, RubricItem, Suite, TaskSpec


class FlakyHealthClient:
    def __init__(self, ready_after: int):
        self.attempts = 0
        self.ready_after = ready_after
        self.clears = 0

    def health(self) -> bool:
        self.attempts += 1
        return self.attempts > self.ready_after

    def clear_history(self, timeout: int = 30) -> None:
        self.clears += 1

    def recover_after_timeout(self, timeout_sec: int = 90, poll_sec: float = 3.0) -> bool:
        return True

    def invoke_tool(self, name, args):
        raise AssertionError("unexpected tool call")


def test_wait_for_agent_ready_polls_until_health(monkeypatch):
    sleeps = []
    monkeypatch.setattr(time, "sleep", lambda seconds: sleeps.append(seconds))
    client = FlakyHealthClient(ready_after=2)

    assert wait_for_agent_ready(client, timeout_sec=10, poll_sec=1) is True
    assert client.attempts == 3
    assert sleeps == [1, 1]


def test_wait_for_agent_ready_times_out(monkeypatch):
    monkeypatch.setattr(time, "sleep", lambda _seconds: None)
    client = FlakyHealthClient(ready_after=99)

    assert wait_for_agent_ready(client, timeout_sec=0, poll_sec=1) is False


class SetupClient:
    def __init__(self, fail_clears: int = 0):
        self.fail_clears = fail_clears
        self.clears = 0
        self.chats = []
        self.seeded_memories = []

    def health(self) -> bool:
        return True

    def recover_after_timeout(self, timeout_sec: int = 90, poll_sec: float = 3.0) -> bool:
        return True

    def clear_history(self, timeout: int = 30) -> None:
        self.clears += 1
        if self.clears <= self.fail_clears:
            raise AgentTimeoutError("clear timed out")

    def invoke_tool(self, name, args):
        raise AssertionError("unexpected agent-side tool call")

    def chat(self, message, timeout_sec=None, benchmark_task_id=None):
        self.chats.append((message, timeout_sec, benchmark_task_id))
        return ChatResponse(response="done", history=[])

    def seed_memory(self, memory, timeout=30):
        self.seeded_memories.append((memory, timeout))
        return {"status": "seeded", "id": memory["id"]}


def test_prepare_task_isolation_retries_clear(monkeypatch):
    sleeps = []
    monkeypatch.setattr(time, "sleep", lambda seconds: sleeps.append(seconds))
    client = SetupClient(fail_clears=1)
    suite = Suite(
        name="phone",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=__import__("pathlib").Path("suite.json"),
    )
    task = TaskSpec(
        id="open_settings",
        category="single_step",
        description_for_judge="Open settings.",
        prompt="open settings",
        rubric=[RubricItem(id="ok", check="ok")],
        hard_assertions=HardAssertions(min_tool_calls=1, max_tool_calls=3),
    )

    prepare_task_isolation(client, suite, task, ready_timeout_sec=10, setup_attempts=3)

    assert client.clears == 2


def test_prepare_task_isolation_runs_agent_prompt_after_environment_setup(monkeypatch):
    setup_calls = []

    def fake_environment_setup(environment_url, task_id=None, timeout=30):
        setup_calls.append((environment_url, task_id, timeout))

    monkeypatch.setattr(
        "runner.recovery.call_environment_setup",
        fake_environment_setup,
    )
    client = SetupClient()
    suite = Suite(
        name="phone",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=__import__("pathlib").Path("suite.json"),
        prompt_prefix="ADB benchmark rules",
    )
    task = TaskSpec(
        id="open_settings",
        category="single_step",
        description_for_judge="Open settings.",
        prompt="open settings",
        rubric=[RubricItem(id="ok", check="ok")],
        hard_assertions=HardAssertions(min_tool_calls=1, max_tool_calls=3),
        setup={"type": "agent_prompt", "prompt": "open a settings sub-page", "timeout_sec": 45},
    )

    prepare_task_isolation(
        client,
        suite,
        task,
        environment_url="http://127.0.0.1:9090",
        benchmark_task_id="suite.json:open_settings",
        ready_timeout_sec=10,
        setup_attempts=1,
    )

    assert setup_calls == [("http://127.0.0.1:9090", "suite.json:open_settings", 180)]
    assert client.chats == [
        ("ADB benchmark rules\n\nopen a settings sub-page", 45, "suite.json:open_settings")
    ]
    assert client.clears == 2


def test_prepare_task_isolation_runs_seed_memory_with_environment_setup(monkeypatch):
    setup_calls = []

    def fake_environment_setup(environment_url, task_id=None, timeout=30):
        setup_calls.append((environment_url, task_id, timeout))

    monkeypatch.setattr(
        "runner.recovery.call_environment_setup",
        fake_environment_setup,
    )
    client = SetupClient()
    suite = Suite(
        name="memory",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=__import__("pathlib").Path("personamem.json"),
    )
    task = TaskSpec(
        id="recall_fact",
        category="memory",
        description_for_judge="Recall a seeded memory.",
        prompt="what do I like?",
        rubric=[RubricItem(id="ok", check="ok")],
        hard_assertions=HardAssertions(min_tool_calls=1, max_tool_calls=3),
        setup={
            "type": "seed_memory",
            "memories": [
                {
                    "id": "mem-1",
                    "content": "The user likes flashcards.",
                    "tags": ["study"],
                }
            ],
            "timeout_sec": 7,
        },
    )

    prepare_task_isolation(
        client,
        suite,
        task,
        environment_url="http://127.0.0.1:9090",
        benchmark_task_id="personamem.json:recall_fact",
        ready_timeout_sec=10,
        setup_attempts=1,
    )

    assert setup_calls == [("http://127.0.0.1:9090", "personamem.json:recall_fact", 180)]
    assert client.seeded_memories == [
        ({"id": "mem-1", "content": "The user likes flashcards.", "tags": ["study"]}, 7)
    ]


def test_prepare_task_isolation_raises_after_exhausting_retries(monkeypatch):
    monkeypatch.setattr(time, "sleep", lambda _seconds: None)
    client = SetupClient(fail_clears=3)
    suite = Suite(
        name="phone",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=__import__("pathlib").Path("suite.json"),
    )
    task = TaskSpec(
        id="open_settings",
        category="single_step",
        description_for_judge="Open settings.",
        prompt="open settings",
        rubric=[RubricItem(id="ok", check="ok")],
        hard_assertions=HardAssertions(min_tool_calls=1, max_tool_calls=3),
    )

    with pytest.raises(AgentTimeoutError, match="clear timed out"):
        prepare_task_isolation(client, suite, task, ready_timeout_sec=10, setup_attempts=2)


def test_prepare_task_isolation_raises_when_agent_never_ready(monkeypatch):
    monkeypatch.setattr(time, "sleep", lambda _seconds: None)

    class DownClient:
        def health(self) -> bool:
            return False

    suite = Suite(
        name="phone",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=__import__("pathlib").Path("suite.json"),
    )
    task = TaskSpec(
        id="open_settings",
        category="single_step",
        description_for_judge="Open settings.",
        prompt="open settings",
        rubric=[RubricItem(id="ok", check="ok")],
        hard_assertions=HardAssertions(min_tool_calls=1, max_tool_calls=3),
    )

    with pytest.raises(ResetError, match="agent not ready"):
        prepare_task_isolation(DownClient(), suite, task, ready_timeout_sec=0)
