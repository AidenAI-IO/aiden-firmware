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

bad_go_test="$tmpdir/bad_coords_test.go"
cat >"$bad_go_test" <<'EOF'
package agent

func badPointFixture() string {
	return `{"point":{"x":0.5,"y":320}}`
}
EOF

python3 - "$repo_root/scripts" "$bad_go_test" <<'PY'
import sys
from pathlib import Path

sys.path.insert(0, sys.argv[1])
from check_normalized_coords import check_go_test_file

bad_go_test = Path(sys.argv[2])
violations = check_go_test_file(bad_go_test)
if not violations:
    raise SystemExit("expected fixture Go test to fail coordinate check")
print("fixture rejected as expected")
PY

echo "check normalized coords script tests passed"
