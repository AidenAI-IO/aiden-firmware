import json
from pathlib import Path

from runner.agent_client import AgentTimeoutError, ChatResponse
from runner.judge import JudgeConfig, JudgeOutput
from runner.models import RubricVerdict
from runner.runtask import run_one_task
from runner.suite import HardAssertions, RubricItem, Suite, TaskSpec, TraceObservationSpec
import runner.runtask as runtask_mod


class FakeClient:
    def __init__(self, response="ok"):
        self.response = response
        self.messages = []
        self.skill_requests = []

    def health(self):
        return True

    def clear_history(self, timeout=30):
        pass

    def recover_after_timeout(self, timeout_sec=90, poll_sec=3.0):
        return True

    def invoke_tool(self, name, args):
        raise AssertionError(f"unexpected tool invoke: {name}")

    def chat(self, message, timeout_sec=None, attachments=None, skills=None):
        self.messages.append(message)
        self.skill_requests.append(list(skills or []))
        return ChatResponse(
            response=self.response,
            history=[{"type": "assistant", "content": self.response}],
        )


def test_run_one_task_passes_without_judge_when_hard_assertions_pass(tmp_path: Path):
    suite = Suite(
        name="smoke",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
    )
    task = TaskSpec(
        id="chat_smoke",
        category="diagnostic",
        description_for_judge="Smoke chat task.",
        prompt="ping",
        rubric=[RubricItem(id="responds", check="Agent responds.")],
        hard_assertions=HardAssertions(min_tool_calls=0, max_tool_calls=0),
    )

    result = run_one_task(
        FakeClient(), suite, task, 1, tmp_path / "artifacts", None, None, "run-1"
    )

    assert result.status == "passed"
    assert result.hard_assertions.response_exists is True


def test_run_one_task_fails_without_judge_when_expected_answer_is_wrong(tmp_path: Path):
    suite = Suite(
        name="persona",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
    )
    task = TaskSpec(
        id="personamem_case",
        category="memory",
        description_for_judge="Choose the personalized option.",
        prompt="Choose one option.",
        rubric=[RubricItem(id="chooses_correct", check="Agent chooses correct option.")],
        hard_assertions=HardAssertions(min_tool_calls=0, max_tool_calls=0),
        expected_answer="(c)",
        answer_format="option_letter",
    )

    result = run_one_task(
        FakeClient("I choose <final_answer>(b)</final_answer>"),
        suite,
        task,
        1,
        tmp_path / "artifacts",
        None,
        None,
        "run-1",
    )

    assert result.status == "failed"
    assert result.metrics["expected_answer_match"] is False
    assert result.metrics["predicted_answer"] == "(b)"
    assert result.hard_assertion_failures[-1].id == "expected_answer"
    assert result.hard_assertion_failures[-1].requirement == "Final answer must be (c)."
    assert result.hard_assertion_failures[-1].actual == "Predicted answer was (b)."


def test_evaluate_task_history_applies_hard_assertions_and_expected_answer(tmp_path: Path):
    from runner.runtask import evaluate_task_history

    suite = Suite(
        name="s",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "s.json",
    )
    task = TaskSpec(
        id="answer_case",
        category="single_step",
        description_for_judge="Answer C.",
        prompt="Pick C",
        rubric=[],
        hard_assertions=HardAssertions(
            min_tool_calls=0,
            max_tool_calls=2,
            response_required=True,
        ),
        expected_answer="(c)",
        answer_format="option_letter",
    )
    history = [{"type": "assistant", "content": "<final_answer>(c)</final_answer>"}]

    result = evaluate_task_history(
        suite=suite,
        task=task,
        history=history,
        attempt=1,
        artifact_dir=tmp_path / "artifacts",
        judge_cfg=None,
        judge_cache_dir=None,
        run_id="run-1",
        timed_out=False,
        metrics={},
    )

    assert result.status == "passed"
    assert result.hard_assertions is not None
    assert result.hard_assertions.expected_answer is True
    assert (tmp_path / "artifacts" / "history.json").exists()
    assert (tmp_path / "artifacts" / "trace.json").exists()


