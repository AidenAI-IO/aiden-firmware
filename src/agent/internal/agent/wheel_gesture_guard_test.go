package agent

import (
	"aiden-agent/internal/agent/screen"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestWheelGestureGuardCommitsOnlySuccessfulToolCalls(t *testing.T) {
	guard := newWheelNudgeGuard(nil)
	call := wheelNudgeGuardCall(validWheelGuardInput(351, 460, 0, 20, 0, 0, "normalized"))

	if result, allowed := guard.BeforeToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("valid wheel call unexpectedly blocked: allowed=%v result=%#v", allowed, result)
	}
	if guard.total != 0 {
		t.Fatalf("before hook committed usage: total=%d, want 0", guard.total)
	}

	guard.AfterToolCall(context.Background(), call, ToolResult{
		Output: "hid write failed",
		Error:  NewToolError(CodeToolExecutionFailed, "hid write failed"),
	})
	if guard.total != 0 || len(guard.columns) != 0 {
		t.Fatalf("failed wheel call committed state: total=%d columns=%#v", guard.total, guard.columns)
	}

	if result, allowed := guard.BeforeToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("retry after failed execution should be allowed: allowed=%v result=%#v", allowed, result)
	}
	guard.AfterToolCall(context.Background(), call, ToolResult{Output: "ok"})
	if guard.total != 1 || len(guard.columns) != 1 || guard.columns[0].used != 1 {
		t.Fatalf("successful wheel call state = total=%d columns=%#v", guard.total, guard.columns)
	}
}

func TestWheelGestureGuardRequiresScreenshotFromCurrentRun(t *testing.T) {
	screen := &screen.ScreenState{}
	screen.UpdateScreenshot(uniformWheelScreenshotJPEG(t, 500, 1000), 500, 1000)
	guard := newWheelNudgeGuard(screen)
	call := wheelNudgeGuardCall(validWheelGuardInput(351, 460, 0, 20, 0, 0, "normalized"))

	if result, allowed := guard.BeforeToolCall(context.Background(), call); allowed || result.Error == nil {
		t.Fatalf("prior-run screenshot should be rejected: allowed=%v result=%#v", allowed, result)
	} else if !strings.Contains(result.Output, "current task") {
		t.Fatalf("blocked output = %q, want current-task screenshot guidance", result.Output)
	}

	screen.UpdateScreenshot(uniformWheelScreenshotJPEG(t, 500, 1000), 500, 1000)
	if result, allowed := guard.BeforeToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("current-run screenshot should allow wheel call: allowed=%v result=%#v", allowed, result)
	}
}

func TestWheelGestureGuardCountsActionWhenOnlyPostActionObservationFails(t *testing.T) {
	guard := newWheelNudgeGuard(nil)
	call := wheelNudgeGuardCall(validWheelGuardInput(351, 460, 0, 20, 0, 0, "normalized"))
	if result, allowed := guard.BeforeToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("valid wheel call unexpectedly blocked: allowed=%v result=%#v", allowed, result)
	}

	observationFailure := NewToolErrorWithDetails(
		CodeToolExecutionFailed,
		"wheel_nudge completed, but screenshot failed",
		map[string]any{postActionCompletedDetail: true},
	)
	guard.AfterToolCall(context.Background(), call, ToolResult{Output: observationFailure.Message, Error: observationFailure})
	if guard.total != 1 || len(guard.columns) != 1 || guard.columns[0].used != 1 {
		t.Fatalf("executed wheel action was not counted: total=%d columns=%#v", guard.total, guard.columns)
	}
}

func TestWheelGestureGuardNoMovementAllowsMicroRetry(t *testing.T) {
	guard := newWheelNudgeGuard(nil)
	first := wheelNudgeGuardCall(validWheelGuardInput(351, 460, 0, 20, 0, 0, "normalized"))
	if result, allowed := guard.BeforeToolCall(context.Background(), first); !allowed || result.Error != nil {
		t.Fatalf("first wheel call unexpectedly blocked: allowed=%v result=%#v", allowed, result)
	}
	guard.AfterToolCall(context.Background(), first, ToolResult{Output: "ok"})

	unchanged := wheelNudgeGuardCall(validWheelGuardInput(351, 460, 0, 20, 0, 0, "normalized"))
	if result, allowed := guard.BeforeToolCall(context.Background(), unchanged); allowed || result.Error == nil {
		t.Fatalf("unchanged value should request a micro retry: allowed=%v result=%#v", allowed, result)
	}
	if guard.columns[0].pending != nil {
		t.Fatalf("no-movement observation must clear pending state: %#v", guard.columns[0].pending)
	}

	micro := wheelNudgeGuardCall(`{"picker_id":"test-picker","column_x":351,"center_y":460,"coord_space":"normalized","remaining_gap":1,"current_value":0,"target_value":20,"cycle_size":0,"cycle_start":0,"row_spacing":42}`)
	if result, allowed := guard.BeforeToolCall(context.Background(), micro); !allowed || result.Error != nil {
		t.Fatalf("micro retry after no movement should be allowed: allowed=%v result=%#v", allowed, result)
	}
}

