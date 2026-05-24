package agent

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	absMouseMaxPos = 32767

	// defaultTapHoldMs is the dwell between a touch press and release so iOS
	// registers a tap rather than dropping the sub-millisecond event or
	// treating it as a long-press.
	defaultTapHoldMs = 60

	// defaultSwipeHoldBeforeMs is the dwell at the start of a swipe so iOS
	// recognises the touchdown frame before the touch begins moving. The
	// edge-back recogniser in particular rejects swipes that move on the same
	// frame as the press.
	defaultSwipeHoldBeforeMs = 80

	// defaultSwipeHoldAfterMs keeps the touch down briefly at the destination
	// before releasing. Releasing immediately after the final move leaves a
	// high terminal velocity, which mobile UIs often interpret as inertial
	// scrolling instead of a precise drag-like swipe.
	defaultSwipeHoldAfterMs = 300

	defaultSwipeDurationMs = 700
	defaultSwipeSteps      = 24

	phoneBackStartX = 0.001
	phoneBackEndX   = 0.75
	phoneBackY      = 0.50

	phoneHomeX      = 0.50
	phoneHomeStartY = 0.999
	phoneHomeEndY   = 0.18

	// defaultCursorSettleMs is the dwell between positioning the HID absolute
	// cursor and pressing a button at that position. iOS HID cursor mode
	// smoothly animates the cursor toward the target; if the press lands while
	// the cursor is still in flight the input is interpreted as a drag from
	// the previous cursor position rather than a tap, which triggers
	// long-press menus or off-target activations.
	defaultCursorSettleMs = 80

	// screenDimensionsStaleAfter bounds how long cached screenshot dimensions
	// are trusted for pixel-coordinate resolution. Past this age the cache may
	// not match the current HDMI capture resolution.
	screenDimensionsStaleAfter = 30 * time.Second
)

var hidKeyboardMap = map[string]uint8{
	"a": 0x04, "b": 0x05, "c": 0x06, "d": 0x07,
	"e": 0x08, "f": 0x09, "g": 0x0a, "h": 0x0b,
	"i": 0x0c, "j": 0x0d, "k": 0x0e, "l": 0x0f,
	"m": 0x10, "n": 0x11, "o": 0x12, "p": 0x13,
	"q": 0x14, "r": 0x15, "s": 0x16, "t": 0x17,
	"u": 0x18, "v": 0x19, "w": 0x1a, "x": 0x1b,
	"y": 0x1c, "z": 0x1d,
	"1": 0x1e, "2": 0x1f, "3": 0x20, "4": 0x21,
	"5": 0x22, "6": 0x23, "7": 0x24, "8": 0x25,
	"9": 0x26, "0": 0x27,
	"enter": 0x28, "escape": 0x29, "backspace": 0x2a,
	"tab": 0x2b, "space": 0x2c, "minus": 0x2d,
	"equal": 0x2e, "leftbrace": 0x2f, "rightbrace": 0x30,
	"backslash": 0x31, "semicolon": 0x33, "apostrophe": 0x34,
	"grave": 0x35, "comma": 0x36, "dot": 0x37, "slash": 0x38,
	"capslock": 0x39,
	"f1":       0x3a, "f2": 0x3b, "f3": 0x3c, "f4": 0x3d,
	"f5": 0x3e, "f6": 0x3f, "f7": 0x40, "f8": 0x41,
	"f9": 0x42, "f10": 0x43, "f11": 0x44, "f12": 0x45,
	"printscreen": 0x46, "scrolllock": 0x47, "pause": 0x48,
	"insert": 0x49, "home": 0x4a, "pageup": 0x4b,
	"delete": 0x4c, "end": 0x4d, "pagedown": 0x4e,
	"right": 0x4f, "left": 0x50, "down": 0x51, "up": 0x52,
}

var hidModifierMap = map[string]uint8{
	"ctrl": 0x01, "lctrl": 0x01, "rctrl": 0x10,
	"shift": 0x02, "lshift": 0x02, "rshift": 0x20,
	"alt": 0x04, "lalt": 0x04, "ralt": 0x40,
	"meta": 0x08, "lmeta": 0x08, "rmeta": 0x80,
	"super": 0x08, "win": 0x08, "cmd": 0x08,
}

