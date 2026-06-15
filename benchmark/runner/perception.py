from __future__ import annotations
import dataclasses as dc
import re
from typing import Any

from runner.models import RubricVerdict, ToolCall, Trace
from runner.suite import RubricItem, Suite, TaskSpec


CLICK_TOOLS = {"mouse_click", "touch_gesture"}

FALLBACK_MOUSE_CLICK_DESCRIPTION = (
    'Move mouse to a position and click. Input JSON: {"x": 500, "y": 300, '
    '"button": "left", "coord_space": "normalized"}. Normalized coordinates '
    "use 0-1000 range where (0,0) is top-left, (1000,1000) is bottom-right, "
    "and (500,500) is center."
)

_COORD_RANGE_RE = re.compile(
    r"\bx\b[^\[]*\[\s*(?P<x_min>-?\d+(?:\.\d+)?)\s*,\s*"
    r"(?P<x_max>-?\d+(?:\.\d+)?)\s*\].*?"
    r"\by\b[^\[]*\[\s*(?P<y_min>-?\d+(?:\.\d+)?)\s*,\s*"
    r"(?P<y_max>-?\d+(?:\.\d+)?)\s*\]",
    re.IGNORECASE | re.DOTALL,
)


@dc.dataclass
class CoordinateRange:
    x_min: float
    x_max: float
    y_min: float
    y_max: float


@dc.dataclass
class FirstClickEvaluation:
    verdicts: list[RubricVerdict]
    first_click: dict[str, Any] | None
    expected: dict[str, float] | None
    passed: bool


def is_perception_first_click_task(suite: Suite, task: TaskSpec) -> bool:
    return (
        suite.name == "perception_v1"
        and task.category == "perception"
        and bool(task.input_screenshot)
    )


def build_perception_prompt(prompt: str, mouse_click_description: str) -> str:
    description = mouse_click_description.strip() or FALLBACK_MOUSE_CLICK_DESCRIPTION
    return (
        f"{prompt.rstrip()}\n\n"
        "Available click tool description. Follow this exact coordinate convention "
        "when selecting the first click target:\n"
        f"mouse_click: {description}\n\n"
        'For this static perception benchmark, prefer mouse_click with '
        'coord_space:"normalized" and x/y in the 0-1000 range. Do not use 0-1 '
        "unit coordinates."
    )


def evaluate_first_click_rubric(
    trace: Trace, rubric: list[RubricItem]
) -> FirstClickEvaluation | None:
    if not _rubric_is_locally_supported(rubric):
        return None

    first = first_click_tool_call(trace)
    coords = first_click_coordinates(first) if first is not None else None
    first_summary = _first_click_summary(first, coords)
    verdicts: list[RubricVerdict] = []
    expected: CoordinateRange | None = None

    for item in rubric:
        check = item.check
        lower = f"{item.id} {check}".lower()
        coord_range = parse_coordinate_range(check)
        if coord_range is not None:
            expected = coord_range
            passed = _coordinates_in_range(first, coords, coord_range)
            verdicts.append(
                RubricVerdict(
                    id=item.id,
                    verdict="yes" if passed else "no",
                    reason=_coordinate_reason(first, coords, coord_range, passed),
                )
            )
        elif "called_click_tool" in lower or (
            "touch_gesture" in lower and "mouse_click" in lower and "at least one" in lower
        ):
            verdicts.append(
                RubricVerdict(
                    id=item.id,
                    verdict="yes" if first is not None else "no",
                    reason=(
                        f"First click-like tool call is {first.tool} at step {first.step}."
                        if first is not None
                        else "No mouse_click or touch_gesture call was found."
                    ),
                )
            )
        elif "0-1" in lower or "0..1" in lower:
            passed = coords is not None and not _looks_like_unit_coordinates(coords)
            verdicts.append(
                RubricVerdict(
                    id=item.id,
                    verdict="yes" if passed else "no",
                    reason=(
                        f"First click coordinates are x={coords[0]:g}, y={coords[1]:g}."
                        if coords is not None
                        else "No first click coordinates were available."
                    ),
                )
            )

    return FirstClickEvaluation(
        verdicts=verdicts,
        first_click=first_summary,
        expected=_range_summary(expected),
        passed=bool(verdicts) and all(v.verdict == "yes" for v in verdicts),
    )


