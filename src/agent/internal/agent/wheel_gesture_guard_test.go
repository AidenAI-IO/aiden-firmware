package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestWheelGestureGuardStopsFourthNudgeOnSameColumn(t *testing.T) {
	var guard wheelNudgeGuard
	for attempt := 0; attempt < wheelNudgeMaxPerColumn; attempt++ {
		call := wheelNudgeGuardCall(validWheelGuardInput(351, 460, attempt, 10, 0, 0, "normalized"))
		if result, allowed := guard.BeforeToolCall(context.Background(), call); !allowed {
			t.Fatalf("attempt %d unexpectedly blocked: %#v", attempt+1, result)
		}
	}

	result, allowed := guard.BeforeToolCall(context.Background(), wheelNudgeGuardCall(validWheelGuardInput(351, 460, 3, 10, 0, 0, "normalized")))
	if allowed {
		t.Fatal("fourth nudge on one column should be blocked")
	}
	if result.Error == nil || result.Error.Code != CodeWheelGestureLimit {
		t.Fatalf("blocked result error = %#v, want %s", result.Error, CodeWheelGestureLimit)
	}
	if !strings.Contains(result.Output, "column") || !strings.Contains(result.Output, "3/3") {
		t.Fatalf("blocked output = %q, want per-column usage", result.Output)
	}
	if guard.total != wheelNudgeMaxPerColumn {
		t.Fatalf("blocked attempt must not increment total: got %d", guard.total)
	}
}

func TestWheelGestureGuardStopsSeventhNudgeAcrossColumns(t *testing.T) {
	var guard wheelNudgeGuard
	inputs := []string{
		validWheelGuardInput(150, 460, 0, 10, 0, 0, "normalized"),
		validWheelGuardInput(150, 460, 1, 10, 0, 0, "normalized"),
		validWheelGuardInput(350, 460, 0, 10, 0, 0, "normalized"),
		validWheelGuardInput(350, 460, 1, 10, 0, 0, "normalized"),
		validWheelGuardInput(650, 460, 0, 10, 0, 0, "normalized"),
		validWheelGuardInput(650, 460, 1, 10, 0, 0, "normalized"),
	}
	for index, input := range inputs {
		if result, allowed := guard.BeforeToolCall(context.Background(), wheelNudgeGuardCall(input)); !allowed {
			t.Fatalf("attempt %d unexpectedly blocked: %#v", index+1, result)
		}
	}

	result, allowed := guard.BeforeToolCall(
		context.Background(),
		wheelNudgeGuardCall(validWheelGuardInput(850, 460, 0, 10, 0, 0, "normalized")),
	)
	if allowed {
		t.Fatal("seventh wheel nudge in one run should be blocked")
	}
	if result.Error == nil || result.Error.Code != CodeWheelGestureLimit {
		t.Fatalf("blocked result error = %#v, want %s", result.Error, CodeWheelGestureLimit)
	}
	if !strings.Contains(result.Output, "6/6") {
		t.Fatalf("blocked output = %q, want total usage", result.Output)
	}
}

func TestWheelGestureGuardBucketsNearbyColumnCoordinates(t *testing.T) {
	var guard wheelNudgeGuard
	for attempt, x := range []float64{349, 351, 369} {
		input := validWheelGuardInput(x, 460, attempt, 10, 0, 0, "normalized")
		if result, allowed := guard.BeforeToolCall(context.Background(), wheelNudgeGuardCall(input)); !allowed {
			t.Fatalf("column x=%.0f unexpectedly blocked: %#v", x, result)
		}
	}

	if _, allowed := guard.BeforeToolCall(
		context.Background(),
		wheelNudgeGuardCall(validWheelGuardInput(352, 460, 3, 10, 0, 0, "normalized")),
	); allowed {
		t.Fatal("nearby coordinates should share the same per-column limit")
	}
}

func TestWheelGestureGuardBucketsCoordinatesAfterToolClamping(t *testing.T) {
	var guard wheelNudgeGuard
	for attempt, x := range []float64{-25, 0, 20} {
		input := validWheelGuardInput(x, 460, attempt, 10, 0, 0, "normalized")
		if result, allowed := guard.BeforeToolCall(context.Background(), wheelNudgeGuardCall(input)); !allowed {
			t.Fatalf("column x=%.0f unexpectedly blocked: %#v", x, result)
		}
	}

	if _, allowed := guard.BeforeToolCall(
		context.Background(),
		wheelNudgeGuardCall(validWheelGuardInput(10, 460, 3, 10, 0, 0, "normalized")),
	); allowed {
		t.Fatal("coordinates clamped to the same screen edge should share one column limit")
	}
}