def test_evaluate_task_history_records_expected_memory_failure_details(tmp_path: Path):
    from runner.runtask import evaluate_task_history

    suite = Suite(
        name="s",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "s.json",
    )
    task = TaskSpec(
        id="memory_case",
        category="memory",
        description_for_judge="Recall a memory.",
        prompt="What do I prefer?",
        rubric=[],
        hard_assertions=HardAssertions(min_tool_calls=1, max_tool_calls=2),
        expected_recalled_memory_ids=["personamem_solo_travel"],
    )
    history = [
        {
            "type": "tool_call",
            "tool_name": "recall_memory",
            "tool_input": "{}",
        },
        {
            "type": "tool_result",
            "tool_name": "recall_memory",
            "content": json.dumps({"results": [{"id": "personamem_campfire_storytelling"}]}),
        },
        {"type": "assistant", "content": "I found a different memory."},
    ]

    result = evaluate_task_history(
        suite=suite,
        task=task,
        history=history,
        attempt=1,
        artifact_dir=tmp_path / "artifacts",
        judge_cfg=None,
        judge_cache_dir=None,
        run_id="run-1",
        timed_out=False,
        metrics={},
    )

    assert result.status == "failed"
    assert result.hard_assertion_failures[-1].id == "expected_recalled_memory"
    assert result.hard_assertion_failures[-1].requirement == "Must recall memory id(s): personamem_solo_travel."
    assert (
        result.hard_assertion_failures[-1].actual
        == "Missing: personamem_solo_travel. Recalled: personamem_campfire_storytelling."
    )


def test_run_one_task_applies_suite_prompt_prefix(tmp_path: Path):
    suite = Suite(
        name="persona",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
        prompt_prefix="You must call recall_memory before answering.",
    )
    task = TaskSpec(
        id="personamem_case",
        category="memory",
        description_for_judge="Choose the personalized option.",
        prompt="Choose one option.",
        rubric=[RubricItem(id="chooses_correct", check="Agent chooses correct option.")],
        hard_assertions=HardAssertions(min_tool_calls=0, max_tool_calls=0),
    )
    client = FakeClient("ok")

    result = run_one_task(
        client, suite, task, 1, tmp_path / "artifacts", None, None, "run-1"
    )

    assert result.status == "passed"
    assert client.messages == [
        "You must call recall_memory before answering.\n\nChoose one option."
    ]


def test_run_one_task_requests_active_skill_and_marks_trace_observation(tmp_path: Path):
    suite = Suite(
        name="phone",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
        trace_observations=[
            TraceObservationSpec(
                id="skill_read_device_operator",
                description="Loaded device-operator skill.",
                skill_name="device-operator",
            )
        ],
    )
    task = TaskSpec(
        id="open_settings",
        category="single_step",
        description_for_judge="Open Settings.",
        prompt="open settings",
        rubric=[RubricItem(id="done", check="Done.")],
        hard_assertions=HardAssertions(min_tool_calls=0, max_tool_calls=0),
    )
    client = FakeClient("ok")

    result = run_one_task(
        client,
        suite,
        task,
        1,
        tmp_path / "artifacts",
        None,
        None,
        "run-1",
        active_skills=["device-operator"],
    )

    assert client.skill_requests == [["device-operator"]]
    assert result.metrics["active_skills"] == ["device-operator"]
    obs = result.metrics["trace_observations"][0]
    assert obs["id"] == "skill_read_device_operator"
    assert obs["passed"] is True
    assert "requested active skill" in obs["reason"]


class TimeoutClient(FakeClient):
    def __init__(self):
        super().__init__()
        self.history = [
            {"type": "tool_call", "tool_name": "screenshot", "tool_input": "{}"},
            {"type": "tool_result", "content": "{}"},
        ]

    def chat(self, message, timeout_sec=None, attachments=None, skills=None):
        raise AgentTimeoutError("deadline exceeded")

    def get_history(self):
        return self.history


