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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
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

	// defaultSwipeHoldAfterMs defaults to zero so a swipe releases as soon as
	// it reaches the destination. Holding at the end makes phone UIs look like
	// the touch never released and can leave the screen stuck in a dragged
	// state. Callers that need a drag-like dwell can still pass hold_after_ms.
	defaultSwipeHoldAfterMs = 0

	defaultSwipeDurationMs = 700
	defaultSwipeSteps      = 24

	// defaultDirectionalSwipeDistance is the normalized travel for swipe_left/right/up/down.
	// Coordinates use 0-1000 normalized scale.
	defaultDirectionalSwipeDistance = 500.0
	directionalSwipeLargeDistance   = 700.0
	directionalSwipeMediumDistance  = 500.0
	directionalSwipeSmallDistance   = 200.0
	directionalSwipeTinyDistance    = 40.0

	phoneBackStartX = 1
	phoneBackEndX   = 750
	phoneBackY      = 500

	phoneHomeX      = 500
	phoneHomeStartY = 999
	phoneHomeEndY   = 180

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

	// defaultHIDWriteTimeout bounds each HID report write. Linux hidg writes can
	// otherwise block forever when the USB host stops polling, which leaves the
	// last successful pressed report active and prevents swipe release reports
	// from being sent.
	defaultHIDWriteTimeout = 750 * time.Millisecond

	// touchReleaseReportCount repeats the button-up/touch-up report for touch-like
	// gestures. On phone USB HID hosts a single final release report can be missed
	// or coalesced with the final move, leaving the UI in a pressed/dragged state.
	// Repeating the release is harmless for normal hosts and gives the target
	// several polling frames to observe button-up.
	touchReleaseReportCount   = 3
	touchReleaseReportDelayMs = 15

	// defaultKeyboardTapHoldMs matches example_usb_hid's default tap duration.
	// Zero hold often fails on macOS/iOS hosts that miss sub-frame modifier chords.
	defaultKeyboardTapHoldMs = 50
	// keyboardModifierTapHoldMs keeps modifier chords pressed long enough for
	// macOS/iOS hosts to register shortcuts like Cmd+Q.
	keyboardModifierTapHoldMs = 120
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
	path         string
	mu           sync.Mutex
	file         io.WriteCloser
	open         func(string) (io.WriteCloser, error)
	writeTimeout time.Duration
}

type screenState struct {
	mu          sync.RWMutex
	width       int
	height      int
	active      screenActiveArea
	phoneScreen PhoneScreenInfo
	updatedAt   time.Time
}

type screenMappingState struct {
	width     int
	height    int
	active    screenActiveArea
	updatedAt time.Time
}

// screenActiveArea represents the mirrored phone touch region inside the
// captured HDMI frame. When the companion app reports the phone's original
// screen dimensions, this is the largest centered region in the frame with the
// same aspect ratio. Falling back to "visible non-black content" is only an
// approximation for when accurate phone screen info is unavailable.
type screenActiveArea struct {
	X      int  `json:"x"`
	Y      int  `json:"y"`
	Width  int  `json:"width"`
	Height int  `json:"height"`
	Valid  bool `json:"valid"`
}

type pointerState struct {
	mu    sync.Mutex
	x     int
	y     int
	valid bool
}

func (s *screenState) Update(width, height int) {
	s.UpdateActiveArea(width, height, screenActiveArea{})
}

func (s *screenState) UpdatePhoneScreenInfo(info PhoneScreenInfo) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phoneScreen = info
}

func (s *screenState) ClearPhoneScreenInfo() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phoneScreen = PhoneScreenInfo{}
}