// HIDDevice manages a single HID device file with lazy open and auto-reopen.
type HIDDevice struct {
	path string
	mu   sync.Mutex
	file io.WriteCloser
	open func(string) (io.WriteCloser, error)
}

type screenState struct {
	mu        sync.RWMutex
	width     int
	height    int
	updatedAt time.Time
}

type pointerState struct {
	mu    sync.Mutex
	x     int
	y     int
	valid bool
}

func (s *screenState) Update(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.width = width
	s.height = height
	s.updatedAt = time.Now()
}

func (s *screenState) Dimensions() (width, height int, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.width <= 0 || s.height <= 0 {
		return 0, 0, false
	}
	return s.width, s.height, true
}

func (s *screenState) DimensionsWithAge() (width, height int, age time.Duration, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.width <= 0 || s.height <= 0 {
		return 0, 0, 0, false
	}
	if s.updatedAt.IsZero() {
		return s.width, s.height, 0, true
	}
	return s.width, s.height, time.Since(s.updatedAt), true
}

func (s *pointerState) Update(x, y int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.x = x
	s.y = y
	s.valid = true
}

func (s *pointerState) Current() (x, y int, ok bool) {
	if s == nil {
		return 0, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.valid {
		return 0, 0, false
	}
	return s.x, s.y, true
}

func NewHIDDevice(path string) *HIDDevice {
	return &HIDDevice{
		path: path,
		open: func(path string) (io.WriteCloser, error) {
			return os.OpenFile(path, os.O_WRONLY, 0)
		},
	}
}

func (d *HIDDevice) Write(data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writeLocked(data, nil)
}

func (d *HIDDevice) writeLocked(data []byte, after func()) error {
	if err := d.ensureOpenLocked(); err != nil {
		return err
	}

	n, err := d.file.Write(data)
	if err != nil {
		d.closeLocked()
		if n == 0 && hidShouldRetryWrite(err) {
			if reopenErr := d.ensureOpenLocked(); reopenErr == nil {
				n2, retryErr := d.file.Write(data)
				if retryErr == nil && n2 == len(data) {
					if after != nil {
						after()
					}
					return nil
				}
				d.closeLocked()
				if retryErr != nil {
					return fmt.Errorf("write %s: %w", d.path, retryErr)
				}
				return fmt.Errorf("write %s: short write after retry (%d/%d bytes)", d.path, n2, len(data))
			}
		}
		return fmt.Errorf("write %s: %w", d.path, err)
	}
	if n != len(data) {
		d.closeLocked()
		return fmt.Errorf("write %s: short write (%d/%d bytes)", d.path, n, len(data))
	}
	if after != nil {
		after()
	}
	return nil
}

func (d *HIDDevice) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closeLocked()
}

func (d *HIDDevice) ensureOpenLocked() error {
	if d.file != nil {
		return nil
	}
	opener := d.open
	if opener == nil {
		opener = func(path string) (io.WriteCloser, error) {
			return os.OpenFile(path, os.O_WRONLY, 0)
		}
	}
	f, err := opener(d.path)
	if err != nil {
		return fmt.Errorf("open %s: %w", d.path, err)
	}
	d.file = f
	return nil
}

func (d *HIDDevice) closeLocked() {
	if d.file != nil {
		_ = d.file.Close()
		d.file = nil
	}
}

func hidShouldRetryWrite(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ESHUTDOWN) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ENODEV) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "transport endpoint shutdown") ||
		strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "connection reset")
}

// KeyboardTapTool sends a key press then release via HID.
type KeyboardTapTool struct {
	dev *HIDDevice
}

func (t *KeyboardTapTool) Name() string { return "keyboard_tap" }

