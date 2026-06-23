"""Unit tests for patch.py."""
from skillopt.patch import apply_edit, apply_patch
from skillopt.types import Edit, Patch


def test_apply_edit_append():
    skill = "# Skill\nSome content."
    edit = Edit(op="append", content="## New Section\nMore content.")
    result = apply_edit(skill, edit)
    assert "## New Section" in result
    assert result.endswith("More content.\n")


def test_apply_edit_insert_after():
    skill = "# Skill\n## Section A\nContent A.\n## Section B\nContent B."
    edit = Edit(op="insert_after", target="## Section A", content="Inserted text.")
    result = apply_edit(skill, edit)
    assert "## Section A\n\nInserted text.\n" in result


def test_apply_edit_insert_after_fallback():
    skill = "# Skill\nContent."
    edit = Edit(op="insert_after", target="MISSING", content="Fallback.")
    result = apply_edit(skill, edit)
    # Should fallback to append
    assert result.endswith("Fallback.\n")


def test_apply_edit_replace():
    skill = "Use the old method."
    edit = Edit(op="replace", target="old method", content="new approach")
    result = apply_edit(skill, edit)
    assert result == "Use the new approach."


def test_apply_edit_replace_missing_target():
    skill = "Some text."
    edit = Edit(op="replace", target="MISSING", content="replacement")
    result = apply_edit(skill, edit)
    assert result == "Some text."  # unchanged


def test_apply_edit_delete():
    skill = "Keep this. Remove this. Keep this."
    edit = Edit(op="delete", target="Remove this. ")
    result = apply_edit(skill, edit)
    assert result == "Keep this. Keep this."


def test_apply_patch():
    skill = "# Skill\nOld content."
    patch = Patch(edits=[
        Edit(op="replace", target="Old content", content="New content"),
        Edit(op="append", content="## Footer\nEnd."),
    ])
    result = apply_patch(skill, patch)
    assert "New content" in result
    assert "## Footer" in result
    assert "Old content" not in result