func (s *screenState) PhoneScreenInfo() PhoneScreenInfo {
	if s == nil {
		return PhoneScreenInfo{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.phoneScreen
}

func (s *screenState) UpdateActiveArea(width, height int, active screenActiveArea) {
	if width <= 0 || height <= 0 {
		return
	}
	if active.Valid {
		if active.X < 0 || active.Y < 0 || active.Width <= 0 || active.Height <= 0 || active.X+active.Width > width || active.Y+active.Height > height {
			active = screenActiveArea{}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.width = width
	s.height = height
	s.active = active
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
	width, height, _, age, ok = s.ActiveAreaWithAge()
	return width, height, age, ok
}

func (s *screenState) ActiveAreaWithAge() (width, height int, active screenActiveArea, age time.Duration, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.width <= 0 || s.height <= 0 {
		return 0, 0, screenActiveArea{}, 0, false
	}
	active = s.active
	if !active.Valid {
		active = screenActiveArea{X: 0, Y: 0, Width: s.width, Height: s.height, Valid: true}
	}
	if s.updatedAt.IsZero() {
		return s.width, s.height, active, 0, true
	}
	return s.width, s.height, active, time.Since(s.updatedAt), true
}

func (s *screenState) MappingState() screenMappingState {
	if s == nil {
		return screenMappingState{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return screenMappingState{
		width:     s.width,
		height:    s.height,
		active:    s.active,
		updatedAt: s.updatedAt,
	}
}

func (s *screenState) RestoreMappingState(state screenMappingState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.width = state.width
	s.height = state.height
	s.active = state.active
	s.updatedAt = state.updatedAt
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

// pointerController sends hid.usb1 reports in absolute mouse or touchscreen mode.
type pointerController struct {
	dev         *HIDDevice
	state       *pointerState
	touchscreen bool
}

func newPointerController(hid HIDConfig) *pointerController {
	return &pointerController{
		dev:         NewHIDDevice(hid.MouseDeviceOrDefault()),
		state:       &pointerState{},
		touchscreen: hid.PointerTouchscreen(),
	}
}

func NewHIDDevice(path string) *HIDDevice {
	return &HIDDevice{
		path:         path,
		writeTimeout: defaultHIDWriteTimeout,
		open: func(path string) (io.WriteCloser, error) {
			return os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
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

	n, err := d.writeOnceLocked(data)
	if err != nil {
		d.closeLocked()
		if n == 0 && hidShouldRetryWrite(err) {
			if reopenErr := d.ensureOpenLocked(); reopenErr == nil {
				n2, retryErr := d.writeOnceLocked(data)
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
	// Sync to ensure data is flushed to USB gadget driver
	if f, ok := d.file.(*os.File); ok {
		_ = f.Sync()
	}
	return nil
}

func (d *HIDDevice) writeOnceLocked(data []byte) (int, error) {
	if fdFile, ok := d.file.(interface{ Fd() uintptr }); ok {
		return d.writeFDLocked(int(fdFile.Fd()), data)
	}
	return d.file.Write(data)
}

func (d *HIDDevice) writeFDLocked(fd int, data []byte) (int, error) {
	timeout := d.writeTimeout
	if timeout <= 0 {
		timeout = defaultHIDWriteTimeout
	}
	deadline := time.Now().Add(timeout)

	for {
		n, err := unix.Write(fd, data)
		if err == nil {
			return n, nil
		}
		if err == unix.EINTR {
			continue
		}
		if !hidWriteWouldBlock(err) {
			return n, err
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, fmt.Errorf("timed out after %s waiting to write HID report", timeout)
		}

		pollTimeoutMs := int(remaining.Milliseconds())
		if pollTimeoutMs < 1 {
			pollTimeoutMs = 1
		}
		fds := []unix.PollFd{{
			Fd:     int32(fd),
			Events: unix.POLLOUT | unix.POLLERR | unix.POLLHUP,
		}}
		ready, pollErr := unix.Poll(fds, pollTimeoutMs)
		if pollErr == unix.EINTR {
			continue
		}
		if pollErr != nil {
			return 0, pollErr
		}
		if ready == 0 {
			return 0, fmt.Errorf("timed out after %s waiting to write HID report", timeout)
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return 0, syscall.EPIPE
		}
	}
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

func hidWriteWouldBlock(err error) bool {
	return errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK)
}

// KeyboardTapTool sends a key press then release via HID.
type KeyboardTapTool struct {
	dev *HIDDevice
}

func (t *KeyboardTapTool) Name() string { return "keyboard_tap" }

func (t *KeyboardTapTool) Description() string {
	return `Press and release keyboard keys. Input JSON: {"keys": ["ctrl", "c"]}. ` +
		`For known semantic platform actions such as back, app search, app switching, copy, paste, undo, redo, select all, delete backward/forward, find, send, or browser navigation, prefer quick_action first; use keyboard_tap as a low-level fallback or for custom key input. ` +
		`Supports: a-z, 0-9, f1-f12, enter, escape, backspace, tab, space, delete, ` +
		`up, down, left, right, home, end, pageup, pagedown, insert, printscreen. ` +
		`For normal text deletion in an input field, use backspace; the delete key is forward-delete and should only be used when intentionally deleting the character after the cursor. ` +
		`Modifiers: ctrl, shift, alt, meta/super/win/cmd. ` +
		`Modifier-only taps are supported (e.g. {"keys":["meta"]} for Android Home). ` +
		`Multiple keys are pressed simultaneously (e.g. ctrl+c). ` +
		`Optional hold_ms keeps the chord pressed before release (default 50ms, 120ms when modifiers are used).`
}

func (t *KeyboardTapTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"keys": map[string]any{
				"type":        "array",
				"minItems":    1,
				"maxItems":    6,
				"description": "Keys pressed simultaneously, e.g. [\"ctrl\",\"c\"] or [\"meta\"]. Use backspace for ordinary text deletion before the cursor; delete is forward-delete after the cursor.",
				"items": map[string]any{
					"type": "string",
				},
			},
			"hold_ms": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Optional press duration before release.",
			},
		},
		"required": []string{"keys"},
	}
}

func (t *KeyboardTapTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		Keys   []string `json:"keys"`
		HoldMs int      `json:"hold_ms"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v. Expected JSON format: {\"keys\": [\"ctrl\", \"c\"]}. Common mistakes: missing quotes around key names, incorrect comma placement in array", err), nil
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
	if modifier == 0 && len(keys) == 0 {
		return "error: at least one key or modifier is required", nil
	}

	holdMs := args.HoldMs
	if holdMs <= 0 {
		holdMs = defaultKeyboardTapHoldMs
		if modifier != 0 {
			holdMs = keyboardModifierTapHoldMs
		}
	}

	if err := t.tapKeyboardChord(modifier, keys, holdMs); err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	return "ok", nil
}

func (t *KeyboardTapTool) tapKeyboardChord(modifier uint8, keys []uint8, holdMs int) error {
	// HID keyboard report: [modifier, reserved, key1..key6]
	report := make([]byte, 8)
	report[0] = modifier
	for i := 0; i < len(keys) && i < 6; i++ {
		report[2+i] = keys[i]
	}

	if err := t.dev.Write(report); err != nil {
		return err
	}
	if holdMs > 0 {
		time.Sleep(time.Duration(holdMs) * time.Millisecond)
	}
	return t.dev.Write(make([]byte, 8))
}

// KeyboardTextTool types a string character by character via HID.
type KeyboardTextTool struct {
	dev *HIDDevice
}

func (t *KeyboardTextTool) Name() string { return "keyboard_text" }

func (t *KeyboardTextTool) Description() string {
	return `US-keyboard ASCII text input only via USB HID physical keyboard (not the on-screen soft keyboard). ` +
		`Allowed characters: a-z, A-Z, 0-9, space, and common US-keyboard punctuation. ` +
		`For model/tool calls, pass JSON only, for example {"text":"App Store"}; do not pass a bare string. ` +
		`Do NOT pass non-ASCII text, emoji, or spaced romanization — use enter_text_in_field for input box entry. ` +
		`For Chinese targets without enter_text_in_field, use pinyin or English keywords (e.g. {"text":"weixin"}), then tap the on-screen candidate. ` +
		`keyboard_text remains for simple standalone ASCII typing outside the enter_text_in_field workflow. ` +
		`Bare plain text is accepted only as a legacy compatibility fallback.`
}

func (t *KeyboardTextTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": "US-keyboard ASCII text to type.",
			},
		},
		"required": []string{"text"},
	}
}

func (t *KeyboardTextTool) Call(_ context.Context, input string) (string, error) {
	text, errText := parseKeyboardTextInput(input)
	if errText != "" {
		return errText, nil
	}

	if unsupported := unsupportedKeyboardTextRunes(text); len(unsupported) > 0 {
		return fmt.Sprintf(
			"error: keyboard_text supports only US-keyboard ASCII characters; unsupported characters: %q. Use enter_text_in_field for this target.",
			string(unsupported),
		), nil
	}
	if looksLikeSpacedRomanizationBlob(text) {
		return "error: keyboard_text received spaced romanization; use enter_text_in_field instead.", nil
	}

	releaseReport := make([]byte, 8)
	for _, ch := range text {
		modifier, code, ok := charToHIDKey(byte(ch))
		if !ok {
			continue
		}
		report := make([]byte, 8)
		report[0] = modifier
		report[2] = code
		// Press then release immediately, same as keyboard_tap.
		if err := t.dev.Write(report); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := t.dev.Write(releaseReport); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
	}

	return "ok", nil
}

// MouseClickTool moves the mouse to coordinates and clicks.
type MouseClickTool struct {
	pc     *pointerController
	screen *screenState
}

func (t *MouseClickTool) Name() string { return "mouse_click" }

func (t *MouseClickTool) Description() string {
	return `Move mouse to a position and click. Input JSON: {"x": 500, "y": 300, "button": "left", "coord_space": "normalized"}. ` +
		`coord_space options: "pixel", "normalized", "absolute". Default is "auto": x/y in [0,1000] are treated as normalized, otherwise pixel coordinates are used when a recent screenshot has cached screen dimensions, otherwise HID absolute values in the range 0-32767. ` +
		`Normalized coordinates use 0-1000 range where (0,0) is top-left, (1000,1000) is bottom-right, (500,500) is center. ` +
		`Choose the visual center of the target in the latest screenshot; for small controls, estimate the control bounds and click the midpoint, biased inward. ` +
		`pointer_mode absolute (default): moves to absolute HID coordinates, waits for iOS cursor smoothing, then clicks. ` +
		`pointer_mode touchscreen (Android): sends a finger down/up at the resolved coordinate. ` +
		`Use coord_space:"pixel" only when calibrated; pixel coordinates require a recent screenshot, are stale after 30s, and are rejected if outside cached bounds. ` +
		`Click once and inspect the returned post-action screenshot before repeating. ` +
		`Button options: "left" (default), "right", "middle".`
}

func (t *MouseClickTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"x": coordinateSchema("X coordinate."),
			"y": coordinateSchema("Y coordinate."),
			"button": map[string]any{
				"type":        "string",
				"enum":        []string{"left", "right", "middle"},
				"description": "Mouse button; defaults to left.",
			},
			"coord_space": coordSpaceSchema(),
		},
		"required": []string{"x", "y"},
	}
}

func (t *MouseClickTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		X          pointerCoordinate `json:"x"`
		Y          pointerCoordinate `json:"y"`
		Button     string            `json:"button"`
		CoordSpace string            `json:"coord_space"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v. Expected JSON format: {\"x\": 500, \"y\": 300, \"button\": \"left\", \"coord_space\": \"normalized\"}. Common mistakes: x and y must be numbers, missing quotes around field names", err), nil
	}

	absX, absY, err := resolvePointerPositionForSurface(t.screen, t.pc.touchscreen, args.X.Float64(), args.Y.Float64(), args.CoordSpace, coordinateSpaceAuto)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	btn := mouseButtonByte(args.Button)

	if err := tapPointer(t.pc, absX, absY, btn); err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}

	return "ok", nil
}

// MouseMoveTool moves the mouse to coordinates without clicking.
type MouseMoveTool struct {
	pc     *pointerController
	screen *screenState
}

func (t *MouseMoveTool) Name() string { return "mouse_move" }

func (t *MouseMoveTool) Description() string {
	return `Move mouse to a position without clicking. Input JSON: {"x": 500, "y": 300, "coord_space": "normalized"}. ` +
		`coord_space options: "pixel", "normalized", "absolute". Default is "auto": x/y in [0,1000] are treated as normalized, otherwise pixel coordinates are used when a recent screenshot has cached screen dimensions, otherwise HID absolute values in the range 0-32767. ` +
		`Normalized coordinates use 0-1000 range where (0,0) is top-left, (1000,1000) is bottom-right, (500,500) is center. ` +
		`pointer_mode absolute (default): writes absolute HID coordinates directly. ` +
		`pointer_mode touchscreen (Android): moves the logical touch point without pressing.`
}

func (t *MouseMoveTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"x":           coordinateSchema("X coordinate."),
			"y":           coordinateSchema("Y coordinate."),
			"coord_space": coordSpaceSchema(),
		},
		"required": []string{"x", "y"},
	}
}

func (t *MouseMoveTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		X          pointerCoordinate `json:"x"`
		Y          pointerCoordinate `json:"y"`
		CoordSpace string            `json:"coord_space"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v. Expected JSON format: {\"x\": 500, \"y\": 300, \"coord_space\": \"normalized\"}. Common mistakes: x and y must be numbers, missing quotes around field names", err), nil
	}

	absX, absY, err := resolvePointerPositionForSurface(t.screen, t.pc.touchscreen, args.X.Float64(), args.Y.Float64(), args.CoordSpace, coordinateSpaceAuto)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}

	if err := positionPointer(t.pc, absX, absY, 0); err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}

	return "ok", nil
}

// TouchGestureTool executes touch-like pointer gestures for mobile UI control.
type TouchGestureTool struct {
	pc     *pointerController
	screen *screenState
}

func (t *TouchGestureTool) Name() string { return "touch_gesture" }

func (t *TouchGestureTool) Description() string {
	return `Perform a touch-like gesture using the pointer HID device (absolute mouse or touchscreen depending on agent pointer_mode). ` +
		`For known semantic platform actions such as back, home, app search, app switching, notification shade, quick settings, and browser navigation, prefer quick_action first; use touch_gesture as a low-level fallback or for custom screen gestures. ` +
		`Input JSON examples: {"type":"tap","point":{"x":500,"y":500}}, {"type":"swipe","start":{"x":200,"y":500},"end":{"x":800,"y":500},"duration_ms":700,"steps":24}, {"type":"swipe_left"}, {"type":"back"}, {"type":"home"}. ` +
		`IMPORTANT: "point", "start", and "end" must be objects with named keys "x" and "y". NEVER omit the key names: {"x":500,"y":300} is correct, {500,300} is invalid and will error. ` +
		`Supported types: "tap", "double_tap", "long_press", "drag", "swipe", "swipe_left", "swipe_right", "swipe_up", "swipe_down", "back" (left-edge back), "home" (bottom-edge home). ` +
		`coord_space defaults to "normalized" (x/y in [0,1000]) and also supports "pixel" and "absolute". ` +
		`Normalized coordinates use 0-1000 range where (0,0) is top-left, (1000,1000) is bottom-right, (500,500) is center. ` +
		`Choose the visual center of the target in the latest screenshot; for small controls, estimate the control bounds and touch the midpoint, biased inward. ` +
		`pointer_mode absolute (default, iOS): every gesture moves to absolute coordinates before pressing; choose tap targets from the latest screenshot center and prefer normalized coordinates. ` +
		`pointer_mode touchscreen (Android): gestures are sent as single-finger touch down/move/up reports. ` +
		`Directional swipes accept optional "distance" (normalized 0-1000 travel, default 500), "anchor" (fixed-axis coordinate 0-1000, default 500), and "strength" ("large", "medium", "small", "tiny"). ` +
		`Directional swipe names describe finger movement, not the content you want to reveal: "swipe_up" moves the finger upward and usually scrolls the viewport down to lower/newer items; "swipe_down" moves the finger downward and usually scrolls the viewport up to upper/older items. For chat or message history where older messages are above, use "swipe_down" (pull down), not "swipe_up". ` +
		`For precise vertical or horizontal controls, first probe with medium/large, observe the screenshot, then use small/tiny near the target; if you overshoot, reverse direction and reduce strength. ` +
		`For locally scrollable regions such as pickers, modal lists, embedded scroll views, or partial dialogs, keep start/end coordinates inside the control's visible bounds so the outer container does not capture the gesture. ` +
		`Absolute-mode gestures wait for iOS HID-cursor smoothing to settle before pressing. ` +
		`Tap and double_tap accept an optional "hold_ms" (dwell between press and release, default 60ms). ` +
		`Swipe defaults to a slower 700ms / 24-step motion, applies "hold_before_ms" of 80ms after the press, and releases immediately at the destination by default; pass "hold_after_ms" only when a drag-like end dwell is required. ` +
		`For phone edge gestures, do not use conservative inset coordinates such as 50-100: "back" starts at normalized x=1 and "home" starts at normalized y=999. Drag keeps the previous 250ms / 12-step motion with 0ms hold defaults to avoid unintended long-press behaviour during slow content drag.`
}

func (t *TouchGestureTool) ArgsSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"type": map[string]any{
				"type":        "string",
				"enum":        []string{"tap", "double_tap", "long_press", "drag", "swipe", "swipe_left", "swipe_right", "swipe_up", "swipe_down", "back", "home"},
				"description": "Gesture type.",
			},
			"point":          pointSchema("Point for tap, double_tap, or long_press."),
			"start":          pointSchema("Start point for swipe or drag."),
			"end":            pointSchema("End point for swipe or drag."),
			"coord_space":    coordSpaceSchema(),
			"button":         map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}},
			"duration_ms":    nonNegativeIntegerSchema("Gesture duration in milliseconds."),
			"hold_before_ms": nonNegativeIntegerSchema("Optional dwell after pressing before a swipe begins."),
			"hold_after_ms":  nonNegativeIntegerSchema("Optional dwell at the destination before release."),
			"hold_ms":        nonNegativeIntegerSchema("Tap or long-press hold duration in milliseconds."),
			"pause_ms":       nonNegativeIntegerSchema("Pause between taps for double_tap."),
			"steps": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Number of movement steps for swipe or drag.",
			},
			"distance": coordinateSchema("Directional swipe travel in normalized units."),
			"anchor":   coordinateSchema("Directional swipe fixed-axis coordinate in normalized units."),
			"strength": map[string]any{
				"type":        "string",
				"enum":        []string{"large", "medium", "small", "tiny"},
				"description": "Directional swipe preset distance.",
			},
		},
		"required": []string{"type"},
	}
}