func TestWheelGestureGuardAllowsKnownDirectionWithoutProbe(t *testing.T) {
	var guard wheelNudgeGuard
	result, allowed := guard.BeforeToolCall(
		context.Background(),
		wheelNudgeGuardCall(`{"picker_id":"test-picker","column_x":184,"center_y":274,"coord_space":"screenshot","direction":"up","remaining_gap":11,"current_value":10,"target_value":21,"cycle_size":24,"cycle_start":0,"increasing_direction":"up","row_spacing":42,"value_step":1}`),
	)
	if !allowed || result.Error != nil {
		t.Fatalf("known visible-row direction should not require a probe: allowed=%v result=%#v", allowed, result)
	}
	if guard.total != 1 || len(guard.columns) != 1 || guard.columns[0].direction != "up" {
		t.Fatalf("known-direction nudge state = total=%d columns=%#v", guard.total, guard.columns)
	}
}

func TestWheelGestureGuardKeepsPhysicalHourAndMinuteColumnsSeparate(t *testing.T) {
	var guard wheelNudgeGuard
	for attempt := 0; attempt < wheelNudgeMaxPerColumn; attempt++ {
		call := wheelNudgeGuardCall(validWheelGuardInput(196, 274, attempt, 10, 0, 0, "screenshot"))
		if result, allowed := guard.BeforeToolCall(context.Background(), call); !allowed {
			t.Fatalf("hour attempt %d unexpectedly blocked: %#v", attempt+1, result)
		}
	}

	minute := wheelNudgeGuardCall(validWheelGuardInput(291, 274, 0, 10, 0, 0, "screenshot"))
	if result, allowed := guard.BeforeToolCall(context.Background(), minute); !allowed {
		t.Fatalf("minute column should have a fresh budget: %#v", result)
	}
}

func TestWheelGestureGuardDoesNotClampHighResolutionScreenshotColumns(t *testing.T) {
	var guard wheelNudgeGuard
	first := wheelNudgeGuardCall(validWheelGuardInput(1100, 274, 0, 10, 0, 0, "screenshot"))
	if result, allowed := guard.BeforeToolCall(context.Background(), first); !allowed {
		t.Fatalf("first high-resolution screenshot column unexpectedly blocked: %#v", result)
	}

	secondColumnWithoutProbe := wheelNudgeGuardCall(validWheelGuardInput(1200, 274, 0, 10, 0, 0, "screenshot"))
	result, allowed := guard.BeforeToolCall(context.Background(), secondColumnWithoutProbe)
	if !allowed || result.Error != nil {
		t.Fatalf("distinct screenshot columns above x=1000 must stay separate: allowed=%v result=%#v", allowed, result)
	}
	if len(guard.columns) != 2 {
		t.Fatalf("high-resolution columns were merged: %#v", guard.columns)
	}
}

func TestWheelGestureGuardSeparatesSameXColumnsByPickerID(t *testing.T) {
	var guard wheelNudgeGuard
	hour := wheelNudgeGuardCall(`{"picker_id":"alarm-create","column_x":196,"direction":"up","remaining_gap":11,"current_value":10,"target_value":21,"cycle_size":24,"cycle_start":0,"increasing_direction":"up","row_spacing":42,"value_step":1,"coord_space":"screenshot","center_y":274}`)
	if result, allowed := guard.BeforeToolCall(context.Background(), hour); !allowed {
		t.Fatalf("hour column unexpectedly blocked: %#v", result)
	}

	month := wheelNudgeGuardCall(`{"picker_id":"date-editor","column_x":196,"direction":"up","remaining_gap":11,"current_value":10,"target_value":21,"cycle_size":24,"cycle_start":0,"increasing_direction":"up","row_spacing":42,"value_step":1,"coord_space":"screenshot","center_y":274}`)
	result, allowed := guard.BeforeToolCall(context.Background(), month)
	if !allowed || result.Error != nil {
		t.Fatalf("same-x column on another picker should have independent state: allowed=%v result=%#v", allowed, result)
	}
	if len(guard.columns) != 2 || guard.columns[0].pickerID == guard.columns[1].pickerID {
		t.Fatalf("same-x picker columns leaked identity: %#v", guard.columns)
	}
}

