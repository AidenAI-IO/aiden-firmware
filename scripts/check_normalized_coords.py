#!/usr/bin/env python3
"""Reject 0-1 style normalized UI coordinates; canonical range is 0-1000."""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

UI_TOOLS = frozenset({"touch_gesture", "mouse_click", "mouse_move"})
COORD_KEYS = frozenset({"x", "y", "anchor", "distance"})
POINT_KEYS = frozenset({"point", "start", "end"})

GO_TEST_PATTERN = re.compile(
    r'"point"\s*:\s*\{[^}]*"(?:x|y)"\s*:\s*0\.\d+',
    re.DOTALL,
)


def is_zero_to_one_coord(value: object) -> bool:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return False
    number = float(value)
    return 0 < abs(number) < 1


def check_coord_value(path: str, value: object, violations: list[str]) -> None:
    if is_zero_to_one_coord(value):
        violations.append(f"{path}: {value!r} looks like a 0-1 normalized coordinate; use 0-1000 instead")


def walk_point_object(prefix: str, obj: object, violations: list[str]) -> None:
    if not isinstance(obj, dict):
        return
    for key in ("x", "y"):
        if key in obj:
            check_coord_value(f"{prefix}.{key}", obj[key], violations)


def walk_tool_args(prefix: str, args: object, violations: list[str]) -> None:
    if not isinstance(args, dict):
        return

    for key in COORD_KEYS:
        if key in args:
            check_coord_value(f"{prefix}.{key}", args[key], violations)

    for key in POINT_KEYS:
        if key in args:
            walk_point_object(f"{prefix}.{key}", args[key], violations)


def walk_tool_sequence(prefix: str, sequence: object, violations: list[str]) -> None:
    if not isinstance(sequence, list):
        return
    for index, step in enumerate(sequence):
        if not isinstance(step, dict):
            continue
        tool = step.get("tool")
        step_prefix = f"{prefix}[{index}]"
        if tool in UI_TOOLS:
            walk_tool_args(f"{step_prefix}.args", step.get("args"), violations)


def walk_json(prefix: str, obj: object, violations: list[str]) -> None:
    if isinstance(obj, dict):
        if "tool_sequence" in obj:
            walk_tool_sequence(f"{prefix}.tool_sequence", obj["tool_sequence"], violations)
        for key, value in obj.items():
            walk_json(f"{prefix}.{key}" if prefix else key, value, violations)
    elif isinstance(obj, list):
        for index, item in enumerate(obj):
            walk_json(f"{prefix}[{index}]", item, violations)


def check_benchmark_suite(path: Path) -> list[str]:
    violations: list[str] = []
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        return [f"{path}: invalid JSON: {exc}"]

    walk_json(str(path), data, violations)
    return violations


def check_go_test_file(path: Path) -> list[str]:
    text = path.read_text(encoding="utf-8")
    if "touch_gesture" not in text and '"point"' not in text:
        return []

    violations: list[str] = []
    for match in GO_TEST_PATTERN.finditer(text):
        snippet = match.group(0).replace("\n", " ")
        violations.append(
            f"{path}: embedded touch point uses 0-1 coordinates: {snippet[:120]}"
        )
    return violations


def main() -> int:
    repo_root = Path(__file__).resolve().parents[1]
    violations: list[str] = []

    suite_root = repo_root / "benchmark" / "suites"
    for path in sorted(suite_root.rglob("*.json")):
        violations.extend(check_benchmark_suite(path))

    test_root = repo_root / "src" / "agent"
    for path in sorted(test_root.rglob("*_test.go")):
        violations.extend(check_go_test_file(path))

    if violations:
        print("Found 0-1 style normalized UI coordinates:", file=sys.stderr)
        for item in violations:
            print(f"  - {item}", file=sys.stderr)
        return 1

    print("normalized coordinate check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
