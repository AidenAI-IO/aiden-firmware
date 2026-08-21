import json
from pathlib import Path

from PIL import Image

from runner.agent_client import AgentTimeoutError, ChatResponse
from runner.judge import JudgeConfig, JudgeOutput
from runner.models import RubricVerdict
from runner.runtask import run_one_task
from runner.suite import (
    HardAssertions,
    MockEnvironmentSpec,
    RubricItem,
    Suite,
    TaskSpec,
    TraceObservationSpec,
)
import runner.runtask as runtask_mod


class FakeClient:
    def __init__(self, response="ok"):
        self.response = response
        self.messages = []
        self.attachments = []
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
        self.attachments.append(attachments)
        self.skill_requests.append(list(skills or []))
        return ChatResponse(
            response=self.response,
            history=[{"type": "assistant", "content": self.response}],
        )


def test_run_one_task_includes_static_screenshot_dimensions(tmp_path: Path):
    screenshot_dir = tmp_path / "screenshots"
    screenshot_dir.mkdir()
    screenshot_path = screenshot_dir / "screen.jpg"
    Image.new("RGB", (37, 91), "white").save(screenshot_path, format="JPEG")

    suite = Suite(
        name="perception",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
    )
    task = TaskSpec(
        id="tap_target",
        category="perception",
        description_for_judge="Tap the target.",
        prompt="tap the target",
        rubric=[],
        hard_assertions=HardAssertions(min_tool_calls=0, max_tool_calls=0),
        input_screenshot="screenshots/screen.jpg",
    )
    client = FakeClient("done")

    result = run_one_task(
        client, suite, task, 1, tmp_path / "artifacts", None, None, "run-1"
    )

    assert result.status == "passed"
    assert client.attachments[0] is not None
    attachment = client.attachments[0][0]
    assert attachment["width"] == 37
    assert attachment["height"] == 91


def test_run_one_task_uses_mock_screenshot_without_static_attachment(
    tmp_path: Path,
    monkeypatch,
):
    screenshot_dir = tmp_path / "screenshots"
    screenshot_dir.mkdir()
    fixture_path = screenshot_dir / "fixture.jpg"
    Image.new("RGB", (37, 91), "white").save(fixture_path, format="JPEG")
    suite = Suite(
        name="perception",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
    )
    task = TaskSpec(
        id="tap_target",
        category="perception",
        description_for_judge="Tap the target.",
        prompt="tap the target",
        rubric=[],
        hard_assertions=HardAssertions(min_tool_calls=0, max_tool_calls=0),
        input_screenshot="screenshots/fixture.jpg",
        mock_environment=MockEnvironmentSpec(
            platform="ios",
            phone_bridge={},
            tools={},
            screen="screenshots/fixture.jpg",
            single_frame=True,
        ),
    )
    capture_calls = []

    def capture_fixture(environment_url, out_path, benchmark_task_id=None):
        capture_calls.append((environment_url, benchmark_task_id))
        out_path.write_bytes(fixture_path.read_bytes())
        return 37, 91

    monkeypatch.setattr(runtask_mod, "take_environment_screenshot", capture_fixture)
    client = FakeClient("done")
    artifact_dir = tmp_path / "artifacts"

    result = run_one_task(
        client,
        suite,
        task,
        1,
        artifact_dir,
        None,
        None,
        "run-1",
        environment_url="http://mock-environment.test",
    )

    assert result.status == "passed"
    assert client.attachments == [None]
    assert capture_calls == [("http://mock-environment.test", None)]
    with Image.open(artifact_dir / "pre.jpg") as image:
        assert image.size == (37, 91)
    assert not (artifact_dir / "post.jpg").exists()


