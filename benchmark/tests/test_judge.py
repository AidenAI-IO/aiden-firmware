import json
from pathlib import Path

from runner.judge import JUDGE_PROMPT_VERSION, JUDGE_TEMPLATE, JudgeConfig, judge_task
from runner.suite import RubricItem


def test_judge_uses_configured_api_key_env(monkeypatch):
    seen = {}

    class FakeResponse:
        status = 200

        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return json.dumps({
                "choices": [{"message": {"content": json.dumps({
                    "items": [{"id": "ok", "verdict": "yes", "reason": "passed"}],
                    "overall_notes": "ok",
                })}}]
            }).encode("utf-8")

    def fake_urlopen(req, timeout):
        seen["authorization"] = req.headers.get("Authorization")
        seen["url"] = req.full_url
        seen["timeout"] = timeout
        return FakeResponse()

    monkeypatch.setenv("JUDGE_API_KEY", "sk-judge")
    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    result = judge_task(
        description="check",
        rubric=[RubricItem(id="ok", check="pass")],
        pre_screenshot=None,
        post_screenshot=None,
        trace={"tool_calls": []},
        final_response="done",
        cfg=JudgeConfig(api_key_env="JUDGE_API_KEY", base_url="https://judge.example.com/v1/"),
    )

    assert result.verdicts[0].verdict == "yes"
    assert seen == {
        "authorization": "Bearer sk-judge",
        "url": "https://judge.example.com/v1/chat/completions",
        "timeout": 120,
    }


def test_judge_requires_identity_consistency_for_chosen_targets():
    assert "target identity consistent" in JUDGE_TEMPLATE
    assert "Do not substitute" in JUDGE_TEMPLATE


def test_judge_uses_visual_outcome_instead_of_gesture_wording():
    assert "judge scrolling by the visible before/after content" in JUDGE_TEMPLATE


def test_judge_does_not_import_task_goal_requirements_into_rubric():
    assert "Even if TASK GOAL mentions a page, path, or starting state" in JUDGE_TEMPLATE


def test_judge_prompt_version_tracks_evidence_contract():
    assert JUDGE_PROMPT_VERSION == "v3-evidence-consistency"
    assert "status-bar icon" in JUDGE_TEMPLATE


def test_judge_sends_only_pre_and_post_screenshots(monkeypatch, tmp_path: Path):
    seen = {}
    pre = tmp_path / "pre.jpg"
    post = tmp_path / "post.jpg"
    pre.write_bytes(b"pre-image")
    post.write_bytes(b"post-image")

    class FakeResponse:
        status = 200

        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return json.dumps({
                "choices": [{"message": {"content": json.dumps({
                    "items": [{"id": "ok", "verdict": "yes", "reason": "passed"}],
                    "overall_notes": "ok",
                })}}]
            }).encode("utf-8")

    def fake_urlopen(req, timeout):
        seen["payload"] = json.loads(req.data.decode("utf-8"))
        return FakeResponse()

    monkeypatch.setenv("JUDGE_API_KEY", "sk-judge")
    monkeypatch.setattr("urllib.request.urlopen", fake_urlopen)

    result = judge_task(
        description="check",
        rubric=[RubricItem(id="ok", check="pass")],
        pre_screenshot=pre,
        post_screenshot=post,
        trace={"tool_calls": []},
        final_response="done",
        cfg=JudgeConfig(api_key_env="JUDGE_API_KEY"),
    )

    content = seen["payload"]["messages"][0]["content"]
    labels = [part["text"].rstrip(":") for part in content if part["type"] == "text" and part["text"].endswith(":")]
    images = [part for part in content if part["type"] == "image_url"]
    assert labels == ["PRE-SCREENSHOT", "POST-SCREENSHOT"]
    assert len(images) == 2
    assert result.image_count == 2
    assert result.image_labels == ["PRE-SCREENSHOT", "POST-SCREENSHOT"]


def test_judge_does_not_fallback_to_agent_toml_api_key(monkeypatch, tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text('[model]\napi_key = "sk-agent"\n', encoding="utf-8")

    monkeypatch.setenv("AIDEN_AGENT_CONFIG", str(config))
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)

    try:
        judge_task(
            description="check",
            rubric=[RubricItem(id="ok", check="pass")],
            pre_screenshot=None,
            post_screenshot=None,
            trace={"tool_calls": []},
            final_response="done",
            cfg=JudgeConfig(),
        )
    except RuntimeError as e:
        assert str(e) == "missing env var OPENROUTER_API_KEY"
    else:
        raise AssertionError("expected missing OPENROUTER_API_KEY error")
