#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
checker="$repo_root/scripts/check_normalized_coords.py"

if ! python3 "$checker"; then
    echo "expected current benchmark suites and agent tests to pass coordinate check" >&2
    exit 1
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

bad_suite="$tmpdir/bad_suite.json"
cat >"$bad_suite" <<'EOF'
{
  "name": "bad_coords_fixture",
  "tasks": [
    {
      "id": "bad",
      "setup": {
        "tool_sequence": [
          {
            "tool": "touch_gesture",
            "args": { "type": "tap", "point": { "x": 0.5, "y": 320 } }
          }
        ]
      }
    }
  ]
}
EOF

python3 - "$repo_root/scripts" "$bad_suite" <<'PY'
import sys
from pathlib import Path

sys.path.insert(0, sys.argv[1])
from check_normalized_coords import check_benchmark_suite

bad_suite = Path(sys.argv[2])
violations = check_benchmark_suite(bad_suite)
if not violations:
    raise SystemExit("expected fixture suite to fail coordinate check")
print("fixture rejected as expected")
PY

echo "check normalized coords script tests passed"
