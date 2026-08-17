package mnk

import (
	"aiden-agent/internal/agent/screen"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"
)

// HIDProvider implements Provider interface for USB HID device control.
// This bridges the MNK interface to the existing HID implementation in tools_hid.go.
type HIDProvider struct {
	// pointerDev is the HID device for pointer (mouse/touchscreen)
	pointerDev Device

	// pointerState tracks current pointer position
	pointerState *pointerState

	// touchscreen indicates if we're in touchscreen mode vs absolute mouse mode
	touchscreen bool

	// keyboardDev is the HID device for standard boot keyboard
	keyboardDev Device

	// androidKeyboardDev is the Android extension keyboard device (Consumer Control)
	androidKeyboardDev Device

	// screenState provides coordinate resolution with active_area awareness
	screenState *screen.ScreenState

	// keyboardLayout for text input (qwerty/azerty/qwertz)
	keyboardLayout string

	// gate optionally wraps keyboard/pointer writes for iOS absolute isolation.
	gate ProfileGate

	// Timing constants (internal, not exposed to callers)
	tapHoldMs             int // Default tap hold duration (60ms for iOS)
	swipeHoldBeforeMs     int // Dwell before swipe begins (80ms for iOS edge gestures)
	swipeHoldAfterMs      int // Dwell at swipe end (0ms to avoid stuck state)
	swipeDurationMs       int // Total swipe duration (700ms)
	swipeSteps            int // Interpolation steps (24)
	cursorSettleMs        int // Cursor settle delay for absolute mode (80ms)
	releaseRepeatCount    int // Touch release repetition (3)
	releaseRepeatDelayMs  int // Delay between release repeats (15ms)
	keyboardTapHoldMs     int // Default keyboard tap hold (50ms)
	keyboardModifierHoldMs int // Keyboard modifier hold (120ms)
}

// Default timing values based on iOS/Android HID requirements
const (
	defaultTapHoldMs             = 60  // iOS drops faster events
	defaultSwipeHoldBeforeMs     = 80  // iOS edge gesture recognition
	defaultSwipeHoldAfterMs      = 0   // Avoid stuck dragged state
	defaultSwipeDurationMs       = 700 // Low-inertia motion
	defaultSwipeSteps            = 24  // Smooth interpolation
	defaultCursorSettleMs        = 80  // iOS cursor animation
	defaultReleaseRepeatCount    = 3   // USB polling workaround
	defaultReleaseRepeatDelayMs  = 15  // Delay between releases
	defaultDoubleClickPauseMs    = 100 // Pause between double-click taps
	defaultKeyboardTapHoldMs     = 50  // Standard key hold
	defaultKeyboardModifierHoldMs = 120 // Modifier chord hold
	absMouseMaxPos               = 32767            // HID absolute coordinate max
	screenDimensionsStaleAfter   = 30 * time.Second // Max age for cached screen dimensions
)

// NewHIDProvider creates a new HID-based MNK provider.
// Devices should be shared with any ProfileGate that closes FDs across USB profile switches.
func NewHIDProvider(pointerDev, keyboardDev, androidKeyboardDev Device, screenState *screen.ScreenState, touchscreen bool, keyboardLayout string, gate ProfileGate) *HIDProvider {
	if keyboardLayout == "" {
		keyboardLayout = "qwerty"
	}

	return &HIDProvider{
		pointerDev:            pointerDev,
		pointerState:          &pointerState{},
		touchscreen:           touchscreen,
		keyboardDev:           keyboardDev,
		androidKeyboardDev:    androidKeyboardDev,
		screenState:           screenState,
		keyboardLayout:        keyboardLayout,
		gate:                  gate,
		tapHoldMs:             defaultTapHoldMs,
		swipeHoldBeforeMs:     defaultSwipeHoldBeforeMs,
		swipeHoldAfterMs:      defaultSwipeHoldAfterMs,
		swipeDurationMs:       defaultSwipeDurationMs,
		swipeSteps:            defaultSwipeSteps,
		cursorSettleMs:        defaultCursorSettleMs,
		releaseRepeatCount:    defaultReleaseRepeatCount,
		releaseRepeatDelayMs:  defaultReleaseRepeatDelayMs,
		keyboardTapHoldMs:     defaultKeyboardTapHoldMs,
		keyboardModifierHoldMs: defaultKeyboardModifierHoldMs,
	}
}

