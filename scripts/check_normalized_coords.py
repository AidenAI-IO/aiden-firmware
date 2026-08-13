#!/usr/bin/env python3
"""Enforce the single normalized 0-1000 UI coordinate contract."""

from __future__ import annotations

import re
import sys
from pathlib import Path

GO_TEST_PATTERN = re.compile(
    r'"point"\s*:\s*\{[^}]*"(?:x|y)"\s*:\s*0\.\d+',
    re.DOTALL,
)
RETIRED_COORD_SPACE_PATTERN = re.compile(
    r"\b(?:coord_space|coordSpace|CoordSpace)\b"
)
RETIRED_COORD_MODE_GUIDANCE_PATTERN = re.compile(
    r"\bpixel-based pointer actions?\b|\bnormalized coordinate preference\b",
    re.IGNORECASE,
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


def check_retired_coord_space(path: Path) -> list[str]:
    text = path.read_text(encoding="utf-8")
    match = RETIRED_COORD_SPACE_PATTERN.search(text)
    if match:
        return [
            f"{path}: retired coordinate-space field is still present: {match.group(0)}"
        ]
    return []


def check_retired_coord_mode_guidance(path: Path) -> list[str]:
    text = path.read_text(encoding="utf-8")
    if RETIRED_COORD_MODE_GUIDANCE_PATTERN.search(text):
        return [f"{path}: retired coordinate mode guidance is still present"]
    return []


def main() -> int:
    repo_root = Path(__file__).resolve().parents[1]
    violations: list[str] = []

    test_root = repo_root / "src" / "agent"
    for path in sorted(test_root.rglob("*_test.go")):
        violations.extend(check_go_test_file(path))

    for root in (repo_root / "src" / "agent", repo_root / "docs"):
        for path in sorted(root.rglob("*")):
            if not path.is_file() or path.name.endswith("_test.go"):
                continue
            if path.suffix.lower() not in {".go", ".md", ".json", ".yaml", ".yml", ".toml"}:
                continue
            violations.extend(check_retired_coord_space(path))
            violations.extend(check_retired_coord_mode_guidance(path))

    benchmark_contract_paths = [
        repo_root / "benchmark" / "adbandroid" / "bridge" / "tools_api.py",
        repo_root / "benchmark" / "adbandroid" / "README.md",
        repo_root / "benchmark" / "mobilegym" / "bridge" / "tools_api.py",
        repo_root / "benchmark" / "vphone" / "bridge" / "tools_api.py",
    ]
    benchmark_contract_paths.extend(
        sorted((repo_root / "benchmark" / "suites").rglob("*.json"))
    )
    for path in benchmark_contract_paths:
        violations.extend(check_retired_coord_space(path))
        violations.extend(check_retired_coord_mode_guidance(path))

    if violations:
        print("Found normalized coordinate contract violations:", file=sys.stderr)
        for item in violations:
            print(f"  - {item}", file=sys.stderr)
        return 1

    print("normalized coordinate check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
