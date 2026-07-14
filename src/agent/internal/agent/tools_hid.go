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
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	absMouseMaxPos = 32767

	defaultHIDRefreshStatePath = "/run/aiden_usb_ecm_watchdog.state"

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
	defaultDirectionalSwipeDistance = 700.0
	directionalSwipeLargeDistance   = 700.0
	directionalSwipeMediumDistance  = 500.0
	directionalSwipeSmallDistance   = 200.0
	directionalSwipeTinyDistance    = 40.0

	wheelNudgeDefaultY     = 460.0
	wheelNudgeDefaultMs    = 1400
	wheelNudgeDefaultSteps = 18

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

var androidExtensionUsageMap = map[string]uint16{
	"android_back":                0x0224,
	"android_home":                0x0223,
	"menu":                        0x0040,
	"search":                      0x0221,
	"power":                       0x0030,
	"sleep":                       0x0032,
	"volume_mute":                 0x00e2,
	"volumeup":                    0x00e9,
	"volume_up":                   0x00e9,
	"volumedown":                  0x00ea,
	"volume_down":                 0x00ea,
	"media_fast_forward":          0x00b3,
	"media_rewind":                0x00b4,
	"media_next":                  0x00b5,
	"media_previous":              0x00b6,
	"media_stop":                  0x00b7,
	"media_play_pause":            0x00cd,
	"screenshot":                  0x0065,
	"key_usage_screenshot":        0x0065,
	"window":                      0x0067,
	"key_usage_window":            0x0067,
	"brightness_up":               0x006f,
	"key_usage_brightness_up":     0x006f,
	"brightness_down":             0x0070,
	"key_usage_brightness_down":   0x0070,
	"dictate":                     0x00d8,
	"key_usage_dictate":           0x00d8,
	"emoji_picker":                0x00d9,
	"key_usage_emoji_picker":      0x00d9,
	"media_audio_track":           0x0173,
	"key_usage_media_audio_track": 0x0173,
	"profile_switch":              0x019c,
	"key_usage_profile_switch":    0x019c,
	"settings":                    0x019f,
	"key_usage_settings":          0x019f,
	"new":                         0x0201,
	"key_usage_new":               0x0201,
	"close":                       0x0203,
	"key_usage_close":             0x0203,
	"print":                       0x0208,
	"key_usage_print":             0x0208,
	"refresh":                     0x0227,
	"key_usage_refresh":           0x0227,
	"fullscreen":                  0x0232,
	"key_usage_fullscreen":        0x0232,
	"language_switch":             0x029d,
	"key_usage_language_switch":   0x029d,
	// AOSP Generic.kl checks HID usage codes before Linux scan codes.
	// 0x0c01A2 is mapped to ALL_APPS, while 0x0c029F is mapped to
	// RECENT_APPS / KEYCODE_APP_SWITCH.
	"app_switch": 0x029f,
}

var absolutePointerModeExtensionReports = map[string]uint16{
	"volume_mute":               1 << 0,
	"volumeup":                  1 << 1,
	"volume_up":                 1 << 1,
	"volumedown":                1 << 2,
	"volume_down":               1 << 2,
	"media_play_pause":          1 << 3,
	"media_stop":                1 << 4,
	"media_next":                1 << 5,
	"media_previous":            1 << 6,
	"media_rewind":              1 << 7,
	"media_fast_forward":        1 << 8,
	"screenshot":                1 << 9,
	"key_usage_screenshot":      1 << 9,
	"brightness_up":             1 << 10,
	"key_usage_brightness_up":   1 << 10,
	"brightness_down":           1 << 11,
	"key_usage_brightness_down": 1 << 11,
}

const absolutePointerModeExtensionKeyList = "KEYCODE_VOLUME_MUTE, KEYCODE_VOLUME_UP, KEYCODE_VOLUME_DOWN, KEYCODE_MEDIA_PLAY_PAUSE, KEYCODE_MEDIA_STOP, KEYCODE_MEDIA_NEXT, KEYCODE_MEDIA_PREVIOUS, KEYCODE_MEDIA_REWIND, KEYCODE_MEDIA_FAST_FORWARD, KEY_USAGE_SCREENSHOT, KEY_USAGE_BRIGHTNESS_UP, KEY_USAGE_BRIGHTNESS_DOWN"

type androidKeyboardTapAlias struct {
	Keycode           int
	Replacement       string
	UnsupportedReason string
}

// Keep Android framework keycodes separate from the generic HID usage table.
// They are routed through hid.usb2, which advertises a Consumer Control
// interface instead of the boot keyboard report used by hid.usb0.
var androidKeyboardTapAliases = map[string]androidKeyboardTapAlias{
	"keycode_call": {
		Keycode:           5,
		UnsupportedReason: "call pickup requires an Android telephony/media key path beyond the current hid.usb2 Consumer Control interface",
	},
	"keycode_endcall": {
		Keycode:           6,
		UnsupportedReason: "call hangup requires an Android telephony/media key path beyond the current hid.usb2 Consumer Control interface",
	},
	"keycode_home": {
		Keycode:     3,
		Replacement: "android_home",
	},
	"keycode_menu": {
		Keycode:     82,
		Replacement: "menu",
	},
	"keycode_back": {
		Keycode:     4,
		Replacement: "android_back",
	},
	"keycode_search": {
		Keycode:     84,
		Replacement: "search",
	},
	"keycode_camera": {
		Keycode:           27,
		UnsupportedReason: "camera shutter requires a camera/media key path beyond the current hid.usb2 Consumer Control interface",
	},
	"keycode_focus": {
		Keycode:           80,
		UnsupportedReason: "camera focus requires a camera/media key path beyond the current hid.usb2 Consumer Control interface",
	},
	"keycode_power": {
		Keycode:     26,
		Replacement: "power",
	},
	"keycode_sleep": {
		Keycode:     223,
		Replacement: "sleep",
	},
	"keycode_wakeup": {
		Keycode:           224,
		UnsupportedReason: "wakeup requires a Generic Desktop/System Control HID path beyond the current hid.usb2 Consumer Control interface",
	},
	"keycode_soft_sleep": {
		Keycode:           276,
		UnsupportedReason: "soft sleep has no verified standard Consumer Control usage on this gadget",
	},
	"keycode_notification": {
		Keycode:           83,
		UnsupportedReason: "notification center has no verified standard Consumer Control usage on this gadget; use quick_action notification_center or touch_gesture instead",
	},
	"keycode_mute": {
		Keycode:           91,
		UnsupportedReason: "KEYCODE_MUTE is microphone mute, not speaker mute; hid.usb2 only exposes system speaker/stream volume mute",
	},
	"keycode_volume_mute": {
		Keycode:     164,
		Replacement: "volume_mute",
	},
	"keycode_volume_up": {
		Keycode:     24,
		Replacement: "volume_up",
	},
	"keycode_volume_down": {
		Keycode:     25,
		Replacement: "volume_down",
	},
	"keycode_media_play_pause": {
		Keycode:     85,
		Replacement: "media_play_pause",
	},
	"keycode_media_stop": {
		Keycode:     86,
		Replacement: "media_stop",
	},
	"keycode_media_next": {
		Keycode:     87,
		Replacement: "media_next",
	},
	"keycode_media_previous": {
		Keycode:     88,
		Replacement: "media_previous",
	},
	"keycode_media_rewind": {
		Keycode:     89,
		Replacement: "media_rewind",
	},
	"keycode_media_fast_forward": {
		Keycode:     90,
		Replacement: "media_fast_forward",
	},
	"keycode_app_switch": {
		Keycode:     187,
		Replacement: "app_switch",
	},
}

