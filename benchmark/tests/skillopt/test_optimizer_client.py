"""Unit tests for optimizer_client.py JSON extraction."""
import pytest
from runner.skillopt.optimizer_client import OptimizerError, extract_json


def test_extract_json_bare():
    raw = '{"foo": 1, "bar": "x"}'
    assert extract_json(raw) == {"foo": 1, "bar": "x"}


def test_extract_json_with_prose():
    raw = 'Here is my analysis:\n{"patch": {"edits": []}}\nDone.'
    assert extract_json(raw) == {"patch": {"edits": []}}


def test_extract_json_with_code_fence():
    raw = '```json\n{"a": 1}\n```'
    assert extract_json(raw) == {"a": 1}


def test_extract_json_with_unlabeled_fence():
    raw = '```\n{"a": 2}\n```'
    assert extract_json(raw) == {"a": 2}


def test_extract_json_no_object():
    with pytest.raises(OptimizerError):
        extract_json("just plain text, no JSON here")


def test_extract_json_invalid():
    with pytest.raises(OptimizerError):
        extract_json('{"broken": ')


def test_extract_json_nested():
    raw = 'prose {"outer": {"inner": [1, 2, 3]}} more prose'
    assert extract_json(raw) == {"outer": {"inner": [1, 2, 3]}}