def test_run_one_task_falls_back_to_fixture_for_mock_pre_artifact(
    tmp_path: Path,
    monkeypatch,
):
    screenshot_dir = tmp_path / "screenshots"
    screenshot_dir.mkdir()
    fixture_path = screenshot_dir / "fixture.jpg"
    Image.new("RGB", (37, 91), "white").save(fixture_path, format="JPEG")
    suite = Suite(
        name="perception",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
    )
    task = TaskSpec(
        id="tap_target",
        category="perception",
        description_for_judge="Tap the target.",
        prompt="tap the target",
        rubric=[],
        hard_assertions=HardAssertions(min_tool_calls=0, max_tool_calls=0),
        input_screenshot="screenshots/fixture.jpg",
        mock_environment=MockEnvironmentSpec(
            platform="ios",
            phone_bridge={},
            tools={},
            screen="screenshots/fixture.jpg",
            single_frame=True,
        ),
    )

    def fail_capture(*args, **kwargs):
        raise RuntimeError("provider unavailable")

    monkeypatch.setattr(runtask_mod, "take_environment_screenshot", fail_capture)
    client = FakeClient("done")
    artifact_dir = tmp_path / "artifacts"

    result = run_one_task(
        client,
        suite,
        task,
        1,
        artifact_dir,
        None,
        None,
        "run-1",
        environment_url="http://mock-environment.test",
    )

    assert result.status == "passed"
    assert client.attachments == [None]
    assert result.metrics["pre_screenshot_error"] == "provider unavailable"
    assert (artifact_dir / "pre.jpg").read_bytes() == fixture_path.read_bytes()
    assert not (artifact_dir / "post.jpg").exists()


def test_run_one_task_keeps_dynamic_mock_screenshot_attachment_and_post_artifact(
    tmp_path: Path,
    monkeypatch,
):
    screenshot_dir = tmp_path / "screenshots"
    screenshot_dir.mkdir()
    fixture_path = screenshot_dir / "fixture.jpg"
    Image.new("RGB", (37, 91), "white").save(fixture_path, format="JPEG")
    suite = Suite(
        name="dynamic-mock",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
    )
    task = TaskSpec(
        id="tap_target",
        category="perception",
        description_for_judge="Tap the target.",
        prompt="tap the target",
        rubric=[],
        hard_assertions=HardAssertions(min_tool_calls=0, max_tool_calls=0),
        input_screenshot="screenshots/fixture.jpg",
        mock_environment=MockEnvironmentSpec(
            platform="ios",
            phone_bridge={},
            tools={},
            screen="screenshots/fixture.jpg",
        ),
    )
    capture_calls = []

    def capture_post(environment_url, out_path, benchmark_task_id=None):
        capture_calls.append((environment_url, out_path.name, benchmark_task_id))
        Image.new("RGB", (37, 91), "black").save(out_path, format="JPEG")
        return 37, 91

    monkeypatch.setattr(runtask_mod, "take_environment_screenshot", capture_post)
    client = FakeClient("done")
    artifact_dir = tmp_path / "artifacts"

    result = run_one_task(
        client,
        suite,
        task,
        1,
        artifact_dir,
        None,
        None,
        "run-1",
        environment_url="http://mock-environment.test",
    )

    assert result.status == "passed"
    attachment = client.attachments[0][0]
    assert attachment["width"] == 37
    assert attachment["height"] == 91
    assert capture_calls == [
        ("http://mock-environment.test", "post.jpg", None),
    ]
    assert (artifact_dir / "pre.jpg").read_bytes() == fixture_path.read_bytes()
    assert (artifact_dir / "post.jpg").exists()


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


def test_run_one_task_writes_consolidation_artifact_from_setup(
    tmp_path: Path, monkeypatch
):
    suite = Suite(
        name="reflection",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / "suite.json",
    )
    task = TaskSpec(
        id="reflection_case",
        category="memory",
        description_for_judge="Reflection contract.",
        prompt="confirm",
        rubric=[RubricItem(id="ok", check="responds")],
        hard_assertions=HardAssertions(min_tool_calls=0, max_tool_calls=0),
        setup={"type": "seed_episode"},
    )

    monkeypatch.setattr(
        runtask_mod,
        "prepare_task_isolation",
        lambda *args, **kwargs: {
            "type": "seed_episode",
            "episode_id": "ep-1",
            "consolidated": True,
            "consolidation": {
                "episode_id": "ep-1",
                "status": "done",
                "assessment": {"goal_result": "unknown"},
                "memory_ids": [],
            },
        },
    )

    artifact_dir = tmp_path / "artifacts"
    result = run_one_task(
        FakeClient("done"), suite, task, 1, artifact_dir, None, None, "run-1"
    )

    assert result.status == "passed"
    assert json.loads((artifact_dir / "consolidation.json").read_text())["assessment"]["goal_result"] == "unknown"
    assert not (artifact_dir / "setup.json").exists()


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