// Click performs a press-hold-release at the specified position.
func (p *HIDProvider) Click(ctx context.Context, x, y float64, button string, holdMs int) error {
	return runPointerGate(p.gate, ctx, func() error {
		return p.clickLocked(x, y, button, holdMs)
	})
}

func (p *HIDProvider) clickLocked(x, y float64, button string, holdMs int) error {
	if err := p.validateCoordinate(x, y); err != nil {
		return err
	}

	absX, absY, err := p.normalizedToAbsolute(x, y)
	if err != nil {
		return err
	}

	buttonByte := p.mouseButtonByte(button)

	// Use caller-provided holdMs if specified, otherwise use default
	if holdMs <= 0 {
		holdMs = p.tapHoldMs
	}

	// Settle cursor if in absolute mouse mode (not needed for touchscreen)
	if !p.touchscreen {
		if err := p.settlePointer(absX, absY); err != nil {
			return err
		}
	}

	// Press
	if err := p.pressPointer(absX, absY, buttonByte); err != nil {
		return err
	}

	// Hold
	time.Sleep(time.Duration(holdMs) * time.Millisecond)

	// Release with repetition for touchscreen mode
	return p.releasePointerRepeated(absX, absY)
}

// DoubleClick performs two clicks in rapid succession.
func (p *HIDProvider) DoubleClick(ctx context.Context, x, y float64, button string) error {
	return runPointerGate(p.gate, ctx, func() error {
		if err := p.clickLocked(x, y, button, 0); err != nil {
			return err
		}
		time.Sleep(time.Duration(defaultDoubleClickPauseMs) * time.Millisecond)
		return p.clickLocked(x, y, button, 0)
	})
}

// Drag performs a gesture along a path of points.
func (p *HIDProvider) Drag(ctx context.Context, path [][2]float64, button string) error {
	return runPointerGate(p.gate, ctx, func() error {
		return p.dragLocked(path, button)
	})
}

func (p *HIDProvider) dragLocked(path [][2]float64, button string) error {
	if len(path) < 2 {
		return fmt.Errorf("drag path must contain at least 2 points, got %d", len(path))
	}

	// Validate all points first
	for i, point := range path {
		if err := p.validateCoordinate(point[0], point[1]); err != nil {
			return fmt.Errorf("invalid point %d in path: %w", i, err)
		}
	}

	// Convert path to absolute coordinates
	absPath := make([][2]int, len(path))
	for i, point := range path {
		absX, absY, err := p.normalizedToAbsolute(point[0], point[1])
		if err != nil {
			return fmt.Errorf("failed to convert point %d: %w", i, err)
		}
		absPath[i] = [2]int{absX, absY}
	}

	buttonByte := p.mouseButtonByte(button)

	// Settle cursor at start position
	if !p.touchscreen {
		if err := p.settlePointer(absPath[0][0], absPath[0][1]); err != nil {
			return err
		}
	}

	// Press at start
	if err := p.pressPointer(absPath[0][0], absPath[0][1], buttonByte); err != nil {
		return err
	}

	// Hold before starting movement
	time.Sleep(time.Duration(p.swipeHoldBeforeMs) * time.Millisecond)

	// Interpolate and move through each segment
	if err := p.dragAlongPath(absPath, buttonByte); err != nil {
		// Attempt to release even if drag failed
		_ = p.releasePointerRepeated(absPath[len(absPath)-1][0], absPath[len(absPath)-1][1])
		return err
	}

	// Hold at end
	if p.swipeHoldAfterMs > 0 {
		time.Sleep(time.Duration(p.swipeHoldAfterMs) * time.Millisecond)
	}

	// Release at final position
	endPoint := absPath[len(absPath)-1]
	return p.releasePointerRepeated(endPoint[0], endPoint[1])
}

