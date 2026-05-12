package agent

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

const (
	absMouseMaxPos = 32767
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
	"f1": 0x3a, "f2": 0x3b, "f3": 0x3c, "f4": 0x3d,
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
	file *os.File
}

func NewHIDDevice(path string) *HIDDevice {
	return &HIDDevice{path: path}
}

func (d *HIDDevice) Write(data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.file == nil {
		f, err := os.OpenFile(d.path, os.O_WRONLY, 0)
		if err != nil {
			return fmt.Errorf("open %s: %w", d.path, err)
		}
		d.file = f
	}

	_, err := d.file.Write(data)
	if err != nil {
		d.file.Close()
		d.file = nil
		return fmt.Errorf("write %s: %w", d.path, err)
	}
	return nil
}

func (d *HIDDevice) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file != nil {
		d.file.Close()
		d.file = nil
	}
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
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if len(args.Keys) == 0 {
		return "", fmt.Errorf("keys array is required")
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
			return "", fmt.Errorf("unknown key: %q", k)
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
		return "", err
	}
	// Release
	if err := t.dev.Write(make([]byte, 8)); err != nil {
		return "", err
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
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if args.Text == "" {
		return "", fmt.Errorf("text is required")
	}

	for _, ch := range args.Text {
		modifier, code, ok := charToHIDKey(byte(ch))
		if !ok {
			continue
		}
		report := make([]byte, 8)
		report[0] = modifier
		report[2] = code
		if err := t.dev.Write(report); err != nil {
			return "", err
		}
		if err := t.dev.Write(make([]byte, 8)); err != nil {
			return "", err
		}
	}

	return "ok", nil
}

// MouseClickTool moves the mouse to absolute coordinates and clicks.
type MouseClickTool struct {
	dev *HIDDevice
}

func (t *MouseClickTool) Name() string { return "mouse_click" }

func (t *MouseClickTool) Description() string {
	return `Move mouse to absolute position and click. Input JSON: {"x": 500, "y": 300, "button": "left"}. ` +
		`Coordinates are in screen pixels. Button options: "left" (default), "right", "middle".`
}

func (t *MouseClickTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		X      int    `json:"x"`
		Y      int    `json:"y"`
		Button string `json:"button"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	btn := mouseButtonByte(args.Button)

	// Press
	if err := writeAbsMouseReport(t.dev, args.X, args.Y, btn); err != nil {
		return "", err
	}
	// Release
	if err := writeAbsMouseReport(t.dev, args.X, args.Y, 0); err != nil {
		return "", err
	}

	return "ok", nil
}

// MouseMoveTool moves the mouse to absolute coordinates without clicking.
type MouseMoveTool struct {
	dev *HIDDevice
}

func (t *MouseMoveTool) Name() string { return "mouse_move" }

func (t *MouseMoveTool) Description() string {
	return `Move mouse to absolute position without clicking. Input JSON: {"x": 500, "y": 300}. ` +
		`Coordinates are in screen pixels.`
}

func (t *MouseMoveTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	if err := writeAbsMouseReport(t.dev, args.X, args.Y, 0); err != nil {
		return "", err
	}

	return "ok", nil
}

// MouseScrollTool sends mouse wheel events.
type MouseScrollTool struct {
	dev *HIDDevice
}

func (t *MouseScrollTool) Name() string { return "mouse_scroll" }

func (t *MouseScrollTool) Description() string {
	return `Scroll the mouse wheel. Input JSON: {"delta": -3}. ` +
		`Positive values scroll up, negative scroll down. Range: -127 to 127.`
}

func (t *MouseScrollTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		Delta int `json:"delta"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if args.Delta == 0 {
		return "ok", nil
	}
	if args.Delta < -127 {
		args.Delta = -127
	} else if args.Delta > 127 {
		args.Delta = 127
	}

	// Wheel report: [report_id=2, wheel_value]
	report := []byte{0x02, byte(int8(args.Delta))}
	if err := t.dev.Write(report); err != nil {
		return "", err
	}

	return "ok", nil
}

// writeAbsMouseReport writes an absolute mouse position report.
// Report format: [report_id=1, buttons, x_low, x_high, y_low, y_high]
func writeAbsMouseReport(dev *HIDDevice, x, y int, buttons uint8) error {
	absX := clampUint16(x, absMouseMaxPos)
	absY := clampUint16(y, absMouseMaxPos)

	report := make([]byte, 6)
	report[0] = 0x01 // report ID
	report[1] = buttons
	binary.LittleEndian.PutUint16(report[2:4], absX)
	binary.LittleEndian.PutUint16(report[4:6], absY)

	return dev.Write(report)
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