func TestWheelGestureGuardAcceptsCorrectDirectionBeyondPlannedRows(t *testing.T) {
	guard := newWheelNudgeGuard(nil)
	first := wheelNudgeGuardCall(`{"picker_id":"alarm-create-hour","column_x":422,"center_y":257,"remaining_gap":9,"current_value":13,"target_value":4,"cycle_size":24,"cycle_start":0,"row_spacing":48,"value_step":1}`)
	allowAndCommitWheel(t, guard, first)

	// Physical picker inertia moved six rows even though the bounded plan
	// targeted fewer. The direction and declared visible row ordering are still
	// correct, so the guard must accept the fresh value and continue the loop.
	next := wheelNudgeGuardCall(`{"picker_id":"alarm-create-hour","column_x":422,"center_y":257,"remaining_gap":3,"current_value":7,"target_value":4,"cycle_size":24,"cycle_start":0,"row_spacing":48,"value_step":1}`)
	if result, allowed := guard.BeforeToolCall(context.Background(), next); !allowed || result.Error != nil {
		t.Fatalf("correct-direction 13 -> 7 observation unexpectedly blocked: allowed=%v result=%#v", allowed, result)
	}
	if guard.columns[0].pending != nil {
		t.Fatalf("accepted observation left stale pending state: %#v", guard.columns[0].pending)
	}
}

func TestWheelGestureGuardReportsRequiredStepSignForOppositeMapping(t *testing.T) {
	guard := newWheelNudgeGuard(nil)
	first := wheelNudgeGuardCall(`{"picker_id":"alarm-create-hour","column_x":422,"center_y":257,"remaining_gap":9,"current_value":13,"target_value":4,"cycle_size":24,"cycle_start":0,"row_spacing":48,"value_step":1}`)
	allowAndCommitWheel(t, guard, first)

	wrongSign := wheelNudgeGuardCall(`{"picker_id":"alarm-create-hour","column_x":422,"center_y":257,"remaining_gap":3,"current_value":7,"target_value":4,"cycle_size":24,"cycle_start":0,"row_spacing":48,"value_step":-1}`)
	result, allowed := guard.BeforeToolCall(context.Background(), wrongSign)
	if allowed || result.Error == nil {
		t.Fatalf("opposite value_step sign must be blocked: allowed=%v result=%#v", allowed, result)
	}
	if !strings.Contains(result.Output, "requires value_step with positive sign") {
		t.Fatalf("blocked result = %#v, want explicit required sign", result)
	}
}

func TestWheelObservationRowsSolvesLargeCycleWithoutEnumeration(t *testing.T) {
	const cycleSize = 1_000_000_007
	const valueStep = 37
	const wantRows = 900_000_000
	afterValue := int((int64(wantRows) * valueStep) % cycleSize)
	observation := wheelNudgeObservation{
		beforeValue: 0,
		direction:   "up",
		cycleSize:   cycleSize,
		cycleStart:  0,
	}

	rows, ok := wheelObservationRows(observation, afterValue, valueStep)
	if !ok || rows != wantRows {
		t.Fatalf("large-cycle rows = %d, %v, want %d, true", rows, ok, wantRows)
	}
}

func TestWheelObservationRowsRejectsUnreachableLargeCycleDelta(t *testing.T) {
	observation := wheelNudgeObservation{
		beforeValue: 0,
		direction:   "up",
		cycleSize:   1_000_000_000,
		cycleStart:  0,
	}

	if rows, ok := wheelObservationRows(observation, 999_999_999, 6); ok {
		t.Fatalf("unreachable large-cycle rows = %d, true, want false", rows)
	}
}

func TestWheelObservationRowsMapsZeroResidueToSmallestPositiveCycle(t *testing.T) {
	observation := wheelNudgeObservation{
		beforeValue: 13,
		direction:   "up",
		cycleSize:   24,
		cycleStart:  0,
	}

	rows, ok := wheelObservationRows(observation, 13, 6)
	if !ok || rows != 4 {
		t.Fatalf("full-cycle rows = %d, %v, want 4, true", rows, ok)
	}
}

func TestWheelGestureGuardLocksFinalTargetPerColumn(t *testing.T) {
	guard := newWheelNudgeGuard(nil)
	probe := wheelNudgeGuardCall(`{"picker_id":"alarm-create","column_x":157,"center_y":274,"coord_space":"screenshot","remaining_gap":1,"current_value":17,"target_value":1,"cycle_size":24,"cycle_start":0,"row_spacing":43}`)
	allowAndCommitWheel(t, guard, probe)

	intermediate := wheelNudgeGuardCall(`{"picker_id":"alarm-create","column_x":157,"center_y":274,"coord_space":"screenshot","remaining_gap":3,"current_value":18,"target_value":15,"cycle_size":24,"cycle_start":0,"row_spacing":43,"value_step":1}`)
	result, allowed := guard.BeforeToolCall(context.Background(), intermediate)
	if allowed || result.Error == nil {
		t.Fatalf("changing the final target on an active column must be blocked: allowed=%v result=%#v", allowed, result)
	}
	if !strings.Contains(result.Output, "target_value=1") || !strings.Contains(result.Output, "must remain fixed") {
		t.Fatalf("blocked output = %q, want locked target guidance", result.Output)
	}

	finalTarget := wheelNudgeGuardCall(`{"picker_id":"alarm-create","column_x":157,"center_y":274,"coord_space":"screenshot","remaining_gap":7,"current_value":18,"target_value":1,"cycle_size":24,"cycle_start":0,"row_spacing":43,"value_step":1}`)
	if result, allowed := guard.BeforeToolCall(context.Background(), finalTarget); !allowed || result.Error != nil {
		t.Fatalf("locked final target should remain usable after rejecting the intermediate target: allowed=%v result=%#v", allowed, result)
	}
}

func TestWheelGestureGuardMatchesScreenshotWheelWithNormalizedTouch(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{X: 711, Y: 28, Width: 498, Height: 1052, Valid: true})
	guard := newWheelNudgeGuard(screenState)
	screenState.UpdateScreenshot(uniformWheelScreenshotJPEG(t, 498, 1052), 498, 1052)
	wheel := wheelNudgeGuardCall(validWheelGuardInput(304, 289, 48, 5, 60, 0, "screenshot"))
	if result, allowed := guard.BeforeToolCall(context.Background(), wheel); !allowed || result.Error != nil {
		t.Fatalf("wheel call unexpectedly blocked: allowed=%v result=%#v", allowed, result)
	}
	guard.AfterToolCall(context.Background(), wheel, ToolResult{Output: "ok"})

	tap := ToolCall{
		Spec:  ToolSpec{Name: "touch_gesture"},
		Input: `{"type":"tap","coord_space":"normalized","point":{"x":611,"y":488}}`,
	}
	if result, allowed := guard.BeforeToolCall(context.Background(), tap); allowed || result.Error == nil {
		t.Fatalf("normalized touch on screenshot-space wheel column should be blocked: allowed=%v result=%#v", allowed, result)
	}
}