// dragAlongPath interpolates and moves through a multi-point path.
func (p *HIDProvider) dragAlongPath(absPath [][2]int, buttonByte uint8) error {
	// Calculate total path length for timing distribution
	totalLength := 0.0
	for i := 1; i < len(absPath); i++ {
		dx := float64(absPath[i][0] - absPath[i-1][0])
		dy := float64(absPath[i][1] - absPath[i-1][1])
		totalLength += math.Sqrt(dx*dx + dy*dy)
	}

	if totalLength == 0 {
		return nil // All points are the same
	}

	// Distribute steps proportionally across segments
	for i := 1; i < len(absPath); i++ {
		start := absPath[i-1]
		end := absPath[i]

		dx := float64(end[0] - start[0])
		dy := float64(end[1] - start[1])
		segmentLength := math.Sqrt(dx*dx + dy*dy)

		// Proportional step count and duration for this segment
		segmentSteps := int(math.Round(float64(p.swipeSteps) * segmentLength / totalLength))
		if segmentSteps < 1 {
			segmentSteps = 1
		}
		segmentDurationMs := int(math.Round(float64(p.swipeDurationMs) * segmentLength / totalLength))

		// Interpolate along this segment
		stepDelayMs := 0
		if segmentSteps > 0 {
			stepDelayMs = segmentDurationMs / segmentSteps
		}

		for step := 1; step <= segmentSteps; step++ {
			progress := float64(step) / float64(segmentSteps)
			x := start[0] + int(math.Round(dx*progress))
			y := start[1] + int(math.Round(dy*progress))

			if err := p.movePointer(x, y, buttonByte); err != nil {
				return err
			}

			if step < segmentSteps && stepDelayMs > 0 {
				time.Sleep(time.Duration(stepDelayMs) * time.Millisecond)
			}
		}
	}

	return nil
}

// Keypress sends one or more keys simultaneously.
func (p *HIDProvider) Keypress(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return fmt.Errorf("keypress requires at least one key")
	}

	// Resolve keys into modifiers and key codes
	resolved, err := p.resolveKeys(keys)
	if err != nil {
		return err
	}

	// Check if this is an Android extension key
	if resolved.androidExtensionKey != "" {
		return runExtraKeysGate(p.gate, ctx, func() error {
			return p.tapAndroidExtension(resolved.androidExtensionKey, resolved.androidUsage)
		})
	}

	isolate := resolved.modifier != 0
	return runKeyboardGate(p.gate, ctx, isolate, func() error {
		return p.tapKeyboardChord(resolved.modifier, resolved.keys)
	})
}

// Move positions the pointer without pressing any button.
func (p *HIDProvider) Move(ctx context.Context, x, y float64) error {
	return runPointerGate(p.gate, ctx, func() error {
		if err := p.validateCoordinate(x, y); err != nil {
			return err
		}

		if p.touchscreen {
			return InvalidArguments("move is unsupported in touchscreen mode (no hover capability)")
		}

		absX, absY, err := p.normalizedToAbsolute(x, y)
		if err != nil {
			return err
		}

		return p.movePointer(absX, absY, 0)
	})
}

// Scroll sends wheel/scroll input.
func (p *HIDProvider) Scroll(ctx context.Context, scrollX, scrollY int) error {
	if p.touchscreen {
		// In touchscreen mode, convert to swipe gesture (pointer gate via Drag)
		return p.scrollAsSwipe(ctx, scrollX, scrollY)
	}

	return runPointerGate(p.gate, ctx, func() error {
		// Absolute mouse mode: send wheel events
		// Current HID implementation only supports vertical scrolling
		if scrollY != 0 {
			if err := p.scrollPointer(scrollY); err != nil {
				return err
			}
		}

		// Note: scrollX is not implemented in current HID absolute mouse mode
		// This would require horizontal wheel support in the HID descriptor

		return nil
	})
}

// scrollAsSwipe converts scroll to a swipe gesture for touchscreen mode.
func (p *HIDProvider) scrollAsSwipe(ctx context.Context, scrollX, scrollY int) error {
	// Convert scroll delta to swipe gesture
	// Negative scrollY = content moves down (finger swipes up)
	// Positive scrollY = content moves up (finger swipes down)

	centerX := 500.0
	centerY := 500.0
	distance := 200.0 // Small swipe distance

	if math.Abs(float64(scrollY)) > math.Abs(float64(scrollX)) {
		// Vertical scroll is dominant
		if scrollY < 0 {
			// Swipe up
			return p.Drag(ctx, [][2]float64{
				{centerX, centerY + distance/2},
				{centerX, centerY - distance/2},
			}, ButtonLeft)
		}
		// Swipe down
		return p.Drag(ctx, [][2]float64{
			{centerX, centerY - distance/2},
			{centerX, centerY + distance/2},
		}, ButtonLeft)
	} else if scrollX != 0 {
		// Horizontal scroll is dominant
		if scrollX < 0 {
			// Swipe left
			return p.Drag(ctx, [][2]float64{
				{centerX + distance/2, centerY},
				{centerX - distance/2, centerY},
			}, ButtonLeft)
		}
		// Swipe right
		return p.Drag(ctx, [][2]float64{
			{centerX - distance/2, centerY},
			{centerX + distance/2, centerY},
		}, ButtonLeft)
	}

	return nil
}

