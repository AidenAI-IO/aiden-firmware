package mnk

import "context"

// Provider defines a minimal set of mouse/keyboard primitives for device input.
// This interface isolates tools from device-specific implementations (HID, ADB, etc).
//
// Coordinate system: Normalized 0-1000 scale where (0,0) is top-left, (1000,1000) is bottom-right.
// The provider implementation handles conversion to device-specific absolute coordinates.
//
// Context: Callers must pass the request/tool context so platform gates (e.g. iOS
// keyboard isolation batch) can observe sticky isolate/restore across a run.
type Provider interface {
	// Click performs a press-hold-release at the specified position.
	// For touchscreen mode: simulates finger tap (button parameter ignored).
	// For absolute mouse mode: performs button click with proper button state.
	//
	// Parameters:
	//   x, y: Normalized coordinates (0-1000)
	//   button: "left" | "right" | "middle" (ignored in touchscreen mode)
	//   holdMs: Duration to hold before release. Use higher values (500+) for long-press.
	//           Default timing is handled by implementation (typically 60ms for tap).
	Click(ctx context.Context, x, y float64, button string, holdMs int) error

	// DoubleClick performs two clicks in rapid succession.
	// The pause between clicks is handled by implementation (typically 100ms).
	DoubleClick(ctx context.Context, x, y float64, button string) error

	// Drag performs a gesture along a path of points.
	// The path is interpolated smoothly with implementation-defined timing and steps.
	//
	// Parameters:
	//   path: Sequence of (x,y) normalized coordinates. Must have at least 2 points.
	//   button: "left" | "right" | "middle" (ignored in touchscreen mode)
	//
	// Implementation handles:
	//   - Smooth interpolation between consecutive points
	//   - Appropriate dwell before movement (for edge gesture recognition)
	//   - Step count for motion smoothness
	//   - Platform-specific timing requirements
	Drag(ctx context.Context, path [][2]float64, button string) error

	// Keypress sends one or more keys simultaneously.
	// Supports modifier+key combinations as a single chord.
	//
	// Parameters:
	//   keys: Array of key names to press simultaneously, e.g.:
	//         ["enter"]                    - single key
	//         ["ctrl", "a"]                - Ctrl+A chord
	//         ["ctrl", "shift", "escape"]  - three-key combination
	//
	// Supported key names:
	//   - Letters: "a"-"z"
	//   - Numbers: "0"-"9"
	//   - Modifiers: "ctrl", "shift", "alt", "meta"/"cmd" (with l/r variants: "lctrl", "rshift", etc.)
	//   - Function: "f1"-"f12"
	//   - Navigation: "up", "down", "left", "right", "home", "end", "pageup", "pagedown"
	//   - Edit: "enter", "escape", "backspace", "tab", "space", "delete"
	//   - Android extensions: "volume_up", "volume_down", "android_back", "android_home", "power", etc.
	//
	// Implementation handles proper HID report construction with modifier byte + key array.
	Keypress(ctx context.Context, keys []string) error

	// Move positions the pointer without pressing any button.
	// In touchscreen mode, this is typically unsupported (no hover).
	// In absolute mouse mode, moves the cursor to the specified position.
	Move(ctx context.Context, x, y float64) error

	// Scroll sends wheel/scroll input.
	//
	// Parameters:
	//   scrollX: Horizontal scroll delta (positive = right)
	//   scrollY: Vertical scroll delta (positive = up)
	//
	// In touchscreen mode, implementation may convert to swipe gestures.
	// In absolute mouse mode, sends wheel events.
	Scroll(ctx context.Context, scrollX, scrollY int) error
}

// Point represents a normalized coordinate point.
type Point struct {
	X float64 // Normalized X coordinate (0-1000)
	Y float64 // Normalized Y coordinate (0-1000)
}

// Button constants for click/drag operations.
const (
	ButtonLeft   = "left"
	ButtonRight  = "right"
	ButtonMiddle = "middle"
)

// Common key name constants for Keypress.
const (
	// Modifiers
	KeyCtrl  = "ctrl"
	KeyShift = "shift"
	KeyAlt   = "alt"
	KeyMeta  = "meta"
	KeyCmd   = "cmd"

	// Common keys
	KeyEnter     = "enter"
	KeyEscape    = "escape"
	KeyBackspace = "backspace"
	KeyTab       = "tab"
	KeySpace     = "space"
	KeyDelete    = "delete"

	// Navigation
	KeyUp       = "up"
	KeyDown     = "down"
	KeyLeft     = "left"
	KeyRight    = "right"
	KeyHome     = "home"
	KeyEnd      = "end"
	KeyPageUp   = "pageup"
	KeyPageDown = "pagedown"

	// Android extensions
	KeyVolumeUp       = "volume_up"
	KeyVolumeDown     = "volume_down"
	KeyAndroidBack    = "android_back"
	KeyAndroidHome    = "android_home"
	KeyPower          = "power"
	KeyMediaPlayPause = "media_play_pause"
)