func TestWheelGestureGuardClearsColumnsAfterSuccessfulNavigationTap(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{X: 711, Y: 28, Width: 498, Height: 1052, Valid: true})
	guard := newWheelNudgeGuard(screenState)
	screenState.UpdateScreenshot(uniformWheelScreenshotJPEG(t, 498, 1052), 498, 1052)
	wheel := wheelNudgeGuardCall(validWheelGuardInput(314, 289, 48, 5, 60, 0, "screenshot"))
	if result, allowed := guard.BeforeToolCall(context.Background(), wheel); !allowed || result.Error != nil {
		t.Fatalf("wheel call unexpectedly blocked: allowed=%v result=%#v", allowed, result)
	}
	guard.AfterToolCall(context.Background(), wheel, ToolResult{Output: "ok"})

	save := ToolCall{
		Spec:  ToolSpec{Name: "touch_gesture"},
		Input: `{"type":"tap","coord_space":"screenshot","point":{"x":452,"y":80}}`,
	}
	if result, allowed := guard.BeforeToolCall(context.Background(), save); !allowed || result.Error != nil {
		t.Fatalf("save tap unexpectedly blocked: allowed=%v result=%#v", allowed, result)
	}
	guard.AfterToolCall(context.Background(), save, ToolResult{Output: `{"screen_changed":true}`})
	if len(guard.columns) != 0 {
		t.Fatalf("successful navigation tap left stale wheel columns: %#v", guard.columns)
	}
}

func TestWheelGestureGuardClearsColumnsAfterOtherNavigationTool(t *testing.T) {
	guard := newWheelNudgeGuard(nil)
	allowAndCommitWheel(t, guard, wheelNudgeGuardCall(validWheelGuardInput(314, 289, 48, 5, 60, 0, "screenshot")))

	navigation := ToolCall{Spec: ToolSpec{Name: "quick_action"}, Input: `{"action":"back"}`}
	if result, allowed := guard.BeforeToolCall(context.Background(), navigation); !allowed || result.Error != nil {
		t.Fatalf("navigation tool unexpectedly blocked: allowed=%v result=%#v", allowed, result)
	}
	guard.AfterToolCall(context.Background(), navigation, ToolResult{Output: `{"screen_changed":true}`})
	if len(guard.columns) != 0 {
		t.Fatalf("successful navigation tool left stale wheel columns: %#v", guard.columns)
	}
}

func TestWheelGestureGuardKeepsColumnsAfterNonNavigationScreenChange(t *testing.T) {
	guard := newWheelNudgeGuard(nil)
	allowAndCommitWheel(t, guard, wheelNudgeGuardCall(validWheelGuardInput(314, 289, 48, 5, 60, 0, "screenshot")))

	toggle := ToolCall{Spec: ToolSpec{Name: "touch_gesture"}, Input: `{"type":"tap","coord_space":"screenshot","point":{"x":450,"y":500}}`}
	if result, allowed := guard.BeforeToolCall(context.Background(), toggle); !allowed || result.Error != nil {
		t.Fatalf("outside toggle unexpectedly blocked: allowed=%v result=%#v", allowed, result)
	}
	guard.AfterToolCall(context.Background(), toggle, ToolResult{Output: `{"screen_changed":true}`})
	if len(guard.columns) != 1 {
		t.Fatalf("non-navigation screen change cleared wheel ownership: %#v", guard.columns)
	}
}

func TestWheelGestureGuardAllowsDistantYearPickerProgress(t *testing.T) {
	guard := newWheelNudgeGuard(nil)
	current := 2026
	target := 1970
	steps := 0
	for current != target {
		input := validWheelGuardInput(351, 460, current, target, 0, 0, "normalized")
		call := wheelNudgeGuardCall(input)
		if result, allowed := guard.BeforeToolCall(context.Background(), call); !allowed || result.Error != nil {
			t.Fatalf("year progress blocked after %d steps at %d: allowed=%v result=%#v", steps, current, allowed, result)
		}
		guard.AfterToolCall(context.Background(), call, ToolResult{Output: "ok"})
		gap := current - target
		current -= wheelNudgeRowsForGap(gap)
		steps++
	}
	if steps != 13 {
		t.Fatalf("distant year picker completed in %d steps, want 13 coarse-to-fine nudges", steps)
	}
}

func TestWheelGestureGuardAllowsConservativePickerProgress(t *testing.T) {
	guard := newWheelNudgeGuard(nil)
	// The real iOS minute picker moved three rows for nominal four-row drags,
	// then two rows for nominal three-row drags. Reaching 00 from 29 therefore
	// needs an eleventh call even though the initial progress-derived budget is
	// ten calls.
	for _, current := range []int{29, 26, 23, 20, 17, 14, 11, 8, 6, 4, 2} {
		call := wheelNudgeGuardCall(fmt.Sprintf(`{"picker_id":"alarm-create","column_x":590,"center_y":261,"remaining_gap":%d,"current_value":%d,"target_value":0,"cycle_size":60,"cycle_start":0,"row_spacing":38,"value_step":1}`, current, current))
		if result, allowed := guard.BeforeToolCall(context.Background(), call); !allowed || result.Error != nil {
			t.Fatalf("conservative minute progress blocked at %02d: allowed=%v result=%#v", current, allowed, result)
		}
		guard.AfterToolCall(context.Background(), call, ToolResult{Output: "ok"})
	}
	if got := guard.columns[0].used; got != 11 {
		t.Fatalf("minute column used %d calls, want 11", got)
	}
	if got := guard.columns[0].limit; got < 11 {
		t.Fatalf("conservative progress column limit = %d, want at least 11", got)
	}
}

