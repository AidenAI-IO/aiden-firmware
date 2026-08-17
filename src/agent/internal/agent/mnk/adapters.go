package mnk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TouchGestureToolAdapter 使用 MNK Provider 的 touch_gesture 工具适配器
type TouchGestureToolAdapter struct {
	provider     Provider
	deviceTypeFn func() string
}

// NewTouchGestureToolAdapter 创建适配器
func NewTouchGestureToolAdapter(provider Provider, deviceTypeFn func() string) *TouchGestureToolAdapter {
	return &TouchGestureToolAdapter{
		provider:     provider,
		deviceTypeFn: deviceTypeFn,
	}
}

// Call 处理 touch_gesture 工具调用。
// 参数/配置类失败返回 *Error（InvalidArguments / ModuleUnavailable）；
// Provider 执行失败返回 ExecutionFailed。调用方应映射为 structured ToolError 并返回 (msg, nil)。
func (t *TouchGestureToolAdapter) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		Type       string        `json:"type"`
		Point      *pointerPoint `json:"point"`
		Start      *pointerPoint `json:"start"`
		End        *pointerPoint `json:"end"`
		Button     string        `json:"button"`
		DurationMs *int          `json:"duration_ms"`
		HoldMs     *int          `json:"hold_ms"`
		PauseMs    *int          `json:"pause_ms"`
		Distance   *float64      `json:"distance"`
		Anchor     *float64      `json:"anchor"`
		Strength   string        `json:"strength"`
	}

	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", InvalidArgumentsf("invalid input: %v. Common mistakes: missing quotes around string values, incorrect comma placement, point/start/end must be objects with named keys like {\"x\":500,\"y\":300} not bare values. Example: {\"type\":\"tap\",\"point\":{\"x\":500,\"y\":500}}", err)
	}

	gestureType := strings.ToLower(strings.TrimSpace(args.Type))
	if gestureType == "" {
		return "", InvalidArguments("type is required")
	}

	button := args.Button
	if button == "" {
		button = ButtonLeft
	}

	switch gestureType {
	case "tap":
		if args.Point == nil {
			return "", InvalidArguments("point is required for tap")
		}
		if err := t.requireProvider(); err != nil {
			return "", err
		}
		return t.handleTap(ctx, args.Point, button, 0)

	case "long_press":
		if args.Point == nil {
			return "", InvalidArguments("point is required for long_press")
		}
		holdMs := 500
		if args.HoldMs != nil && *args.HoldMs > 0 {
			holdMs = *args.HoldMs
		} else if args.DurationMs != nil && *args.DurationMs > 0 {
			holdMs = *args.DurationMs
		}
		if err := t.requireProvider(); err != nil {
			return "", err
		}
		return t.handleTap(ctx, args.Point, button, holdMs)

	case "double_tap":
		if args.Point == nil {
			return "", InvalidArguments("point is required for double_tap")
		}
		if err := t.requireProvider(); err != nil {
			return "", err
		}
		return t.handleDoubleTap(ctx, args.Point, button)

	case "swipe", "drag":
		if args.Start == nil || args.End == nil {
			return "", InvalidArgumentsf("start and end are required for %s", gestureType)
		}
		if err := t.requireProvider(); err != nil {
			return "", err
		}
		return t.handleSwipe(ctx, args.Start, args.End, button)

	case "swipe_left", "swipe_right", "swipe_up", "swipe_down":
		if err := t.requireProvider(); err != nil {
			return "", err
		}
		return t.handleDirectionalSwipe(ctx, gestureType, args.Distance, args.Anchor, args.Strength, button)

	case "back", "edge_back", "left_edge_back":
		if err := t.requireProvider(); err != nil {
			return "", err
		}
		return t.handleEdgeBack(ctx, button)

	case "home", "home_swipe", "bottom_edge_home":
		if err := t.requireProvider(); err != nil {
			return "", err
		}
		return t.handleEdgeHome(ctx, button)

	default:
		return "", InvalidArgumentsf("unsupported gesture type: %q", args.Type)
	}
}

