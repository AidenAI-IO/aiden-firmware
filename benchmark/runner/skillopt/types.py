"""Core dataclasses for SkillOpt.

Field names align with microsoft/SkillOpt for easier algorithm porting.
Subset of the upstream types: drops slow_update / meta_skill / spreadsheet
specific fields that we don't need in the first cut.
"""
from __future__ import annotations
import dataclasses as dc
from typing import Any, Literal


EditOp = Literal["append", "insert_after", "replace", "delete"]


def _safe_int(value: Any, default: int = 0) -> int:
    try:
        return int(value)
    except (TypeError, ValueError):
        return default


@dc.dataclass
class Edit:
    """A single edit operation on a skill document.

    Field semantics mirror microsoft/SkillOpt:
      - append:       use ``content`` (target ignored)
      - insert_after: use ``target`` + ``content``; falls back to append
                      when target is missing
      - replace:      use ``target`` + ``content``; first match only
      - delete:       use ``target``; first match only
    """

    op: EditOp
    content: str = ""
    target: str = ""

    # Provenance / aggregation metadata (filled by aggregate / select stages)
    support_count: int | None = None
    source_type: Literal["failure", "success"] | None = None
    merge_level: int | None = None

    @classmethod
    def from_dict(cls, d: dict) -> Edit:
        return cls(
            op=d.get("op", "append"),
            content=d.get("content", ""),
            target=d.get("target", ""),
            support_count=d.get("support_count"),
            source_type=d.get("source_type"),
            merge_level=d.get("merge_level"),
        )

    def to_dict(self) -> dict:
        out: dict[str, Any] = {"op": self.op, "content": self.content}
        if self.target:
            out["target"] = self.target
        if self.support_count is not None:
            out["support_count"] = self.support_count
        if self.source_type is not None:
            out["source_type"] = self.source_type
        if self.merge_level is not None:
            out["merge_level"] = self.merge_level
        return out


@dc.dataclass
class Patch:
    """A bundle of edits with an optimizer-supplied rationale."""

    edits: list[Edit] = dc.field(default_factory=list)
    reasoning: str = ""

    @classmethod
    def from_dict(cls, d: dict) -> Patch:
        edits_raw = d.get("edits", []) or []
        return cls(
            edits=[Edit.from_dict(e) if isinstance(e, dict) else e for e in edits_raw],
            reasoning=d.get("reasoning", ""),
        )

    def to_dict(self) -> dict:
        return {
            "reasoning": self.reasoning,
            "edits": [e.to_dict() for e in self.edits],
        }


@dc.dataclass
class FailureSummaryEntry:
    failure_type: str
    count: int = 0
    description: str = ""

    @classmethod
    def from_dict(cls, d: dict) -> FailureSummaryEntry:
        return cls(
            failure_type=d.get("failure_type", ""),
            count=_safe_int(d.get("count", 0)),
            description=d.get("description", ""),
        )

    def to_dict(self) -> dict:
        return {
            "failure_type": self.failure_type,
            "count": self.count,
            "description": self.description,
        }


@dc.dataclass
class RawPatch:
    """Output of one minibatch reflect call (analyst_error or analyst_success)."""

    patch: Patch
    source_type: Literal["failure", "success"] = "failure"
    batch_size: int = 0
    failure_summary: list[FailureSummaryEntry] = dc.field(default_factory=list)

    @classmethod
    def from_dict(cls, d: dict | None) -> RawPatch | None:
        if d is None:
            return None
        inner = d.get("patch", d)
        if not isinstance(inner, dict):
            return None
        return cls(
            patch=Patch.from_dict(inner),
            source_type=d.get("source_type", "failure"),
            batch_size=_safe_int(d.get("batch_size", 0)),
            failure_summary=[
                FailureSummaryEntry.from_dict(fs)
                for fs in d.get("failure_summary", []) or []
                if isinstance(fs, dict)
            ],
        )

    def to_dict(self) -> dict:
        out: dict[str, Any] = {
            "patch": self.patch.to_dict(),
            "source_type": self.source_type,
            "batch_size": self.batch_size,
        }
        if self.failure_summary:
            out["failure_summary"] = [fs.to_dict() for fs in self.failure_summary]
        return out


@dc.dataclass
class RolloutResult:
    """One scored task rollout, the unit consumed by Reflect."""

    id: str
    hard: int                   # 0 or 1
    soft: float                 # 0.0 - 1.0
    n_turns: int = 0
    fail_reason: str = ""
    task_description: str = ""
    artifact_dir: str = ""      # where conversation/trace lives on disk
    extras: dict[str, Any] = dc.field(default_factory=dict)

    @classmethod
    def from_dict(cls, d: dict) -> RolloutResult:
        known = {"id", "hard", "soft", "n_turns", "fail_reason",
                 "task_description", "artifact_dir"}
        extras = {k: v for k, v in d.items() if k not in known}
        return cls(
            id=str(d.get("id", "")),
            hard=int(d.get("hard", 0)),
            soft=float(d.get("soft", 0.0)),
            n_turns=int(d.get("n_turns", 0)),
            fail_reason=str(d.get("fail_reason", "")),
            task_description=str(d.get("task_description", "")),
            artifact_dir=str(d.get("artifact_dir", "")),
            extras=extras,
        )

    def to_dict(self) -> dict:
        out: dict[str, Any] = {
            "id": self.id,
            "hard": self.hard,
            "soft": self.soft,
        }
        for attr in ("n_turns", "fail_reason", "task_description", "artifact_dir"):
            v = getattr(self, attr)
            if v:
                out[attr] = v
        out.update(self.extras)
        return out


@dc.dataclass
class StepDecision:
    """Result of one optimization step (one apply + selection eval)."""

    step: int
    candidate_score: float
    current_score: float
    accepted: bool
    reason: str = ""
    edits_applied: list[Edit] = dc.field(default_factory=list)
    edits_rejected: list[Edit] = dc.field(default_factory=list)


@dc.dataclass
class OptimizationResult:
    """Final outcome of an optimization run."""

    skill_name: str
    initial_score: float
    best_score: float
    best_skill: str
    steps: list[StepDecision] = dc.field(default_factory=list)
    accepted_count: int = 0
    rejected_count: int = 0