func pointSchema(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"description":          description,
		"properties": map[string]any{
			"x": coordinateSchema("X coordinate."),
			"y": coordinateSchema("Y coordinate."),
		},
		"required": []string{"x", "y"},
	}
}

func coordinateSchema(description string) map[string]any {
	return map[string]any{
		"type":        "number",
		"description": description,
	}
}

func coordSpaceSchema() map[string]any {
	return map[string]any{
		"type":        "string",
		"enum":        []string{"auto", "pixel", "normalized", "absolute"},
		"description": "Coordinate space; normalized uses 0-1000 screen coordinates.",
	}
}

func nonNegativeIntegerSchema(description string) map[string]any {
	return map[string]any{
		"type":        "integer",
		"minimum":     0,
		"description": description,
	}
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
		Distance     *float64      `json:"distance"`
		Anchor       *float64      `json:"anchor"`
		Strength     string        `json:"strength"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v. Common mistakes: missing quotes around string values, incorrect comma placement, point/start/end must be objects with named keys like {\"x\":500,\"y\":300} not bare values. Example: {\"type\":\"tap\",\"point\":{\"x\":500,\"y\":500}}", err), nil
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
		point, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, args.Point, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := tapPointerWithHold(t.pc, point.x, point.y, button, intOrDefault(args.HoldMs, defaultTapHoldMs)); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
	case "double_tap":
		point, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, args.Point, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		holdMs := intOrDefault(args.HoldMs, defaultTapHoldMs)
		if err := tapPointerWithHold(t.pc, point.x, point.y, button, holdMs); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		sleepMs(intOrDefault(args.PauseMs, 100))
		if err := tapPointerWithHold(t.pc, point.x, point.y, button, holdMs); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
	case "long_press":
		point, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, args.Point, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := settlePointer(t.pc, point.x, point.y); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := pressPointer(t.pc, point.x, point.y, button); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		released := false
		defer func() {
			if !released {
				_ = releasePointerRepeated(t.pc, point.x, point.y, touchReleaseReportCount, touchReleaseReportDelayMs)
			}
		}()
		sleepMs(intOrDefault(args.DurationMs, 500))
		if err := releasePointerRepeated(t.pc, point.x, point.y, touchReleaseReportCount, touchReleaseReportDelayMs); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		released = true
	case "swipe_left", "swipe_right", "swipe_up", "swipe_down":
		preset, err := directionalSwipePreset(args.Strength)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		start, end, err := directionalSwipeEndpoints(t.screen, t.pc.touchscreen, gestureType, args.Distance, args.Anchor, preset)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := runSwipeLikeGesture(
			t.pc,
			start,
			end,
			button,
			intOrDefault(args.DurationMs, preset.durationMs),
			intOrDefault(args.HoldBeforeMs, preset.holdBeforeMs),
			intOrDefault(args.HoldAfterMs, preset.holdAfterMs),
			positiveIntOrDefault(args.Steps, preset.steps),
		); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
	case "drag":
		start, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, args.Start, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		end, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, args.End, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := runPositionedDragGesture(
			t.pc,
			start,
			end,
			button,
			intOrDefault(args.DurationMs, 250),
			intOrDefault(args.HoldBeforeMs, 0),
			intOrDefault(args.HoldAfterMs, 0),
			positiveIntOrDefault(args.Steps, 12),
		); err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
	case "swipe":
		start, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, args.Start, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		end, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, args.End, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := runSwipeLikeGesture(
			t.pc,
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
	case "back", "edge_back", "left_edge_back":
		start, err := resolvePointOrDefaultNormalized(t.screen, t.pc.touchscreen, args.Start, coordSpace, phoneBackStartX, phoneBackY)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		end, err := resolvePointOrDefaultNormalized(t.screen, t.pc.touchscreen, args.End, coordSpace, phoneBackEndX, phoneBackY)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := runPositionedDragGesture(
			t.pc,
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
		start, err := resolvePointOrDefaultNormalized(t.screen, t.pc.touchscreen, args.Start, coordSpace, phoneHomeX, phoneHomeStartY)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		end, err := resolvePointOrDefaultNormalized(t.screen, t.pc.touchscreen, args.End, coordSpace, phoneHomeX, phoneHomeEndY)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if err := runPositionedDragGesture(
			t.pc,
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
	pc *pointerController
}

func (t *MouseScrollTool) Name() string { return "mouse_scroll" }

func (t *MouseScrollTool) Description() string {
	return `Scroll the mouse wheel. Input JSON: {"delta": -3}. ` +
		`Positive values scroll up, negative scroll down. Range: -127 to 127. This is a wheel event and is not equivalent to a mobile swipe gesture. ` +
		`Works in pointer_mode absolute; pointer_mode touchscreen ignores wheel events.`
}

func (t *MouseScrollTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"delta": rangedIntegerArgSchema("Mouse wheel delta. Positive scrolls up, negative scrolls down.", -127, 127),
	}, "delta")
}

func (t *MouseScrollTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		Delta int `json:"delta"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v. Expected JSON format: {\"delta\": -3}. Delta must be a number between -127 and 127", err), nil
	}
	if args.Delta == 0 {
		return "ok", nil
	}
	if args.Delta < -127 {
		args.Delta = -127
	} else if args.Delta > 127 {
		args.Delta = 127
	}

	if err := scrollPointer(t.pc, args.Delta); err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}

	return "ok", nil
}