// ============================================================================
// Low-level HID primitives (delegating to existing tools_hid.go implementations)
// ============================================================================

// ============================================================================
// Coordinate conversion and validation
// ============================================================================

func (p *HIDProvider) validateCoordinate(x, y float64) error {
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return InvalidArguments("coordinates must be finite")
	}
	if x < 0 || x > 1000 || y < 0 || y > 1000 {
		return InvalidArgumentsf("coordinates must be in range 0-1000, got x=%.2f y=%.2f", x, y)
	}
	return nil
}

func (p *HIDProvider) normalizedToAbsolute(x, y float64) (int, int, error) {
	// Uses the same logic as tools_hid.go:normalizedToAbsolutePointForSurface
	// Handles active_area for de-blackbarring and touchscreen vs absolute mode
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return 0, 0, fmt.Errorf("coordinates must be finite")
	}
	if x < 0 || x > 1000 || y < 0 || y > 1000 {
		return 0, 0, fmt.Errorf("coordinates must use the normalized 0-1000 scale, got x=%.2f y=%.2f", x, y)
	}

	// Try to use active_area from screen state
	if p.screenState != nil {
		if width, height, active, age, ok := p.screenState.ActiveAreaWithAge(); ok && age < screenDimensionsStaleAfter && active.Valid {
			activePixelX := (clampFloat(x, 0, 1000) / 1000.0) * float64(active.Width-1)
			activePixelY := (clampFloat(y, 0, 1000) / 1000.0) * float64(active.Height-1)

			if p.touchscreen {
				// Touchscreen mode: project to active_area, then scale against full frame
				fullFramePixelX := float64(active.X) + activePixelX
				fullFramePixelY := float64(active.Y) + activePixelY
				absX := scalePixelToAbsolute(fullFramePixelX, width)
				absY := scalePixelToAbsolute(fullFramePixelY, height)
				return absX, absY, nil
			}

			// Absolute mouse mode: scale within active_area coordinate system
			absX := activeLocalAxisToAbsolute(activePixelX, active.X, active.Width, width)
			absY := activeLocalAxisToAbsolute(activePixelY, active.Y, active.Height, height)
			return absX, absY, nil
		}
	}

	// Fallback: simple linear mapping
	absX := int(math.Round(clampFloat(x, 0, 1000) / 1000.0 * absMouseMaxPos))
	absY := int(math.Round(clampFloat(y, 0, 1000) / 1000.0 * absMouseMaxPos))
	return absX, absY, nil
}

const nearlyFullSourceAxisRatio = 0.9

func activeLocalAxisToAbsolute(local float64, activeStart, activeSize, sourceSize int) int {
	if activeSize <= 0 || sourceSize <= 0 {
		return 0
	}
	// If active area covers nearly the whole source, preserve offset to cancel jitter
	if float64(activeSize)/float64(sourceSize) >= nearlyFullSourceAxisRatio {
		return scalePixelToAbsolute(float64(activeStart)+local, sourceSize)
	}
	return scalePixelToAbsolute(local, activeSize)
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

func (p *HIDProvider) mouseButtonByte(button string) uint8 {
	normalized := strings.ToLower(strings.TrimSpace(button))
	switch normalized {
	case "right":
		return 0x02
	case "middle":
		return 0x04
	default:
		return 0x01 // left
	}
}

// ============================================================================
// Pointer operations (touchscreen and absolute mouse)
// ============================================================================

func (p *HIDProvider) settlePointer(x, y int) error {
	// Move cursor and wait for animation to complete (absolute mouse mode only)
	if err := p.movePointer(x, y, 0); err != nil {
		return err
	}
	time.Sleep(time.Duration(p.cursorSettleMs) * time.Millisecond)
	return nil
}

func (p *HIDProvider) pressPointer(x, y int, button uint8) error {
	return p.movePointer(x, y, button)
}

func (p *HIDProvider) releasePointerRepeated(x, y int) error {
	// Repeat release multiple times for touchscreen USB polling workaround
	var firstErr error
	released := false
	for i := 0; i < p.releaseRepeatCount; i++ {
		if err := p.movePointer(x, y, 0); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			released = true
		}
		if i < p.releaseRepeatCount-1 {
			time.Sleep(time.Duration(p.releaseRepeatDelayMs) * time.Millisecond)
		}
	}
	if released {
		return nil
	}
	return firstErr
}

