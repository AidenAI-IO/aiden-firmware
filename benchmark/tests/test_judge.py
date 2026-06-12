import json
from pathlib import Path

from runner.judge import JudgeConfig, judge_task
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
        cfg=JudgeConfig(api_key_env="JUDGE_API_KEY"),
    )

    assert result.verdicts[0].verdict == "yes"
    assert seen == {"authorization": "Bearer sk-judge", "timeout": 120}


def test_judge_does_not_fallback_to_agent_toml_api_key(monkeypatch, tmp_path: Path):
    config = tmp_path / "agent.toml"
    config.write_text('[model]\napi_key = "sk-agent"\n', encoding="utf-8")

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
