package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

const (
	wheelNudgeMinPerColumn    = 10
	wheelNudgeMaxPerColumn    = 64
	wheelNudgeMaxTotal        = 128
	wheelNudgeRetrySlack      = 2
	wheelNudgeCenterTolerance = 60.0
	wheelNudgeColumnTolerance = 60.0
	wheelNudgeActionBarInset  = 100.0
	wheelNudgeMinSafetyHeight = 240.0
)

// wheelNudgeGuard is a run-scoped tool execution policy. It validates wheel
// progress before execution and commits usage only after a successful result.
type wheelNudgeGuard struct {
	screen            *screenState
	total             int
	columns           []wheelNudgeColumnUsage
	pendingWheel      *wheelNudgeAttempt
	pendingNavigation string
}

type wheelNudgeColumnUsage struct {
	pickerID   string
	centerX    float64
	centerY    float64
	rowSpacing float64
	coordSpace string
	used       int
	limit      int
	direction  string
	allowProbe bool
	pending    *wheelNudgeObservation
}

type wheelNudgeAttempt struct {
	input       string
	args        wheelNudgeArgs
	plan        wheelNudgePlan
	columnIndex int
	columnX     float64
	centerY     float64
	rowSpacing  float64
	coordSpace  string
	columnLimit int
}

type wheelNudgeObservation struct {
	beforeValue int
	direction   string
	cycleSize   int
	cycleStart  int
}

func newWheelNudgeGuard(screen *screenState) *wheelNudgeGuard {
	return &wheelNudgeGuard{screen: screen}
}

func (g *wheelNudgeGuard) BeforeToolCall(_ context.Context, call ToolCall) (ToolResult, bool) {
	if g == nil {
		return ToolResult{}, true
	}
	toolName := strings.ToLower(strings.TrimSpace(call.Spec.Name))
	g.pendingNavigation = ""
	if toolName == "touch_gesture" {
		return g.beforeTouchGesture(call)
	}
	if toolName == "mouse_click" {
		return g.beforeMouseClick(call)
	}
	if toolName != "wheel_nudge" {
		if isWheelNavigationToolCall(call) {
			g.pendingNavigation = wheelToolCallKey(call)
		}
		return ToolResult{}, true
	}
	g.pendingWheel = nil

	args, err := parseWheelNudgeArgs(call.Input)
	if err != nil {
		// WheelNudgeTool uses the same parser and will reject this call without
		// touching the device. Calls that cannot execute do not consume budget.
		return ToolResult{}, true
	}
	columnX, centerY, rowSpacing, coordSpace := g.guardCoordinates(args.CoordSpace, *args.ColumnX, wheelCenterY(args), *args.RowSpacing)
	columnIndex := g.columnIndex(args.PickerID, columnX, centerY, coordSpace)
	columnUsed := 0
	columnLimit := wheelNudgeLimitForGap(planGapOrZero(args))
	representativeX := columnX
	if columnIndex >= 0 {
		columnUsed = g.columns[columnIndex].used
		columnLimit = g.columns[columnIndex].limit
		representativeX = g.columns[columnIndex].centerX
	}
	plan, planErr := planWheelNudge(args)
	if planErr != nil {
		return invalidWheelResult(planErr.Error(), map[string]any{"column_x": representativeX, "retry_same_column": true}), false
	}
	if columnIndex < 0 {
		columnLimit = wheelNudgeLimitForGap(plan.gap)
	}
	if columnIndex >= 0 {
		column := &g.columns[columnIndex]
		if column.pending != nil {
			observedDirection, ok := wheelObservedIncreasingDirection(*column.pending, *args.CurrentValue)
			if !ok {
				column.pending = nil
				column.allowProbe = true
				message := "wheel direction probe produced no measurable value change; do not declare a mapping or issue a larger nudge—use an adjacent visible-row tap or retry one micro nudge after a fresh screenshot"
				return invalidWheelResult(message, map[string]any{"column_x": representativeX, "retry_same_column": true}), false
			}
			if args.IncreasingDirection != observedDirection {
				message := fmt.Sprintf("wheel observed direction mismatch: the previous finger-%s nudge changed %d -> %d, which requires increasing_direction=\"%s\", got %q", column.pending.direction, column.pending.beforeValue, *args.CurrentValue, observedDirection, args.IncreasingDirection)
				return invalidWheelResult(message, map[string]any{
					"column_x": representativeX, "required_increasing_direction": observedDirection, "retry_same_column": true,
				}), false
			}
			column.direction = observedDirection
			column.pending = nil
		}
		if plan.probe && column.direction != "" && !column.allowProbe {
			message := fmt.Sprintf("wheel direction is already known for this column: provide increasing_direction=%q", column.direction)
			return invalidWheelResult(message, map[string]any{"column_x": representativeX, "required_increasing_direction": column.direction, "retry_same_column": true}), false
		}
		if column.direction != "" && args.IncreasingDirection != column.direction && !(plan.probe && column.allowProbe) {
			message := fmt.Sprintf("wheel direction mapping changed unexpectedly: this column was observed with increasing_direction=\"%s\", got %q", column.direction, args.IncreasingDirection)
			return invalidWheelResult(message, map[string]any{"column_x": representativeX, "required_increasing_direction": column.direction, "retry_same_column": true}), false
		}
	}

	if g.total >= wheelNudgeMaxTotal {
		return g.blockedResult(
			fmt.Sprintf(
				"wheel gesture safety stop: refusing wheel_nudge because this run already used %d/%d wheel nudges; report the last visible value and do not retry this wheel",
				g.total,
				wheelNudgeMaxTotal,
			),
			representativeX,
			columnUsed,
			columnLimit,
		), false
	}
	if columnUsed >= columnLimit {
		return g.blockedResult(
			fmt.Sprintf(
				"wheel gesture safety stop: refusing wheel_nudge because the column near x=%.0f already used %d/%d nudges (total %d/%d); report the last visible value and do not retry this column",
				representativeX,
				columnUsed,
				columnLimit,
				g.total,
				wheelNudgeMaxTotal,
			),
			representativeX,
			columnUsed,
			columnLimit,
		), false
	}
	g.pendingWheel = &wheelNudgeAttempt{
		input:       call.Input,
		args:        args,
		plan:        plan,
		columnIndex: columnIndex,
		columnX:     columnX,
		centerY:     centerY,
		rowSpacing:  rowSpacing,
		coordSpace:  coordSpace,
		columnLimit: columnLimit,
	}
	return ToolResult{}, true
}