def _memory_suite_and_task(tmp_path: Path):
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
        description_for_judge="Recall the expected memory.",
        prompt="What do I prefer?",
        rubric=[RubricItem(id="uses_memory", check="Uses the memory.")],
        hard_assertions=HardAssertions(min_tool_calls=1, max_tool_calls=3),
        expected_recalled_memory_ids=["personamem_music_expression"],
    )
    return suite, task


def _compressed_recall_history(episode_id: str = "ep-1"):
    return [
        {
            "type": "tool_call",
            "tool_name": "recall_memory",
            "tool_input": "{}",
            "episode_id": episode_id,
        },
        {
            "type": "tool_result",
            "tool_name": "recall_memory",
            "content": "[Large tool result omitted from public history (8406 chars)]",
            "episode_id": episode_id,
        },
        {
            "type": "assistant",
            "content": "I used the stored preference.",
            "episode_id": episode_id,
        },
    ]


def _compressed_recall_and_device_history(episode_id: str = "ep-1"):
    recall_history = _compressed_recall_history(episode_id)
    return recall_history[:-1] + [
        {
            "type": "tool_call",
            "tool_name": "recall_device_memory",
            "tool_input": "{}",
            "episode_id": episode_id,
        },
        {
            "type": "tool_result",
            "tool_name": "recall_device_memory",
            "content": '{"results":[]}',
            "episode_id": episode_id,
        },
        recall_history[-1],
    ]


def test_compressed_recall_with_episode_continues_to_judge_and_persists_evidence(
    tmp_path: Path,
    monkeypatch,
):
    from runner.runtask import evaluate_task_history

    suite, task = _memory_suite_and_task(tmp_path)
    judge_called = []

    def fake_judge_task(**kwargs):
        judge_called.append(kwargs)
        return JudgeOutput(
            verdicts=[RubricVerdict(id="uses_memory", verdict="yes", reason="used")],
            overall_notes="",
            cache_key="k",
            raw_response="{}",
        )

    monkeypatch.setattr(runtask_mod, "judge_task", fake_judge_task)

    result = evaluate_task_history(
        suite=suite,
        task=task,
        history=_compressed_recall_history(),
        episode={
            "id": "ep-1",
            "retrieved_memory_refs": ["personamem_music_expression"],
        },
        attempt=1,
        artifact_dir=tmp_path / "artifacts",
        judge_cfg=JudgeConfig(),
        judge_cache_dir=None,
        run_id="run-1",
        timed_out=False,
    )

    assert result.status == "passed"
    assert len(judge_called) == 1
    assert result.metrics["recalled_memory_ids"] == ["personamem_music_expression"]
    assert result.metrics["memory_recall_evidence_source"] == "episode"
    assert result.metrics["expected_recalled_memory_match"] is True
    trace = json.loads((tmp_path / "artifacts" / "trace.json").read_text())
    assert trace["recalled_memory_ids"] == ["personamem_music_expression"]
    assert trace["memory_recall_evidence_source"] == "episode"
    assert trace["expected_recalled_memory_match"] is True


def test_episode_missing_expected_memory_is_real_failure(tmp_path: Path):
    from runner.runtask import evaluate_task_history

    suite, task = _memory_suite_and_task(tmp_path)
    result = evaluate_task_history(
        suite=suite,
        task=task,
        history=_compressed_recall_history(),
        episode={"id": "ep-1", "retrieved_memory_refs": ["other-memory"]},
        attempt=1,
        artifact_dir=tmp_path / "artifacts",
        judge_cfg=None,
        judge_cache_dir=None,
        run_id="run-1",
        timed_out=False,
    )

    assert result.status == "failed"
    assert result.metrics["memory_recall_evidence_source"] == "episode"
    assert result.metrics["expected_recalled_memory_match"] is False