func (t *TouchGestureToolAdapter) requireProvider() error {
	if t == nil || t.provider == nil {
		return ModuleUnavailable("touch_gesture is not configured")
	}
	return nil
}

func (t *TouchGestureToolAdapter) handleTap(ctx context.Context, point *pointerPoint, button string, holdMs int) (string, error) {
	if err := t.provider.Click(ctx, point.X.Float64(), point.Y.Float64(), button, holdMs); err != nil {
		return "", WrapExecutionFailed(err)
	}
	return "ok", nil
}

func (t *TouchGestureToolAdapter) handleDoubleTap(ctx context.Context, point *pointerPoint, button string) (string, error) {
	if err := t.provider.DoubleClick(ctx, point.X.Float64(), point.Y.Float64(), button); err != nil {
		return "", WrapExecutionFailed(err)
	}
	return "ok", nil
}

func (t *TouchGestureToolAdapter) handleSwipe(ctx context.Context, start, end *pointerPoint, button string) (string, error) {
	path := [][2]float64{
		{start.X.Float64(), start.Y.Float64()},
		{end.X.Float64(), end.Y.Float64()},
	}
	if err := t.provider.Drag(ctx, path, button); err != nil {
		return "", WrapExecutionFailed(err)
	}
	return "ok", nil
}

func (t *TouchGestureToolAdapter) handleDirectionalSwipe(ctx context.Context, gestureType string, distance, anchor *float64, strength, button string) (string, error) {
	travel := 700.0 // 默认距离
	if distance != nil && *distance > 0 {
		travel = *distance
	}

	switch strings.ToLower(strength) {
	case "large":
		travel = 700.0
	case "medium":
		travel = 500.0
	case "small":
		travel = 200.0
	case "tiny":
		travel = 40.0
	case "", "default":
		// keep travel from distance/default
	default:
		return "", InvalidArgumentsf("unsupported strength: %q", strength)
	}

	center := 500.0
	if anchor != nil {
		center = *anchor
	}

	half := travel / 2

	var path [][2]float64
	switch gestureType {
	case "swipe_left":
		path = [][2]float64{
			{center + half, center},
			{center - half, center},
		}
	case "swipe_right":
		path = [][2]float64{
			{center - half, center},
			{center + half, center},
		}
	case "swipe_up":
		path = [][2]float64{
			{center, center + half},
			{center, center - half},
		}
	case "swipe_down":
		path = [][2]float64{
			{center, center - half},
			{center, center + half},
		}
	}

	if err := t.provider.Drag(ctx, path, button); err != nil {
		return "", WrapExecutionFailed(err)
	}
	return "ok", nil
}

func (t *TouchGestureToolAdapter) handleEdgeBack(ctx context.Context, button string) (string, error) {
	path := [][2]float64{
		{1, 500},
		{750, 500},
	}
	if err := t.provider.Drag(ctx, path, button); err != nil {
		return "", WrapExecutionFailed(err)
	}
	return "ok", nil
}

func (t *TouchGestureToolAdapter) handleEdgeHome(ctx context.Context, button string) (string, error) {
	path := [][2]float64{
		{500, 999},
		{500, 180},
	}
	if err := t.provider.Drag(ctx, path, button); err != nil {
		return "", WrapExecutionFailed(err)
	}
	return "ok", nil
}

// KeyboardTapToolAdapter 使用 MNK Provider 的 keyboard_tap 工具适配器
type KeyboardTapToolAdapter struct {
	provider Provider
}

// NewKeyboardTapToolAdapter 创建适配器
func NewKeyboardTapToolAdapter(provider Provider) *KeyboardTapToolAdapter {
	return &KeyboardTapToolAdapter{
		provider: provider,
	}
}