func (g *wheelNudgeGuard) AfterToolCall(_ context.Context, call ToolCall, result ToolResult) ToolResult {
	if g == nil {
		return result
	}
	toolName := strings.ToLower(strings.TrimSpace(call.Spec.Name))
	switch toolName {
	case "wheel_nudge":
		attempt := g.pendingWheel
		g.pendingWheel = nil
		if attempt == nil || attempt.input != call.Input || !wheelActionExecuted(result) {
			return result
		}
		columnIndex := attempt.columnIndex
		if columnIndex < 0 {
			g.columns = append(g.columns, wheelNudgeColumnUsage{
				pickerID:   attempt.args.PickerID,
				centerX:    attempt.columnX,
				centerY:    attempt.centerY,
				rowSpacing: attempt.rowSpacing,
				coordSpace: attempt.coordSpace,
				limit:      attempt.columnLimit,
			})
			columnIndex = len(g.columns) - 1
		}
		column := &g.columns[columnIndex]
		column.centerX = attempt.columnX
		column.centerY = attempt.centerY
		column.rowSpacing = attempt.rowSpacing
		column.limit = attempt.columnLimit
		column.used++
		column.allowProbe = false
		g.total++
		if attempt.args.IncreasingDirection != "unknown" {
			column.direction = attempt.args.IncreasingDirection
		}
		if attempt.plan.rowOffset == 0 {
			column.pending = &wheelNudgeObservation{
				beforeValue: *attempt.args.CurrentValue,
				direction:   attempt.args.Direction,
				cycleSize:   *attempt.args.CycleSize,
				cycleStart:  *attempt.args.CycleStart,
			}
		} else {
			column.pending = nil
		}
	default:
		release := g.pendingNavigation != "" && g.pendingNavigation == wheelToolCallKey(call)
		g.pendingNavigation = ""
		if release && !result.IsError() && toolResultScreenChanged(result) {
			g.columns = nil
		}
	}
	return result
}

func planGapOrZero(args wheelNudgeArgs) int {
	if args.CurrentValue == nil || args.TargetValue == nil || args.CycleSize == nil {
		return 0
	}
	cycleStart := 0
	if args.CycleStart != nil {
		cycleStart = *args.CycleStart
	}
	gap, _ := wheelDomainDistance(*args.CurrentValue, *args.TargetValue, *args.CycleSize, cycleStart)
	return gap
}