func (t *KeyboardTapTool) Description() string {
	return `Press and release keyboard keys. Input JSON: {"keys": ["ctrl", "c"]}. ` +
		`Supports: a-z, 0-9, f1-f12, enter, escape, backspace, tab, space, delete, ` +
		`up, down, left, right, home, end, pageup, pagedown, insert, printscreen. ` +
		`Modifiers: ctrl, shift, alt, meta/super/win/cmd. ` +
		`Multiple keys are pressed simultaneously (e.g. ctrl+c).`
}

func (t *KeyboardTapTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v", err), nil
	}
	if len(args.Keys) == 0 {
		return "error: keys array is required", nil
	}

	var modifier uint8
	var keys []uint8
	for _, k := range args.Keys {
		k = strings.ToLower(strings.TrimSpace(k))
		if mod, ok := hidModifierMap[k]; ok {
			modifier |= mod
		} else if code, ok := hidKeyboardMap[k]; ok {
			keys = append(keys, code)
		} else {
			return fmt.Sprintf("error: unknown key: %q", k), nil
		}
	}

	// HID keyboard report: [modifier, reserved, key1..key6]
	report := make([]byte, 8)
	report[0] = modifier
	for i := 0; i < len(keys) && i < 6; i++ {
		report[2+i] = keys[i]
	}

	// Press
	if err := t.dev.Write(report); err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	// Release
	if err := t.dev.Write(make([]byte, 8)); err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}

	return "ok", nil
}

// KeyboardTextTool types a string character by character via HID.
type KeyboardTextTool struct {
	dev *HIDDevice
}

func (t *KeyboardTextTool) Name() string { return "keyboard_text" }

func (t *KeyboardTextTool) Description() string {
	return `Type a string of text character by character. Input JSON: {"text": "hello world"}. ` +
		`Supports ASCII printable characters. Each character is pressed and released sequentially.`
}

func (t *KeyboardTextTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v", err), nil
	}
	if args.Text == "" {
		return "error: text is required", nil
	}

	skipped := make([]rune, 0)
	for _, ch := range args.Text {
		if ch > 0x7F {
			skipped = append(skipped, ch)
			continue
		}

		modifier, code, ok := charToHIDKey(byte(ch))
		if !ok {
			skipped = append(skipped, ch)
			continue
		}
		report := make([]byte, 8)
		report[0] = modifier
		report[2] = code
		if err := t.dev.Write(report); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := t.dev.Write(make([]byte, 8)); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
	}

	if len(skipped) > 0 {
		return fmt.Sprintf("ok; skipped unsupported characters: %q", string(skipped)), nil
	}
	return "ok", nil
}

// MouseClickTool moves the mouse to absolute coordinates and clicks.
type MouseClickTool struct {
	dev    *HIDDevice
	screen *screenState
	state  *pointerState
}

func (t *MouseClickTool) Name() string { return "mouse_click" }

func (t *MouseClickTool) Description() string {
	return `Move mouse to a position and click. Input JSON: {"x": 500, "y": 300, "button": "left", "coord_space": "pixel"}. ` +
		`coord_space options: "pixel", "normalized", "absolute". Default is "auto": pixel coordinates when a recent screenshot has cached screen dimensions, otherwise HID absolute values in the range 0-32767. ` +
		`Cached screen dimensions are considered stale after 30s; take a screenshot before using pixel coordinates. ` +
		`Recommended for stability on mobile screens whose capture resolution can change: pass coord_space:"normalized" with x/y in [0,1]. ` +
		`The cursor is positioned at the target and allowed to settle before the press, then the press and release are separated by a brief hold, so iOS HID-cursor mode registers a tap rather than a drag or long-press. ` +
		`Button options: "left" (default), "right", "middle".`
}

