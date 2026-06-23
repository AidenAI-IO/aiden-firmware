"""Aggregate stage: merge RawPatches into a single ranked Patch.

Strategy (simplified from microsoft/SkillOpt):
  1. Flatten edits from every RawPatch.
  2. Tag each edit with its source_type (failure/success).
  3. Deduplicate by (op, target, content) — identical edits stack their
     support_count.
  4. Rank by support_count desc, then prefer failure-sourced edits over
     success-sourced (failures tend to be more actionable).
  5. Clip to edit_budget.
"""
from __future__ import annotations
from collections import OrderedDict

from skillopt.types import Edit, Patch, RawPatch


def _edit_key(edit: Edit) -> tuple[str, str, str]:
    return (edit.op, edit.target.strip(), edit.content.strip())


def aggregate(
    raw_patches: list[RawPatch],
    edit_budget: int,
) -> Patch:
    """Merge, dedupe, rank, clip. Returns a single Patch ready for apply."""
    if not raw_patches:
        return Patch(edits=[], reasoning="no patches produced")

    bucket: "OrderedDict[tuple[str, str, str], Edit]" = OrderedDict()
    for rp in raw_patches:
        for edit in rp.patch.edits:
            edit.source_type = edit.source_type or rp.source_type
            key = _edit_key(edit)
            if key in bucket:
                existing = bucket[key]
                existing.support_count = (existing.support_count or 1) + 1
                # If two sources voted for the same edit, mark as cross-source.
                if existing.source_type and edit.source_type and existing.source_type != edit.source_type:
                    existing.source_type = "success"  # pretty arbitrary tie-break
            else:
                if edit.support_count is None:
                    edit.support_count = 1
                bucket[key] = edit

    def rank_key(e: Edit) -> tuple:
        # Higher support first; failure-sourced before success-sourced.
        return (
            -(e.support_count or 1),
            0 if e.source_type == "failure" else 1,
        )

    ranked = sorted(bucket.values(), key=rank_key)
    clipped = ranked[:max(0, edit_budget)]
    reasoning = "\n".join(rp.patch.reasoning for rp in raw_patches if rp.patch.reasoning)
    return Patch(edits=clipped, reasoning=reasoning)


def format_rejected_context(rejected: list[Edit], max_chars: int = 2000) -> str:
    """Format rejected edits into a compact text block for the next reflect call.

    Helps the optimizer avoid re-proposing edits that already failed the gate.
    """
    if not rejected:
        return ""
    lines: list[str] = []
    for edit in rejected:
        snippet = (edit.content or edit.target or "").strip().replace("\n", " ")
        if len(snippet) > 200:
            snippet = snippet[:200] + "…"
        lines.append(f"- op={edit.op} target={edit.target[:60]!r} content={snippet!r}")
    text = "\n".join(lines)
    if len(text) > max_chars:
        text = text[:max_chars] + "\n…[truncated]"
    return text