// writeAbsMouseReport writes an absolute mouse report: [buttons, x_lo, x_hi, y_lo, y_hi, wheel].
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

// writeTouchscreenReport writes a single-contact touch report:
// [flags, contact_id, x_lo, x_hi, y_lo, y_hi]. flags bit0=tip switch, bit1=in range.
func writeTouchscreenReport(dev *HIDDevice, state *pointerState, x, y int, touching bool) error {
	absX := clampUint16(x, absMouseMaxPos)
	absY := clampUint16(y, absMouseMaxPos)
	report := make([]byte, 6)
	if touching {
		report[0] = 0x03
	}
	report[1] = 0x01
	binary.LittleEndian.PutUint16(report[2:4], absX)
	binary.LittleEndian.PutUint16(report[4:6], absY)

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

func (pc *pointerController) currentXY() (int, int) {
	if pc.state != nil {
		if x, y, ok := pc.state.Current(); ok {
			return x, y
		}
	}
	return absMouseMaxPos / 2, absMouseMaxPos / 2
}

func (pc *pointerController) moveTo(x, y int, buttons uint8) error {
	if pc.touchscreen {
		return writeTouchscreenReport(pc.dev, pc.state, x, y, buttons != 0)
	}
	return writeAbsMouseReport(pc.dev, pc.state, x, y, buttons, 0)
}

func (pc *pointerController) scroll(delta int) error {
	if delta == 0 {
		return nil
	}
	if delta < -127 {
		delta = -127
	} else if delta > 127 {
		delta = 127
	}
	if pc.touchscreen {
		return nil
	}
	x, y := pc.currentXY()
	return writeAbsMouseReport(pc.dev, pc.state, x, y, 0, int8(delta))
}

type pointerPoint struct {
	X pointerCoordinate `json:"x"`
	Y pointerCoordinate `json:"y"`
}

type pointerCoordinate float64

func (c *pointerCoordinate) UnmarshalJSON(data []byte) error {
	var number float64
	if err := json.Unmarshal(data, &number); err == nil {
		return c.setFinite(number)
	}

	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		value, parseErr := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if parseErr != nil {
			return fmt.Errorf("parse coordinate %q: %w", text, parseErr)
		}
		return c.setFinite(value)
	}

	return fmt.Errorf("coordinate must be a number or numeric string")
}