func (t *MouseClickTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		X          float64 `json:"x"`
		Y          float64 `json:"y"`
		Button     string  `json:"button"`
		CoordSpace string  `json:"coord_space"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v", err), nil
	}

	absX, absY, err := resolvePointerPosition(t.screen, args.X, args.Y, args.CoordSpace, coordinateSpaceAuto)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	btn := mouseButtonByte(args.Button)

	if err := tapPointer(t.dev, t.state, absX, absY, btn); err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}

	return "ok", nil
}

// MouseMoveTool moves the mouse to absolute coordinates without clicking.
type MouseMoveTool struct {
	dev    *HIDDevice
	screen *screenState
	state  *pointerState
}

func (t *MouseMoveTool) Name() string { return "mouse_move" }

func (t *MouseMoveTool) Description() string {
	return `Move mouse to a position without clicking. Input JSON: {"x": 500, "y": 300, "coord_space": "pixel"}. ` +
		`coord_space options: "pixel", "normalized", "absolute". Default is "auto": pixel coordinates when a recent screenshot has cached screen dimensions, otherwise HID absolute values in the range 0-32767. ` +
		`Cached screen dimensions are considered stale after 30s; take a screenshot before using pixel coordinates.`
}

func (t *MouseMoveTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		X          float64 `json:"x"`
		Y          float64 `json:"y"`
		CoordSpace string  `json:"coord_space"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v", err), nil
	}

	absX, absY, err := resolvePointerPosition(t.screen, args.X, args.Y, args.CoordSpace, coordinateSpaceAuto)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}

	if err := movePointer(t.dev, t.state, absX, absY, 0); err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}

	return "ok", nil
}

// TouchGestureTool executes touch-like pointer gestures for mobile UI control.
type TouchGestureTool struct {
	dev    *HIDDevice
	screen *screenState
	state  *pointerState
}

func (t *TouchGestureTool) Name() string { return "touch_gesture" }

func (t *TouchGestureTool) Description() string {
	return `Perform a touch-like gesture using the absolute mouse HID device. ` +
		`Input JSON examples: {"type":"tap","point":{"x":0.5,"y":0.5}}, {"type":"swipe","start":{"x":0.001,"y":0.5},"end":{"x":0.75,"y":0.5},"duration_ms":700,"steps":24}, {"type":"back"}, {"type":"home"}. ` +
		`Supported types: "tap", "double_tap", "long_press", "drag", "swipe", "back" (left-edge back), "home" (bottom-edge home). ` +
		`coord_space defaults to "normalized" (x/y in [0,1]) and also supports "pixel" and "absolute". ` +
		`Every gesture first positions the cursor at the target and waits for iOS HID-cursor smoothing to settle before pressing, so clicks register on the intended element instead of as drags from the previous cursor position. ` +
		`Tap and double_tap accept an optional "hold_ms" (dwell between press and release, default 60ms). ` +
		`Swipe defaults to a slower 700ms / 24-step motion, applies "hold_before_ms" of 80ms after the press, and holds at the destination for "hold_after_ms" 300ms before release to reduce inertial scrolling. ` +
		`For phone edge gestures, do not use conservative inset coordinates such as 0.05-0.10: "back" starts at normalized x=0.001 and "home" starts at normalized y=0.999. Drag keeps the previous 250ms / 12-step motion with 0ms hold defaults to avoid unintended long-press behaviour during slow content drag.`
}

