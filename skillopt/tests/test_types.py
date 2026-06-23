"""Unit tests for SkillOpt dataclasses."""

from skillopt.types import FailureSummaryEntry, RawPatch


def test_failure_summary_entry_defaults_malformed_count_to_zero():
    entry = FailureSummaryEntry.from_dict({"failure_type": "timeout", "count": "N/A"})

    assert entry.failure_type == "timeout"
    assert entry.count == 0


def test_raw_patch_defaults_malformed_counts_and_skips_non_dict_summaries():
    raw = RawPatch.from_dict({
        "patch": {"edits": []},
        "source_type": "failure",
        "batch_size": "unknown",
        "failure_summary": [
            {"failure_type": "timeout", "count": None},
            "not-a-summary",
        ],
    })

    assert raw is not None
    assert raw.batch_size == 0
    assert len(raw.failure_summary) == 1
    assert raw.failure_summary[0].count == 0