func (c *pointerCoordinate) setFinite(value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("coordinate must be a finite number")
	}
	*c = pointerCoordinate(value)
	return nil
}

func (c pointerCoordinate) Float64() float64 {
	return float64(c)
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

func resolveRequiredPoint(screen *screenState, touchscreen bool, point *pointerPoint, coordSpace string) (resolvedPointerPoint, error) {
	if point == nil {
		return resolvedPointerPoint{}, fmt.Errorf("point is required")
	}

	x, y, err := resolvePointerPositionForSurface(screen, touchscreen, point.X.Float64(), point.Y.Float64(), coordSpace, coordinateSpaceNormalized)
	if err != nil {
		return resolvedPointerPoint{}, err
	}
	return resolvedPointerPoint{x: x, y: y}, nil
}

func resolvePointOrDefaultNormalized(screen *screenState, touchscreen bool, point *pointerPoint, coordSpace string, defaultX, defaultY float64) (resolvedPointerPoint, error) {
	if point != nil {
		return resolveRequiredPoint(screen, touchscreen, point, coordSpace)
	}

	if _, err := normalizeCoordinateSpace(coordSpace, coordinateSpaceNormalized); err != nil {
		return resolvedPointerPoint{}, err
	}

	x, y, err := resolvePointerPositionForSurface(screen, touchscreen, defaultX, defaultY, coordinateSpaceNormalized, coordinateSpaceNormalized)
	if err != nil {
		return resolvedPointerPoint{}, err
	}
	return resolvedPointerPoint{x: x, y: y}, nil
}

func resolvePointerPosition(screen *screenState, x, y float64, coordSpace string, defaultSpace string) (int, int, error) {
	return resolvePointerPositionForSurface(screen, false, x, y, coordSpace, defaultSpace)
}

func resolvePointerPositionForSurface(screen *screenState, touchscreen bool, x, y float64, coordSpace string, defaultSpace string) (int, int, error) {
	space, err := normalizeCoordinateSpace(coordSpace, defaultSpace)
	if err != nil {
		return 0, 0, err
	}

	switch space {
	case coordinateSpaceAuto:
		if looksLikeNormalizedPoint(x, y) {
			absX, absY, err := normalizedToAbsolutePointForSurface(screen, touchscreen, x, y)
			if err != nil {
				return 0, 0, err
			}
			return absX, absY, nil
		}
		if screen != nil {
			if width, height, active, age, ok := screen.ActiveAreaWithAge(); ok && age < screenDimensionsStaleAfter {
				return pixelToAbsolutePoint(x, y, width, height, active)
			}
		}
		return int(clampFloat(math.Round(x), 0, absMouseMaxPos)), int(clampFloat(math.Round(y), 0, absMouseMaxPos)), nil
	case coordinateSpacePixel:
		if screen == nil {
			return 0, 0, fmt.Errorf("pixel coordinates require known screen dimensions; call screenshot first or use coord_space normalized/absolute")
		}
		width, height, active, age, ok := screen.ActiveAreaWithAge()
		if !ok {
			return 0, 0, fmt.Errorf("pixel coordinates require known screen dimensions; call screenshot first or use coord_space normalized/absolute")
		}
		if age >= screenDimensionsStaleAfter {
			return 0, 0, fmt.Errorf("cached screen dimensions are %.0fs old; call screenshot to refresh before using pixel coordinates", age.Seconds())
		}
		return pixelToAbsolutePoint(x, y, width, height, active)
	case coordinateSpaceNormalized:
		absX, absY, err := normalizedToAbsolutePointForSurface(screen, touchscreen, x, y)
		if err != nil {
			return 0, 0, err
		}
		return absX, absY, nil
	case coordinateSpaceAbsolute:
		return int(clampFloat(math.Round(x), 0, absMouseMaxPos)), int(clampFloat(math.Round(y), 0, absMouseMaxPos)), nil
	}

	return 0, 0, fmt.Errorf("unsupported coord_space: %q", coordSpace)
}

func normalizedToAbsolutePointForSurface(screen *screenState, touchscreen bool, x, y float64) (int, int, error) {
	// Normalized coordinates are always interpreted within active_area:
	// 0-1000 maps to the mirrored phone touch region inside the HDMI frame.
	//
	// pointer_mode absolute:
	// Scale within active_area's own coordinate system because the absolute
	// HID cursor surface tracks the de-blackbarred phone region.
	//
	// pointer_mode touchscreen:
	// First project the normalized point into the active_area inside the HDMI
	// frame, then scale against the full frame because the touchscreen HID
	// surface covers the complete mirrored frame.
	if screen != nil {
		if width, height, active, age, ok := screen.ActiveAreaWithAge(); ok && age < screenDimensionsStaleAfter && active.Valid {
			activePixelX := (clampFloat(x, 0, 1000) / 1000.0) * float64(active.Width-1)
			activePixelY := (clampFloat(y, 0, 1000) / 1000.0) * float64(active.Height-1)
			if touchscreen {
				fullFramePixelX := float64(active.X) + activePixelX
				fullFramePixelY := float64(active.Y) + activePixelY
				return scalePixelToAbsolute(fullFramePixelX, width), scalePixelToAbsolute(fullFramePixelY, height), nil
			}
			return scalePixelToAbsolute(activePixelX, active.Width), scalePixelToAbsolute(activePixelY, active.Height), nil
		}
	}
	absX, absY := normalizedToAbsolutePoint(x, y)
	return absX, absY, nil
}

func looksLikeNormalizedPoint(x, y float64) bool {
	return x >= 0 && x <= 1000 && y >= 0 && y <= 1000
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
	return int(math.Round(clampFloat(x, 0, 1000) / 1000.0 * absMouseMaxPos)), int(math.Round(clampFloat(y, 0, 1000) / 1000.0 * absMouseMaxPos))
}

func pixelToAbsolutePoint(x, y float64, width, height int, active screenActiveArea) (int, int, error) {
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("invalid screen dimensions: %dx%d", width, height)
	}
	if x < 0 || y < 0 || x > float64(width-1) || y > float64(height-1) {
		return 0, 0, fmt.Errorf("pixel coordinates x=%.2f y=%.2f are outside cached screenshot bounds %dx%d; use coord_space normalized with 0-1000 coordinates, where 500,500 is center, or refresh/calibrate the screenshot dimensions", x, y, width, height)
	}
	if !active.Valid {
		active = screenActiveArea{X: 0, Y: 0, Width: width, Height: height, Valid: true}
	}
	if x < float64(active.X) || y < float64(active.Y) || x > float64(active.X+active.Width-1) || y > float64(active.Y+active.Height-1) {
		return 0, 0, fmt.Errorf("pixel coordinates x=%.2f y=%.2f are outside active screen area x=%d y=%d width=%d height=%d within screenshot %dx%d", x, y, active.X, active.Y, active.Width, active.Height, width, height)
	}
	return scalePixelToAbsolute(x-float64(active.X), active.Width), scalePixelToAbsolute(y-float64(active.Y), active.Height), nil
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

func tapPointer(pc *pointerController, x, y int, button uint8) error {
	return tapPointerWithHold(pc, x, y, button, defaultTapHoldMs)
}

func tapPointerWithHold(pc *pointerController, x, y int, button uint8, holdMs int) error {
	if err := settlePointer(pc, x, y); err != nil {
		return err
	}
	if err := pressPointer(pc, x, y, button); err != nil {
		return err
	}
	sleepMs(holdMs)
	return releasePointerRepeated(pc, x, y, touchReleaseReportCount, touchReleaseReportDelayMs)
}

func settlePointer(pc *pointerController, x, y int) error {
	if pc.touchscreen {
		return nil
	}
	if err := movePointer(pc, x, y, 0); err != nil {
		return err
	}
	sleepMs(defaultCursorSettleMs)
	return nil
}

func positionPointer(pc *pointerController, x, y int, buttons uint8) error {
	return movePointer(pc, x, y, buttons)
}

func pressPointer(pc *pointerController, x, y int, button uint8) error {
	return movePointer(pc, x, y, button)
}

func releasePointer(pc *pointerController, x, y int) error {
	return movePointer(pc, x, y, 0)
}

func releasePointerRepeated(pc *pointerController, x, y int, count int, delayMs int) error {
	if count < 1 {
		count = 1
	}

	var firstErr error
	released := false
	for i := 0; i < count; i++ {
		if err := releasePointer(pc, x, y); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			released = true
		}
		if i+1 < count {
			sleepMs(delayMs)
		}
	}
	if released {
		return nil
	}
	return firstErr
}

