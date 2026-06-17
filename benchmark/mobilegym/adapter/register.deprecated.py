"""DEPRECATED: Agent registration is no longer needed.

MobileGym is now used as a pure simulator, not a test framework.
We don't register agents with MobileGym's agent registry.

See benchmark/mobilegym/README.md for the new approach.
"""
from __future__ import annotations

import sys

print("⚠️  DEPRECATED: Agent registration is no longer needed", file=sys.stderr)
print("MobileGym is now used as a pure simulator.", file=sys.stderr)
print("See benchmark/mobilegym/README.md for details.", file=sys.stderr)
sys.exit(2)
