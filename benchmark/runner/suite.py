from __future__ import annotations
import dataclasses as dc
import hashlib
import json
import re
from pathlib import Path
from typing import Any

VALID_CATEGORIES = {"diagnostic", "single_step", "multi_step", "memory", "perception"}

class SuiteValidationError(ValueError):
    pass

@dc.dataclass
class RubricItem:
    id: str
    check: str

@dc.dataclass
class TraceObservationSpec:
    id: str
    description: str
    skill_name: str

@dc.dataclass
class HardAssertions:
    min_tool_calls: int = 0
    max_tool_calls: int = 50
    must_complete_within_sec: int = 180
    response_required: bool = True
    required_tools: list[str] = dc.field(default_factory=list)
    forbidden_tools: list[str] = dc.field(default_factory=list)

@dc.dataclass
class TaskSpec:
    id: str
    category: str
    description_for_judge: str
    prompt: str
    rubric: list[RubricItem]
    hard_assertions: HardAssertions
    setup: dict[str, Any] | None = None
    repeats: int = 1
    input_screenshot: str | None = None
    expected_answer: str | None = None
    answer_format: str | None = None
    expected_recalled_memory_ids: list[str] = dc.field(default_factory=list)

@dc.dataclass
class Suite:
    name: str
    global_reset: dict[str, Any]
    tasks: list[TaskSpec]
    sha256: str
    source_path: Path
    prompt_prefix: str = ""
    trace_observations: list[TraceObservationSpec] = dc.field(default_factory=list)