func movePointer(pc *pointerController, x, y int, buttons uint8) error {
	return pc.moveTo(x, y, buttons)
}

func scrollPointer(pc *pointerController, delta int) error {
	return pc.scroll(delta)
}

func runSwipeLikeGesture(pc *pointerController, start, end resolvedPointerPoint, button uint8, durationMs, holdBeforeMs, holdAfterMs, steps int) error {
	return dragPointer(pc, start, end, button, durationMs, holdBeforeMs, holdAfterMs, steps)
}

func runPositionedDragGesture(pc *pointerController, start, end resolvedPointerPoint, button uint8, durationMs, holdBeforeMs, holdAfterMs, steps int) error {
	return dragPointer(pc, start, end, button, durationMs, holdBeforeMs, holdAfterMs, steps)
}

type directionalSwipeSettings struct {
	distance     float64
	durationMs   int
	steps        int
	holdBeforeMs int
	holdAfterMs  int
}

func directionalSwipePreset(strength string) (directionalSwipeSettings, error) {
	switch strings.ToLower(strings.TrimSpace(strength)) {
	case "", "default":
		return directionalSwipeSettings{distance: defaultDirectionalSwipeDistance, durationMs: defaultSwipeDurationMs, steps: defaultSwipeSteps, holdBeforeMs: defaultSwipeHoldBeforeMs, holdAfterMs: defaultSwipeHoldAfterMs}, nil
	case "large":
		return directionalSwipeSettings{distance: directionalSwipeLargeDistance, durationMs: 800, steps: 28, holdBeforeMs: 90, holdAfterMs: defaultSwipeHoldAfterMs}, nil
	case "medium":
		return directionalSwipeSettings{distance: directionalSwipeMediumDistance, durationMs: 650, steps: 22, holdBeforeMs: 90, holdAfterMs: defaultSwipeHoldAfterMs}, nil
	case "small":
		return directionalSwipeSettings{distance: directionalSwipeSmallDistance, durationMs: 420, steps: 14, holdBeforeMs: 100, holdAfterMs: defaultSwipeHoldAfterMs}, nil
	case "tiny":
		return directionalSwipeSettings{distance: directionalSwipeTinyDistance, durationMs: 320, steps: 10, holdBeforeMs: 100, holdAfterMs: defaultSwipeHoldAfterMs}, nil
	default:
		return directionalSwipeSettings{}, fmt.Errorf("unsupported strength: %q", strength)
	}
}

