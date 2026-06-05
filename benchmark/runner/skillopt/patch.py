"""Apply edits to a skill document.

Vendored from microsoft/SkillOpt (skillopt/optimizer/skill.py), simplified:
  - dropped slow_update protected region (we don't ship slow_update yet)
  - returns the same per-edit report shape so observability stays compatible
"""
from __future__ import annotations
from typing import Any

from runner.skillopt.types import Edit, Patch


def _edit_fields(edit: Edit | dict) -> tuple[str, str, str]:
    if isinstance(edit, Edit):
        return edit.op, (edit.content or "").strip(), edit.target
    return (
        edit.get("op", ""),
        (edit.get("content", "") or "").strip(),
        edit.get("target", ""),
    )


def _apply_edit_with_report(skill: str, edit: Edit | dict) -> tuple[str, dict]:
    op, content, target = _edit_fields(edit)
    report: dict[str, Any] = {
        "op": op,
        "target": target[:200],
        "content_preview": content[:200],
        "status": "unknown",
    }

    if op == "append":
        report["status"] = "applied_append"
        return skill.rstrip() + "\n\n" + content + "\n", report

    if op == "insert_after":
        if not target or target not in skill:
            report["status"] = "applied_insert_after_fallback_append"
            return skill.rstrip() + "\n\n" + content + "\n", report
        idx = skill.index(target) + len(target)
        newline = skill.find("\n", idx)
        insert_at = newline + 1 if newline != -1 else len(skill)
        report["status"] = "applied_insert_after"
        return skill[:insert_at] + "\n" + content + "\n" + skill[insert_at:], report

    if op == "replace":
        if not target:
            report["status"] = "skipped_replace_missing_target"
            return skill, report
        if target not in skill:
            report["status"] = "skipped_replace_target_not_found"
            return skill, report
        report["status"] = "applied_replace"
        return skill.replace(target, content, 1), report

    if op == "delete":
        if not target:
            report["status"] = "skipped_delete_missing_target"
            return skill, report
        if target not in skill:
            report["status"] = "skipped_delete_target_not_found"
            return skill, report
        report["status"] = "applied_delete"
        return skill.replace(target, "", 1), report

    report["status"] = "skipped_unknown_op"
    return skill, report


def apply_edit(skill: str, edit: Edit | dict) -> str:
    """Apply a single edit. Unknown ops and missing targets are silently skipped."""
    updated, _ = _apply_edit_with_report(skill, edit)
    return updated


def apply_patch_with_report(
    skill: str,
    patch: Patch | dict,
) -> tuple[str, list[dict]]:
    """Apply a patch sequentially. Returns (updated_skill, per_edit_reports)."""
    edits = patch.edits if isinstance(patch, Patch) else patch.get("edits", [])
    reports: list[dict] = []
    for idx, edit in enumerate(edits, start=1):
        try:
            skill, report = _apply_edit_with_report(skill, edit)
            report["index"] = idx
        except Exception as exc:  # pragma: no cover - defensive
            report = {
                "index": idx,
                "op": "",
                "target": "",
                "content_preview": "",
                "status": "error",
                "error": str(exc),
            }
        reports.append(report)
    return skill, reports


def apply_patch(skill: str, patch: Patch | dict) -> str:
    """Apply a patch (list of edits) and discard the report."""
    updated, _ = apply_patch_with_report(skill, patch)
    return updated