// Call 处理 keyboard_tap 工具调用。
func (t *KeyboardTapToolAdapter) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		Keys []string `json:"keys"`
	}

	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", InvalidArgumentsf("invalid input: %v. Expected JSON format: {\"keys\": [\"enter\"]}. Common mistakes: missing quotes around key names, incorrect comma placement in array", err)
	}

	if len(args.Keys) == 0 {
		return "", InvalidArguments("keys array is required")
	}

	// Validate/expand keys before the nil-provider check so unsupported KEYCODE
	// aliases and mixed chords still return InvalidArguments without a backend.
	if _, err := ResolveKeypressKeys(args.Keys, ""); err != nil {
		return "", err
	}

	if t == nil || t.provider == nil {
		return "", ModuleUnavailable("keyboard_tap is not configured")
	}

	if err := t.provider.Keypress(ctx, args.Keys); err != nil {
		return "", WrapExecutionFailed(err)
	}

	return "ok", nil
}

// MouseMoveToolAdapter 使用 MNK Provider 的 mouse_move 工具适配器
type MouseMoveToolAdapter struct {
	provider Provider
}

// NewMouseMoveToolAdapter 创建适配器
func NewMouseMoveToolAdapter(provider Provider) *MouseMoveToolAdapter {
	return &MouseMoveToolAdapter{
		provider: provider,
	}
}

// Call 处理 mouse_move 工具调用。
func (t *MouseMoveToolAdapter) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		X pointerCoordinate `json:"x"`
		Y pointerCoordinate `json:"y"`
	}

	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", InvalidArgumentsf("invalid input: %v. Expected JSON format: {\"x\": 500, \"y\": 300}. Coordinates always use the normalized 0-1000 scale", err)
	}

	if t == nil || t.provider == nil {
		return "", ModuleUnavailable("mouse_move is not configured")
	}

	if err := t.provider.Move(ctx, args.X.Float64(), args.Y.Float64()); err != nil {
		return "", WrapExecutionFailed(err)
	}

	return "ok", nil
}

// MouseScrollToolAdapter 使用 MNK Provider 的 mouse_scroll 工具适配器
type MouseScrollToolAdapter struct {
	provider Provider
}

// NewMouseScrollToolAdapter 创建适配器
func NewMouseScrollToolAdapter(provider Provider) *MouseScrollToolAdapter {
	return &MouseScrollToolAdapter{
		provider: provider,
	}
}

// Call 处理 mouse_scroll 工具调用。
func (t *MouseScrollToolAdapter) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		Delta int `json:"delta"`
	}

	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", InvalidArgumentsf("invalid input: %v. Expected JSON format: {\"delta\": -3}. Delta must be a number between -127 and 127", err)
	}
	if args.Delta == 0 {
		return "ok", nil
	}
	if args.Delta < -127 || args.Delta > 127 {
		return "", InvalidArguments("delta must be between -127 and 127")
	}

	if t == nil || t.provider == nil {
		return "", ModuleUnavailable("mouse_scroll is not configured")
	}

	if err := t.provider.Scroll(ctx, 0, args.Delta); err != nil {
		return "", WrapExecutionFailed(err)
	}

	return "ok", nil
}

// pointerPoint 和 pointerCoordinate 辅助类型（从 tools_hid.go 复制）
type pointerPoint struct {
	X pointerCoordinate `json:"x"`
	Y pointerCoordinate `json:"y"`
}

type pointerCoordinate float64

func (c pointerCoordinate) Float64() float64 {
	return float64(c)
}

func (c *pointerCoordinate) UnmarshalJSON(data []byte) error {
	var number float64
	if err := json.Unmarshal(data, &number); err == nil {
		*c = pointerCoordinate(number)
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		value, parseErr := parseFloat(strings.TrimSpace(text))
		if parseErr != nil {
			return fmt.Errorf("parse coordinate %q: %w", text, parseErr)
		}
		*c = pointerCoordinate(value)
		return nil
	}

	return fmt.Errorf("coordinate must be a number or numeric string")
}

func parseFloat(s string) (float64, error) {
	var value float64
	_, err := fmt.Sscanf(s, "%f", &value)
	return value, err
}
