package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

const (
	wheelNudgeMaxTotal        = 8
	wheelNudgeMaxPerColumn    = 4
	wheelNudgeCenterTolerance = 60.0
	// This tolerance is used in either normalized or screenshot-pixel space.
	// Keep it below the ~95px separation between the hour and minute columns
	// in the cropped physical-phone frame while still absorbing OCR jitter.
	wheelNudgeColumnTolerance = 60.0
)

// wheelNudgeGuard enforces wheel_nudge safety limits for one executor Run.
// The guard lives in roleLoopState, so a new user Run starts with fresh counts
// while planner and executor tool calls in the same Run share one budget.
type wheelNudgeGuard struct {
	total   int
	columns []wheelNudgeColumnUsage
}

type wheelNudgeColumnUsage struct {
	pickerID   string
	centerX    float64
	centerY    float64
	coordSpace string
	used       int
	direction  string
	pending    *wheelNudgeObservation
}

type wheelNudgeObservation struct {
	beforeValue int
	direction   string
	cycleSize   int
	cycleStart  int
}

func (g *wheelNudgeGuard) BeforeToolCall(_ context.Context, call ToolCall) (ToolResult, bool) {
	if g == nil {
		return ToolResult{}, true
	}
	toolName := strings.ToLower(strings.TrimSpace(call.Spec.Name))
	if toolName == "touch_gesture" {
		return g.beforeTouchGesture(call)
	}
	if toolName != "wheel_nudge" {
		return ToolResult{}, true
	}

	args, err := parseWheelNudgeArgs(call.Input)
	if err != nil {
		// WheelNudgeTool uses the same parser and will reject this call without
		// touching the device. Calls that cannot execute do not consume budget.
		return ToolResult{}, true
	}
	coordSpace := normalizedWheelCoordSpace(args.CoordSpace)
	columnX := *args.ColumnX
	if coordSpace == coordinateSpaceNormalized {
		columnX = clampFloat(columnX, 0, 1000)
	} else {
		columnX = math.Max(0, columnX)
	}
	centerY := wheelCenterY(args)
	columnIndex := g.columnIndex(args.PickerID, columnX, centerY, coordSpace)
	columnUsed := 0
	representativeX := columnX
	if columnIndex >= 0 {
		columnUsed = g.columns[columnIndex].used
		representativeX = g.columns[columnIndex].centerX
	}
	plan, planErr := planWheelNudge(args)
	if planErr != nil {
		return invalidWheelResult(planErr.Error(), map[string]any{"column_x": representativeX, "retry_same_column": true}), false
	}
	if columnIndex >= 0 {
		column := &g.columns[columnIndex]
		if column.pending != nil {
			observedDirection, ok := wheelObservedIncreasingDirection(*column.pending, *args.CurrentValue)
			if !ok {
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
		if plan.probe && column.direction != "" {
			message := fmt.Sprintf("wheel direction is already known for this column: provide increasing_direction=%q", column.direction)
			return invalidWheelResult(message, map[string]any{"column_x": representativeX, "required_increasing_direction": column.direction, "retry_same_column": true}), false
		}
		if column.direction != "" && args.IncreasingDirection != column.direction {
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
		), false
	}
	if columnUsed >= wheelNudgeMaxPerColumn {
		return g.blockedResult(
			fmt.Sprintf(
				"wheel gesture safety stop: refusing wheel_nudge because the column near x=%.0f already used %d/%d nudges (total %d/%d); report the last visible value and do not retry this column",
				representativeX,
				columnUsed,
				wheelNudgeMaxPerColumn,
				g.total,
				wheelNudgeMaxTotal,
			),
			representativeX,
			columnUsed,
		), false
	}

	if columnIndex < 0 {
		g.columns = append(g.columns, wheelNudgeColumnUsage{
			pickerID:   args.PickerID,
			centerX:    columnX,
			centerY:    centerY,
			coordSpace: coordSpace,
		})
		columnIndex = len(g.columns) - 1
	}
	g.columns[columnIndex].centerX = columnX
	g.columns[columnIndex].centerY = centerY
	g.total++
	g.columns[columnIndex].used++
	if args.IncreasingDirection != "unknown" {
		g.columns[columnIndex].direction = args.IncreasingDirection
	}
	if plan.rowOffset == 0 {
		g.columns[columnIndex].pending = &wheelNudgeObservation{
			beforeValue: *args.CurrentValue,
			direction:   args.Direction,
			cycleSize:   *args.CycleSize,
			cycleStart:  *args.CycleStart,
		}
	} else {
		g.columns[columnIndex].pending = nil
	}
	return ToolResult{}, true
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
	default:
		return ToolResult{}, true
	}
	coordSpace := normalizedWheelCoordSpace(args.CoordSpace)
	for _, point := range points {
		if point == nil {
			continue
		}
		x := float64(point.X)
		for _, column := range g.columns {
			if column.coordSpace != coordSpace || math.Abs(column.centerX-x) > wheelNudgeColumnTolerance {
				continue
			}
			if column.used >= wheelNudgeMaxPerColumn {
				message := fmt.Sprintf("wheel gesture safety stop: refusing touch_gesture %s near exhausted wheel column x=%.0f (%d/%d nudges used); do not bypass the wheel limit with generic touch input", gestureType, column.centerX, column.used, wheelNudgeMaxPerColumn)
				return g.blockedResult(message, column.centerX, column.used), false
			}
			message := fmt.Sprintf("active wheel column is owned by wheel_nudge: refusing touch_gesture %s near x=%.0f because generic taps or drags can activate fields outside the picker; continue with wheel_nudge using the latest visible value", gestureType, column.centerX)
			return invalidWheelResult(message, map[string]any{
				"column_x":          column.centerX,
				"column_used":       column.used,
				"retry_same_column": true,
			}), false
		}
	}
	return ToolResult{}, true
}

func (g *wheelNudgeGuard) blockedResult(message string, columnX float64, columnUsed int) ToolResult {
	toolErr := NewToolErrorWithDetails(CodeWheelGestureLimit, message, map[string]any{
		"total_used":        g.total,
		"total_limit":       wheelNudgeMaxTotal,
		"column_x":          columnX,
		"column_used":       columnUsed,
		"column_limit":      wheelNudgeMaxPerColumn,
		"retry_same_column": false,
	})
	return ToolResult{Output: message, Error: toolErr}
}
