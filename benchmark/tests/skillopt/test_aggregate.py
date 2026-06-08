"""Unit tests for aggregate.py."""
from runner.skillopt.aggregate import aggregate, format_rejected_context
from runner.skillopt.types import Edit, Patch, RawPatch


def test_aggregate_dedupes_identical_edits():
    e1 = Edit(op="append", content="X")
    e2 = Edit(op="append", content="X")  # identical
    rp1 = RawPatch(patch=Patch(edits=[e1]), source_type="failure", batch_size=2)
    rp2 = RawPatch(patch=Patch(edits=[e2]), source_type="success", batch_size=3)
    merged = aggregate([rp1, rp2], edit_budget=10)
    assert len(merged.edits) == 1
    assert merged.edits[0].support_count == 2


def test_aggregate_ranks_by_support():
    a = Edit(op="append", content="A")
    b = Edit(op="append", content="B")
    c = Edit(op="append", content="C")
    rp1 = RawPatch(patch=Patch(edits=[a, b]), source_type="failure", batch_size=2)
    rp2 = RawPatch(patch=Patch(edits=[a, c]), source_type="failure", batch_size=2)
    merged = aggregate([rp1, rp2], edit_budget=10)
    # a should be first (support=2), b/c follow (support=1)
    assert merged.edits[0].content == "A"
    assert merged.edits[0].support_count == 2


def test_aggregate_clips_to_budget():
    edits = [Edit(op="append", content=f"E{i}") for i in range(10)]
    rp = RawPatch(patch=Patch(edits=edits), source_type="failure", batch_size=1)
    merged = aggregate([rp], edit_budget=3)
    assert len(merged.edits) == 3


def test_aggregate_failure_priority_over_success():
    fail_edit = Edit(op="append", content="X")
    succ_edit = Edit(op="append", content="Y")
    rp_fail = RawPatch(patch=Patch(edits=[fail_edit]), source_type="failure", batch_size=1)
    rp_succ = RawPatch(patch=Patch(edits=[succ_edit]), source_type="success", batch_size=1)
    merged = aggregate([rp_fail, rp_succ], edit_budget=10)
    # Same support_count → failure wins tie
    assert merged.edits[0].content == "X"


def test_aggregate_empty():
    merged = aggregate([], edit_budget=4)
    assert merged.edits == []


def test_format_rejected_context():
    rejected = [
        Edit(op="append", content="bad idea 1"),
        Edit(op="replace", target="X", content="Y"),
    ]
    text = format_rejected_context(rejected)
    assert "bad idea 1" in text
    assert "op=append" in text
    assert "op=replace" in text


def test_format_rejected_context_empty():
    assert format_rejected_context([]) == ""
