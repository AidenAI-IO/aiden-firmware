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
    return issues


def _section(skill_text: str, heading: str) -> str:
    pattern = re.compile(rf"(?ms)^##\s+{re.escape(heading)}\s*$.*?(?=^##\s+|\Z)")
    match = pattern.search(skill_text)
    return match.group(0) if match else ""


def _lint_failed_attempt_thresholds(skill_text: str) -> list[SkillLintIssue]:
    section = _section(skill_text, "Failed Attempt Handling")
    if not section:
        return []
    normalized = " ".join(section.lower().split())
    stops_at_two = re.search(
        r"after\s+2\s+total\s+failed\s+attempts\s+on\s+the\s+same\s+goal,?\s+stop\s+and\s+report",
        normalized,
    )
    continues_after_two = re.search(
        r"after\s+2\s+failed\s+attempts\s+on\s+the\s+same\s+goal,?\s+(choose|change)",
        normalized,
    )
    escalates_after_three = re.search(r"after\s+3\s+failed\s+attempts\s+total", normalized)
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