def test_episode_authoritative_empty_memory_refs_is_real_failure(tmp_path: Path):
    from runner.runtask import evaluate_task_history

    suite, task = _memory_suite_and_task(tmp_path)
    result = evaluate_task_history(
        suite=suite,
        task=task,
        history=_compressed_recall_history(),
        episode={"id": "ep-1"},
        attempt=1,
        artifact_dir=tmp_path / "artifacts",
        judge_cfg=None,
        judge_cache_dir=None,
        run_id="run-1",
        timed_out=False,
    )

    assert result.status == "failed"
    assert result.metrics["recalled_memory_ids"] == []
    assert result.metrics["memory_recall_evidence_source"] == "episode"
    assert result.metrics["expected_recalled_memory_match"] is False


def test_ambiguous_episode_empty_memory_refs_is_real_failure(tmp_path: Path):
    from runner.runtask import evaluate_task_history

    suite, task = _memory_suite_and_task(tmp_path)
    result = evaluate_task_history(
        suite=suite,
        task=task,
        history=_compressed_recall_and_device_history(),
        episode={"id": "ep-1", "retrieved_memory_refs": []},
        attempt=1,
        artifact_dir=tmp_path / "artifacts",
        judge_cfg=None,
        judge_cache_dir=None,
        run_id="run-1",
        timed_out=False,
    )

    assert result.status == "failed"
    assert result.metrics["recalled_memory_ids"] == []
    assert result.metrics["memory_recall_evidence_source"] == "episode"
    assert result.metrics["expected_recalled_memory_match"] is False


def test_episode_missing_one_expected_memory_reports_attributable_recall(tmp_path: Path):
    from runner.runtask import evaluate_task_history

    suite, task = _memory_suite_and_task(tmp_path)
    task.expected_recalled_memory_ids = ["memory-a", "memory-b"]
    result = evaluate_task_history(
        suite=suite,
        task=task,
        history=_compressed_recall_and_device_history(),
        episode={"id": "ep-1", "retrieved_memory_refs": ["memory-a"]},
        attempt=1,
        artifact_dir=tmp_path / "artifacts",
        judge_cfg=None,
        judge_cache_dir=None,
        run_id="run-1",
        timed_out=False,
    )

    assert result.status == "failed"
    assert result.metrics["recalled_memory_ids"] == ["memory-a"]
    assert result.metrics["memory_recall_evidence_source"] == "episode"
    assert result.metrics["expected_recalled_memory_match"] is False


def test_compressed_recall_without_episode_is_judge_error(tmp_path: Path):
    from runner.runtask import evaluate_task_history

    suite, task = _memory_suite_and_task(tmp_path)
    result = evaluate_task_history(
        suite=suite,
        task=task,
        history=_compressed_recall_history(),
        episode=None,
        attempt=1,
        artifact_dir=tmp_path / "artifacts",
        judge_cfg=None,
        judge_cache_dir=None,
        run_id="run-1",
        timed_out=False,
    )

    assert result.status == "judge_error"
    assert result.hard_assertions.expected_recalled_memory is None
    assert result.metrics["memory_recall_evidence_source"] == "unavailable"
    assert result.metrics["expected_recalled_memory_match"] is None
    trace = json.loads((tmp_path / "artifacts" / "trace.json").read_text())
    assert trace["recalled_memory_ids"] == []
    assert trace["memory_recall_evidence_source"] == "unavailable"
    assert trace["expected_recalled_memory_match"] is None


def test_missing_recall_call_fails_even_when_episode_contains_expected_id(tmp_path: Path):
    from runner.runtask import evaluate_task_history

    suite, task = _memory_suite_and_task(tmp_path)
    task.hard_assertions.min_tool_calls = 0
    history = [
        {
            "type": "assistant",
            "content": "I recalled personamem_music_expression.",
            "episode_id": "ep-1",
        }
    ]
    result = evaluate_task_history(
        suite=suite,
        task=task,
        history=history,
        episode={"retrieved_memory_refs": ["personamem_music_expression"]},
        attempt=1,
        artifact_dir=tmp_path / "artifacts",
        judge_cfg=None,
        judge_cache_dir=None,
        run_id="run-1",
        timed_out=False,
    )

    assert result.status == "failed"
    assert result.metrics["recalled_memory_ids"] == []
    assert result.metrics["expected_recalled_memory_match"] is False
    assert "No recall_memory call" in result.hard_assertion_failures[-1].actual


