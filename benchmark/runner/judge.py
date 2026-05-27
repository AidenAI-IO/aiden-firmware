from __future__ import annotations
import base64
import dataclasses as dc
import hashlib
import json
import os
from pathlib import Path
from typing import Any
import anthropic
import httpx

from runner.models import RubricVerdict
from runner.suite import RubricItem

JUDGE_PROMPT_VERSION = "v1"

@dc.dataclass
class JudgeConfig:
    provider: str = "openrouter"
    model: str = "anthropic/claude-sonnet-4-6"
    api_key_env: str = "OPENROUTER_API_KEY"

@dc.dataclass
class JudgeOutput:
    verdicts: list[RubricVerdict]
    overall_notes: str
    cache_key: str
    raw_response: str

JUDGE_TEMPLATE = """You are evaluating whether an agent completed a task.

TASK GOAL: {description}

Below are:
- Pre-screenshot: state before the agent acted (when applicable)
- Post-screenshot: state after the agent finished (when applicable)
- Tool trace: every action the agent took
- Agent's final reply: what the agent said it did

For each rubric item, answer ONLY "yes" or "no" with a one-sentence reason
grounded in the screenshots/trace/response. Do not be lenient. If evidence
does not clearly show the required state, answer "no".

RUBRIC:
{rubric_lines}

Respond as JSON only, no prose:
{{
  "items": [{{"id": "...", "verdict": "yes" or "no", "reason": "..."}}, ...],
  "overall_notes": "..."
}}"""

def _read_image_b64(p: Path) -> str:
    return base64.b64encode(p.read_bytes()).decode("ascii")

def _cache_key(pre: Path | None, post: Path | None, trace_json: str, rubric: list[RubricItem],
               description: str, final_response: str, model: str) -> str:
    h = hashlib.sha256()
    if pre is not None:
        h.update(pre.read_bytes())
    if post is not None:
        h.update(post.read_bytes())
    h.update(trace_json.encode("utf-8"))
    h.update(description.encode("utf-8"))
    h.update(final_response.encode("utf-8"))
    for r in rubric:
        h.update(r.id.encode()); h.update(r.check.encode())
    h.update(model.encode())
    h.update(JUDGE_PROMPT_VERSION.encode())
    return h.hexdigest()

def judge_task(
    description: str,
    rubric: list[RubricItem],
    pre_screenshot: Path | None,
    post_screenshot: Path | None,
    trace: dict[str, Any],
    final_response: str,
    cfg: JudgeConfig,
    cache_dir: Path | None = None,
) -> JudgeOutput:
    trace_json = json.dumps(trace, ensure_ascii=False, sort_keys=True)
    key = _cache_key(pre_screenshot, post_screenshot, trace_json, rubric, description,
                     final_response, cfg.model)
    if cache_dir is not None:
        cached = cache_dir / f"{key}.json"
        if cached.exists():
            data = json.loads(cached.read_text("utf-8"))
            verdicts = [RubricVerdict(**v) for v in data["verdicts"]]
            return JudgeOutput(verdicts=verdicts, overall_notes=data["overall_notes"],
                               cache_key=key, raw_response=data["raw_response"])
    rubric_lines = "\n".join(f"{i+1}. {{\"id\": \"{r.id}\", \"check\": \"{r.check}\"}}"
                              for i, r in enumerate(rubric))
    prompt = JUDGE_TEMPLATE.format(
        description=description, rubric_lines=rubric_lines,
    )
    # Build OpenAI-compatible multimodal content
    user_content: list[dict[str, Any]] = [{"type": "text", "text": prompt}]
    if pre_screenshot is not None and pre_screenshot.exists():
        user_content += [
            {"type": "text", "text": "PRE-SCREENSHOT:"},
            {"type": "image_url", "image_url": {
                "url": f"data:image/jpeg;base64,{_read_image_b64(pre_screenshot)}"}},
        ]
    if post_screenshot is not None and post_screenshot.exists():
        user_content += [
            {"type": "text", "text": "POST-SCREENSHOT:"},
            {"type": "image_url", "image_url": {
                "url": f"data:image/jpeg;base64,{_read_image_b64(post_screenshot)}"}},
        ]
    user_content += [
        {"type": "text", "text": f"TOOL TRACE:\n{trace_json}"},
        {"type": "text", "text": f"FINAL RESPONSE:\n{final_response}"},
    ]
    # Use OpenRouter's OpenAI-compatible chat completions endpoint
    api_key = os.environ[cfg.api_key_env]
    with httpx.Client(timeout=120) as http_client:
        r = http_client.post(
            "https://openrouter.ai/api/v1/chat/completions",
            headers={
                "Authorization": f"Bearer {api_key}",
                "Content-Type": "application/json",
            },
            json={
                "model": cfg.model,
                "messages": [{"role": "user", "content": user_content}],
                "max_tokens": 1024,
            },
        )
        r.raise_for_status()
        body = r.json()
    raw = body["choices"][0]["message"]["content"]
    parsed = _parse_judge_json(raw)
    verdicts = [RubricVerdict(id=v["id"], verdict=v["verdict"], reason=v["reason"])
                for v in parsed["items"]]
    out = JudgeOutput(verdicts=verdicts, overall_notes=parsed.get("overall_notes", ""),
                      cache_key=key, raw_response=raw)
    if cache_dir is not None:
        cache_dir.mkdir(parents=True, exist_ok=True)
        (cache_dir / f"{key}.json").write_text(json.dumps({
            "verdicts": [dc.asdict(v) for v in verdicts],
            "overall_notes": out.overall_notes,
            "raw_response": raw,
        }), encoding="utf-8")
    return out

def _parse_judge_json(raw: str) -> dict[str, Any]:
    s = raw.strip()
    start = s.find("{")
    end = s.rfind("}")
    if start == -1 or end == -1:
        raise ValueError(f"judge response has no JSON object: {raw[:200]}")
    return json.loads(s[start:end+1])