func TestWheelGestureGuardKeepsColumnIdentityWhenDomainChanges(t *testing.T) {
	var guard wheelNudgeGuard
	first := wheelNudgeGuardCall(`{"picker_id":"date-editor","column_x":196,"direction":"up","remaining_gap":4,"current_value":1,"target_value":5,"cycle_size":31,"cycle_start":1,"increasing_direction":"up","row_spacing":42,"value_step":1,"coord_space":"screenshot","center_y":274}`)
	if result, allowed := guard.BeforeToolCall(context.Background(), first); !allowed {
		t.Fatalf("first day-column nudge unexpectedly blocked: %#v", result)
	}

	second := wheelNudgeGuardCall(`{"picker_id":"date-editor","column_x":196,"direction":"up","remaining_gap":3,"current_value":2,"target_value":5,"cycle_size":30,"cycle_start":1,"increasing_direction":"up","row_spacing":42,"value_step":1,"coord_space":"screenshot","center_y":274}`)
	if result, allowed := guard.BeforeToolCall(context.Background(), second); !allowed {
		t.Fatalf("day-column nudge after month-domain change unexpectedly blocked: %#v", result)
	}
	if len(guard.columns) != 1 || guard.columns[0].used != 2 {
		t.Fatalf("domain change reset physical column identity: %#v", guard.columns)
	}
}

func TestWheelGestureGuardRejectsDirectionOppositeShortestCyclicPath(t *testing.T) {
	var guard wheelNudgeGuard
	probe := wheelNudgeGuardCall(`{"picker_id":"test-picker","column_x":210,"direction":"up","remaining_gap":11,"current_value":10,"target_value":21,"cycle_size":24,"cycle_start":0,"increasing_direction":"unknown","row_spacing":42,"value_step":1,"coord_space":"screenshot","center_y":304}`)
	if result, allowed := guard.BeforeToolCall(context.Background(), probe); !allowed {
		t.Fatalf("micro probe unexpectedly blocked: %#v", result)
	}

	wrong := wheelNudgeGuardCall(`{"picker_id":"test-picker","column_x":210,"direction":"up","remaining_gap":4,"current_value":11,"target_value":7,"cycle_size":24,"cycle_start":0,"increasing_direction":"up","row_spacing":42,"value_step":1,"coord_space":"screenshot","center_y":304}`)
	result, allowed := guard.BeforeToolCall(context.Background(), wrong)
	if allowed {
		t.Fatal("01 -> 21 on a 24-hour wheel should reject the increasing direction")
	}
	if result.Error == nil || result.Error.Code != CodeInvalidArguments {
		t.Fatalf("blocked result error = %#v, want %s", result.Error, CodeInvalidArguments)
	}
	if !strings.Contains(result.Output, `direction="down"`) {
		t.Fatalf("blocked output = %q, want required down direction", result.Output)
	}
}

func TestWheelGestureGuardRejectsFirstProbeOppositeVisibleRowOrdering(t *testing.T) {
	var guard wheelNudgeGuard
	call := wheelNudgeGuardCall(`{"picker_id":"test-picker","column_x":314,"center_y":270,"coord_space":"screenshot","direction":"down","remaining_gap":10,"current_value":2,"target_value":12,"cycle_size":60,"cycle_start":0,"increasing_direction":"unknown","row_spacing":42,"value_step":1}`)

	result, allowed := guard.BeforeToolCall(context.Background(), call)
	if allowed {
		t.Fatal("finger-down probe must be blocked when values increase below the center row")
	}
	if result.Error == nil || !strings.Contains(result.Output, `direction="up"`) {
		t.Fatalf("blocked result = %#v, want derived finger-up direction", result)
	}
	if guard.total != 0 {
		t.Fatalf("blocked wrong-direction probe consumed budget: %d", guard.total)
	}
}

func TestWheelGestureGuardValidatesObservedProbeDirection(t *testing.T) {
	var guard wheelNudgeGuard
	probe := wheelNudgeGuardCall(`{"picker_id":"test-picker","column_x":314,"center_y":270,"coord_space":"screenshot","direction":"up","remaining_gap":10,"current_value":2,"target_value":12,"cycle_size":60,"cycle_start":0,"increasing_direction":"unknown","row_spacing":42}`)
	if result, allowed := guard.BeforeToolCall(context.Background(), probe); !allowed {
		t.Fatalf("derived-direction micro probe unexpectedly blocked: %#v", result)
	}

	wrongMapping := wheelNudgeGuardCall(`{"picker_id":"test-picker","column_x":314,"center_y":270,"coord_space":"screenshot","direction":"down","remaining_gap":9,"current_value":3,"target_value":12,"cycle_size":60,"cycle_start":0,"increasing_direction":"down","row_spacing":42,"value_step":1}`)
	result, allowed := guard.BeforeToolCall(context.Background(), wrongMapping)
	if allowed {
		t.Fatal("mapping that contradicts the observed 2 -> 3 probe must be blocked")
	}
	if result.Error == nil || !strings.Contains(result.Output, `increasing_direction="up"`) {
		t.Fatalf("blocked result = %#v, want observed increasing direction", result)
	}
}