func (t *TouchGestureTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		Type         string        `json:"type"`
		Point        *pointerPoint `json:"point"`
		Start        *pointerPoint `json:"start"`
		End          *pointerPoint `json:"end"`
		CoordSpace   string        `json:"coord_space"`
		Button       string        `json:"button"`
		DurationMs   *int          `json:"duration_ms"`
		HoldBeforeMs *int          `json:"hold_before_ms"`
		HoldAfterMs  *int          `json:"hold_after_ms"`
		HoldMs       *int          `json:"hold_ms"`
		PauseMs      *int          `json:"pause_ms"`
		Steps        *int          `json:"steps"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v", err), nil
	}

	gestureType := strings.ToLower(strings.TrimSpace(args.Type))
	if gestureType == "" {
		return "error: type is required", nil
	}

	coordSpace := strings.TrimSpace(args.CoordSpace)
	if coordSpace == "" {
		coordSpace = coordinateSpaceNormalized
	}
	button := mouseButtonByte(args.Button)

	switch gestureType {
	case "tap":
		point, err := resolveRequiredPoint(t.screen, args.Point, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := tapPointerWithHold(t.dev, t.state, point.x, point.y, button, intOrDefault(args.HoldMs, defaultTapHoldMs)); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
	case "double_tap":
		point, err := resolveRequiredPoint(t.screen, args.Point, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		holdMs := intOrDefault(args.HoldMs, defaultTapHoldMs)
		if err := tapPointerWithHold(t.dev, t.state, point.x, point.y, button, holdMs); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		sleepMs(intOrDefault(args.PauseMs, 100))
		if err := tapPointerWithHold(t.dev, t.state, point.x, point.y, button, holdMs); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
	case "long_press":
		point, err := resolveRequiredPoint(t.screen, args.Point, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := settlePointer(t.dev, t.state, point.x, point.y); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := pressPointer(t.dev, t.state, point.x, point.y, button); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		sleepMs(intOrDefault(args.DurationMs, 500))
		if err := releasePointer(t.dev, t.state, point.x, point.y); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
	case "drag", "swipe":
		start, err := resolveRequiredPoint(t.screen, args.Start, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		end, err := resolveRequiredPoint(t.screen, args.End, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		defaultDuration := 250
		defaultSteps := 12
		defaultHoldBefore := 0
		defaultHoldAfter := 0
		if gestureType == "swipe" {
			defaultDuration = defaultSwipeDurationMs
			defaultSteps = defaultSwipeSteps
			defaultHoldBefore = defaultSwipeHoldBeforeMs
			defaultHoldAfter = defaultSwipeHoldAfterMs
		}
		if err := dragPointer(
			t.dev,
			t.state,
			start,
			end,
			button,
			intOrDefault(args.DurationMs, defaultDuration),
			intOrDefault(args.HoldBeforeMs, defaultHoldBefore),
			intOrDefault(args.HoldAfterMs, defaultHoldAfter),
			positiveIntOrDefault(args.Steps, defaultSteps),
		); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
	case "back", "edge_back", "left_edge_back":
		start, err := resolvePointOrDefaultNormalized(t.screen, args.Start, coordSpace, phoneBackStartX, phoneBackY)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		end, err := resolvePointOrDefaultNormalized(t.screen, args.End, coordSpace, phoneBackEndX, phoneBackY)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := dragPointer(
			t.dev,
			t.state,
			start,
			end,
			button,
			intOrDefault(args.DurationMs, defaultSwipeDurationMs),
			intOrDefault(args.HoldBeforeMs, defaultSwipeHoldBeforeMs),
			intOrDefault(args.HoldAfterMs, defaultSwipeHoldAfterMs),
			positiveIntOrDefault(args.Steps, defaultSwipeSteps),
		); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
	case "home", "home_swipe", "bottom_edge_home":
		start, err := resolvePointOrDefaultNormalized(t.screen, args.Start, coordSpace, phoneHomeX, phoneHomeStartY)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		end, err := resolvePointOrDefaultNormalized(t.screen, args.End, coordSpace, phoneHomeX, phoneHomeEndY)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := dragPointer(
			t.dev,
			t.state,
			start,
			end,
			button,
			intOrDefault(args.DurationMs, defaultSwipeDurationMs),
			intOrDefault(args.HoldBeforeMs, defaultSwipeHoldBeforeMs),
			intOrDefault(args.HoldAfterMs, defaultSwipeHoldAfterMs),
			positiveIntOrDefault(args.Steps, defaultSwipeSteps),
		); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
	default:
		return fmt.Sprintf("error: unsupported gesture type: %q", args.Type), nil
	}

	return "ok", nil
}

// MouseScrollTool sends mouse wheel events.
type MouseScrollTool struct {
	dev   *HIDDevice
	state *pointerState
}

func (t *MouseScrollTool) Name() string { return "mouse_scroll" }

func (t *MouseScrollTool) Description() string {
	return `Scroll the mouse wheel. Input JSON: {"delta": -3}. ` +
		`Positive values scroll up, negative scroll down. Range: -127 to 127. This is a wheel event and is not equivalent to a mobile swipe gesture.`
}

func (t *MouseScrollTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		Delta int `json:"delta"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v", err), nil
	}
	if args.Delta == 0 {
		return "ok", nil
	}
	if args.Delta < -127 {
		args.Delta = -127
	} else if args.Delta > 127 {
		args.Delta = 127
	}

	if err := scrollPointer(t.dev, t.state, args.Delta); err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}

	return "ok", nil
}

