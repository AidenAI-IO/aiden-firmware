from __future__ import annotations
import dataclasses as dc
import hashlib
import json
import re
from pathlib import Path
from typing import Any

from runner.platform import (
    TargetPlatform,
    normalize_target_platform,
    resolve_mock_platform,
)

VALID_CATEGORIES = {"diagnostic", "single_step", "multi_step", "memory", "perception", "device_operation"}

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
    skill_name: str = ""
    tool_name: str = ""
    input_contains: dict[str, Any] = dc.field(default_factory=dict)

@dc.dataclass
class RequiredToolCallSpec:
    tool: str
    input_contains: dict[str, Any] = dc.field(default_factory=dict)

@dc.dataclass
class HardAssertions:
    min_tool_calls: int = 0
    max_tool_calls: int = 50
    must_complete_within_sec: int = 180
    response_required: bool = True
    required_tools: list[str] = dc.field(default_factory=list)
    forbidden_tools: list[str] = dc.field(default_factory=list)
    prohibited_actions: list[str] = dc.field(default_factory=list)
    required_tool_calls: list[RequiredToolCallSpec] = dc.field(default_factory=list)

@dc.dataclass
class MockToolResponseSpec:
    input_contains: dict[str, Any] = dc.field(default_factory=dict)
    screen_contains: str = ""
    output: Any = dc.field(default_factory=dict)
    is_error: bool = False
    error: str = ""
    screen_text: str = ""

@dc.dataclass
class MockEnvironmentSpec:
    platform: TargetPlatform
    phone_bridge: dict[str, Any]
    tools: dict[str, list[MockToolResponseSpec]]
    screen: str | None = None
    screen_text: str = ""
    default_tool_response: MockToolResponseSpec | None = None

@dc.dataclass
class TaskSpec:
    id: str
    category: str
    description_for_judge: str
    prompt: str
    rubric: list[RubricItem]
    hard_assertions: HardAssertions
    setup: dict[str, Any] | None = None
    mock_environment: MockEnvironmentSpec | None = None
    repeats: int = 1
    input_screenshot: str | None = None
    expected_answer: str | None = None
    answer_format: str | None = None
    platforms: list[str] = dc.field(default_factory=list)
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
    mock_environment: MockEnvironmentSpec | None = None


def effective_mock_environment(
    suite: Suite,
    task: TaskSpec,
) -> MockEnvironmentSpec | None:
    return task.mock_environment or suite.mock_environment


