from skillopt.skill_lint import lint_skill_text


def test_lint_detects_conflicting_failed_attempt_thresholds():
    skill = """
## Failed Attempt Handling

After a failed attempt:

1. Observe with `screenshot`.
2. Compare expected vs observed result.
3. Never repeat the exact same failed action more than once. After 2 total failed attempts on the same goal, stop and report the blocker to the user immediately instead of continuing to attempt untested actions that waste turns.
4. Change one variable at a time: target location, gesture type, coordinate space, navigation path, or input method.
5. After 2 failed attempts on the same goal, choose a different strategy.
6. After 3 failed attempts total, summarize what was tried and ask the user or switch to diagnosis.
"""

    issues = lint_skill_text(skill)

    assert [issue.code for issue in issues] == ["conflicting_failed_attempt_thresholds"]


def test_lint_accepts_consistent_failed_attempt_escalation():
    skill = """
## Failed Attempt Handling

After a failed attempt:

1. Observe with `screenshot`.
2. Compare expected vs observed result.
3. Do not repeat the exact same failed action more than once.
4. After 2 failed attempts on the same goal, change strategy instead of retrying the same path.
5. After 3 failed attempts total on the same goal, stop and report the blocker or ask the user for help.
"""

    assert lint_skill_text(skill) == []


def test_lint_detects_conflicting_failed_attempt_thresholds_with_worded_numbers():
    skill = """
## Failed Attempt Handling

1. After two failed attempts on the same goal, stop and report the blocker.
2. After two failed attempts on the same goal, change strategy.
3. After three total failed attempts on the same goal, ask the user for help.
"""

    issues = lint_skill_text(skill)

    assert [issue.code for issue in issues] == ["conflicting_failed_attempt_thresholds"]