func wheelNudgeLimitForGap(gap int) int {
	remaining := gap
	required := 0
	for remaining > 0 {
		remaining -= wheelNudgeRowsForGap(remaining)
		required++
	}
	return min(wheelNudgeMaxPerColumn, max(wheelNudgeMinPerColumn, required+wheelNudgeRetrySlack))
}

func wheelIncreasingDirectionFromVisibleStep(valueStep int) string {
	if valueStep > 0 {
		return "up"
	}
	return "down"
}

func wheelObservedIncreasingDirection(observation wheelNudgeObservation, afterValue int) (string, bool) {
	delta, ok := wheelSignedDelta(observation.beforeValue, afterValue, observation.cycleSize, observation.cycleStart)
	if !ok || delta == 0 {
		return "", false
	}
	if delta > 0 {
		return observation.direction, true
	}
	return oppositeWheelDirection(observation.direction), true
}

func wheelSignedDelta(from, to, cycleSize, cycleStart int) (int, bool) {
	if cycleSize == 0 {
		return to - from, true
	}
	cycleEnd := cycleStart + cycleSize
	if from < cycleStart || to < cycleStart || from >= cycleEnd || to >= cycleEnd {
		return 0, false
	}
	forward := (to - from + cycleSize) % cycleSize
	backward := (from - to + cycleSize) % cycleSize
	if forward == backward {
		return 0, false
	}
	if forward < backward {
		return forward, true
	}
	return -backward, true
}

func oppositeWheelDirection(direction string) string {
	if direction == "up" {
		return "down"
	}
	return "up"
}

func (g *wheelNudgeGuard) columnIndex(pickerID string, columnX, centerY float64, coordSpace string) int {
	closest := -1
	closestDistance := math.MaxFloat64
	for index, column := range g.columns {
		if column.pickerID != pickerID || column.coordSpace != coordSpace {
			continue
		}
		if math.Abs(column.centerY-centerY) > wheelNudgeCenterTolerance {
			continue
		}
		distance := math.Abs(column.centerX - columnX)
		if distance <= wheelNudgeColumnTolerance && distance < closestDistance {
			closest = index
			closestDistance = distance
		}
	}
	return closest
}

func wheelDistanceForGap(gap int) string {
	switch {
	case gap <= 1:
		return "micro"
	case gap <= 4:
		return "small"
	case gap <= 8:
		return "medium"
	default:
		return "large"
	}
}

func wheelSemanticTarget(args wheelNudgeArgs) (int, []string, bool) {
	if args.CurrentValue == nil || args.TargetValue == nil || args.CycleSize == nil || args.IncreasingDirection == "" || args.IncreasingDirection == "unknown" {
		return 0, nil, false
	}
	current := *args.CurrentValue
	target := *args.TargetValue
	cycle := *args.CycleSize
	cycleStart := 0
	if args.CycleStart != nil {
		cycleStart = *args.CycleStart
	}
	increase := args.IncreasingDirection
	decrease := "down"
	if increase == "down" {
		decrease = "up"
	}
	if cycle == 0 {
		if target >= current {
			return target - current, []string{increase}, true
		}
		return current - target, []string{decrease}, true
	}
	cycleEnd := cycleStart + cycle
	if current < cycleStart || target < cycleStart || current >= cycleEnd || target >= cycleEnd {
		return 0, nil, false
	}
	forward := (target - current + cycle) % cycle
	backward := (current - target + cycle) % cycle
	switch {
	case forward < backward:
		return forward, []string{increase}, true
	case backward < forward:
		return backward, []string{decrease}, true
	default:
		return forward, []string{increase, decrease}, true
	}
}

func wheelDomainDistance(from, target, cycleSize, cycleStart int) (int, bool) {
	if cycleSize == 0 {
		return int(math.Abs(float64(target - from))), true
	}
	cycleEnd := cycleStart + cycleSize
	if from < cycleStart || target < cycleStart || from >= cycleEnd || target >= cycleEnd {
		return 0, false
	}
	forward := (target - from + cycleSize) % cycleSize
	backward := (from - target + cycleSize) % cycleSize
	return min(forward, backward), true
}

func invalidWheelResult(message string, details map[string]any) ToolResult {
	return ToolResult{
		Output: message,
		Error:  NewToolErrorWithDetails(CodeInvalidArguments, message, details),
	}
}