class EpisodeClient(FakeClient):
    def __init__(self, *, inline_content, episode=None, episode_error=None):
        super().__init__("done")
        self.inline_content = inline_content
        self.episode = episode
        self.episode_error = episode_error
        self.episode_requests = []

    def chat(self, message, timeout_sec=None, attachments=None, skills=None):
        self.messages.append(message)
        return ChatResponse(
            response="done",
            history=[
                {
                    "type": "tool_call",
                    "tool_name": "recall_memory",
                    "tool_input": "{}",
                    "episode_id": "ep/one",
                },
                {
                    "type": "tool_result",
                    "tool_name": "recall_memory",
                    "content": self.inline_content,
                    "episode_id": "ep/one",
                },
                {"type": "assistant", "content": "done", "episode_id": "ep/one"},
            ],
        )

    def get_episode(self, episode_id):
        self.episode_requests.append(episode_id)
        if self.episode_error is not None:
            raise self.episode_error
        return self.episode


def test_run_one_task_fetches_unique_episode_and_saves_it(tmp_path: Path):
    suite, task = _memory_suite_and_task(tmp_path)
    episode = {
        "id": "ep/one",
        "retrieved_memory_refs": ["personamem_music_expression"],
    }
    client = EpisodeClient(
        inline_content="[Large tool result omitted from public history (8406 chars)]",
        episode=episode,
    )

    result = run_one_task(
        client,
        suite,
        task,
        1,
        tmp_path / "artifacts",
        None,
        None,
        "run-1",
    )

    assert result.status == "passed"
    assert client.episode_requests == ["ep/one"]
    assert json.loads((tmp_path / "artifacts" / "episode.json").read_text()) == episode
    assert result.metrics["memory_recall_evidence_source"] == "episode"


def test_run_one_task_complete_inline_result_does_not_fetch_episode(tmp_path: Path):
    suite, task = _memory_suite_and_task(tmp_path)
    client = EpisodeClient(
        inline_content=json.dumps(
            {"results": [{"id": "personamem_music_expression"}]}
        ),
        episode_error=RuntimeError("episode unavailable"),
    )

    result = run_one_task(
        client,
        suite,
        task,
        1,
        tmp_path / "artifacts",
        None,
        None,
        "run-1",
    )

    assert result.status == "passed"
    assert result.metrics["memory_recall_evidence_source"] == "inline"
    assert client.episode_requests == []
    assert "episode_error" not in result.metrics
    assert not (tmp_path / "artifacts" / "episode.json").exists()


def test_run_one_task_without_memory_id_assertion_does_not_fetch_episode(
    tmp_path: Path,
):
    suite, task = _memory_suite_and_task(tmp_path)
    task.expected_recalled_memory_ids = []
    client = EpisodeClient(
        inline_content="[Large tool result omitted from public history (8406 chars)]",
        episode={"id": "ep/one", "retrieved_memory_refs": []},
    )

    result = run_one_task(
        client,
        suite,
        task,
        1,
        tmp_path / "artifacts",
        None,
        None,
        "run-1",
    )

    assert result.status == "passed"
    assert client.episode_requests == []
    assert not (tmp_path / "artifacts" / "episode.json").exists()


def test_run_one_task_episode_fetch_failure_with_compressed_result_is_judge_error(
    tmp_path: Path,
):
    suite, task = _memory_suite_and_task(tmp_path)
    client = EpisodeClient(
        inline_content="[Large tool result omitted from public history (8406 chars)]",
        episode_error=RuntimeError("episode unavailable"),
    )

    result = run_one_task(
        client,
        suite,
        task,
        1,
        tmp_path / "artifacts",
        None,
        None,
        "run-1",
    )

    assert result.status == "judge_error"
    assert result.metrics["memory_recall_evidence_source"] == "unavailable"
    assert result.metrics["expected_recalled_memory_match"] is None
    assert "episode unavailable" in result.metrics["episode_error"]


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
