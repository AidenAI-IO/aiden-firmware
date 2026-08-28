from __future__ import annotations

import dataclasses as dc
from enum import Enum
from typing import Any


class ActionType(str, Enum):
    CLICK = "CLICK"
    DOUBLE_TAP = "DOUBLE_TAP"
    LONG_PRESS = "LONG_PRESS"
    SWIPE = "SWIPE"
    DRAG = "DRAG"
    TYPE = "TYPE"
    TYPE_TEXT = "TYPE_TEXT"
    ENTER = "ENTER"
    BACK = "BACK"
    HOME = "HOME"
    WAIT = "WAIT"


@dc.dataclass(frozen=True)
class Action:
    action_type: Any
    data: dict[str, Any]


def build_action(name: str, payload: dict[str, Any]) -> Any:
    action_name = name.strip().lower()
    if action_name == "tap":
        return _tap_action(payload)
    if action_name == "swipe":
        return _make_action("SWIPE", _motion_payload(payload))
    if action_name == "drag":
        return _make_action("DRAG", _motion_payload(payload))
    if action_name == "type_text":
        text = str(payload.get("text", payload.get("value", "")))
        data: dict[str, Any] = {"value": text}
        if "point" in payload or ("x" in payload and "y" in payload):
            data["point"] = _point(payload)
        if "clear" in payload:
            data["clear"] = payload["clear"]
        return _make_action("TYPE", data)
    if action_name == "key":
        return _key_action(payload)
    if action_name == "back":
        return _make_action("BACK", {})
    if action_name == "home":
        return _make_action("HOME", {})
    if action_name == "wait":
        return _make_action("WAIT", {"value": _duration_seconds(payload)})
    raise ValueError(f"unsupported action: {name}")


def action_to_dict(action: Any) -> dict[str, Any]:
    action_type = getattr(action, "action_type", getattr(action, "type", None))
    data = getattr(action, "data", getattr(action, "params", {}))
    return {"action_type": _action_type_name(action_type), "data": dict(data or {})}


def _make_action(type_name: str, data: dict[str, Any]) -> Any:
    action_cls, action_type_cls = _action_classes()
    action_type = getattr(action_type_cls, type_name, type_name)
    try:
        return action_cls(action_type=action_type, data=data)
    except TypeError:
        return action_cls(action_type, data)


def _tap_action(payload: dict[str, Any]) -> Any:
    point = _point(payload)
    kind = str(payload.get("kind", payload.get("type", ""))).strip().lower()
    if kind == "long_press":
        return _make_action(
            "LONG_PRESS",
            {"point": point, "duration": _duration_milliseconds(payload, ms_keys=("duration_ms", "hold_ms"))},
        )
    if int(payload.get("count", 1) or 1) == 2:
        return _make_action("DOUBLE_TAP", {"point": point})
    return _make_action("CLICK", {"point": point})


def _key_action(payload: dict[str, Any]) -> Any:
    key = str(payload.get("key", "")).strip()
    alias = key.lower().replace("_", "-")
    if alias in {"enter", "return"}:
        return _make_action("ENTER", {})
    if alias in {"home", "go-home"}:
        return _make_action("HOME", {})
    if alias in {"back", "escape", "esc"}:
        return _make_action("BACK", {})

    raise ValueError(f"unsupported key: {key}")


def _action_classes() -> tuple[type[Any], Any]:
    try:
        from bench_env.env.base import Action as MobileGymAction
        from bench_env.env.base import ActionType as MobileGymActionType

        return MobileGymAction, MobileGymActionType
    except Exception:
        return Action, ActionType


def _point(payload: dict[str, Any]) -> list[float]:
    point = payload.get("point", payload)
    try:
        return [float(point["x"]), float(point["y"])]
    except (KeyError, TypeError, ValueError) as e:
        raise ValueError(f"Invalid point coordinates: {e}") from e


def _motion_payload(payload: dict[str, Any]) -> dict[str, Any]:
    if "point1" in payload and "point2" in payload:
        point1 = _point(payload["point1"])
        point2 = _point(payload["point2"])
    elif "start_x" in payload:
        point1 = [float(payload["start_x"]), float(payload["start_y"])]
        point2 = [float(payload["end_x"]), float(payload["end_y"])]
    else:
        point1 = _point(payload["start"])
        point2 = _point(payload["end"])
    return {"point1": point1, "point2": point2, "duration": _duration_milliseconds(payload)}


def _duration_seconds(payload: dict[str, Any], ms_keys: tuple[str, ...] = ("duration_ms",)) -> float:
    if "duration" in payload:
        return float(payload["duration"])
    if "value" in payload:
        return float(payload["value"])
    for key in ms_keys:
        if key in payload:
            return float(payload[key]) / 1000
    return 0.0


def _duration_milliseconds(payload: dict[str, Any], ms_keys: tuple[str, ...] = ("duration_ms",)) -> float:
    if "duration" in payload:
        return float(payload["duration"])
    if "value" in payload:
        return float(payload["value"])
    for key in ms_keys:
        if key in payload:
            return float(payload[key])
    return 0.0


def _action_type_name(action_type: Any) -> str:
    if hasattr(action_type, "name"):
        return str(action_type.name)
    if hasattr(action_type, "value"):
        return str(action_type.value)
    return str(action_type)