func normalizedWheelCoordSpace(coordSpace string) string {
	coordSpace = strings.ToLower(strings.TrimSpace(coordSpace))
	if coordSpace == "" || coordSpace == coordinateSpaceAuto {
		return coordinateSpaceNormalized
	}
	return coordSpace
}

func (g *wheelNudgeGuard) guardCoordinates(coordSpace string, x, y, rowSpacing float64) (float64, float64, float64, string) {
	coordSpace = normalizedWheelCoordSpace(coordSpace)
	switch coordSpace {
	case coordinateSpaceNormalized:
		return clampFloat(x, 0, 1000), clampFloat(y, 0, 1000), rowSpacing, coordinateSpaceNormalized
	case coordinateSpaceAbsolute:
		return clampFloat(x, 0, absMouseMaxPos) / absMouseMaxPos * 1000,
			clampFloat(y, 0, absMouseMaxPos) / absMouseMaxPos * 1000,
			rowSpacing / absMouseMaxPos * 1000,
			coordinateSpaceNormalized
	case coordinateSpaceScreenshot, coordinateSpacePixel:
		if g != nil && g.screen != nil {
			_, _, active, age, ok := g.screen.ActiveAreaWithAge()
			if ok && age < screenDimensionsStaleAfter && active.Width > 1 && active.Height > 1 {
				return clampFloat(x, 0, float64(active.Width-1)) / float64(active.Width-1) * 1000,
					clampFloat(y, 0, float64(active.Height-1)) / float64(active.Height-1) * 1000,
					rowSpacing / float64(active.Height-1) * 1000,
					coordinateSpaceNormalized
			}
		}
	}
	return math.Max(0, x), math.Max(0, y), rowSpacing, coordSpace
}

func wheelCenterY(args wheelNudgeArgs) float64 {
	if args.CenterY != nil {
		return *args.CenterY
	}
	return wheelNudgeDefaultY
}

func (g *wheelNudgeGuard) beforeTouchGesture(call ToolCall) (ToolResult, bool) {
	if len(g.columns) == 0 {
		return ToolResult{}, true
	}
	var args struct {
		Type       string        `json:"type"`
		Point      *pointerPoint `json:"point"`
		Start      *pointerPoint `json:"start"`
		End        *pointerPoint `json:"end"`
		CoordSpace string        `json:"coord_space"`
		Anchor     *float64      `json:"anchor"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return ToolResult{}, true
	}
	gestureType := strings.ToLower(strings.TrimSpace(args.Type))
	var points []*pointerPoint
	switch gestureType {
	case "tap", "double_tap", "long_press":
		points = []*pointerPoint{args.Point}
	case "drag", "swipe":
		points = []*pointerPoint{args.Start, args.End}
	case "swipe_up", "swipe_down":
		anchor := 500.0
		if args.Anchor != nil {
			anchor = *args.Anchor
		}
		for _, column := range g.columns {
			x, _, _, pointSpace := g.guardCoordinates(coordinateSpaceNormalized, anchor, column.centerY, 0)
			if column.coordSpace == pointSpace && math.Abs(column.centerX-x) <= wheelNudgeColumnTolerance {
				message := "active picker column is owned by wheel_nudge: refusing a directional swipe anchored on that column"
				return invalidWheelResult(message, map[string]any{"column_x": column.centerX, "retry_same_column": true}), false
			}
		}
		return ToolResult{}, true
	case "swipe_left", "swipe_right":
		return ToolResult{}, true
	case "back", "home":
		g.pendingNavigation = wheelToolCallKey(call)
		return ToolResult{}, true
	default:
		return ToolResult{}, true
	}
	coordSpace := normalizedWheelCoordSpace(args.CoordSpace)
	navigationCandidate := false
	for _, point := range points {
		if point == nil {
			continue
		}
		for _, column := range g.columns {
			x, y, _, pointSpace := g.guardCoordinates(coordSpace, point.X.Float64(), point.Y.Float64(), 0)
			if pointSpace == coordinateSpaceNormalized && wheelPointInActionBar(y) {
				navigationCandidate = true
			}
			if column.coordSpace != pointSpace || !wheelPointInsideColumnSafetyZone(column, x, y) {
				continue
			}
			if column.used >= column.limit {
				message := fmt.Sprintf("wheel gesture safety stop: refusing touch_gesture %s near exhausted wheel column x=%.0f (%d/%d nudges used); do not bypass the wheel limit with generic touch input", gestureType, column.centerX, column.used, column.limit)
				return g.blockedResult(message, column.centerX, column.used, column.limit), false
			}
			message := fmt.Sprintf("active wheel column is owned by wheel_nudge: refusing touch_gesture %s near x=%.0f because generic taps or drags can activate fields outside the picker; continue with wheel_nudge using the latest visible value", gestureType, column.centerX)
			return invalidWheelResult(message, map[string]any{
				"column_x":          column.centerX,
				"column_used":       column.used,
				"retry_same_column": true,
			}), false
		}
	}
	if navigationCandidate {
		g.pendingNavigation = wheelToolCallKey(call)
	}
	return ToolResult{}, true
}

func (g *wheelNudgeGuard) beforeMouseClick(call ToolCall) (ToolResult, bool) {
	if len(g.columns) == 0 {
		return ToolResult{}, true
	}
	var args struct {
		X          pointerCoordinate `json:"x"`
		Y          pointerCoordinate `json:"y"`
		CoordSpace string            `json:"coord_space"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return ToolResult{}, true
	}
	coordSpace := normalizedWheelCoordSpace(args.CoordSpace)
	navigationCandidate := false
	for _, column := range g.columns {
		x, y, _, pointSpace := g.guardCoordinates(coordSpace, args.X.Float64(), args.Y.Float64(), 0)
		if pointSpace == coordinateSpaceNormalized && wheelPointInActionBar(y) {
			navigationCandidate = true
		}
		if column.coordSpace != pointSpace || !wheelPointInsideColumnSafetyZone(column, x, y) {
			continue
		}
		message := fmt.Sprintf("active wheel column is owned by wheel_nudge: refusing mouse_click near x=%.0f", column.centerX)
		return invalidWheelResult(message, map[string]any{"column_x": column.centerX, "retry_same_column": true}), false
	}
	if navigationCandidate {
		g.pendingNavigation = wheelToolCallKey(call)
	}
	return ToolResult{}, true
}