type keyboardTapResolvedInput struct {
	Keys                []string
	AndroidExtensionKey string
	AndroidUsage        uint16
}

// HIDDevice manages a single HID device file with lazy open and auto-reopen.
type HIDDevice struct {
	path         string
	mu           sync.Mutex
	file         io.WriteCloser
	open         func(string) (io.WriteCloser, error)
	writeTimeout time.Duration
	openedAt     time.Time
	refreshState string
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
		refreshState: defaultHIDRefreshStatePath,
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
	if err := d.reopenStaleFileLocked(); err != nil {
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
	d.openedAt = time.Now()
	return nil
}

func (d *HIDDevice) closeLocked() {
	if d.file != nil {
		_ = d.file.Close()
		d.file = nil
		d.openedAt = time.Time{}
	}
}

func (d *HIDDevice) reopenStaleFileLocked() error {
	if d.file == nil {
		return nil
	}
	if !d.openedAt.IsZero() && d.refreshState != "" {
		if info, err := os.Stat(d.refreshState); err == nil && info.ModTime().After(d.openedAt) {
			d.closeLocked()
			return d.ensureOpenLocked()
		}
	}

	statter, ok := d.file.(interface{ Stat() (os.FileInfo, error) })
	if !ok {
		return nil
	}
	fdInfo, err := statter.Stat()
	if err != nil {
		d.closeLocked()
		return d.ensureOpenLocked()
	}
	pathInfo, err := os.Stat(d.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		d.closeLocked()
		return d.ensureOpenLocked()
	}
	if !os.SameFile(fdInfo, pathInfo) {
		d.closeLocked()
		return d.ensureOpenLocked()
	}
	return nil
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
	dev         *HIDDevice
	androidDev  *HIDDevice
	pointerMode string
}

func (t *KeyboardTapTool) Name() string { return "keyboard_tap" }

func (t *KeyboardTapTool) Description() string {
	return `Press and release keyboard keys, pressed simultaneously as a chord (e.g. {"keys":["ctrl","c"]}). Prefer quick_action first for semantic platform actions; use keyboard_tap as a low-level fallback or for custom key input.`
}

func (t *KeyboardTapTool) ArgsSchema() map[string]any {
	keysSchema := stringArrayArgSchema("Keys pressed simultaneously, e.g. [\"ctrl\",\"c\"] or [\"meta\"]. "+
		"Standard boot-keyboard keys: a-z, 0-9, f1-f12, enter, escape, backspace, tab, space, delete, arrows, home, end, pageup/down, insert, printscreen; modifiers ctrl, shift, alt, meta/super/win/cmd; modifier-only taps allowed. "+
		"Use backspace for ordinary text deletion before the cursor; delete is forward-delete after the cursor. "+
		"Android extension keys (hid.usb2) use KEYCODE_*/KEY_USAGE_* aliases (see the Android key guide for the full list), are single-key taps only, and cannot be combined with modifiers/chords. "+
		"When hid.pointer_mode is absolute, hid.usb2 only supports media, volume, screenshot, and brightness keys: "+absolutePointerModeExtensionKeyList+".", []string{"ctrl", "c"}, []string{"meta"})
	keysSchema["minItems"] = 1
	keysSchema["maxItems"] = 6

	return objectArgsSchema(map[string]any{
		"keys":    keysSchema,
		"hold_ms": minIntegerArgSchema("Optional press duration before release (default 50ms, or 120ms when modifiers are used).", 0),
	}, "keys")
}

func (t *KeyboardTapTool) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		Keys   []string `json:"keys"`
		HoldMs int      `json:"hold_ms"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "invalid input: %v. Expected JSON format: {\"keys\": [\"ctrl\", \"c\"]}. Common mistakes: missing quotes around key names, incorrect comma placement in array", err), nil
	}
	if len(args.Keys) == 0 {
		return toolErrorResultString(ctx, CodeInvalidArguments, "keys array is required"), nil
	}

	resolved, err := resolveKeyboardTapKeys(args.Keys)
	if err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
	}

	if resolved.AndroidExtensionKey != "" {
		holdMs := args.HoldMs
		if holdMs <= 0 {
			holdMs = defaultKeyboardTapHoldMs
		}
		if err := t.tapAndroidExtension(resolved.AndroidExtensionKey, resolved.AndroidUsage, holdMs); err != nil {
			code := CodeToolExecutionFailed
			if errors.Is(err, errAndroidExtensionUnavailable) {
				code = CodeModuleUnavailable
			} else if errors.Is(err, errAndroidExtensionKeyUnavailableInPointerMode) {
				code = CodeInvalidArguments
			}
			return toolErrorResultf(ctx, code, "%v", err), nil
		}
		return "ok", nil
	}

	var modifier uint8
	var keys []uint8
	for _, k := range resolved.Keys {
		if mod, ok := hidModifierMap[k]; ok {
			modifier |= mod
		} else if code, ok := hidKeyboardMap[k]; ok {
			keys = append(keys, code)
		} else {
			return toolErrorResultf(ctx, CodeInvalidArguments, "unknown key: %q", k), nil
		}
	}
	if modifier == 0 && len(keys) == 0 {
		return toolErrorResultString(ctx, CodeInvalidArguments, "at least one key or modifier is required"), nil
	}

	holdMs := args.HoldMs
	if holdMs <= 0 {
		holdMs = defaultKeyboardTapHoldMs
		if modifier != 0 {
			holdMs = keyboardModifierTapHoldMs
		}
	}

	if err := t.tapKeyboardChord(modifier, keys, holdMs); err != nil {
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
	}
	return "ok", nil
}

func resolveKeyboardTapKeys(rawKeys []string) (keyboardTapResolvedInput, error) {
	resolved := keyboardTapResolvedInput{Keys: make([]string, 0, len(rawKeys))}
	androidKeys := make([]string, 0, 1)
	for _, key := range rawKeys {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if normalized == "" {
			continue
		}
		if alias, ok := androidKeyboardTapAliases[normalized]; ok {
			if alias.UnsupportedReason != "" {
				return keyboardTapResolvedInput{}, fmt.Errorf("android-only key %q (keycode %d) is not supported by keyboard_tap: %s", normalized, alias.Keycode, alias.UnsupportedReason)
			}
			normalized = alias.Replacement
		}
		if usage, ok := androidExtensionUsageMap[normalized]; ok {
			androidKeys = append(androidKeys, normalized)
			resolved.AndroidUsage = usage
			continue
		}
		resolved.Keys = append(resolved.Keys, normalized)
	}
	if len(androidKeys) > 1 {
		return keyboardTapResolvedInput{}, fmt.Errorf("keyboard_tap supports one Android extension key at a time, got %v", androidKeys)
	}
	if len(androidKeys) == 1 {
		if len(resolved.Keys) > 0 {
			return keyboardTapResolvedInput{}, fmt.Errorf("Android extension key %q cannot be combined with standard keyboard keys or modifiers", androidKeys[0])
		}
		resolved.AndroidExtensionKey = androidKeys[0]
		return resolved, nil
	}
	if len(resolved.Keys) == 0 {
		return keyboardTapResolvedInput{}, fmt.Errorf("at least one key or modifier is required")
	}
	if len(resolved.Keys) > 6 {
		return keyboardTapResolvedInput{}, fmt.Errorf("keyboard_tap supports at most 6 simultaneous keys after alias expansion")
	}
	return resolved, nil
}

var errAndroidExtensionUnavailable = errors.New("android extension keyboard device is not configured")
var errAndroidExtensionKeyUnavailableInPointerMode = errors.New("android extension key is unavailable in the configured pointer mode")

func (t *KeyboardTapTool) pointerModeOrDefault() string {
	switch strings.ToLower(strings.TrimSpace(t.pointerMode)) {
	case "touchscreen":
		return "touchscreen"
	default:
		return "absolute"
	}
}

func (t *KeyboardTapTool) androidExtensionPressReport(key string, usage uint16) ([]byte, error) {
	if t.pointerModeOrDefault() != "absolute" {
		return []byte{byte(usage), byte(usage >> 8)}, nil
	}
	report, ok := absolutePointerModeExtensionReports[key]
	if !ok {
		return nil, fmt.Errorf("%w: %q requires hid.pointer_mode=\"touchscreen\"; hid.pointer_mode=\"absolute\" only exposes these hid.usb2 keys: %s", errAndroidExtensionKeyUnavailableInPointerMode, key, absolutePointerModeExtensionKeyList)
	}
	return []byte{byte(report), byte(report >> 8)}, nil
}

func (t *KeyboardTapTool) tapAndroidExtension(key string, usage uint16, holdMs int) error {
	report, err := t.androidExtensionPressReport(key, usage)
	if err != nil {
		return err
	}
	if t.androidDev == nil {
		return fmt.Errorf("%w; ensure hid.android_keyboard_device exists to use %s", errAndroidExtensionUnavailable, key)
	}
	if err := t.androidDev.Write(report); err != nil {
		return err
	}
	if holdMs > 0 {
		sleepMs(holdMs)
	}
	return t.androidDev.Write([]byte{0x00, 0x00})
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
		sleepMs(holdMs)
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
		`Do not transliterate Chinese/CJK targets to pinyin or guessed ASCII keywords; if enter_text_in_field is unavailable, report the blocker instead. ` +
		`keyboard_text remains for simple standalone ASCII typing outside the enter_text_in_field workflow. ` +
		`Bare plain text is accepted only as a legacy compatibility fallback.`
}

func (t *KeyboardTextTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"text": stringArgSchema("US-keyboard ASCII text to type."),
	}, "text")
}

func (t *KeyboardTextTool) Call(ctx context.Context, input string) (string, error) {
	text, errText := parseKeyboardTextInput(input)
	if errText != "" {
		return toolErrorResultString(ctx, CodeInvalidArguments, errText), nil
	}

	if unsupported := unsupportedKeyboardTextRunes(text); len(unsupported) > 0 {
		return toolErrorResultf(
			ctx,
			CodeInvalidArguments,
			"keyboard_text supports only US-keyboard ASCII characters; unsupported characters: %q. Use enter_text_in_field for this target.",
			string(unsupported),
		), nil
	}
	if looksLikeSpacedRomanizationBlob(text) {
		return toolErrorResultString(ctx, CodeInvalidArguments, "keyboard_text received spaced romanization; use enter_text_in_field instead."), nil
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
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
		}
		if err := t.dev.Write(releaseReport); err != nil {
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
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
	return `Move the mouse to a position and click. Prefer normalized coordinates (0-1000) from the latest screenshot, aiming at the visual center of the target. Use coord_space:"screenshot" only for pixels measured directly in that returned image. Click once and inspect the post-action screenshot before repeating.`
}

func (t *MouseClickTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"x":           coordinateSchema("X coordinate.", 500),
		"y":           coordinateSchema("Y coordinate.", 300),
		"button":      stringEnumArgSchema("Mouse button; defaults to left.", "left", "right", "middle"),
		"coord_space": coordSpaceSchema(),
	}, "x", "y")
}

func (t *MouseClickTool) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		X          pointerCoordinate `json:"x"`
		Y          pointerCoordinate `json:"y"`
		Button     string            `json:"button"`
		CoordSpace string            `json:"coord_space"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "invalid input: %v. Expected JSON format: {\"x\": 500, \"y\": 300, \"button\": \"left\", \"coord_space\": \"normalized\"}. Common mistakes: x and y must be numbers, missing quotes around field names", err), nil
	}

	absX, absY, err := resolvePointerPositionForSurface(t.screen, t.pc.touchscreen, args.X.Float64(), args.Y.Float64(), args.CoordSpace, coordinateSpaceAuto)
	if err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
	}
	btn := mouseButtonByte(args.Button)

	if err := tapPointer(t.pc, absX, absY, btn); err != nil {
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
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
	return `Move the mouse without clicking. Prefer normalized coordinates (0-1000); use coord_space:"screenshot" only for pixels measured directly in the latest returned image.`
}