func (p *HIDProvider) movePointer(x, y int, buttons uint8) error {
	if p.touchscreen {
		return p.writeTouchscreenReport(x, y, buttons != 0)
	}
	return p.writeAbsMouseReport(x, y, buttons, 0)
}

func (p *HIDProvider) scrollPointer(delta int) error {
	if p.touchscreen {
		return nil // Scroll not supported in touchscreen mode
	}
	if delta == 0 {
		return nil
	}
	if delta < -127 {
		delta = -127
	} else if delta > 127 {
		delta = 127
	}

	// Get current position
	x, y := p.getCurrentPosition()
	return p.writeAbsMouseReport(x, y, 0, int8(delta))
}

func (p *HIDProvider) getCurrentPosition() (int, int) {
	if p.pointerState != nil {
		if x, y, ok := p.pointerState.Current(); ok {
			return x, y
		}
	}
	return absMouseMaxPos / 2, absMouseMaxPos / 2
}

// ============================================================================
// HID report writing
// ============================================================================

func writeDevice(dev Device, report []byte) error {
	if dev == nil {
		return fmt.Errorf("hid device is not configured")
	}
	return dev.Write(report)
}

// writeAbsMouseReport writes a 6-byte absolute mouse report:
// [buttons, x_lo, x_hi, y_lo, y_hi, wheel]
func (p *HIDProvider) writeAbsMouseReport(x, y int, buttons uint8, wheel int8) error {
	absX := clampUint16(x, absMouseMaxPos)
	absY := clampUint16(y, absMouseMaxPos)

	report := make([]byte, 6)
	report[0] = buttons
	binary.LittleEndian.PutUint16(report[1:3], absX)
	binary.LittleEndian.PutUint16(report[3:5], absY)
	report[5] = byte(wheel)

	// Update state after successful write
	if err := writeDevice(p.pointerDev, report); err != nil {
		return err
	}

	if p.pointerState != nil {
		p.pointerState.Update(int(absX), int(absY))
	}
	return nil
}

