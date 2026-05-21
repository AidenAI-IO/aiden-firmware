from __future__ import annotations
import dataclasses as dc
from benchmark.runner.models import HardAssertionResults, Trace
from benchmark.runner.suite import HardAssertions

@dc.dataclass
class AssertionOutcome:
    all_passed: bool
    results: HardAssertionResults

def evaluate_hard_assertions(trace: Trace, spec: HardAssertions, timed_out: bool) -> AssertionOutcome:
    results = HardAssertionResults(
        min_tool_calls=trace.total_tool_calls >= spec.min_tool_calls,
        max_tool_calls=trace.total_tool_calls <= spec.max_tool_calls,
        timeout=not timed_out,
        response_exists=bool(trace.final_response) if spec.response_required else True,
    )
    all_passed = (
        results.min_tool_calls
        and results.max_tool_calls
        and results.timeout
        and results.response_exists
    )
    return AssertionOutcome(all_passed=bool(all_passed), results=results)