// writeAbsMouseReport writes an absolute mouse report that matches the
// configured hid.usb1 descriptor: [buttons, x_low, x_high, y_low, y_high, wheel].
func writeAbsMouseReport(dev *HIDDevice, state *pointerState, x, y int, buttons uint8, wheel int8) error {
	absX := clampUint16(x, absMouseMaxPos)
	absY := clampUint16(y, absMouseMaxPos)

	report := make([]byte, 6)
	report[0] = buttons
	binary.LittleEndian.PutUint16(report[1:3], absX)
	binary.LittleEndian.PutUint16(report[3:5], absY)
	report[5] = byte(wheel)

	var after func()
	if state != nil {
		after = func() {
			state.Update(int(absX), int(absY))
		}
	}

	dev.mu.Lock()
	defer dev.mu.Unlock()
	return dev.writeLocked(report, after)
}

type pointerPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type resolvedPointerPoint struct {
	x int
	y int
}

const (
	coordinateSpaceAuto       = "auto"
	coordinateSpacePixel      = "pixel"
	coordinateSpaceNormalized = "normalized"
	coordinateSpaceAbsolute   = "absolute"
)

func resolveRequiredPoint(screen *screenState, point *pointerPoint, coordSpace string) (resolvedPointerPoint, error) {
	if point == nil {
		return resolvedPointerPoint{}, fmt.Errorf("point is required")
	}

	x, y, err := resolvePointerPosition(screen, point.X, point.Y, coordSpace, coordinateSpaceNormalized)
	if err != nil {
		return resolvedPointerPoint{}, err
	}
	return resolvedPointerPoint{x: x, y: y}, nil
}

func resolvePointOrDefaultNormalized(screen *screenState, point *pointerPoint, coordSpace string, defaultX, defaultY float64) (resolvedPointerPoint, error) {
	if point != nil {
		return resolveRequiredPoint(screen, point, coordSpace)
	}

	if _, err := normalizeCoordinateSpace(coordSpace, coordinateSpaceNormalized); err != nil {
		return resolvedPointerPoint{}, err
	}

	x, y := normalizedToAbsolutePoint(defaultX, defaultY)
	return resolvedPointerPoint{x: x, y: y}, nil
}

func resolvePointerPosition(screen *screenState, x, y float64, coordSpace string, defaultSpace string) (int, int, error) {
	space, err := normalizeCoordinateSpace(coordSpace, defaultSpace)
	if err != nil {
		return 0, 0, err
	}

	switch space {
	case coordinateSpaceAuto:
		if screen != nil {
			if width, height, age, ok := screen.DimensionsWithAge(); ok && age < screenDimensionsStaleAfter {
				return pixelToAbsolutePoint(x, y, width, height)
			}
		}
		return int(clampFloat(math.Round(x), 0, absMouseMaxPos)), int(clampFloat(math.Round(y), 0, absMouseMaxPos)), nil
	case coordinateSpacePixel:
		if screen == nil {
			return 0, 0, fmt.Errorf("pixel coordinates require known screen dimensions; call screenshot first or use coord_space normalized/absolute")
		}
		width, height, age, ok := screen.DimensionsWithAge()
		if !ok {
			return 0, 0, fmt.Errorf("pixel coordinates require known screen dimensions; call screenshot first or use coord_space normalized/absolute")
		}
		if age >= screenDimensionsStaleAfter {
			return 0, 0, fmt.Errorf("cached screen dimensions are %.0fs old; call screenshot to refresh before using pixel coordinates", age.Seconds())
		}
		return pixelToAbsolutePoint(x, y, width, height)
	case coordinateSpaceNormalized:
		absX, absY := normalizedToAbsolutePoint(x, y)
		return absX, absY, nil
	case coordinateSpaceAbsolute:
		return int(clampFloat(math.Round(x), 0, absMouseMaxPos)), int(clampFloat(math.Round(y), 0, absMouseMaxPos)), nil
	}

	return 0, 0, fmt.Errorf("unsupported coord_space: %q", coordSpace)
}