func wheelToolCallKey(call ToolCall) string {
	return strings.ToLower(strings.TrimSpace(call.Spec.Name)) + "\x00" + call.Input
}

func isWheelNavigationToolCall(call ToolCall) bool {
	toolName := strings.ToLower(strings.TrimSpace(call.Spec.Name))
	switch toolName {
	case toolBridgeOpenApp, "search_launch_app":
		return true
	case "quick_action":
		var args quickActionArgs
		trimmed := strings.TrimSpace(call.Input)
		if strings.HasPrefix(trimmed, "{") {
			if err := json.Unmarshal([]byte(trimmed), &args); err != nil || args.List {
				return false
			}
		} else {
			args.Action = trimmed
		}
		action := strings.ToLower(strings.TrimSpace(args.Action))
		switch action {
		case "back", "home", "hide_app", "quit_app", "app_switch", "spotlight_search":
			return true
		}
	}
	return false
}

func wheelPointInActionBar(y float64) bool {
	return y < wheelNudgeActionBarInset || y > 1000-wheelNudgeActionBarInset
}

func wheelPointInsideColumnSafetyZone(column wheelNudgeColumnUsage, x, y float64) bool {
	if math.Abs(column.centerX-x) > wheelNudgeColumnTolerance {
		return false
	}
	if column.coordSpace == coordinateSpaceNormalized && wheelPointInActionBar(y) {
		return false
	}
	halfHeight := max(wheelNudgeMinSafetyHeight, 6*column.rowSpacing)
	return math.Abs(column.centerY-y) <= halfHeight
}

func toolResultScreenChanged(result ToolResult) bool {
	var payload struct {
		ScreenChanged *bool `json:"screen_changed"`
	}
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil || payload.ScreenChanged == nil {
		return false
	}
	return *payload.ScreenChanged
}

func wheelActionExecuted(result ToolResult) bool {
	if !result.IsError() {
		return true
	}
	if result.Error == nil || result.Error.Details == nil {
		return false
	}
	completed, _ := result.Error.Details[postActionCompletedDetail].(bool)
	return completed
}

func (g *wheelNudgeGuard) blockedResult(message string, columnX float64, columnUsed, columnLimit int) ToolResult {
	toolErr := NewToolErrorWithDetails(CodeWheelGestureLimit, message, map[string]any{
		"total_used":        g.total,
		"total_limit":       wheelNudgeMaxTotal,
		"column_x":          columnX,
		"column_used":       columnUsed,
		"column_limit":      columnLimit,
		"retry_same_column": false,
	})
	return ToolResult{Output: message, Error: toolErr}
}