func TestWheelGestureGuardBudgetsSupportThreeLargeColumns(t *testing.T) {
	if wheelNudgeMinPerColumn != 12 || wheelNudgeMaxPerColumn != 64 || wheelNudgeMaxTotal != 128 {
		t.Fatalf("wheel budgets = min:%d max:%d total:%d, want 12, 64, and 128", wheelNudgeMinPerColumn, wheelNudgeMaxPerColumn, wheelNudgeMaxTotal)
	}
	if short := wheelNudgeLimitForGap(20); short != wheelNudgeMinPerColumn {
		t.Fatalf("short wheel limit = %d, want %d", short, wheelNudgeMinPerColumn)
	}
	if distant := wheelNudgeLimitForGap(200); distant <= 24 || distant >= wheelNudgeMaxPerColumn {
		t.Fatalf("distant wheel limit = %d, want a progress-derived limit between 25 and %d", distant, wheelNudgeMaxPerColumn-1)
	}
}

func TestWheelGestureGuardStopsNudgeAfterPerColumnLimit(t *testing.T) {
	var guard wheelNudgeGuard
	limit := wheelNudgeLimitForGap(200)
	for attempt := 0; attempt < limit; attempt++ {
		call := wheelNudgeGuardCall(validWheelGuardInput(351, 460, attempt, 200, 0, 0, "normalized"))
		allowAndCommitWheel(t, &guard, call)
	}

	result, allowed := guard.BeforeToolCall(context.Background(), wheelNudgeGuardCall(validWheelGuardInput(351, 460, limit, 200, 0, 0, "normalized")))
	if allowed {
		t.Fatal("nudge after per-column limit should be blocked")
	}
	if result.Error == nil || result.Error.Code != CodeWheelGestureLimit {
		t.Fatalf("blocked result error = %#v, want %s", result.Error, CodeWheelGestureLimit)
	}
	wantUsage := fmt.Sprintf("%d/%d", limit, limit)
	if !strings.Contains(result.Output, "column") || !strings.Contains(result.Output, wantUsage) {
		t.Fatalf("blocked output = %q, want per-column usage", result.Output)
	}
	if guard.total != limit {
		t.Fatalf("blocked attempt must not increment total: got %d", guard.total)
	}
}

func TestWheelGestureGuardStopsNudgeAfterTotalLimit(t *testing.T) {
	var guard wheelNudgeGuard
	for index := 0; index < wheelNudgeMaxTotal; index++ {
		input := validWheelGuardInput(100+float64(index)*100, 460, 0, 20, 0, 0, "screenshot")
		input = strings.Replace(input, `"test-picker"`, fmt.Sprintf(`"picker-%d"`, index), 1)
		allowAndCommitWheel(t, &guard, wheelNudgeGuardCall(input))
	}

	result, allowed := guard.BeforeToolCall(
		context.Background(),
		wheelNudgeGuardCall(validWheelGuardInput(99999, 460, 0, 10, 0, 0, "screenshot")),
	)
	if allowed {
		t.Fatal("nudge after total limit should be blocked")
	}
	if result.Error == nil || result.Error.Code != CodeWheelGestureLimit {
		t.Fatalf("blocked result error = %#v, want %s", result.Error, CodeWheelGestureLimit)
	}
	wantUsage := fmt.Sprintf("%d/%d", wheelNudgeMaxTotal, wheelNudgeMaxTotal)
	if !strings.Contains(result.Output, wantUsage) {
		t.Fatalf("blocked output = %q, want total usage", result.Output)
	}
}

func TestWheelGestureGuardBucketsNearbyColumnCoordinates(t *testing.T) {
	var guard wheelNudgeGuard
	xs := []float64{349, 351, 369, 352}
	limit := wheelNudgeLimitForGap(28)
	for attempt := 0; attempt < limit; attempt++ {
		x := xs[attempt%len(xs)]
		input := validWheelGuardInput(x, 460, attempt, 28, 0, 0, "normalized")
		allowAndCommitWheel(t, &guard, wheelNudgeGuardCall(input))
	}

	if _, allowed := guard.BeforeToolCall(
		context.Background(),
		wheelNudgeGuardCall(validWheelGuardInput(352, 460, limit, 28, 0, 0, "normalized")),
	); allowed {
		t.Fatal("nearby coordinates should share the same per-column limit")
	}
}

func TestWheelGestureGuardBucketsCoordinatesAfterToolClamping(t *testing.T) {
	var guard wheelNudgeGuard
	xs := []float64{-25, 0, 20, 10}
	limit := wheelNudgeLimitForGap(28)
	for attempt := 0; attempt < limit; attempt++ {
		x := xs[attempt%len(xs)]
		input := validWheelGuardInput(x, 460, attempt, 28, 0, 0, "normalized")
		allowAndCommitWheel(t, &guard, wheelNudgeGuardCall(input))
	}

	if _, allowed := guard.BeforeToolCall(
		context.Background(),
		wheelNudgeGuardCall(validWheelGuardInput(10, 460, limit, 28, 0, 0, "normalized")),
	); allowed {
		t.Fatal("coordinates clamped to the same screen edge should share one column limit")
	}
}