func normalizeCoordinateSpace(coordSpace string, defaultSpace string) (string, error) {
	space := strings.ToLower(strings.TrimSpace(coordSpace))
	if space == "" {
		space = defaultSpace
	}

	switch space {
	case coordinateSpaceAuto, coordinateSpacePixel, coordinateSpaceNormalized, coordinateSpaceAbsolute:
		return space, nil
	default:
		return "", fmt.Errorf("unsupported coord_space: %q", coordSpace)
	}
}

func normalizedToAbsolutePoint(x, y float64) (int, int) {
	return int(math.Round(clampFloat(x, 0, 1) * absMouseMaxPos)), int(math.Round(clampFloat(y, 0, 1) * absMouseMaxPos))
}

func pixelToAbsolutePoint(x, y float64, width, height int) (int, int, error) {
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("invalid screen dimensions: %dx%d", width, height)
	}
	return scalePixelToAbsolute(x, width), scalePixelToAbsolute(y, height), nil
}

func scalePixelToAbsolute(value float64, size int) int {
	if size <= 1 {
		return 0
	}
	maxPixel := float64(size - 1)
	clamped := clampFloat(value, 0, maxPixel)
	return int(math.Round((clamped / maxPixel) * absMouseMaxPos))
}

func clampFloat(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func tapPointer(dev *HIDDevice, state *pointerState, x, y int, button uint8) error {
	return tapPointerWithHold(dev, state, x, y, button, defaultTapHoldMs)
}

func tapPointerWithHold(dev *HIDDevice, state *pointerState, x, y int, button uint8, holdMs int) error {
	if err := settlePointer(dev, state, x, y); err != nil {
		return err
	}
	if err := pressPointer(dev, state, x, y, button); err != nil {
		return err
	}
	sleepMs(holdMs)
	return releasePointer(dev, state, x, y)
}

// settlePointer moves the absolute mouse cursor to (x, y) with no button
// pressed and pauses for iOS HID cursor smoothing to settle. A subsequent
// press at the same coordinates then registers as a tap on (x, y) instead of
// a drag from wherever the cursor was last.
func settlePointer(dev *HIDDevice, state *pointerState, x, y int) error {
	if err := movePointer(dev, state, x, y, 0); err != nil {
		return err
	}
	sleepMs(defaultCursorSettleMs)
	return nil
}

func pressPointer(dev *HIDDevice, state *pointerState, x, y int, button uint8) error {
	return movePointer(dev, state, x, y, button)
}

func releasePointer(dev *HIDDevice, state *pointerState, x, y int) error {
	return movePointer(dev, state, x, y, 0)
}

func movePointer(dev *HIDDevice, state *pointerState, x, y int, buttons uint8) error {
	return writeAbsMouseReport(dev, state, x, y, buttons, 0)
}

func scrollPointer(dev *HIDDevice, state *pointerState, delta int) error {
	x := absMouseMaxPos / 2
	y := absMouseMaxPos / 2
	if state != nil {
		if currentX, currentY, ok := state.Current(); ok {
			x = currentX
			y = currentY
		}
	}
	return writeAbsMouseReport(dev, state, x, y, 0, int8(delta))
}

func dragPointer(dev *HIDDevice, state *pointerState, start, end resolvedPointerPoint, button uint8, durationMs, holdBeforeMs, holdAfterMs, steps int) (dragErr error) {
	if steps < 1 {
		steps = 1
	}
	if durationMs < 0 {
		durationMs = 0
	}
	if holdBeforeMs < 0 {
		holdBeforeMs = 0
	}
	if holdAfterMs < 0 {
		holdAfterMs = 0
	}

	if err := settlePointer(dev, state, start.x, start.y); err != nil {
		return err
	}
	if err := pressPointer(dev, state, start.x, start.y, button); err != nil {
		return err
	}

	defer func() {
		relErr := releasePointer(dev, state, end.x, end.y)
		if dragErr == nil {
			dragErr = relErr
		}
	}()

	sleepMs(holdBeforeMs)

	stepDelay := 0
	if steps > 0 {
		stepDelay = durationMs / steps
	}
	for i := 1; i <= steps; i++ {
		progress := float64(i) / float64(steps)
		x := interpolateInt(start.x, end.x, progress)
		y := interpolateInt(start.y, end.y, progress)
		if err := movePointer(dev, state, x, y, button); err != nil {
			return err
		}
		if i < steps {
			sleepMs(stepDelay)
		}
	}

	sleepMs(holdAfterMs)
	return nil
}

func interpolateInt(start, end int, progress float64) int {
	return int(math.Round(float64(start) + (float64(end-start) * progress)))
}

func sleepMs(ms int) {
	if ms <= 0 {
		return
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

func intOrDefault(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func positiveIntOrDefault(value *int, fallback int) int {
	if value == nil || *value <= 0 {
		return fallback
	}
	return *value
}

func clampUint16(val, max int) uint16 {
	if val < 0 {
		return 0
	}
	if val > max {
		return uint16(max)
	}
	return uint16(val)
}

func mouseButtonByte(button string) uint8 {
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "right":
		return 0x02
	case "middle":
		return 0x04
	default:
		return 0x01 // left
	}
}

func charToHIDKey(ch byte) (modifier uint8, code uint8, ok bool) {
	switch {
	case ch >= 'a' && ch <= 'z':
		return 0, 0x04 + (ch - 'a'), true
	case ch >= 'A' && ch <= 'Z':
		return 0x02, 0x04 + (ch - 'A'), true // shift
	case ch >= '1' && ch <= '9':
		return 0, 0x1e + (ch - '1'), true
	case ch == '0':
		return 0, 0x27, true
	case ch == ' ':
		return 0, 0x2c, true
	case ch == '\n' || ch == '\r':
		return 0, 0x28, true
	case ch == '\t':
		return 0, 0x2b, true
	case ch == '-':
		return 0, 0x2d, true
	case ch == '=':
		return 0, 0x2e, true
	case ch == '[':
		return 0, 0x2f, true
	case ch == ']':
		return 0, 0x30, true
	case ch == '\\':
		return 0, 0x31, true
	case ch == ';':
		return 0, 0x33, true
	case ch == '\'':
		return 0, 0x34, true
	case ch == '`':
		return 0, 0x35, true
	case ch == ',':
		return 0, 0x36, true
	case ch == '.':
		return 0, 0x37, true
	case ch == '/':
		return 0, 0x38, true
	// Shifted symbols
	case ch == '!':
		return 0x02, 0x1e, true
	case ch == '@':
		return 0x02, 0x1f, true
	case ch == '#':
		return 0x02, 0x20, true
	case ch == '$':
		return 0x02, 0x21, true
	case ch == '%':
		return 0x02, 0x22, true
	case ch == '^':
		return 0x02, 0x23, true
	case ch == '&':
		return 0x02, 0x24, true
	case ch == '*':
		return 0x02, 0x25, true
	case ch == '(':
		return 0x02, 0x26, true
	case ch == ')':
		return 0x02, 0x27, true
	case ch == '_':
		return 0x02, 0x2d, true
	case ch == '+':
		return 0x02, 0x2e, true
	case ch == '{':
		return 0x02, 0x2f, true
	case ch == '}':
		return 0x02, 0x30, true
	case ch == '|':
		return 0x02, 0x31, true
	case ch == ':':
		return 0x02, 0x33, true
	case ch == '"':
		return 0x02, 0x34, true
	case ch == '~':
		return 0x02, 0x35, true
	case ch == '<':
		return 0x02, 0x36, true
	case ch == '>':
		return 0x02, 0x37, true
	case ch == '?':
		return 0x02, 0x38, true
	}
	return 0, 0, false
}
