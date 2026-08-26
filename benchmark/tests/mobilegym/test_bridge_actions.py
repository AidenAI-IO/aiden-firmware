import sys
import types
from enum import Enum

import pytest

from mobilegym.bridge.actions import Action, ActionType, action_to_dict, build_action


class FakeMobileGymActionType(str, Enum):
    CLICK = "CLICK"
    DOUBLE_TAP = "DOUBLE_TAP"
    LONG_PRESS = "LONG_PRESS"
    SWIPE = "SWIPE"
    DRAG = "DRAG"
    TYPE = "TYPE"
    ENTER = "ENTER"
    BACK = "BACK"
    HOME = "HOME"
    WAIT = "WAIT"


class FakeMobileGymAction:
    def __init__(self, action_type, data):
        self.action_type = action_type
        self.data = data


def install_mobilegym_action_classes(monkeypatch):
    bench_env = types.ModuleType("bench_env")
    env = types.ModuleType("bench_env.env")
    base = types.ModuleType("bench_env.env.base")
    base.Action = FakeMobileGymAction
    base.ActionType = FakeMobileGymActionType
    bench_env.env = env
    env.base = base
    monkeypatch.setitem(sys.modules, "bench_env", bench_env)
    monkeypatch.setitem(sys.modules, "bench_env.env", env)
    monkeypatch.setitem(sys.modules, "bench_env.env.base", base)


def test_fallback_action_objects_do_not_require_mobilegym_submodule():
    action = build_action("tap", {"x": 250, "y": 750})

    assert isinstance(action, Action)
    assert action.action_type == ActionType.CLICK
    assert action.data == {"point": [250.0, 750.0]}
    assert action_to_dict(action) == {
        "action_type": "CLICK",
        "data": {"point": [250.0, 750.0]},
    }


def test_fallback_action_type_does_not_expose_generic_key():
    assert not hasattr(ActionType, "KEY")


@pytest.mark.parametrize(
    ("name", "payload", "expected_type", "expected_data"),
    [
        (
            "tap",
            {"x": 100, "y": 200, "count": 2},
            "DOUBLE_TAP",
            {"point": [100.0, 200.0]},
        ),
        (
            "tap",
            {"x": 100, "y": 200, "kind": "long_press", "duration_ms": 777},
            "LONG_PRESS",
            {"point": [100.0, 200.0], "duration": 777.0},
        ),
        (
            "swipe",
            {
                "start_x": 100,
                "start_y": 200,
                "end_x": 800,
                "end_y": 900,
                "duration_ms": 300,
                "steps": 5,
            },
            "SWIPE",
            {
                "point1": [100.0, 200.0],
                "point2": [800.0, 900.0],
                "duration": 300.0,
                "steps": 5,
            },
        ),
        (
            "drag",
            {"start_x": 200, "start_y": 300, "end_x": 700, "end_y": 600, "duration_ms": 900},
            "DRAG",
            {"point1": [200.0, 300.0], "point2": [700.0, 600.0], "duration": 900.0},
        ),
        ("type_text", {"text": "hello"}, "TYPE", {"value": "hello"}),
        ("key", {"key": "enter"}, "ENTER", {}),
        ("key", {"key": "home"}, "HOME", {}),
        ("key", {"key": "back"}, "BACK", {}),
        ("back", {}, "BACK", {}),
        ("home", {}, "HOME", {}),
        ("wait", {"duration_ms": 250}, "WAIT", {"value": 0.25}),
    ],
)
def test_build_action_converts_supported_tool_payloads(name, payload, expected_type, expected_data):
    action = action_to_dict(build_action(name, payload))

    assert action == {"action_type": expected_type, "data": expected_data}


def test_build_action_rejects_unknown_tool_name():
    with pytest.raises(ValueError, match="unsupported action"):
        build_action("pinch", {})


def test_type_action_includes_optional_point_and_clear():
    action = action_to_dict(build_action("type_text", {"value": "hello", "point": {"x": 1, "y": 2}, "clear": True}))

    assert action == {"action_type": "TYPE", "data": {"value": "hello", "point": [1.0, 2.0], "clear": True}}


def test_key_action_rejects_unsupported_keys():
    with pytest.raises(ValueError, match="unsupported key: volume_up"):
        build_action("key", {"key": "volume_up"})


def test_mobilegym_action_type_without_key_still_supports_enter_alias(monkeypatch):
    install_mobilegym_action_classes(monkeypatch)

    action = build_action("key", {"key": "enter"})

    assert isinstance(action, FakeMobileGymAction)
    assert action.action_type == FakeMobileGymActionType.ENTER
    assert action.data == {}
