from runner.models import ToolCall, Trace
from runner.perception import (
    evaluate_first_click_rubric,
    parse_coordinate_range,
)
from runner.suite import RubricItem


def test_parse_coordinate_range_supports_current_perception_text():
    expected = parse_coordinate_range(
        "For normalized 0-1000 coordinates, x should be roughly in "
        "[750, 980] and y in [380, 550]."
    )

    assert expected is not None
    assert expected.x_min == 750
    assert expected.x_max == 980
    assert expected.y_min == 380
    assert expected.y_max == 550


def test_first_click_rubric_uses_raw_0_1000_values_without_scaling_unit_coordinates():
    rubric = [
        RubricItem(
            id="called_click_tool",
            check="The tool trace contains at least one touch_gesture or mouse_click call.",
        ),
        RubricItem(
            id="does_not_use_0_1_coordinates",
            check=(
                "No touch_gesture, mouse_click, or mouse_move call uses 0-1 style "
                "normalized coordinates."
            ),
        ),
        RubricItem(
            id="click_targets_settings",
            check=(
                "The touch/click coordinates target the Settings icon area. "
                "For normalized 0-1000 coordinates, x should be roughly in "
                "[750, 980] and y in [380, 550]."
            ),
        ),
    ]
    trace = Trace(
        tool_calls=[
            ToolCall(
                step=1,
                tool="mouse_click",
                input={"x": "0.935", "y": "0.083", "coord_space": "normalized"},
            )
        ],
        final_response="",
        total_tool_calls=1,
        total_duration_ms=0,
    )

    result = evaluate_first_click_rubric(trace, rubric)

    assert result is not None
    assert result.passed is False
    verdicts = {item.id: item.verdict for item in result.verdicts}
    assert verdicts["called_click_tool"] == "yes"
    assert verdicts["does_not_use_0_1_coordinates"] == "no"
    assert verdicts["click_targets_settings"] == "no"