def load_suite(path: Path) -> Suite:
    raw_bytes = Path(path).read_bytes()
    sha = hashlib.sha256(raw_bytes).hexdigest()
    try:
        data = json.loads(raw_bytes.decode("utf-8"))
    except json.JSONDecodeError as e:
        raise SuiteValidationError(f"invalid JSON: {e}") from e
    if not isinstance(data.get("tasks"), list):
        raise SuiteValidationError("suite must contain a 'tasks' array")
    seen = set()
    tasks: list[TaskSpec] = []
    for raw in data["tasks"]:
        tid = raw.get("id")
        if not tid or tid in seen:
            raise SuiteValidationError(f"missing or duplicate task id: {tid!r}")
        seen.add(tid)
        cat = raw.get("category")
        if cat not in VALID_CATEGORIES:
            raise SuiteValidationError(f"task {tid}: invalid category {cat!r}")
        rubric_raw = raw.get("rubric") or []
        if not rubric_raw:
            raise SuiteValidationError(f"task {tid}: empty rubric")
        rubric = [RubricItem(id=r["id"], check=r["check"]) for r in rubric_raw]
        ha = raw.get("hard_assertions") or {}
        # Validate hard_assertions types
        rr = ha.get("response_required", True)
        if isinstance(rr, str):
            rr = rr.lower() == "true"
        elif not isinstance(rr, bool):
            raise SuiteValidationError(f"task {tid}: response_required must be bool")
        try:
            min_tc = int(ha.get("min_tool_calls", 0))
            max_tc = int(ha.get("max_tool_calls", 50))
            timeout_sec = int(ha.get("must_complete_within_sec", 180))
        except (ValueError, TypeError) as e:
            raise SuiteValidationError(f"task {tid}: invalid hard_assertions numeric value: {e}") from e
        if min_tc < 0 or max_tc < 0 or timeout_sec <= 0:
            raise SuiteValidationError(f"task {tid}: hard_assertions values must be non-negative")
        required_tools = _string_list_assertion(ha.get("required_tools", []), tid, "required_tools")
        forbidden_tools = _string_list_assertion(ha.get("forbidden_tools", []), tid, "forbidden_tools")
        overlap = sorted(set(required_tools) & set(forbidden_tools))
        if overlap:
            raise SuiteValidationError(
                f"task {tid}: hard_assertions has overlapping required/forbidden tools: {overlap}"
            )
        hard = HardAssertions(
            min_tool_calls=min_tc,
            max_tool_calls=max_tc,
            must_complete_within_sec=timeout_sec,
            response_required=rr,
            required_tools=required_tools,
            forbidden_tools=forbidden_tools,
        )
        # Validate and bound repeats
        try:
            repeats = int(raw.get("repeats", 1))
        except (ValueError, TypeError) as e:
            raise SuiteValidationError(f"task {tid}: invalid repeats: {e}") from e
        if repeats < 1:
            repeats = 1
        elif repeats > 100:
            raise SuiteValidationError(f"task {tid}: repeats {repeats} exceeds max 100")
        expected_answer = raw.get("expected_answer")
        answer_format = raw.get("answer_format")
        if expected_answer is None and answer_format is not None:
            raise SuiteValidationError(f"task {tid}: answer_format requires expected_answer")
        if expected_answer is not None:
            if not isinstance(expected_answer, str):
                raise SuiteValidationError(f"task {tid}: expected_answer must be string")
            answer_format = answer_format or "option_letter"
            if answer_format == "option_letter":
                matches = re.findall(r"\(([a-dA-D])\)|\b([a-dA-D])\b", expected_answer)
                letters = [a or b for a, b in matches]
                if len(letters) != 1:
                    raise SuiteValidationError(
                        f"task {tid}: expected_answer must contain exactly one option A-D, got {expected_answer!r}"
                    )
        if answer_format is not None and answer_format != "option_letter":
            raise SuiteValidationError(f"task {tid}: unsupported answer_format {answer_format!r}")
        expected_recalled_memory_ids = raw.get("expected_recalled_memory_ids", [])
        if not isinstance(expected_recalled_memory_ids, list) or not all(
            isinstance(item, str) and item.strip() for item in expected_recalled_memory_ids
        ):
            raise SuiteValidationError(f"task {tid}: expected_recalled_memory_ids must be a list of non-empty strings")
        tasks.append(TaskSpec(
            id=tid, category=cat,
            description_for_judge=raw["description_for_judge"],
            prompt=raw["prompt"],
            rubric=rubric, hard_assertions=hard,
            setup=raw.get("setup"),
            repeats=repeats,
            input_screenshot=raw.get("input_screenshot"),
            expected_answer=expected_answer,
            answer_format=answer_format,
            expected_recalled_memory_ids=expected_recalled_memory_ids,
        ))
    prompt_prefix = data.get("prompt_prefix", "")
    if not isinstance(prompt_prefix, str):
        raise SuiteValidationError("suite prompt_prefix must be a string")

    trace_observations: list[TraceObservationSpec] = []
    for raw_obs in data.get("trace_observations") or []:
        if not isinstance(raw_obs, dict):
            raise SuiteValidationError("trace_observations entries must be objects")
        obs_id = raw_obs.get("id")
        description = raw_obs.get("description")
        skill_name = raw_obs.get("skill_name")
        if not obs_id or not description or not skill_name:
            raise SuiteValidationError(
                "trace_observations entries require id, description, and skill_name"
            )
        trace_observations.append(
            TraceObservationSpec(id=obs_id, description=description, skill_name=skill_name)
        )

    return Suite(
        name=data.get("name", Path(path).stem),
        global_reset=data.get("global_reset") or {},
        tasks=tasks,
        sha256=sha,
        source_path=Path(path),
        prompt_prefix=prompt_prefix,
        trace_observations=trace_observations,
    )


def _string_list_assertion(raw: Any, task_id: str, field: str) -> list[str]:
    if raw is None:
        return []
    if not isinstance(raw, list) or not all(isinstance(item, str) and item.strip() for item in raw):
        raise SuiteValidationError(f"task {task_id}: hard_assertions.{field} must be a list of non-empty strings")
    seen = set()
    out = []
    for item in raw:
        name = item.strip()
        if name in seen:
            continue
        seen.add(name)
        out.append(name)
    return out