func directionalSwipeEndpoints(screen *screenState, touchscreen bool, gestureType string, distance, anchor *float64, preset directionalSwipeSettings) (resolvedPointerPoint, resolvedPointerPoint, error) {
	travel := preset.distance
	if travel <= 0 {
		travel = defaultDirectionalSwipeDistance
	}
	if distance != nil && *distance > 0 {
		travel = clampFloat(*distance, 1, 1000)
	}
	center := 500.0
	if anchor != nil {
		center = clampFloat(*anchor, 0, 1000)
	}
	half := travel / 2

	var startX, startY, endX, endY float64
	switch gestureType {
	case "swipe_left":
		startX, endX = center+half, center-half
		startY, endY = center, center
	case "swipe_right":
		startX, endX = center-half, center+half
		startY, endY = center, center
	case "swipe_up":
		startY, endY = center+half, center-half
		startX, endX = center, center
	case "swipe_down":
		startY, endY = center-half, center+half
		startX, endX = center, center
	default:
		return resolvedPointerPoint{}, resolvedPointerPoint{}, fmt.Errorf("unsupported directional swipe: %q", gestureType)
	}

	startAbsX, startAbsY, err := normalizedToAbsolutePointForSurface(screen, touchscreen, startX, startY)
	if err != nil {
		return resolvedPointerPoint{}, resolvedPointerPoint{}, err
	}
	endAbsX, endAbsY, err := normalizedToAbsolutePointForSurface(screen, touchscreen, endX, endY)
	if err != nil {
		return resolvedPointerPoint{}, resolvedPointerPoint{}, err
	}
	return resolvedPointerPoint{x: startAbsX, y: startAbsY}, resolvedPointerPoint{x: endAbsX, y: endAbsY}, nil
}