def resolve_mock_task_platform(
    suite: Suite,
    task: TaskSpec,
    *,
    constraint: str | TargetPlatform | None = None,
) -> TargetPlatform:
    spec = effective_mock_environment(suite, task)
    if spec is None:
        raise ValueError(
            f"mock environment task {task.id!r} has no mock environment"
        )
    return resolve_mock_platform(spec.platform, constraint=constraint)

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
        rubric: list[RubricItem] = []
        for r in rubric_raw:
            if not isinstance(r, dict):
                raise SuiteValidationError(f"task {tid}: rubric items must be objects")
            check = r.get("check", r.get("description"))
            if not r.get("id") or not check:
                raise SuiteValidationError(f"task {tid}: rubric items require id and check")
            rubric.append(RubricItem(id=r["id"], check=check))
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
        prohibited_actions = _string_list_assertion(ha.get("prohibited_actions", []), tid, "prohibited_actions")
        required_tool_calls = _required_tool_call_assertions(
            ha.get("required_tool_calls", []), tid
        )
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
            prohibited_actions=prohibited_actions,
            required_tool_calls=required_tool_calls,
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
        platforms = _platform_list(raw.get("platforms", []), tid)
        task_mock_environment = _parse_mock_environment(
            raw.get("mock_environment"),
            path=f"task {tid}.mock_environment",
        )
        tasks.append(TaskSpec(
            id=tid, category=cat,
            description_for_judge=raw["description_for_judge"],
            prompt=raw["prompt"],
            rubric=rubric, hard_assertions=hard,
            setup=raw.get("setup"),
            mock_environment=task_mock_environment,
            repeats=repeats,
            input_screenshot=raw.get("input_screenshot"),
            expected_answer=expected_answer,
            answer_format=answer_format,
            platforms=platforms,
            expected_recalled_memory_ids=expected_recalled_memory_ids,
        ))
    prompt_prefix = data.get("prompt_prefix", "")
    if not isinstance(prompt_prefix, str):
        raise SuiteValidationError("suite prompt_prefix must be a string")

    trace_observations: list[TraceObservationSpec] = []
    seen_obs_ids: set[str] = set()
    for raw_obs in data.get("trace_observations") or []:
        if not isinstance(raw_obs, dict):
            raise SuiteValidationError("trace_observations entries must be objects")
        obs_id = raw_obs.get("id")
        description = raw_obs.get("description")
        skill_name = str(raw_obs.get("skill_name") or "").strip()
        tool_name = str(raw_obs.get("tool_name") or "").strip()
        input_contains = raw_obs.get("input_contains") or {}
        if not isinstance(input_contains, dict):
            raise SuiteValidationError("trace_observations input_contains must be an object")
        if not obs_id or not description or not (skill_name or tool_name):
            raise SuiteValidationError(
                "trace_observations entries require id, description, and skill_name or tool_name"
            )
        if skill_name and tool_name:
            raise SuiteValidationError(
                f"trace_observations {obs_id!r}: specify only one of skill_name or tool_name"
            )
        if obs_id in seen_obs_ids:
            raise SuiteValidationError(f"duplicate trace_observations id: {obs_id!r}")
        seen_obs_ids.add(obs_id)
        trace_observations.append(
            TraceObservationSpec(
                id=obs_id,
                description=description,
                skill_name=skill_name,
                tool_name=tool_name,
                input_contains=input_contains,
            )
        )

    mock_environment = _parse_mock_environment(data.get("mock_environment"))
    if mock_environment is None and any(task.mock_environment is not None for task in tasks):
        missing_task_ids = [task.id for task in tasks if task.mock_environment is None]
        if missing_task_ids:
            raise SuiteValidationError(
                "a suite using task-level mock_environment must define it for every task "
                "or provide a suite-level mock_environment default; missing: "
                + ", ".join(missing_task_ids)
            )

    return Suite(
        name=data.get("name", Path(path).stem),
        global_reset=data.get("global_reset") or {},
        tasks=tasks,
        sha256=sha,
        source_path=Path(path),
        prompt_prefix=prompt_prefix,
        trace_observations=trace_observations,
        mock_environment=mock_environment,
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

def _platform_list(raw: Any, task_id: str) -> list[str]:
    if raw is None:
        return []
    if not isinstance(raw, list):
        raise SuiteValidationError(
            f"task {task_id}: platforms must be a list of ios, android, mac, windows, or linux"
        )
    out: list[str] = []
    for item in raw:
        try:
            platform = normalize_target_platform(item).value
        except ValueError as exc:
            raise SuiteValidationError(
                f"task {task_id}: invalid platform {item!r}"
            ) from exc

        if platform not in out:
            out.append(platform)
    return out


def _required_tool_call_assertions(raw: Any, task_id: str) -> list[RequiredToolCallSpec]:
    if raw is None:
        return []
    if not isinstance(raw, list):
        raise SuiteValidationError(
            f"task {task_id}: hard_assertions.required_tool_calls must be an array"
        )
    out: list[RequiredToolCallSpec] = []
    for index, item in enumerate(raw):
        if not isinstance(item, dict):
            raise SuiteValidationError(
                f"task {task_id}: required_tool_calls[{index}] must be an object"
            )
        tool = str(item.get("tool") or "").strip()
        input_contains = item.get("input_contains") or {}
        if not tool:
            raise SuiteValidationError(
                f"task {task_id}: required_tool_calls[{index}] requires tool"
            )
        if not isinstance(input_contains, dict):
            raise SuiteValidationError(
                f"task {task_id}: required_tool_calls[{index}].input_contains must be an object"
            )
        out.append(RequiredToolCallSpec(tool=tool, input_contains=input_contains))
    return out


def _parse_mock_environment(
    raw: Any,
    *,
    path: str = "mock_environment",
) -> MockEnvironmentSpec | None:
    if raw is None:
        return None
    if not isinstance(raw, dict):
        raise SuiteValidationError(f"{path} must be an object")
    phone_bridge = raw.get("phone_bridge") or {}
    if not isinstance(phone_bridge, dict):
        raise SuiteValidationError(f"{path}.phone_bridge must be an object")
    top_level_platform = raw.get("platform")
    legacy_platform = phone_bridge.get("platform")
    if top_level_platform is None and legacy_platform is None:
        raise SuiteValidationError(
            f"{path} must declare a target platform (ios, android, mac, windows, or linux)"
        )
    try:
        platform = normalize_target_platform(
            top_level_platform if top_level_platform is not None else legacy_platform,
            field=f"{path}.platform",
        )
    except ValueError as exc:
        raise SuiteValidationError(
            f"{path}.platform must be ios, android, mac, windows, or linux"
        ) from exc
    if top_level_platform is not None and legacy_platform is not None:
        try:
            normalized_legacy_platform = normalize_target_platform(
                legacy_platform,
                field=f"{path}.phone_bridge.platform",
            )
        except ValueError as exc:
            raise SuiteValidationError(
                f"{path}.phone_bridge.platform must be ios, android, mac, windows, or linux"
            ) from exc
        if normalized_legacy_platform is not platform:
            raise SuiteValidationError(
                f"{path}.phone_bridge.platform conflicts with {path}.platform"
            )
    phone_bridge = dict(phone_bridge)
    phone_bridge.pop("platform", None)
    app_state = str(phone_bridge.get("app_state") or "").strip().lower()
    if app_state and app_state not in {"active", "background", "inactive"}:
        raise SuiteValidationError(
            f"{path}.phone_bridge.app_state must be active, background, or inactive"
        )
    for field in (
        "connected",
        "return_entry_available",
        "pip_bridge_enabled",
        "fgs_bridge_enabled",
    ):
        if field in phone_bridge and not isinstance(phone_bridge[field], bool):
            raise SuiteValidationError(
                f"{path}.phone_bridge.{field} must be boolean"
            )

    tools_raw = raw.get("tools") or {}
    if not isinstance(tools_raw, dict):
        raise SuiteValidationError(f"{path}.tools must be an object")
    tools: dict[str, list[MockToolResponseSpec]] = {}
    for raw_name, raw_responses in tools_raw.items():
        name = str(raw_name or "").strip()
        if not name:
            raise SuiteValidationError(f"{path}.tools contains an empty tool name")
        items = raw_responses if isinstance(raw_responses, list) else [raw_responses]
        if not items:
            raise SuiteValidationError(
                f"{path}.tools.{name} must contain at least one response"
            )
        tools[name] = [
            _parse_mock_tool_response(item, f"{path}.tools.{name}")
            for item in items
        ]

    default_raw = raw.get("default_tool_response")
    default_response = None
    if default_raw is not None:
        default_response = _parse_mock_tool_response(
            default_raw, f"{path}.default_tool_response"
        )
    screen = raw.get("screen")
    if screen is not None and (not isinstance(screen, str) or not screen.strip()):
        raise SuiteValidationError(f"{path}.screen must be a non-empty string")
    screen_text = raw.get("screen_text", "")
    if not isinstance(screen_text, str):
        raise SuiteValidationError(f"{path}.screen_text must be a string")
    return MockEnvironmentSpec(
        platform=platform,
        phone_bridge=phone_bridge,
        tools=tools,
        screen=screen.strip() if isinstance(screen, str) else None,
        screen_text=screen_text,
        default_tool_response=default_response,
    )


def _parse_mock_tool_response(raw: Any, path: str) -> MockToolResponseSpec:
    if not isinstance(raw, dict):
        raise SuiteValidationError(f"{path} response must be an object")
    input_contains = raw.get("input_contains") or {}
    if not isinstance(input_contains, dict):
        raise SuiteValidationError(f"{path}.input_contains must be an object")
    screen_contains = raw.get("screen_contains", "")
    if not isinstance(screen_contains, str):
        raise SuiteValidationError(f"{path}.screen_contains must be a string")
    is_error = raw.get("is_error", False)
    if not isinstance(is_error, bool):
        raise SuiteValidationError(f"{path}.is_error must be boolean")
    error = raw.get("error", "")
    if not isinstance(error, str):
        raise SuiteValidationError(f"{path}.error must be a string")
    screen_text = raw.get("screen_text", "")
    if not isinstance(screen_text, str):
        raise SuiteValidationError(f"{path}.screen_text must be a string")
    return MockToolResponseSpec(
        input_contains=input_contains,
        screen_contains=screen_contains,
        output=raw.get("output", {}),
        is_error=is_error,
        error=error,
        screen_text=screen_text,
    )
