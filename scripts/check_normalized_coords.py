#!/usr/bin/env python3
"""Reject 0-1 style normalized UI coordinates; canonical range is 0-1000."""

from __future__ import annotations

import re
import sys
from pathlib import Path

GO_TEST_PATTERN = re.compile(
    r'"point"\s*:\s*\{[^}]*"(?:x|y)"\s*:\s*0\.\d+',
    re.DOTALL,
)


def is_zero_to_one_coord(value: object) -> bool:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return False
    number = float(value)
    return 0 < abs(number) < 1


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