func (t *MouseMoveTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"x":           coordinateSchema("X coordinate.", 500),
		"y":           coordinateSchema("Y coordinate.", 300),
		"coord_space": coordSpaceSchema(),
	}, "x", "y")
}

func (t *MouseMoveTool) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		X          pointerCoordinate `json:"x"`
		Y          pointerCoordinate `json:"y"`
		CoordSpace string            `json:"coord_space"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "invalid input: %v. Expected JSON format: {\"x\": 500, \"y\": 300, \"coord_space\": \"normalized\"}. Common mistakes: x and y must be numbers, missing quotes around field names", err), nil
	}

	absX, absY, err := resolvePointerPositionForSurface(t.screen, t.pc.touchscreen, args.X.Float64(), args.Y.Float64(), args.CoordSpace, coordinateSpaceAuto)
	if err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
	}

	if err := positionPointer(t.pc, absX, absY, 0); err != nil {
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
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
	return `Perform a custom touch/pointer gesture via HID. Prefer quick_action for semantic platform actions; use this for tap/swipe/drag and other freehand screen gestures. ` +
		`Base coordinates on the latest screenshot and aim at the visual center of the target. Use coord_space:"screenshot" only for pixels measured directly in that returned image. Swipe direction names describe finger movement, not content scroll. ` +
		`This is a generic input tool and has no picker/wheel semantics; use wheel_nudge for every wheel interaction.`
}

func (t *TouchGestureTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"type":           stringEnumArgSchema("Gesture type. Edge aliases use real edges: back starts at x=1, home starts at y=999.", "tap", "double_tap", "long_press", "drag", "swipe", "swipe_left", "swipe_right", "swipe_up", "swipe_down", "back", "home"),
		"point":          pointSchema("Point for tap, double_tap, or long_press."),
		"start":          pointSchema("Start point for swipe or drag."),
		"end":            pointSchema("End point for swipe or drag."),
		"coord_space":    coordSpaceSchema(),
		"button":         stringEnumArgSchema("Mouse button for drag.", "left", "right", "middle"),
		"duration_ms":    nonNegativeIntegerSchema("Gesture duration in milliseconds."),
		"hold_before_ms": nonNegativeIntegerSchema("Optional dwell after pressing before a swipe begins."),
		"hold_after_ms":  nonNegativeIntegerSchema("Optional dwell at the destination before release."),
		"hold_ms":        nonNegativeIntegerSchema("Tap or long-press hold duration in milliseconds."),
		"pause_ms":       nonNegativeIntegerSchema("Pause between taps for double_tap."),
		"steps":          minIntegerArgSchema("Number of movement steps for swipe or drag.", 1),
		"distance":       coordinateSchema("Directional swipe travel in 0-1000 normalized units (700 ≈ 70% of screen).", 700),
		"anchor":         coordinateSchema("Directional swipe fixed-axis coordinate in 0-1000 normalized units.", 500),
		"strength":       stringEnumArgSchema("Directional swipe preset distance.", "large", "medium", "small", "tiny"),
	}, "type")
}

func pointSchema(description string) map[string]any {
	schema := objectArgsSchema(map[string]any{
		"x": coordinateSchema("X coordinate.", 500),
		"y": coordinateSchema("Y coordinate.", 300),
	}, "x", "y")
	schema["description"] = description
	return schema
}

func (t *TouchGestureTool) Call(ctx context.Context, input string) (string, error) {
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
		return toolErrorResultf(ctx, CodeInvalidArguments, "invalid input: %v. Common mistakes: missing quotes around string values, incorrect comma placement, point/start/end must be objects with named keys like {\"x\":500,\"y\":300} not bare values. Example: {\"type\":\"tap\",\"point\":{\"x\":500,\"y\":500}}", err), nil
	}

	gestureType := strings.ToLower(strings.TrimSpace(args.Type))
	if gestureType == "" {
		return toolErrorResultString(ctx, CodeInvalidArguments, "type is required"), nil
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
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
		}
		if err := tapPointerWithHold(t.pc, point.x, point.y, button, intOrDefault(args.HoldMs, defaultTapHoldMs)); err != nil {
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
		}
	case "double_tap":
		point, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, args.Point, coordSpace)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
		}
		holdMs := intOrDefault(args.HoldMs, defaultTapHoldMs)
		if err := tapPointerWithHold(t.pc, point.x, point.y, button, holdMs); err != nil {
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
		}
		sleepMs(intOrDefault(args.PauseMs, 100))
		if err := tapPointerWithHold(t.pc, point.x, point.y, button, holdMs); err != nil {
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
		}
	case "long_press":
		point, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, args.Point, coordSpace)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
		}
		if err := settlePointer(t.pc, point.x, point.y); err != nil {
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
		}
		if err := pressPointer(t.pc, point.x, point.y, button); err != nil {
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
		}
		released := false
		defer func() {
			if !released {
				_ = releasePointerRepeated(t.pc, point.x, point.y, touchReleaseReportCount, touchReleaseReportDelayMs)
			}
		}()
		sleepMs(intOrDefault(args.DurationMs, 500))
		if err := releasePointerRepeated(t.pc, point.x, point.y, touchReleaseReportCount, touchReleaseReportDelayMs); err != nil {
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
		}
		released = true
	case "swipe_left", "swipe_right", "swipe_up", "swipe_down":
		preset, err := directionalSwipePreset(args.Strength)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
		}
		start, end, err := directionalSwipeEndpoints(t.screen, t.pc.touchscreen, gestureType, args.Distance, args.Anchor, preset)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
		}
		if sameResolvedPointerPoint(start, end) {
			return toolErrorResultString(ctx, CodeInvalidArguments, "directional swipe resolved to the same HID point"), nil
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
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
		}
	case "drag":
		if samePointerPoint(args.Start, args.End) {
			return toolErrorResultString(ctx, CodeInvalidArguments, "drag requires distinct start and end points; a zero-distance drag behaves like a press and may activate the control instead of moving it"), nil
		}
		start, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, args.Start, coordSpace)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
		}
		end, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, args.End, coordSpace)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
		}
		if sameResolvedPointerPoint(start, end) {
			return toolErrorResultString(ctx, CodeInvalidArguments, "drag start and end resolve to the same HID point"), nil
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
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
		}
	case "swipe":
		if args.Start == nil && args.Point != nil {
			return toolErrorResultString(ctx, CodeInvalidArguments, "swipe requires start and end, not point; use swipe_up/down/left/right for directional swipes from center"), nil
		}
		if samePointerPoint(args.Start, args.End) {
			return toolErrorResultString(ctx, CodeInvalidArguments, "swipe requires distinct start and end points"), nil
		}
		start, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, args.Start, coordSpace)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
		}
		end, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, args.End, coordSpace)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
		}
		if sameResolvedPointerPoint(start, end) {
			return toolErrorResultString(ctx, CodeInvalidArguments, "swipe start and end resolve to the same HID point"), nil
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
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
		}
	case "back", "edge_back", "left_edge_back":
		start, err := resolvePointOrDefaultNormalized(t.screen, t.pc.touchscreen, args.Start, coordSpace, phoneBackStartX, phoneBackY)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
		}
		end, err := resolvePointOrDefaultNormalized(t.screen, t.pc.touchscreen, args.End, coordSpace, phoneBackEndX, phoneBackY)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
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
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
		}
	case "home", "home_swipe", "bottom_edge_home":
		start, err := resolvePointOrDefaultNormalized(t.screen, t.pc.touchscreen, args.Start, coordSpace, phoneHomeX, phoneHomeStartY)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
		}
		end, err := resolvePointOrDefaultNormalized(t.screen, t.pc.touchscreen, args.End, coordSpace, phoneHomeX, phoneHomeEndY)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
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
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
		}
	default:
		return toolErrorResultf(ctx, CodeInvalidArguments, "unsupported gesture type: %q", args.Type), nil
	}

	return "ok", nil
}

func samePointerPoint(first, second *pointerPoint) bool {
	return first != nil && second != nil && first.X == second.X && first.Y == second.Y
}

func sameResolvedPointerPoint(first, second resolvedPointerPoint) bool {
	return first.x == second.x && first.y == second.y
}

// WheelNudgeTool performs one bounded interaction inside a visible wheel
// column. It taps an adjacent target row when possible, otherwise it uses a
// low-inertia vertical drag that is less likely to fling past the target.
type WheelNudgeTool struct {
	pc         *pointerController
	screen     *screenState
	durationMs int
}

type wheelNudgeArgs struct {
	PickerID            string   `json:"picker_id"`
	ColumnX             *float64 `json:"column_x"`
	Direction           string   `json:"direction"`
	RemainingGap        *int     `json:"remaining_gap"`
	CurrentValue        *int     `json:"current_value"`
	TargetValue         *int     `json:"target_value"`
	CycleSize           *int     `json:"cycle_size"`
	CycleStart          *int     `json:"cycle_start"`
	IncreasingDirection string   `json:"increasing_direction"`
	RowSpacing          *float64 `json:"row_spacing"`
	ValueStep           *int     `json:"value_step"`
	CenterY             *float64 `json:"center_y"`
	VisibleTargetY      *float64 `json:"visible_target_y"`
	CoordSpace          string   `json:"coord_space"`
}

type wheelNudgePlan struct {
	gap       int
	rows      int
	distance  string
	probe     bool
	rowOffset int
	tapY      *float64
}

func (t *WheelNudgeTool) Name() string { return "wheel_nudge" }

func (t *WheelNudgeTool) Description() string {
	return `Move a visible picker/wheel column toward a target value. This is the only tool for wheel interactions; never attach wheel semantics to touch_gesture. ` +
		`When the target is exactly one visibly observed row above or below the selected row, pass visible_target_y and the tool taps that coordinate. Without that evidence it performs one bounded low-inertia drag. ` +
		`Input JSON: {"picker_id":"alarm-create","column_x":195,"direction":"up","remaining_gap":6,"current_value":10,"target_value":16,"cycle_size":24,"cycle_start":0,"increasing_direction":"up","row_spacing":42,"value_step":1,"center_y":273,"coord_space":"screenshot"}. ` +
		`For a cropped frame_service phone screenshot, use coord_space:"screenshot" and pass column_x/center_y exactly as measured in the latest returned image; do not copy image pixels into normalized coordinates. ` +
		`direction describes the equivalent finger movement toward the target and is used when a drag is required; an adjacent-row tap derives its tap position from target_value and value_step. If values increase below the selected row (value_step > 0), finger-up increases the selected value; if they decrease below it, finger-down increases it. Use increasing_direction:"unknown" without value_step only when visible ordering is insufficient and a one-row direction probe is genuinely needed. ` +
		`Actual drag travel is derived from remaining_gap: gaps of 1, 2-4, 5-8, and 9+ values move at most 1, 2, 3, and 4 measured rows using row_spacing, so it cannot become a fling-like full-column swipe. ` +
		`The tool performs one tap or slow drag and returns a post-action screenshot; read the new centered value and recalculate the remaining gap.`
}

func (t *WheelNudgeTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"picker_id":            map[string]any{"type": "string", "minLength": 1, "description": "Stable identifier for this visible picker instance; change it after navigating to another picker screen."},
		"column_x":             coordinateSchema("X coordinate at the center of the wheel column in the selected coord_space."),
		"direction":            stringEnumArgSchema(`Equivalent finger movement direction toward the target, used for path validation and for non-tap execution. Derive whether up/down increases or decreases from the ordered visible rows before acting; do not guess with the first gesture.`, "up", "down"),
		"remaining_gap":        nonNegativeIntegerSchema("Current shortest-path number of values from the centered value to the target, recalculated from the latest screenshot."),
		"current_value":        nonNegativeIntegerSchema("Current centered numeric value from the latest screenshot."),
		"target_value":         nonNegativeIntegerSchema("Requested numeric target value for this wheel column."),
		"cycle_size":           nonNegativeIntegerSchema("Number of values in the known cyclic domain; use 0 for a non-cyclic numeric wheel. Derive this from the actual component rather than assuming an hour/minute wheel."),
		"cycle_start":          nonNegativeIntegerSchema("Lowest value in a cyclic wheel. Use 0 for 00-based time wheels and 1 for one-based wheels such as months, calendar days, or 12-hour clocks. Ignored when cycle_size is 0."),
		"increasing_direction": stringEnumArgSchema(`Finger direction that increases the value. Derive "up" or "down" from visible row ordering when possible; use "unknown" only for a necessary one-row probe.`, "unknown", "up", "down"),
		"row_spacing":          coordinateSchema("Measured distance between adjacent visible row centers in the selected coord_space."),
		"value_step":           integerArgSchema("Signed ordinal change for one visible row downward; required when increasing_direction is known, omitted for a genuinely unknown one-row probe."),
		"center_y":             coordinateSchema("Vertical center of the visible wheel selection area in the selected coord_space. Normalized default is 460."),
		"visible_target_y":     coordinateSchema("Exact Y coordinate of a target value visibly observed one row above or below center_y. Omit unless the target row is actually visible in the latest screenshot."),
		"coord_space":          stringEnumArgSchema("Coordinate space; screenshot uses pixels in the latest returned cropped screenshot, normalized uses 0-1000.", "auto", "screenshot", "normalized"),
	}, "picker_id", "column_x", "direction", "remaining_gap", "current_value", "target_value", "cycle_size", "cycle_start", "increasing_direction", "row_spacing")
}

func (t *WheelNudgeTool) Call(ctx context.Context, input string) (string, error) {
	args, err := parseWheelNudgeArgs(input)
	if err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
	}

	if args.RowSpacing == nil || *args.RowSpacing <= 0 || math.IsNaN(*args.RowSpacing) || math.IsInf(*args.RowSpacing, 0) {
		return toolErrorResultString(ctx, CodeInvalidArguments, "row_spacing is required and must be a positive finite number measured from the latest screenshot"), nil
	}
	if args.CurrentValue == nil || args.TargetValue == nil || args.CycleSize == nil || args.CycleStart == nil || args.IncreasingDirection == "" {
		return toolErrorResultString(ctx, CodeInvalidArguments, "current_value, target_value, cycle_size, cycle_start, and increasing_direction are required"), nil
	}
	plan, err := planWheelNudge(args)
	if err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
	}
	travel := float64(plan.rows) * *args.RowSpacing
	coordSpace := args.CoordSpace
	if coordSpace == "" || coordSpace == coordinateSpaceAuto {
		coordSpace = coordinateSpaceNormalized
	}

	centerY := wheelNudgeDefaultY
	physicalTravel := travel
	gestureTravel := physicalTravel
	maxY := 1000.0
	if coordSpace == coordinateSpaceScreenshot {
		if t.screen == nil {
			return toolErrorResultString(ctx, CodeInvalidArguments, "screenshot coordinates require a recent screenshot"), nil
		}
		_, _, active, age, ok := t.screen.ActiveAreaWithAge()
		if !ok || age >= screenDimensionsStaleAfter {
			return toolErrorResultString(ctx, CodeInvalidArguments, "screenshot coordinates require a fresh screenshot"), nil
		}
		maxY = float64(active.Height - 1)
		centerY = (wheelNudgeDefaultY / 1000.0) * maxY
	}
	if args.CenterY != nil {
		centerY = *args.CenterY
	}
	if centerY < 0 || centerY > maxY {
		return toolErrorResultf(ctx, CodeInvalidArguments, "center_y=%.0f is outside the visible coordinate range 0..%.0f", centerY, maxY), nil
	}

	x := *args.ColumnX
	if plan.tapY != nil {
		tapY := *plan.tapY
		if tapY < 0 || tapY > maxY {
			return toolErrorResultf(ctx, CodeInvalidArguments, "adjacent wheel row y=%.0f is outside the visible coordinate range 0..%.0f", tapY, maxY), nil
		}
		point, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, &pointerPoint{X: pointerCoordinate(x), Y: pointerCoordinate(tapY)}, coordSpace)
		if err != nil {
			return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
		}
		if err := tapPointerWithHold(t.pc, point.x, point.y, mouseButtonByte("left"), defaultTapHoldMs); err != nil {
			return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
		}
		return fmt.Sprintf("ok: wheel_nudge interaction=tap row_offset=%d target_value=%d", plan.rowOffset, *args.TargetValue), nil
	}

	startOffset := gestureTravel / 2
	var startY, endY float64
	if args.Direction == "up" {
		startY = clampFloat(centerY+startOffset, 0, maxY)
		endY = clampFloat(startY-gestureTravel, 0, maxY)
	} else {
		startY = clampFloat(centerY-startOffset, 0, maxY)
		endY = clampFloat(startY+gestureTravel, 0, maxY)
	}

	start, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, &pointerPoint{X: pointerCoordinate(x), Y: pointerCoordinate(startY)}, coordSpace)
	if err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
	}
	end, err := resolveRequiredPoint(t.screen, t.pc.touchscreen, &pointerPoint{X: pointerCoordinate(x), Y: pointerCoordinate(endY)}, coordSpace)
	if err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "%v", err), nil
	}
	if sameResolvedPointerPoint(start, end) {
		return toolErrorResultString(ctx, CodeInvalidArguments, "wheel_nudge resolved to the same HID point; refresh the screenshot and use a valid center_y/row_spacing"), nil
	}

	durationMs := t.durationMs
	if durationMs <= 0 {
		durationMs = wheelNudgeDefaultMs
	}
	if err := runPositionedDragGesture(
		t.pc,
		start,
		end,
		mouseButtonByte("left"),
		durationMs,
		80,
		0,
		wheelNudgeDefaultSteps,
	); err != nil {
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
	}

	return fmt.Sprintf("ok: wheel_nudge direction=%s distance=%s rows=%d physical_travel=%.0f duration_ms=%d", args.Direction, plan.distance, plan.rows, physicalTravel, durationMs), nil
}

func planWheelNudge(args wheelNudgeArgs) (wheelNudgePlan, error) {
	probe := args.IncreasingDirection == "unknown"
	gap, ok := wheelDomainDistance(*args.CurrentValue, *args.TargetValue, *args.CycleSize, *args.CycleStart)
	if !ok {
		return wheelNudgePlan{}, fmt.Errorf("wheel values are outside the declared domain")
	}
	if *args.RemainingGap != gap {
		return wheelNudgePlan{}, fmt.Errorf("current_value=%d target_value=%d requires remaining_gap=%d, got %d", *args.CurrentValue, *args.TargetValue, gap, *args.RemainingGap)
	}
	if args.ValueStep != nil {
		derivedIncreasing := wheelIncreasingDirectionFromVisibleStep(*args.ValueStep)
		if !probe && args.IncreasingDirection != derivedIncreasing {
			return wheelNudgePlan{}, fmt.Errorf("value_step=%d requires increasing_direction=%q, got %q", *args.ValueStep, derivedIncreasing, args.IncreasingDirection)
		}
		semanticArgs := args
		if probe {
			semanticArgs.IncreasingDirection = derivedIncreasing
		}
		_, allowedDirections, semanticOK := wheelSemanticTarget(semanticArgs)
		if !semanticOK {
			return wheelNudgePlan{}, fmt.Errorf("wheel values are outside the declared domain")
		}
		if !slices.Contains(allowedDirections, args.Direction) {
			return wheelNudgePlan{}, fmt.Errorf("wheel shortest path requires direction=%q, got %q", strings.Join(allowedDirections, " or "), args.Direction)
		}
	}
	rows := wheelNudgeRowsForGap(gap)
	distance := wheelDistanceForGap(gap)
	if probe {
		rows = 1
		distance = "micro"
	}
	rowOffset := 0
	var tapY *float64
	if !probe && args.ValueStep != nil && args.VisibleTargetY != nil {
		rowOffset = wheelAdjacentTargetRowOffset(*args.CurrentValue, *args.TargetValue, *args.ValueStep, *args.CycleSize, *args.CycleStart)
		if rowOffset == 0 {
			return wheelNudgePlan{}, fmt.Errorf("visible_target_y is only valid when target_value is exactly one adjacent row")
		}
		if args.CenterY == nil {
			return wheelNudgePlan{}, fmt.Errorf("center_y is required with visible_target_y")
		}
		expectedY := *args.CenterY + float64(rowOffset)*(*args.RowSpacing)
		tolerance := max(3.0, *args.RowSpacing*0.35)
		if math.Abs(*args.VisibleTargetY-expectedY) > tolerance {
			return wheelNudgePlan{}, fmt.Errorf("visible_target_y=%.0f does not match the observed adjacent row near y=%.0f", *args.VisibleTargetY, expectedY)
		}
		tapY = args.VisibleTargetY
	}
	return wheelNudgePlan{gap: gap, rows: rows, distance: distance, probe: probe, rowOffset: rowOffset, tapY: tapY}, nil
}

func wheelAdjacentTargetRowOffset(current, target, valueStep, cycleSize, cycleStart int) int {
	for _, offset := range []int{-1, 1} {
		candidate := current + offset*valueStep
		if cycleSize > 0 {
			candidate = cycleStart + ((candidate-cycleStart)%cycleSize+cycleSize)%cycleSize
		}
		if candidate == target {
			return offset
		}
	}
	return 0
}

func parseWheelNudgeArgs(input string) (wheelNudgeArgs, error) {
	var args wheelNudgeArgs
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return wheelNudgeArgs{}, fmt.Errorf("invalid input: %v. Expected JSON format: {\"picker_id\":\"alarm-create\",\"column_x\":650,\"direction\":\"up\",\"remaining_gap\":11,\"center_y\":460}", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return wheelNudgeArgs{}, fmt.Errorf("invalid input: expected exactly one JSON object")
	}
	if args.ColumnX == nil || math.IsNaN(*args.ColumnX) || math.IsInf(*args.ColumnX, 0) {
		return wheelNudgeArgs{}, fmt.Errorf("column_x is required and must be a finite number")
	}
	args.PickerID = strings.TrimSpace(args.PickerID)
	if args.PickerID == "" {
		return wheelNudgeArgs{}, fmt.Errorf("picker_id is required and must identify the current visible picker instance")
	}
	args.Direction = strings.ToLower(strings.TrimSpace(args.Direction))
	if args.Direction != "up" && args.Direction != "down" {
		return wheelNudgeArgs{}, fmt.Errorf(`direction must be "up" or "down"`)
	}
	if args.RemainingGap == nil || *args.RemainingGap < 1 {
		return wheelNudgeArgs{}, fmt.Errorf("remaining_gap is required and must be at least 1; if the gap is 0, do not nudge the wheel")
	}
	args.IncreasingDirection = strings.ToLower(strings.TrimSpace(args.IncreasingDirection))
	if args.IncreasingDirection != "" && args.IncreasingDirection != "unknown" && args.IncreasingDirection != "up" && args.IncreasingDirection != "down" {
		return wheelNudgeArgs{}, fmt.Errorf(`increasing_direction must be "unknown", "up", or "down"`)
	}
	if args.CycleSize != nil && *args.CycleSize < 0 {
		return wheelNudgeArgs{}, fmt.Errorf("cycle_size must be non-negative")
	}
	if args.CycleStart != nil && *args.CycleStart < 0 {
		return wheelNudgeArgs{}, fmt.Errorf("cycle_start must be non-negative")
	}
	if args.RowSpacing != nil && (*args.RowSpacing <= 0 || math.IsNaN(*args.RowSpacing) || math.IsInf(*args.RowSpacing, 0)) {
		return wheelNudgeArgs{}, fmt.Errorf("row_spacing must be a positive finite number")
	}
	if args.ValueStep != nil && *args.ValueStep == 0 {
		return wheelNudgeArgs{}, fmt.Errorf("value_step must be non-zero")
	}
	args.CoordSpace = strings.ToLower(strings.TrimSpace(args.CoordSpace))
	if args.CoordSpace != "" && args.CoordSpace != coordinateSpaceAuto && args.CoordSpace != coordinateSpaceScreenshot && args.CoordSpace != coordinateSpaceNormalized {
		return wheelNudgeArgs{}, fmt.Errorf("unsupported coord_space for wheel_nudge: %q", args.CoordSpace)
	}
	if args.CenterY != nil && (math.IsNaN(*args.CenterY) || math.IsInf(*args.CenterY, 0)) {
		return wheelNudgeArgs{}, fmt.Errorf("center_y must be a finite number")
	}
	if args.VisibleTargetY != nil && (math.IsNaN(*args.VisibleTargetY) || math.IsInf(*args.VisibleTargetY, 0)) {
		return wheelNudgeArgs{}, fmt.Errorf("visible_target_y must be a finite number")
	}
	if args.CurrentValue == nil || args.TargetValue == nil || args.CycleSize == nil || args.CycleStart == nil || args.IncreasingDirection == "" || args.RowSpacing == nil {
		return wheelNudgeArgs{}, fmt.Errorf("complete wheel metadata required: provide current_value, target_value, cycle_size, cycle_start, increasing_direction, and measured row_spacing")
	}
	if args.IncreasingDirection != "unknown" && args.ValueStep == nil {
		return wheelNudgeArgs{}, fmt.Errorf("value_step is required when increasing_direction is known")
	}
	return args, nil
}

func wheelNudgeRowsForGap(gap int) int {
	switch {
	case gap <= 1:
		return 1
	case gap <= 4:
		return min(gap, 2)
	case gap <= 8:
		return min(gap, 3)
	default:
		return min(gap, 4)
	}
}

// MouseScrollTool sends mouse wheel events.
type MouseScrollTool struct {
	pc *pointerController
}

func (t *MouseScrollTool) Name() string { return "mouse_scroll" }

func (t *MouseScrollTool) Description() string {
	return `Scroll the mouse wheel. This is a wheel event, not equivalent to a mobile swipe gesture; use touch_gesture for swipes. Unsupported when pointer_mode is touchscreen.`
}

func (t *MouseScrollTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"delta": rangedIntegerArgSchema("Mouse wheel delta. Positive scrolls up, negative scrolls down.", -127, 127),
	}, "delta")
}

func (t *MouseScrollTool) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		Delta int `json:"delta"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "invalid input: %v. Expected JSON format: {\"delta\": -3}. Delta must be a number between -127 and 127", err), nil
	}
	if args.Delta == 0 {
		return "ok", nil
	}
	if args.Delta < -127 || args.Delta > 127 {
		return toolErrorResultString(ctx, CodeInvalidArguments, "delta must be between -127 and 127"), nil
	}
	if t == nil || t.pc == nil {
		return toolErrorResultString(ctx, CodeModuleUnavailable, "mouse_scroll is not configured"), nil
	}
	if t.pc.touchscreen {
		return toolErrorResultString(ctx, CodeInvalidArguments, "mouse_scroll is unsupported when pointer_mode is touchscreen; use touch_gesture"), nil
	}

	if err := scrollPointer(t.pc, args.Delta); err != nil {
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
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

func (p *pointerPoint) UnmarshalJSON(data []byte) error {
	// Accept array format [x, y] as fallback for models that pass coordinates as arrays.
	var arr []json.RawMessage
	if json.Unmarshal(data, &arr) == nil && len(arr) == 2 {
		if err := json.Unmarshal(arr[0], &p.X); err != nil {
			return fmt.Errorf("coordinate must be a number or numeric string")
		}
		if err := json.Unmarshal(arr[1], &p.Y); err != nil {
			return fmt.Errorf("coordinate must be a number or numeric string")
		}
		return nil
	}
	// Normal object format {"x":N,"y":M}
	type plain pointerPoint
	return json.Unmarshal(data, (*plain)(p))
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
	coordinateSpaceScreenshot = "screenshot"
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
				return pixelToAbsolutePoint(x, y, width, height, active, touchscreen)
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
		return pixelToAbsolutePoint(x, y, width, height, active, touchscreen)
	case coordinateSpaceScreenshot:
		if screen == nil {
			return 0, 0, fmt.Errorf("screenshot coordinates require a recent screenshot")
		}
		width, height, active, age, ok := screen.ActiveAreaWithAge()
		if !ok {
			return 0, 0, fmt.Errorf("screenshot coordinates require a recent screenshot")
		}
		if age >= screenDimensionsStaleAfter {
			return 0, 0, fmt.Errorf("cached screenshot dimensions are %.0fs old; call screenshot again before using screenshot coordinates", age.Seconds())
		}
		return screenshotPixelToAbsolutePoint(x, y, width, height, active, touchscreen)
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
			return activeLocalAxisToAbsolute(activePixelX, active.X, active.Width, width), activeLocalAxisToAbsolute(activePixelY, active.Y, active.Height, height), nil
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
	case coordinateSpaceAuto, coordinateSpaceScreenshot, coordinateSpacePixel, coordinateSpaceNormalized, coordinateSpaceAbsolute:
		return space, nil
	default:
		return "", fmt.Errorf("unsupported coord_space: %q", coordSpace)
	}
}