// writeTouchscreenReport writes a 6-byte single-contact touch report:
// [flags, contact_id, x_lo, x_hi, y_lo, y_hi]
// flags: bit0=tip switch (touching), bit1=in range
func (p *HIDProvider) writeTouchscreenReport(x, y int, touching bool) error {
	absX := clampUint16(x, absMouseMaxPos)
	absY := clampUint16(y, absMouseMaxPos)

	report := make([]byte, 6)
	if touching {
		report[0] = 0x03 // tip switch + in range
	}
	report[1] = 0x01 // contact_id
	binary.LittleEndian.PutUint16(report[2:4], absX)
	binary.LittleEndian.PutUint16(report[4:6], absY)

	// Update state after successful write
	if err := writeDevice(p.pointerDev, report); err != nil {
		return err
	}

	if p.pointerState != nil {
		p.pointerState.Update(int(absX), int(absY))
	}
	return nil
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

// ============================================================================
// Keyboard operations
// ============================================================================

type resolvedKeys struct {
	modifier            uint8
	keys                []uint8
	androidExtensionKey string
	androidUsage        uint16
}

// ResolveKeypressKeys validates and expands keyboard_tap / Keypress key names
// (including Android KEYCODE_* aliases) without requiring a configured device.
func ResolveKeypressKeys(keys []string) (*resolvedKeys, error) {
	resolved := &resolvedKeys{keys: make([]uint8, 0, 6)}
	androidKeys := make([]string, 0, 1)

	for _, key := range keys {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if normalized == "" {
			continue
		}

		if alias, ok := androidKeyboardTapAliases[normalized]; ok {
			if alias.UnsupportedReason != "" {
				return nil, InvalidArgumentsf("android-only key %q (keycode %d) is not supported by keyboard_tap: %s", normalized, alias.Keycode, alias.UnsupportedReason)
			}
			normalized = alias.Replacement
		}

		if usage, ok := androidExtensionUsageMap[normalized]; ok {
			androidKeys = append(androidKeys, normalized)
			resolved.androidUsage = usage
			continue
		}

		if mod, ok := hidModifierMap[normalized]; ok {
			resolved.modifier |= mod
			continue
		}

		if usage, ok := hidKeyboardMap[normalized]; ok {
			resolved.keys = append(resolved.keys, usage)
			continue
		}

		return nil, InvalidArgumentsf("unknown key: %q", normalized)
	}

	if len(androidKeys) > 1 {
		return nil, InvalidArgumentsf("keypress supports one Android extension key at a time, got %v", androidKeys)
	}
	if len(androidKeys) == 1 {
		if len(resolved.keys) > 0 || resolved.modifier != 0 {
			return nil, InvalidArgumentsf("Android extension key %q cannot be combined with standard keyboard keys or modifiers", androidKeys[0])
		}
		resolved.androidExtensionKey = androidKeys[0]
		return resolved, nil
	}

	if len(resolved.keys) == 0 && resolved.modifier == 0 {
		return nil, InvalidArguments("at least one key or modifier is required")
	}
	if len(resolved.keys) > 6 {
		return nil, InvalidArguments("keypress supports at most 6 simultaneous keys")
	}

	return resolved, nil
}

func (p *HIDProvider) resolveKeys(keys []string) (*resolvedKeys, error) {
	return ResolveKeypressKeys(keys)
}

func (p *HIDProvider) androidExtensionPressReport(key string, usage uint16) ([]byte, error) {
	// Touchscreen gadgets accept standard Consumer Control usage LE reports.
	if p != nil && p.touchscreen {
		return []byte{byte(usage), byte(usage >> 8)}, nil
	}
	// Absolute pointer-mode hid.usb2 uses a packed bitfield report and only
	// exposes a media/volume/brightness/screenshot subset.
	report, ok := absolutePointerModeExtensionReports[key]
	if !ok {
		return nil, InvalidArgumentsf("android extension key is unavailable in the configured pointer mode: %q requires hid.pointer_mode=\"touchscreen\"; hid.pointer_mode=\"absolute\" only exposes these hid.usb2 keys: %s", key, absolutePointerModeExtensionKeyList)
	}
	return []byte{byte(report), byte(report >> 8)}, nil
}

func (p *HIDProvider) tapAndroidExtension(key string, usage uint16) error {
	if p.androidKeyboardDev == nil {
		return ModuleUnavailablef("android extension keyboard device is not configured; ensure hid.android_keyboard_device exists to use %s", key)
	}

	report, err := p.androidExtensionPressReport(key, usage)
	if err != nil {
		return err
	}

	if err := writeDevice(p.androidKeyboardDev, report); err != nil {
		return err
	}

	holdMs := p.keyboardTapHoldMs
	time.Sleep(time.Duration(holdMs) * time.Millisecond)

	return writeDevice(p.androidKeyboardDev, []byte{0x00, 0x00})
}

func (p *HIDProvider) tapKeyboardChord(modifier uint8, keys []uint8) error {
	// 8-byte HID boot keyboard report: [modifier, reserved, key1..key6]
	report := make([]byte, 8)
	report[0] = modifier
	for i := 0; i < len(keys) && i < 6; i++ {
		report[2+i] = keys[i]
	}

	// Press
	if err := writeDevice(p.keyboardDev, report); err != nil {
		return err
	}

	// Hold (longer for modifier chords)
	holdMs := p.keyboardTapHoldMs
	if modifier != 0 {
		holdMs = p.keyboardModifierHoldMs
	}
	time.Sleep(time.Duration(holdMs) * time.Millisecond)

	// Release (all zeros)
	return writeDevice(p.keyboardDev, make([]byte, 8))
}
