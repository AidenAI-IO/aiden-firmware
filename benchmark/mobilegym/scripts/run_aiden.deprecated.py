#!/usr/bin/env python3
"""DEPRECATED: This script uses MobileGym's test framework.

For the new simulator-only approach, use:
  - scripts/start_simulator.py (start MobileGym simulator)
  - benchmark/runner/main.py (run tests)

See README.md for migration guide.
"""
from __future__ import annotations

import sys

print("=" * 70, file=sys.stderr)
print("⚠️  DEPRECATED: This script uses MobileGym's test framework.", file=sys.stderr)
print("=" * 70, file=sys.stderr)
print("", file=sys.stderr)
print("MobileGym is now used as a pure simulator, not a test framework.", file=sys.stderr)
print("", file=sys.stderr)
print("New approach:", file=sys.stderr)
print("  1. Start simulator:", file=sys.stderr)
print("     python benchmark/mobilegym/scripts/start_simulator.py", file=sys.stderr)
print("", file=sys.stderr)
print("  2. Run benchmark:", file=sys.stderr)
print("     python -m benchmark.runner run \\", file=sys.stderr)
print("       --suite benchmark/suites/mobilegym_basic.json \\", file=sys.stderr)
print("       --agent-url http://localhost:8080", file=sys.stderr)
print("", file=sys.stderr)
print("See benchmark/mobilegym/README.md for details.", file=sys.stderr)
print("=" * 70, file=sys.stderr)
print("", file=sys.stderr)

sys.exit(2)
