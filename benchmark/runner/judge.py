from __future__ import annotations
import base64
import dataclasses as dc
import hashlib
import json
import os
from pathlib import Path
from typing import Any
import anthropic

from benchmark.runner.models import RubricVerdict
from benchmark.runner.suite import RubricItem

JUDGE_PROMPT_VERSION = "v1"

@dc.dataclass
class JudgeConfig:
    provider: str = "anthropic"
    model: str = "claude-sonnet-4-6"
    api_key_env: str = "ANTHROPIC_API_KEY"

@dc.dataclass
class JudgeOutput:
    verdicts: list[RubricVerdict]
    overall_notes: str
    cache_key: str
    raw_response: str

JUDGE_TEMPLATE = """You are evaluating whether a phone-control agent completed a task.

TASK GOAL: {description}

The agent had access to a phone via screenshot+HID tools. Below are:
- Pre-screenshot: phone state before the agent acted
- Post-screenshot: phone state after the agent finished (last step screenshot)
- Tool trace: every action the agent took
- Agent's final reply: what the agent said it did

For each rubric item, answer ONLY "yes" or "no" with a one-sentence reason
grounded in the screenshots/trace. Do not be lenient. If the post-screenshot
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

def _cache_key(pre: Path, post: Path, trace_json: str, rubric: list[RubricItem],
               description: str, model: str) -> str:
    h = hashlib.sha256()
    h.update(pre.read_bytes())
    h.update(post.read_bytes())
    h.update(trace_json.encode("utf-8"))
    h.update(description.encode("utf-8"))
    for r in rubric:
        h.update(r.id.encode()); h.update(r.check.encode())
    h.update(model.encode())
    h.update(JUDGE_PROMPT_VERSION.encode())
    return h.hexdigest()

def judge_task(
    description: str,
    rubric: list[RubricItem],
    pre_screenshot: Path,
    post_screenshot: Path,
    trace: dict[str, Any],
    final_response: str,
    cfg: JudgeConfig,
    cache_dir: Path | None = None,
) -> JudgeOutput:
    trace_json = json.dumps(trace, ensure_ascii=False, sort_keys=True)
    key = _cache_key(pre_screenshot, post_screenshot, trace_json, rubric, description, cfg.model)
    if cache_dir is not None:
        cached = cache_dir / f"{key}.json"
        if cached.exists():
            data = json.loads(cached.read_text("utf-8"))
            verdicts = [RubricVerdict(**v) for v in data["verdicts"]]
            return JudgeOutput(verdicts=verdicts, overall_notes=data["overall_notes"],
                               cache_key=key, raw_response=data["raw_response"])
    client = anthropic.Anthropic(api_key=os.environ[cfg.api_key_env])
    rubric_lines = "\n".join(f"{i+1}. {{\"id\": \"{r.id}\", \"check\": \"{r.check}\"}}"
                              for i, r in enumerate(rubric))
    prompt = JUDGE_TEMPLATE.format(
        description=description, rubric_lines=rubric_lines,
    )
    user_content: list[dict[str, Any]] = [
        {"type": "text", "text": prompt},
        {"type": "text", "text": "PRE-SCREENSHOT:"},
        {"type": "image", "source": {"type": "base64", "media_type": "image/jpeg",
                                       "data": _read_image_b64(pre_screenshot)}},
        {"type": "text", "text": "POST-SCREENSHOT:"},
        {"type": "image", "source": {"type": "base64", "media_type": "image/jpeg",
                                       "data": _read_image_b64(post_screenshot)}},
        {"type": "text", "text": f"TOOL TRACE:\n{trace_json}"},
        {"type": "text", "text": f"FINAL RESPONSE:\n{final_response}"},
    ]
    msg = client.messages.create(
        model=cfg.model, max_tokens=1024,
        messages=[{"role": "user", "content": user_content}],
    )
    raw = "".join(block.text for block in msg.content if block.type == "text")
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