func TestWheelGestureGuardAllowsKnownDirectionWithoutProbe(t *testing.T) {
	var guard wheelNudgeGuard
	call := wheelNudgeGuardCall(`{"picker_id":"test-picker","column_x":184,"center_y":274,"coord_space":"screenshot","remaining_gap":11,"current_value":10,"target_value":21,"cycle_size":24,"cycle_start":0,"row_spacing":42,"value_step":1}`)
	allowAndCommitWheel(t, &guard, call)
	if guard.total != 1 || len(guard.columns) != 1 || guard.columns[0].direction != "up" {
		t.Fatalf("known-direction nudge state = total=%d columns=%#v", guard.total, guard.columns)
	}
}

func TestWheelGestureGuardTracksAdjacentMicroDragWithoutVisibleTarget(t *testing.T) {
	var guard wheelNudgeGuard
	call := wheelNudgeGuardCall(`{"picker_id":"test-picker","column_x":184,"center_y":274,"coord_space":"screenshot","remaining_gap":1,"current_value":10,"target_value":11,"cycle_size":24,"cycle_start":0,"row_spacing":42,"value_step":1}`)
	allowAndCommitWheel(t, &guard, call)
	if len(guard.columns) != 1 || guard.columns[0].pending == nil {
		t.Fatalf("adjacent target without visible_target_y executes a drag and must remain pending: %#v", guard.columns)
	}
}

func TestWheelGestureGuardDoesNotTrackVerifiedAdjacentTap(t *testing.T) {
	var guard wheelNudgeGuard
	call := wheelNudgeGuardCall(`{"picker_id":"test-picker","column_x":184,"center_y":274,"coord_space":"screenshot","remaining_gap":1,"current_value":10,"target_value":11,"cycle_size":24,"cycle_start":0,"row_spacing":42,"value_step":1,"visible_target_y":316}`)
	allowAndCommitWheel(t, &guard, call)
	if len(guard.columns) != 1 || guard.columns[0].pending != nil {
		t.Fatalf("verified adjacent tap must not create drag observation state: %#v", guard.columns)
	}
}

func TestWheelGestureGuardExpandsBudgetAfterDirectionProbe(t *testing.T) {
	var guard wheelNudgeGuard
	probe := wheelNudgeGuardCall(`{"picker_id":"large-picker","column_x":184,"center_y":274,"coord_space":"screenshot","remaining_gap":1,"current_value":0,"target_value":200,"cycle_size":0,"cycle_start":0,"row_spacing":42}`)
	allowAndCommitWheel(t, &guard, probe)

	known := wheelNudgeGuardCall(`{"picker_id":"large-picker","column_x":184,"center_y":274,"coord_space":"screenshot","remaining_gap":199,"current_value":1,"target_value":200,"cycle_size":0,"cycle_start":0,"row_spacing":42,"value_step":1}`)
	if result, allowed := guard.BeforeToolCall(context.Background(), known); !allowed || result.Error != nil {
		t.Fatalf("known step after probe unexpectedly blocked: allowed=%v result=%#v", allowed, result)
	}
	if guard.columns[0].limit != wheelNudgeMinPerColumn {
		t.Fatalf("budget expanded before the larger nudge executed: %d", guard.columns[0].limit)
	}
	guard.AfterToolCall(context.Background(), known, ToolResult{Output: "ok"})
	wantLimit := wheelNudgeLimitForGap(199)
	if guard.columns[0].limit != wantLimit {
		t.Fatalf("column limit after large known gap = %d, want %d", guard.columns[0].limit, wantLimit)
	}
}

func TestWheelGestureGuardRejectedObservationDoesNotExpandBudget(t *testing.T) {
	var guard wheelNudgeGuard
	probe := wheelNudgeGuardCall(`{"picker_id":"large-picker","column_x":184,"center_y":274,"coord_space":"screenshot","remaining_gap":1,"current_value":0,"target_value":200,"cycle_size":0,"cycle_start":0,"row_spacing":42}`)
	allowAndCommitWheel(t, &guard, probe)
	originalLimit := guard.columns[0].limit

	wrongStep := wheelNudgeGuardCall(`{"picker_id":"large-picker","column_x":184,"center_y":274,"coord_space":"screenshot","remaining_gap":199,"current_value":1,"target_value":200,"cycle_size":0,"cycle_start":0,"row_spacing":42,"value_step":-1}`)
	if result, allowed := guard.BeforeToolCall(context.Background(), wrongStep); allowed || result.Error == nil {
		t.Fatalf("step contradicting the probe should be rejected: allowed=%v result=%#v", allowed, result)
	}
	if guard.columns[0].limit != originalLimit {
		t.Fatalf("rejected call changed column limit from %d to %d", originalLimit, guard.columns[0].limit)
	}
}

func TestWheelGestureGuardKeepsPhysicalHourAndMinuteColumnsSeparate(t *testing.T) {
	var guard wheelNudgeGuard
	for attempt := 0; attempt < wheelNudgeMinPerColumn; attempt++ {
		call := wheelNudgeGuardCall(validWheelGuardInput(196, 274, attempt, 28, 0, 0, "screenshot"))
		allowAndCommitWheel(t, &guard, call)
	}

	minute := wheelNudgeGuardCall(validWheelGuardInput(291, 274, 0, 10, 0, 0, "screenshot"))
	if result, allowed := guard.BeforeToolCall(context.Background(), minute); !allowed {
		t.Fatalf("minute column should have a fresh budget: %#v", result)
	}
}