def parse_coordinate_range(text: str) -> CoordinateRange | None:
    match = _COORD_RANGE_RE.search(text or "")
    if not match:
        return None
    return CoordinateRange(
        x_min=float(match.group("x_min")),
        x_max=float(match.group("x_max")),
        y_min=float(match.group("y_min")),
        y_max=float(match.group("y_max")),
    )


def first_click_tool_call(trace: Trace) -> ToolCall | None:
    for call in trace.tool_calls:
        if call.tool in CLICK_TOOLS:
            return call
    return None


def first_click_coordinates(call: ToolCall | None) -> tuple[float, float] | None:
    if call is None:
        return None
    payload = call.input if isinstance(call.input, dict) else {}
    if call.tool == "mouse_click":
        return _xy_from_mapping(payload)
    if call.tool == "touch_gesture":
        for key in ("point", "start"):
            point = payload.get(key)
            if isinstance(point, dict):
                coords = _xy_from_mapping(point)
                if coords is not None:
                    return coords
        return _xy_from_mapping(payload)
    return None


def _rubric_is_locally_supported(rubric: list[RubricItem]) -> bool:
    if not rubric:
        return False
    for item in rubric:
        lower = f"{item.id} {item.check}".lower()
        if parse_coordinate_range(item.check) is not None:
            continue
        if "called_click_tool" in lower:
            continue
        if "touch_gesture" in lower and "mouse_click" in lower and "at least one" in lower:
            continue
        if "0-1" in lower or "0..1" in lower:
            continue
        return False
    return True


def _xy_from_mapping(payload: dict[str, Any]) -> tuple[float, float] | None:
    x = _as_float(payload.get("x"))
    y = _as_float(payload.get("y"))
    if x is None or y is None:
        return None
    return x, y


def _as_float(value: Any) -> float | None:
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def _coordinate_space_allows_normalized(call: ToolCall | None) -> bool:
    if call is None or not isinstance(call.input, dict):
        return False
    coord_space = str(call.input.get("coord_space") or "auto").strip().lower()
    return coord_space in {"", "auto", "normalized"}


def _coordinates_in_range(
    call: ToolCall | None, coords: tuple[float, float] | None, expected: CoordinateRange
) -> bool:
    if coords is None or not _coordinate_space_allows_normalized(call):
        return False
    x, y = coords
    return expected.x_min <= x <= expected.x_max and expected.y_min <= y <= expected.y_max


def _looks_like_unit_coordinates(coords: tuple[float, float]) -> bool:
    x, y = coords
    return 0 <= x <= 1 and 0 <= y <= 1


def _coordinate_reason(
    call: ToolCall | None,
    coords: tuple[float, float] | None,
    expected: CoordinateRange,
    passed: bool,
) -> str:
    expected_text = (
        f"x in [{expected.x_min:g}, {expected.x_max:g}], "
        f"y in [{expected.y_min:g}, {expected.y_max:g}]"
    )
    if call is None:
        return f"No first click-like tool call was found; expected {expected_text}."
    if coords is None:
        return f"First click-like tool call has no usable x/y coordinates; expected {expected_text}."
    coord_space = str(call.input.get("coord_space") or "auto") if isinstance(call.input, dict) else "auto"
    x, y = coords
    if not _coordinate_space_allows_normalized(call):
        return (
            f"First {call.tool} uses coord_space={coord_space!r}, not normalized/auto; "
            f"raw coordinates are x={x:g}, y={y:g}; expected {expected_text}."
        )
    status = "within" if passed else "outside"
    return (
        f"First {call.tool} raw normalized coordinates are x={x:g}, y={y:g}, "
        f"{status} expected {expected_text}."
    )


def _first_click_summary(
    call: ToolCall | None, coords: tuple[float, float] | None
) -> dict[str, Any] | None:
    if call is None:
        return None
    summary: dict[str, Any] = {
        "step": call.step,
        "tool": call.tool,
        "input": call.input,
    }
    if coords is not None:
        summary["x"] = coords[0]
        summary["y"] = coords[1]
    if isinstance(call.input, dict):
        summary["coord_space"] = call.input.get("coord_space") or "auto"
    return summary


def _range_summary(expected: CoordinateRange | None) -> dict[str, float] | None:
    if expected is None:
        return None
    return {
        "x_min": expected.x_min,
        "x_max": expected.x_max,
        "y_min": expected.y_min,
        "y_max": expected.y_max,
    }