func TestWheelSemanticTargetSupportsOneBasedCycles(t *testing.T) {
	current := 12
	target := 1
	cycleSize := 12
	cycleStart := 1
	gap, directions, ok := wheelSemanticTarget(wheelNudgeArgs{
		CurrentValue:        &current,
		TargetValue:         &target,
		CycleSize:           &cycleSize,
		CycleStart:          &cycleStart,
		IncreasingDirection: "up",
	})
	if !ok || gap != 1 || len(directions) != 1 || directions[0] != "up" {
		t.Fatalf("12 -> 1 on a one-based month wheel = gap %d directions %#v ok=%v, want 1 [up] true", gap, directions, ok)
	}
}

func TestWheelGestureGuardIgnoresTouchGestureWithProviderPopulatedWheelMetadata(t *testing.T) {
	var guard wheelNudgeGuard
	swipe := ToolCall{
		Spec:  ToolSpec{Name: "touch_gesture"},
		Input: `{"type":"swipe_down","coord_space":"normalized","start":{"x":500,"y":200},"end":{"x":500,"y":520},"wheel":{"is_picker_row":true,"picker_id":"bad","column_x":0,"center_y":0,"current_value":0,"tapped_value":0,"target_value":0,"cycle_size":0,"cycle_start":0,"row_offset":0,"row_spacing":0,"value_step":0}}`,
	}
	if result, allowed := guard.BeforeToolCall(context.Background(), swipe); !allowed || result.Error != nil {
		t.Fatalf("generic touch gesture must not be inspected by wheel guard: allowed=%v result=%#v", allowed, result)
	}
}

func TestWheelGestureGuardIgnoresOtherToolsAndInvalidInputs(t *testing.T) {
	var guard wheelNudgeGuard

	if result, allowed := guard.BeforeToolCall(context.Background(), ToolCall{
		Spec:  ToolSpec{Name: "touch_gesture"},
		Input: `{"type":"tap","point":{"x":350,"y":300}}`,
	}); !allowed || result.Error != nil {
		t.Fatalf("non-wheel tool should be ignored: allowed=%v result=%#v", allowed, result)
	}
	if result, allowed := guard.BeforeToolCall(context.Background(), wheelNudgeGuardCall(`not-json`)); !allowed || result.Error != nil {
		t.Fatalf("invalid wheel input should be left to schema validation: allowed=%v result=%#v", allowed, result)
	}
	if guard.total != 0 {
		t.Fatalf("ignored calls changed total to %d", guard.total)
	}
}

func TestWheelNudgeGuardDoesNotCountSemanticallyInvalidCalls(t *testing.T) {
	var guard wheelNudgeGuard
	invalidInputs := []string{
		`{"column_x":350,"direction":"left"}`,
		`{"column_x":350,"direction":"up","distance":"huge"}`,
		`{"column_x":350,"direction":"up","coord_space":"pixel"}`,
		`{"column_x":350,"direction":"up","duration_ms":-1}`,
		`{"column_x":"350","direction":"up"}`,
		`{"column_x":350,"direction":"up","travel":80}`,
	}
	for _, input := range invalidInputs {
		if result, allowed := guard.BeforeToolCall(context.Background(), wheelNudgeGuardCall(input)); !allowed || result.Error != nil {
			t.Fatalf("invalid call %s should be left to schema validation: allowed=%v result=%#v", input, allowed, result)
		}
	}
	if guard.total != 0 {
		t.Fatalf("invalid calls changed total to %d", guard.total)
	}
}

func TestWheelNudgeIsNotScriptCallable(t *testing.T) {
	if isScriptCallableTool("wheel_nudge") {
		t.Fatal("wheel_nudge must go through executor safety hooks, not run_script")
	}
}

func wheelNudgeGuardCall(input string) ToolCall {
	return ToolCall{
		Spec:  ToolSpec{Name: "wheel_nudge"},
		Input: input,
	}
}

func validWheelGuardInput(columnX, centerY float64, currentValue, targetValue, cycleSize, cycleStart int, coordSpace string) string {
	gap, ok := wheelDomainDistance(currentValue, targetValue, cycleSize, cycleStart)
	if !ok || gap == 0 {
		panic("validWheelGuardInput requires distinct values inside the declared domain")
	}
	direction := "up"
	if cycleSize == 0 && targetValue < currentValue {
		direction = "down"
	}
	return fmt.Sprintf(
		`{"picker_id":"test-picker","column_x":%g,"center_y":%g,"coord_space":%q,"direction":%q,"remaining_gap":%d,"current_value":%d,"target_value":%d,"cycle_size":%d,"cycle_start":%d,"increasing_direction":"up","row_spacing":42,"value_step":1}`,
		columnX,
		centerY,
		coordSpace,
		direction,
		gap,
		currentValue,
		targetValue,
		cycleSize,
		cycleStart,
	)
}