func normalizedToAbsolutePoint(x, y float64) (int, int) {
	return int(math.Round(clampFloat(x, 0, 1000) / 1000.0 * absMouseMaxPos)), int(math.Round(clampFloat(y, 0, 1000) / 1000.0 * absMouseMaxPos))
}

func pixelToAbsolutePoint(x, y float64, width, height int, active screenActiveArea, touchscreen bool) (int, int, error) {
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("invalid screen dimensions: %dx%d", width, height)
	}
	if !active.Valid {
		active = screenActiveArea{X: 0, Y: 0, Width: width, Height: height, Valid: true}
	}
	// Pixel coordinates are relative to the cropped active area image that the
	// LLM sees, not the full HDMI frame. Bounds check against active area size.
	if x < 0 || y < 0 || x > float64(active.Width-1) || y > float64(active.Height-1) {
		return 0, 0, fmt.Errorf("pixel coordinates x=%.2f y=%.2f are outside screenshot bounds %dx%d; use coord_space normalized with 0-1000 coordinates, where 500,500 is center", x, y, active.Width, active.Height)
	}
	// pointer_mode touchscreen: HID surface covers the full mirrored frame.
	if touchscreen {
		return scalePixelToAbsolute(float64(active.X)+x, width), scalePixelToAbsolute(float64(active.Y)+y, height), nil
	}
	return scalePixelToAbsolute(x, active.Width), scalePixelToAbsolute(y, active.Height), nil
}