def test_run_one_task_preserves_history_after_timeout(tmp_path: Path):
    suite = Suite(
        name="phone",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
    )
    task = TaskSpec(
        id="slow_task",
        category="phone",
        description_for_judge="Times out after using a tool.",
        prompt="do something slow",
        rubric=[RubricItem(id="done", check="Done.")],
        hard_assertions=HardAssertions(min_tool_calls=1, max_tool_calls=4),
    )

    result = run_one_task(
        TimeoutClient(), suite, task, 1, tmp_path / "artifacts", None, None, "run-1"
    )

    assert result.status == "timeout"
    assert result.metrics["tool_calls"] == 1
    history = json.loads((tmp_path / "artifacts" / "history.json").read_text())
    assert history[0]["tool_name"] == "screenshot"


class SummaryHistoryClient(FakeClient):
    """Mimics the current agent: history omits base64 image data and the
    tool_result content is a plain text summary instead."""

    def chat(self, message, timeout_sec=None, attachments=None, skills=None):
        self.messages.append(message)
        return ChatResponse(
            response="done",
            history=[
                {"type": "tool_call", "tool_name": "screenshot", "tool_input": "{}"},
                {"type": "tool_result", "tool_name": "screenshot",
                 "content": ("screenshot returned a screenshot observation: "
                             "format=jpeg width=1080 height=2400 size=167770 bytes. "
                             "Image data omitted from text summary.")},
                {"type": "assistant", "content": "done"},
            ],
        )


def test_run_one_task_sends_live_post_screenshot_to_judge(tmp_path: Path, monkeypatch):
    suite = Suite(
        name="phone",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
    )
    task = TaskSpec(
        id="open_settings",
        category="phone",
        description_for_judge="Open the Settings app.",
        prompt="open settings",
        rubric=[RubricItem(id="settings_open", check="Settings app is open.")],
        hard_assertions=HardAssertions(min_tool_calls=0, max_tool_calls=4),
    )

    captured: dict = {}

    def fake_judge_task(*, post_screenshot, **kwargs):
        captured["post_screenshot"] = post_screenshot
        return JudgeOutput(
            verdicts=[RubricVerdict(id="settings_open", verdict="yes", reason="visible")],
            overall_notes="",
            cache_key="k",
            raw_response="{}",
        )

    monkeypatch.setattr(runtask_mod, "judge_task", fake_judge_task)
    monkeypatch.setattr(runtask_mod, "prepare_task_isolation", lambda *args, **kwargs: None)

    capture_calls = []

    def fake_take_environment_screenshot(
        environment_url,
        out_path,
        benchmark_task_id=None,
    ):
        capture_calls.append((environment_url, out_path.name, benchmark_task_id))
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_bytes(b"jpeg")
        return (1, 1)

    monkeypatch.setattr(
        runtask_mod,
        "take_environment_screenshot",
        fake_take_environment_screenshot,
    )

    result = run_one_task(
        SummaryHistoryClient(), suite, task, 1, tmp_path / "artifacts",
        JudgeConfig(), None, "run-1",
        environment_url="http://127.0.0.1:19090",
        benchmark_task_id="suite.json:open_settings",
    )

    # The judge must receive a real, existing post-screenshot file even though
    # the history carried no embedded image data.
    assert captured["post_screenshot"] is not None
    assert captured["post_screenshot"].exists()
    assert captured["post_screenshot"] == tmp_path / "artifacts" / "post.jpg"
    assert capture_calls == [
        ("http://127.0.0.1:19090", "pre.jpg", "suite.json:open_settings"),
        ("http://127.0.0.1:19090", "post.jpg", "suite.json:open_settings"),
    ]
    assert result.status == "passed"
    assert result.rubric_pass_count == 1
