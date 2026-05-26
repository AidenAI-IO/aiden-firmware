from __future__ import annotations
import dataclasses as dc
import hashlib
import json
from pathlib import Path
from typing import Any

VALID_CATEGORIES = {"diagnostic", "single_step", "multi_step", "memory"}

class SuiteValidationError(ValueError):
    pass

@dc.dataclass
class RubricItem:
    id: str
    check: str

@dc.dataclass
class HardAssertions:
    min_tool_calls: int = 0
    max_tool_calls: int = 50
    must_complete_within_sec: int = 180
    response_required: bool = True

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

@dc.dataclass
class Suite:
    name: str
    global_reset: dict[str, Any]
    tasks: list[TaskSpec]
    sha256: str
    source_path: Path

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
        hard = HardAssertions(
            min_tool_calls=int(ha.get("min_tool_calls", 0)),
            max_tool_calls=int(ha.get("max_tool_calls", 50)),
            must_complete_within_sec=int(ha.get("must_complete_within_sec", 180)),
            response_required=bool(ha.get("response_required", True)),
        )
        tasks.append(TaskSpec(
            id=tid, category=cat,
            description_for_judge=raw["description_for_judge"],
            prompt=raw["prompt"],
            rubric=rubric, hard_assertions=hard,
            setup=raw.get("setup"),
            repeats=int(raw.get("repeats", 1)),
        ))
    return Suite(
        name=data.get("name", Path(path).stem),
        global_reset=data.get("global_reset") or {},
        tasks=tasks,
        sha256=sha,
        source_path=Path(path),
    )