func TestWheelGestureGuardDoesNotClampHighResolutionScreenshotColumns(t *testing.T) {
	var guard wheelNudgeGuard
	first := wheelNudgeGuardCall(validWheelGuardInput(1100, 274, 0, 10, 0, 0, "screenshot"))
	allowAndCommitWheel(t, &guard, first)

	secondColumnWithoutProbe := wheelNudgeGuardCall(validWheelGuardInput(1200, 274, 0, 10, 0, 0, "screenshot"))
	result, allowed := guard.BeforeToolCall(context.Background(), secondColumnWithoutProbe)
	if !allowed || result.Error != nil {
		t.Fatalf("distinct screenshot columns above x=1000 must stay separate: allowed=%v result=%#v", allowed, result)
	}
	guard.AfterToolCall(context.Background(), secondColumnWithoutProbe, ToolResult{Output: "ok"})
	if len(guard.columns) != 2 {
		t.Fatalf("high-resolution columns were merged: %#v", guard.columns)
	}
}

func TestWheelGestureGuardSeparatesSameXColumnsByPickerID(t *testing.T) {
	var guard wheelNudgeGuard
	hour := wheelNudgeGuardCall(`{"picker_id":"alarm-create","column_x":196,"remaining_gap":11,"current_value":10,"target_value":21,"cycle_size":24,"cycle_start":0,"row_spacing":42,"value_step":1,"coord_space":"screenshot","center_y":274}`)
	allowAndCommitWheel(t, &guard, hour)

	month := wheelNudgeGuardCall(`{"picker_id":"date-editor","column_x":196,"remaining_gap":11,"current_value":10,"target_value":21,"cycle_size":24,"cycle_start":0,"row_spacing":42,"value_step":1,"coord_space":"screenshot","center_y":274}`)
	result, allowed := guard.BeforeToolCall(context.Background(), month)
	if !allowed || result.Error != nil {
		t.Fatalf("same-x column on another picker should have independent state: allowed=%v result=%#v", allowed, result)
	}
	guard.AfterToolCall(context.Background(), month, ToolResult{Output: "ok"})
	if len(guard.columns) != 2 || guard.columns[0].pickerID == guard.columns[1].pickerID {
		t.Fatalf("same-x picker columns leaked identity: %#v", guard.columns)
	}
}

func TestWheelGestureGuardKeepsColumnIdentityWhenDomainChanges(t *testing.T) {
	var guard wheelNudgeGuard
	first := wheelNudgeGuardCall(`{"picker_id":"date-editor","column_x":196,"remaining_gap":4,"current_value":1,"target_value":5,"cycle_size":31,"cycle_start":1,"row_spacing":42,"value_step":1,"coord_space":"screenshot","center_y":274}`)
	allowAndCommitWheel(t, &guard, first)

	second := wheelNudgeGuardCall(`{"picker_id":"date-editor","column_x":196,"remaining_gap":3,"current_value":2,"target_value":5,"cycle_size":30,"cycle_start":1,"row_spacing":42,"value_step":1,"coord_space":"screenshot","center_y":274}`)
	if result, allowed := guard.BeforeToolCall(context.Background(), second); !allowed {
		t.Fatalf("day-column nudge after month-domain change unexpectedly blocked: %#v", result)
	}
	guard.AfterToolCall(context.Background(), second, ToolResult{Output: "ok"})
	if len(guard.columns) != 1 || guard.columns[0].used != 2 {
		t.Fatalf("domain change reset physical column identity: %#v", guard.columns)
	}
}

func TestWheelNudgePlanDerivesShortestCyclicDirection(t *testing.T) {
	current, target, cycleSize, cycleStart, remainingGap, valueStep := 15, 9, 24, 0, 6, 1
	plan, err := planWheelNudge(wheelNudgeArgs{
		CurrentValue: &current, TargetValue: &target, CycleSize: &cycleSize,
		CycleStart: &cycleStart, RemainingGap: &remainingGap, ValueStep: &valueStep,
	})
	if err != nil {
		t.Fatalf("planWheelNudge returned error: %v", err)
	}
	if plan.direction != "down" {
		t.Fatalf("15 -> 9 with positive downward row step derived %q, want down", plan.direction)
	}
}

func TestWheelGestureGuardValidatesObservedProbeDirection(t *testing.T) {
	var guard wheelNudgeGuard
	probe := wheelNudgeGuardCall(`{"picker_id":"test-picker","column_x":314,"center_y":270,"coord_space":"screenshot","remaining_gap":1,"current_value":2,"target_value":12,"cycle_size":60,"cycle_start":0,"row_spacing":42}`)
	allowAndCommitWheel(t, &guard, probe)

	wrongMapping := wheelNudgeGuardCall(`{"picker_id":"test-picker","column_x":314,"center_y":270,"coord_space":"screenshot","remaining_gap":9,"current_value":3,"target_value":12,"cycle_size":60,"cycle_start":0,"row_spacing":42,"value_step":-1}`)
	result, allowed := guard.BeforeToolCall(context.Background(), wrongMapping)
	if allowed {
		t.Fatal("mapping that contradicts the observed 2 -> 3 probe must be blocked")
	}
	if result.Error == nil || !strings.Contains(result.Output, "requires value_step with positive sign") {
		t.Fatalf("blocked result = %#v, want explicit required sign", result)
	}
}

func TestWheelGestureGuardValidatesObservedMovementUsingDeclaredStep(t *testing.T) {
	var guard wheelNudgeGuard
	first := wheelNudgeGuardCall(`{"picker_id":"stepped-cycle","column_x":314,"center_y":270,"coord_space":"screenshot","remaining_gap":2,"current_value":0,"target_value":20,"cycle_size":100,"cycle_start":0,"row_spacing":42,"value_step":40}`)
	allowAndCommitWheel(t, &guard, first)

	second := wheelNudgeGuardCall(`{"picker_id":"stepped-cycle","column_x":314,"center_y":270,"coord_space":"screenshot","remaining_gap":1,"current_value":60,"target_value":20,"cycle_size":100,"cycle_start":0,"row_spacing":42,"value_step":40}`)
	if result, allowed := guard.BeforeToolCall(context.Background(), second); !allowed || result.Error != nil {
		t.Fatalf("0 -> 60 must validate as one finger-down step toward a stable target on a 100-value cycle: allowed=%v result=%#v", allowed, result)
	}
}

