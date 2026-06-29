from __future__ import annotations

import dataclasses as dc
import re


@dc.dataclass(frozen=True)
class SkillLintIssue:
    code: str
    message: str
    section: str = ""
    severity: str = "error"

    def to_dict(self) -> dict[str, str]:
        return dc.asdict(self)


def lint_skill_text(skill_text: str) -> list[SkillLintIssue]:
    issues: list[SkillLintIssue] = []
    issues.extend(_lint_failed_attempt_thresholds(skill_text))
    issues.extend(_lint_benchmark_specific_language(skill_text))
    issues.extend(_lint_device_operator_scope(skill_text))
    issues.extend(_lint_overbroad_plan_mode_bans(skill_text))
    issues.extend(_lint_overbroad_screenshot_bans(skill_text))
    issues.extend(_lint_overbroad_locked_device_blockers(skill_text))
    return issues


def _section(skill_text: str, heading: str) -> str:
    pattern = re.compile(rf"(?ms)^##\s+{re.escape(heading)}\s*$.*?(?=^##\s+|\Z)")
    match = pattern.search(skill_text)
    return match.group(0) if match else ""


def _frontmatter_name(skill_text: str) -> str:
    match = re.match(r"(?ms)\A---\s*(.*?)\s*---", skill_text.strip())
    if not match:
        return ""
    for line in match.group(1).splitlines():
        key, sep, value = line.partition(":")
        if sep and key.strip() == "name":
            return value.strip().strip('"\'')
    return ""


def _lint_failed_attempt_thresholds(skill_text: str) -> list[SkillLintIssue]:
    section = _section(skill_text, "Failed Attempt Handling")
    if not section:
        return []
    normalized = " ".join(section.lower().split())
    two = r"(?:2|two)"
    three = r"(?:3|three)"
    total = r"(?:\s+total)?"
    stops_at_two = re.search(
        rf"after\s+{two}{total}\s+failed\s+attempts{total}\s+on\s+the\s+same\s+goal,?\s+stop\s+and\s+report",
        normalized,
    )
    continues_after_two = re.search(
        rf"after\s+{two}{total}\s+failed\s+attempts{total}\s+on\s+the\s+same\s+goal,?\s+(choose|change)",
        normalized,
    )
    escalates_after_three = re.search(rf"after\s+{three}{total}\s+failed\s+attempts{total}", normalized)
    if stops_at_two and (continues_after_two or escalates_after_three):
        return [
            SkillLintIssue(
                code="conflicting_failed_attempt_thresholds",
                section="Failed Attempt Handling",
                message="Failed-attempt policy both reports a blocker after 2 attempts and continues to later strategy/escalation steps.",
            )
        ]
    return []


def _lint_benchmark_specific_language(skill_text: str) -> list[SkillLintIssue]:
    if "task timeouts" not in skill_text.lower():
        return []
    return [
        SkillLintIssue(
            code="benchmark_specific_timeout_language",
            message="Base skill should avoid benchmark-specific timeout wording; keep performance guidance environment-neutral.",
        )
    ]


def _lint_device_operator_scope(skill_text: str) -> list[SkillLintIssue]:
    if _frontmatter_name(skill_text) != "device-operator":
        return []
    normalized = " ".join(skill_text.lower().split())
    if "plan mode" in normalized:
        return [
            SkillLintIssue(
                code="device_operator_plan_mode_scope_leak",
                message="device-operator should describe visible device operation only; plan-mode and routing policy belongs in the agent loop/system prompt.",
            )
        ]
    return []


def _lint_overbroad_plan_mode_bans(skill_text: str) -> list[SkillLintIssue]:
    normalized = " ".join(skill_text.lower().split())
    patterns = (
        r"never\s+use\s+plan\s+mode",
        r"plan\s+mode\s+is\s+prohibited",
        r"all\s+cross-app\s+data\s+transfer\s+tasks.*plan\s+mode",
    )
    if any(re.search(pattern, normalized) for pattern in patterns):
        return [
            SkillLintIssue(
                code="overbroad_plan_mode_ban",
                message="Plan-mode guidance should be preference-based; do not categorically ban plan mode for broad task classes.",
            )
        ]
    return []


def _lint_overbroad_screenshot_bans(skill_text: str) -> list[SkillLintIssue]:
    normalized = " ".join(skill_text.lower().split())
    if re.search(r"(never|may\s+never|do\s+not)\s+call\s+`?screenshot`?\s+consecutively", normalized):
        return [
            SkillLintIssue(
                code="overbroad_screenshot_ban",
                message="Screenshot guidance must allow confirmation/recovery observations; do not ban consecutive screenshots categorically.",
            )
        ]
    return []


def _lint_overbroad_locked_device_blockers(skill_text: str) -> list[SkillLintIssue]:
    normalized = " ".join(_section(skill_text, "Failed Attempt Handling").lower().split())
    if "locked device" not in normalized:
        return []
    if "immediately report" in normalized and "do not make any additional unlock attempts" in normalized:
        return [
            SkillLintIssue(
                code="overbroad_locked_device_blocker",
                section="Failed Attempt Handling",
                message="Locked-device guidance should switch strategy or diagnose before reporting; avoid immediate hard-stop wording.",
            )
        ]
    return []