func screenshotPixelToAbsolutePoint(x, y float64, sourceWidth, sourceHeight int, active screenActiveArea, touchscreen bool) (int, int, error) {
	if sourceWidth <= 0 || sourceHeight <= 0 || !active.Valid || active.Width <= 0 || active.Height <= 0 {
		return 0, 0, fmt.Errorf("invalid screenshot mapping: source=%dx%d active=%+v", sourceWidth, sourceHeight, active)
	}
	if x < 0 || y < 0 || x > float64(active.Width-1) || y > float64(active.Height-1) {
		return 0, 0, fmt.Errorf("screenshot coordinates x=%.2f y=%.2f are outside latest returned screenshot bounds %dx%d", x, y, active.Width, active.Height)
	}
	if touchscreen {
		return scalePixelToAbsolute(float64(active.X)+x, sourceWidth), scalePixelToAbsolute(float64(active.Y)+y, sourceHeight), nil
	}
	return activeLocalAxisToAbsolute(x, active.X, active.Width, sourceWidth), activeLocalAxisToAbsolute(y, active.Y, active.Height, sourceHeight), nil
}

const nearlyFullSourceAxisRatio = 0.9

// activeLocalAxisToAbsolute maps one coordinate axis from a cropped screenshot
// to the absolute HID surface. A narrow active axis represents real HDMI
// letter/pillar-boxing and is de-barred. An active axis covering nearly the
// whole source often comes from dark UI pixels being mistaken for a small
// black bar; preserve its source offset so frame-to-frame crop jitter cancels
// out instead of shifting the touch point.
func activeLocalAxisToAbsolute(local float64, activeStart, activeSize, sourceSize int) int {
	if activeSize <= 0 || sourceSize <= 0 {
		return 0
	}
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

var sleepMs = realSleepMs

func realSleepMs(ms int) {
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
		return "", "text is required"
	}

	if strings.HasPrefix(trimmed, "{") {
		var args struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
			return "", fmt.Sprintf("invalid input: %v", err)
		}
		if args.Text == "" {
			return "", "text is required"
		}
		return args.Text, ""
	}

	if strings.HasPrefix(trimmed, `"`) {
		var text string
		if err := json.Unmarshal([]byte(trimmed), &text); err != nil {
			return "", fmt.Sprintf("invalid input: %v", err)
		}
		if text == "" {
			return "", "text is required"
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