func TestWheelSemanticTargetSupportsOneBasedCycles(t *testing.T) {
	current := 12
	target := 1
	cycleSize := 12
	cycleStart := 1
	valueStep := 1
	gap, directions, ok := wheelSemanticTarget(wheelNudgeArgs{
		CurrentValue: &current,
		TargetValue:  &target,
		CycleSize:    &cycleSize,
		CycleStart:   &cycleStart,
		ValueStep:    &valueStep,
	}, "up")
	if !ok || gap != 1 || len(directions) != 1 || directions[0] != "up" {
		t.Fatalf("12 -> 1 on a one-based month wheel = gap %d directions %#v ok=%v, want 1 [up] true", gap, directions, ok)
	}
}

func TestWheelSemanticTargetHandlesLargeCycleWithoutScanning(t *testing.T) {
	current := 0
	target := 999_999_999
	cycleSize := 1_000_000_000
	cycleStart := 0
	valueStep := 1
	gap, directions, ok := wheelSemanticTarget(wheelNudgeArgs{
		CurrentValue: &current,
		TargetValue:  &target,
		CycleSize:    &cycleSize,
		CycleStart:   &cycleStart,
		ValueStep:    &valueStep,
	}, "up")
	if !ok || gap != 1 || len(directions) != 1 || directions[0] != "down" {
		t.Fatalf("large cyclic target = gap %d directions %#v ok=%v, want 1 [down] true", gap, directions, ok)
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

func TestWheelGestureGuardBlocksTouchGestureOnActiveWheelColumn(t *testing.T) {
	var guard wheelNudgeGuard
	allowAndCommitWheel(t, &guard, wheelNudgeGuardCall(validWheelGuardInput(314, 289, 48, 5, 60, 0, "screenshot")))

	drag := ToolCall{
		Spec:  ToolSpec{Name: "touch_gesture"},
		Input: `{"type":"drag","coord_space":"screenshot","start":{"x":313,"y":513},"end":{"x":313,"y":513},"steps":20}`,
	}
	result, allowed := guard.BeforeToolCall(context.Background(), drag)
	if allowed {
		t.Fatal("touch gesture on an active wheel column should be blocked")
	}
	if result.Error == nil || !strings.Contains(result.Output, "wheel_nudge") {
		t.Fatalf("blocked result = %#v, want wheel_nudge guidance", result)
	}
}

func TestWheelGestureGuardAllowsTapBelowActivePickerForDialogAction(t *testing.T) {
	var guard wheelNudgeGuard
	allowAndCommitWheel(t, &guard, wheelNudgeGuardCall(validWheelGuardInput(456, 547, 29, 30, 60, 0, "normalized")))

	tap := ToolCall{
		Spec:  ToolSpec{Name: "touch_gesture"},
		Input: `{"type":"tap","coord_space":"normalized","point":{"x":481,"y":743}}`,
	}
	if result, allowed := guard.BeforeToolCall(context.Background(), tap); !allowed || result.Error != nil {
		t.Fatalf("tap below the active picker should remain available for dialog actions: allowed=%v result=%#v", allowed, result)
	}
}

func TestWheelGestureGuardBlocksDirectionalSwipeWhilePickerIsActive(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{X: 711, Y: 28, Width: 498, Height: 1052, Valid: true})
	guard := newWheelNudgeGuard(screenState)
	screenState.UpdateScreenshot(uniformWheelScreenshotJPEG(t, 498, 1052), 498, 1052)
	allowAndCommitWheel(t, guard, wheelNudgeGuardCall(validWheelGuardInput(304, 289, 48, 5, 60, 0, "screenshot")))

	swipe := ToolCall{
		Spec:  ToolSpec{Name: "touch_gesture"},
		Input: `{"type":"swipe_up","strength":"small","anchor":611}`,
	}
	if result, allowed := guard.BeforeToolCall(context.Background(), swipe); allowed || result.Error == nil {
		t.Fatalf("directional swipe should be blocked while picker is active: allowed=%v result=%#v", allowed, result)
	}
}

func TestWheelGestureGuardBlocksDirectionalSwipeWithExplicitWheelPoints(t *testing.T) {
	guard := newWheelNudgeGuard(nil)
	allowAndCommitWheel(t, guard, wheelNudgeGuardCall(validWheelGuardInput(400, 260, 15, 7, 24, 0, "normalized")))

	// This is the exact malformed fallback emitted after wheel_nudge failed on
	// the alarm picker. Directional aliases ignore start/end at execution time,
	// but those points still reveal an attempt to bypass the owned wheel column.
	swipe := ToolCall{
		Spec:  ToolSpec{Name: "touch_gesture"},
		Input: `{"type":"swipe_up","start":{"x":400,"y":300},"end":{"x":400,"y":600}}`,
	}
	if result, allowed := guard.BeforeToolCall(context.Background(), swipe); allowed || result.Error == nil {
		t.Fatalf("directional swipe with explicit wheel points should be blocked: allowed=%v result=%#v", allowed, result)
	}
}

func TestWheelGestureGuardAllowsDirectionalSwipeOutsidePickerColumns(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{X: 711, Y: 28, Width: 498, Height: 1052, Valid: true})
	guard := newWheelNudgeGuard(screenState)
	screenState.UpdateScreenshot(uniformWheelScreenshotJPEG(t, 498, 1052), 498, 1052)
	allowAndCommitWheel(t, guard, wheelNudgeGuardCall(validWheelGuardInput(304, 289, 48, 5, 60, 0, "screenshot")))

	swipe := ToolCall{Spec: ToolSpec{Name: "touch_gesture"}, Input: `{"type":"swipe_up","strength":"small","anchor":150}`}
	if result, allowed := guard.BeforeToolCall(context.Background(), swipe); !allowed || result.Error != nil {
		t.Fatalf("directional swipe outside picker column should remain allowed: allowed=%v result=%#v", allowed, result)
	}
}