func dragPointer(pc *pointerController, start, end resolvedPointerPoint, button uint8, durationMs, holdBeforeMs, holdAfterMs, steps int) (dragErr error) {
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

	if err := settlePointer(pc, start.x, start.y); err != nil {
		return err
	}
	if err := pressPointer(pc, start.x, start.y, button); err != nil {
		return err
	}

	defer func() {
		relErr := releasePointerRepeated(pc, end.x, end.y, touchReleaseReportCount, touchReleaseReportDelayMs)
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
		if err := movePointer(pc, x, y, button); err != nil {
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

func clampInt(val, min, max int) int {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
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

func parseKeyboardTextInput(input string) (string, string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", "error: text is required"
	}

	if strings.HasPrefix(trimmed, "{") {
		var args struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
			return "", fmt.Sprintf("error: invalid input: %v", err)
		}
		if args.Text == "" {
			return "", "error: text is required"
		}
		return args.Text, ""
	}

	if strings.HasPrefix(trimmed, `"`) {
		var text string
		if err := json.Unmarshal([]byte(trimmed), &text); err != nil {
			return "", fmt.Sprintf("error: invalid input: %v", err)
		}
		if text == "" {
			return "", "error: text is required"
		}
		return text, ""
	}

	return trimmed, ""
}

func unsupportedKeyboardTextRunes(text string) []rune {
	unsupported := make([]rune, 0)
	for _, ch := range text {
		if ch > 0x7F {
			unsupported = append(unsupported, ch)
			continue
		}
		if _, _, ok := charToHIDKey(byte(ch)); !ok {
			unsupported = append(unsupported, ch)
		}
	}
	return unsupported
}