func TestWheelGestureGuardBlocksMouseClickAcrossCoordinateSpaces(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{X: 711, Y: 28, Width: 498, Height: 1052, Valid: true})
	guard := newWheelNudgeGuard(screenState)
	screenState.UpdateScreenshot(uniformWheelScreenshotJPEG(t, 498, 1052), 498, 1052)
	allowAndCommitWheel(t, guard, wheelNudgeGuardCall(validWheelGuardInput(304, 289, 48, 5, 60, 0, "screenshot")))

	click := ToolCall{
		Spec:  ToolSpec{Name: "mouse_click"},
		Input: `{"x":611,"y":488,"coord_space":"normalized"}`,
	}
	if result, allowed := guard.BeforeToolCall(context.Background(), click); allowed || result.Error == nil {
		t.Fatalf("mouse click should not bypass active wheel ownership: allowed=%v result=%#v", allowed, result)
	}
}

func TestWheelGestureGuardBlocksMouseClickOnExhaustedColumnWithoutRetry(t *testing.T) {
	var guard wheelNudgeGuard
	limit := wheelNudgeLimitForGap(200)
	for attempt := 0; attempt < limit; attempt++ {
		call := wheelNudgeGuardCall(validWheelGuardInput(351, 460, attempt, 200, 0, 0, "normalized"))
		allowAndCommitWheel(t, &guard, call)
	}

	click := ToolCall{
		Spec:  ToolSpec{Name: "mouse_click"},
		Input: `{"x":351,"y":460,"coord_space":"normalized"}`,
	}
	result, allowed := guard.BeforeToolCall(context.Background(), click)
	if allowed {
		t.Fatal("mouse click on an exhausted wheel column should be blocked")
	}
	if result.Error == nil || result.Error.Code != CodeWheelGestureLimit {
		t.Fatalf("blocked result error = %#v, want %s", result.Error, CodeWheelGestureLimit)
	}
	if retry, _ := result.Error.Details["retry_same_column"].(bool); retry {
		t.Fatalf("blocked result = %#v, exhausted column must not be retried", result)
	}
}

func TestWheelGestureGuardBlocksPixelTouchAcrossCoordinateSpaces(t *testing.T) {
	screenState := &screen.ScreenState{}
	screenState.UpdateActiveArea(1920, 1080, screen.ScreenActiveArea{X: 711, Y: 28, Width: 498, Height: 1052, Valid: true})
	guard := newWheelNudgeGuard(screenState)
	screenState.UpdateScreenshot(uniformWheelScreenshotJPEG(t, 498, 1052), 498, 1052)
	allowAndCommitWheel(t, guard, wheelNudgeGuardCall(validWheelGuardInput(304, 289, 48, 5, 60, 0, "screenshot")))

	tap := ToolCall{Spec: ToolSpec{Name: "touch_gesture"}, Input: `{"type":"tap","coord_space":"pixel","point":{"x":304,"y":513}}`}
	if result, allowed := guard.BeforeToolCall(context.Background(), tap); allowed || result.Error == nil {
		t.Fatalf("pixel touch should not bypass screenshot-space wheel ownership: allowed=%v result=%#v", allowed, result)
	}
}

func TestWheelGestureGuardAllowsTouchGestureOutsideActiveWheelColumn(t *testing.T) {
	var guard wheelNudgeGuard
	allowAndCommitWheel(t, &guard, wheelNudgeGuardCall(validWheelGuardInput(314, 289, 48, 5, 60, 0, "screenshot")))

	saveTap := ToolCall{
		Spec:  ToolSpec{Name: "touch_gesture"},
		Input: `{"type":"tap","coord_space":"screenshot","point":{"x":450,"y":95}}`,
	}
	if result, allowed := guard.BeforeToolCall(context.Background(), saveTap); !allowed || result.Error != nil {
		t.Fatalf("touch outside active wheel columns should remain allowed: allowed=%v result=%#v", allowed, result)
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
		`{"column_x":350,"distance":"huge"}`,
		`{"column_x":350,"coord_space":"pixel"}`,
		`{"column_x":350,"duration_ms":-1}`,
		`{"column_x":"350"}`,
		`{"column_x":350,"travel":80}`,
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

func allowAndCommitWheel(t *testing.T, guard *wheelNudgeGuard, call ToolCall) {
	t.Helper()
	if result, allowed := guard.BeforeToolCall(context.Background(), call); !allowed || result.Error != nil {
		t.Fatalf("wheel call unexpectedly blocked: allowed=%v result=%#v input=%s", allowed, result, call.Input)
	}
	guard.AfterToolCall(context.Background(), call, ToolResult{Output: "ok"})
}

func validWheelGuardInput(columnX, centerY float64, currentValue, targetValue, cycleSize, cycleStart int, coordSpace string) string {
	gap, ok := wheelDomainDistance(currentValue, targetValue, cycleSize, cycleStart)
	if !ok || gap == 0 {
		panic("validWheelGuardInput requires distinct values inside the declared domain")
	}
	return fmt.Sprintf(
		`{"picker_id":"test-picker","column_x":%g,"center_y":%g,"coord_space":%q,"remaining_gap":%d,"current_value":%d,"target_value":%d,"cycle_size":%d,"cycle_start":%d,"row_spacing":42,"value_step":1}`,
		columnX,
		centerY,
		coordSpace,
		gap,
		currentValue,
		targetValue,
		cycleSize,
		cycleStart,
	)
}
