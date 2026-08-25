package mnk

import (
	"aiden-agent/internal/agent/screen"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ADBProvider implements Provider interface using Android Debug Bridge (adb).
// This sends input through "adb shell input" commands rather than USB HID.
type ADBProvider struct {
	screen *screen.ScreenState
	client *ADBScreenClient
	runADB adbCommandRunner

	mu                sync.Mutex
	cachedScreenSize  adbInputScreenSize
	screenSizeExpires time.Time
	androidSDK        int
	androidSDKValid   bool

	// Timing constants
	commandTimeout    time.Duration
	gestureTimeoutPad time.Duration
	screenSizeTTL     time.Duration
	textRestoreWait   int // milliseconds
	maxDurationMs     int
}

type adbCommandRunner func(context.Context, string, ...string) ([]byte, []byte, error)

type adbInputScreenSize struct {
	width  int
	height int
}

const (
	adbDefaultCommandTimeout      = 8 * time.Second
	adbDefaultGestureTimeoutPad   = 3 * time.Second
	adbDefaultScreenSizeTTL       = 30 * time.Second
	adbDefaultTextRestoreWait     = 80 // milliseconds
	adbMaxActionDurationMs        = 10000
	adbKeyboardIME                = "com.android.adbkeyboard/.AdbIME"
	adbScreenDimensionsStaleAfter = 30 * time.Second
)

var (
	adbWMSizePattern = regexp.MustCompile(`(?m)(?:Physical|Override) size:\s*([0-9]+)x([0-9]+)`)
)

// NewADBProvider creates a new ADB-based MNK provider.
func NewADBProvider(screen *screen.ScreenState, client *ADBScreenClient, runADB adbCommandRunner) *ADBProvider {
	if client == nil {
		client = NewADBScreenClient()
	}
	if runADB == nil {
		runADB = runADBCommand
	}
	client.setCommandRunner(runADB)

	return &ADBProvider{
		screen:            screen,
		client:            client,
		runADB:            runADB,
		commandTimeout:    adbDefaultCommandTimeout,
		gestureTimeoutPad: adbDefaultGestureTimeoutPad,
		screenSizeTTL:     adbDefaultScreenSizeTTL,
		textRestoreWait:   adbDefaultTextRestoreWait,
		maxDurationMs:     adbMaxActionDurationMs,
	}
}

// Click performs a tap at the specified position.
// holdMs is used to determine if this is a tap (default) or long press (500+).
func (p *ADBProvider) Click(ctx context.Context, x, y float64, button string, holdMs int) error {

	// Validate button (ADB only supports left/touch semantics)
	if button != "" && button != ButtonLeft && button != "left" {
		return fmt.Errorf("adb only supports left-button touch semantics, got %q", button)
	}

	// Validate and resolve coordinates
	pixelX, pixelY, err := p.normalizedToPixel(ctx, x, y)
	if err != nil {
		return err
	}

	// Long press vs tap
	if holdMs >= 500 {
		// Long press: swipe from point to itself with duration
		durationMs := p.clampDuration(holdMs, 500, 1)
		return p.runShellWithTimeout(ctx, p.timeoutForDuration(durationMs),
			"input", "swipe",
			strconv.Itoa(pixelX), strconv.Itoa(pixelY),
			strconv.Itoa(pixelX), strconv.Itoa(pixelY),
			strconv.Itoa(durationMs),
		)
	}

	// Regular tap
	return p.runShell(ctx, "input", "tap", strconv.Itoa(pixelX), strconv.Itoa(pixelY))
}

// DoubleClick performs two taps in rapid succession.
func (p *ADBProvider) DoubleClick(ctx context.Context, x, y float64, button string) error {
	// First tap
	if err := p.Click(ctx, x, y, button, 0); err != nil {
		return err
	}

	// Pause, while allowing a canceled caller to stop the second tap.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(100 * time.Millisecond):
	}

	// Second tap
	return p.Click(ctx, x, y, button, 0)
}

// Back invokes Android's system back action without requiring screen geometry.
func (p *ADBProvider) Back(ctx context.Context) error {
	return p.Keypress(ctx, []string{"keycode_back"})
}

// Home invokes Android's system home action without requiring screen geometry.
func (p *ADBProvider) Home(ctx context.Context) error {
	return p.Keypress(ctx, []string{"keycode_home"})
}

// Drag performs a swipe gesture along a path.
// ADB only supports 2-point swipes, so multi-point paths are broken into segments.
func (p *ADBProvider) Drag(ctx context.Context, path [][2]float64, button string) error {
	return p.dragWithDuration(ctx, path, button, 700)
}

// Swipe performs a short gesture that avoids Android long-press recognition.
func (p *ADBProvider) Swipe(ctx context.Context, path [][2]float64, button string) error {
	return p.SwipeWithDuration(ctx, path, button, defaultSwipeGestureDurationMs)
}

// SwipeWithDuration performs a swipe using the caller-selected motion time.
func (p *ADBProvider) SwipeWithDuration(ctx context.Context, path [][2]float64, button string, durationMs int) error {
	durationMs = p.clampDuration(durationMs, defaultSwipeGestureDurationMs, 1)
	return p.dragWithDuration(ctx, path, button, durationMs)
}

func (p *ADBProvider) dragWithDuration(ctx context.Context, path [][2]float64, button string, totalDurationMs int) error {
	if len(path) < 2 {
		return fmt.Errorf("drag path must contain at least 2 points, got %d", len(path))
	}

	// Validate button
	if button != "" && button != ButtonLeft && button != "left" {
		return fmt.Errorf("adb only supports left-button touch semantics, got %q", button)
	}

	// Validate all points and convert to pixels
	pixelPath := make([][2]int, len(path))
	for i, point := range path {
		x, y, err := p.normalizedToPixel(ctx, point[0], point[1])
		if err != nil {
			return fmt.Errorf("invalid point %d in path: %w", i, err)
		}
		pixelPath[i] = [2]int{x, y}
	}

	// Calculate total path length for duration distribution
	totalLength := 0.0
	for i := 1; i < len(pixelPath); i++ {
		dx := float64(pixelPath[i][0] - pixelPath[i-1][0])
		dy := float64(pixelPath[i][1] - pixelPath[i-1][1])
		totalLength += math.Sqrt(dx*dx + dy*dy)
	}

	if totalLength == 0 {
		return InvalidArguments("drag requires distinct start and end points")
	}

	// Execute each segment as a separate adb swipe
	for i := 1; i < len(pixelPath); i++ {
		start := pixelPath[i-1]
		end := pixelPath[i]

		// Calculate proportional duration for this segment
		dx := float64(end[0] - start[0])
		dy := float64(end[1] - start[1])
		segmentLength := math.Sqrt(dx*dx + dy*dy)
		segmentDurationMs := int(math.Round(float64(totalDurationMs) * segmentLength / totalLength))
		if segmentDurationMs < 1 {
			segmentDurationMs = 1
		}
		segmentDurationMs = p.clampDuration(segmentDurationMs, 0, 0)

		// Execute segment
		if err := p.runShellWithTimeout(ctx, p.timeoutForDuration(segmentDurationMs),
			"input", "swipe",
			strconv.Itoa(start[0]), strconv.Itoa(start[1]),
			strconv.Itoa(end[0]), strconv.Itoa(end[1]),
			strconv.Itoa(segmentDurationMs),
		); err != nil {
			return fmt.Errorf("drag segment %d failed: %w", i, err)
		}
	}

	return nil
}

// Keypress sends key events through adb.
// Supports single keys and combinations (up to 6 simultaneous keys).
func (p *ADBProvider) Keypress(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return fmt.Errorf("keypress requires at least one key")
	}

	// Resolve keys to Android keycodes
	keycodes, err := p.resolveKeycodes(keys)
	if err != nil {
		return err
	}

	// Single key
	if len(keycodes) == 1 {
		return p.runShell(ctx, "input", "keyevent", keycodes[0])
	}

	androidSDK, err := p.androidSDKVersion(ctx)
	if err != nil {
		return err
	}
	if androidSDK < 30 {
		return ModuleUnavailablef("adb input keycombination requires Android 11 (API 30) or later; detected API %d", androidSDK)
	}

	// Android 12 introduced the optional -t duration flag. Android 11 accepts
	// the same key combination without it.
	args := []string{"input", "keycombination"}
	if androidSDK >= 31 {
		args = append(args, "-t", "50")
	}
	args = append(args, keycodes...)

	return p.runShell(ctx, args...)
}

// Move is not supported on ADB (no hover capability).
func (p *ADBProvider) Move(ctx context.Context, x, y float64) error {
	_ = ctx
	_ = x
	_ = y
	return ModuleUnavailable("adb mouse_move is unsupported because adb input has no hover/pointer move primitive; use touch_gesture for taps, swipes, or drags")
}

// Scroll converts scroll to swipe gestures (ADB has no wheel/scroll primitive).
func (p *ADBProvider) Scroll(ctx context.Context, scrollX, scrollY int) error {
	// Convert scroll to directional swipe
	centerX := 500.0
	centerY := 500.0

	// Determine swipe strength based on scroll magnitude
	strength := "small"
	if math.Abs(float64(scrollY)) >= 3 || math.Abs(float64(scrollX)) >= 3 {
		strength = "medium"
	}

	distance := 200.0 // Small distance
	if strength == "medium" {
		distance = 500.0
	}

	// Determine swipe direction
	if math.Abs(float64(scrollY)) > math.Abs(float64(scrollX)) {
		// Vertical scroll
		if scrollY < 0 {
			// Scroll down -> swipe up
			return p.dragWithDuration(ctx, [][2]float64{
				{centerX, centerY + distance/2},
				{centerX, centerY - distance/2},
			}, ButtonLeft, 650)
		} else {
			// Scroll up -> swipe down
			return p.dragWithDuration(ctx, [][2]float64{
				{centerX, centerY - distance/2},
				{centerX, centerY + distance/2},
			}, ButtonLeft, 650)
		}
	} else if scrollX != 0 {
		// Horizontal scroll
		if scrollX < 0 {
			// Scroll left -> swipe right
			return p.dragWithDuration(ctx, [][2]float64{
				{centerX - distance/2, centerY},
				{centerX + distance/2, centerY},
			}, ButtonLeft, 650)
		} else {
			// Scroll right -> swipe left
			return p.dragWithDuration(ctx, [][2]float64{
				{centerX + distance/2, centerY},
				{centerX - distance/2, centerY},
			}, ButtonLeft, 650)
		}
	}

	return nil
}

// ============================================================================
// Coordinate conversion
// ============================================================================

func (p *ADBProvider) normalizedToPixel(ctx context.Context, x, y float64) (int, int, error) {
	// Validate coordinates
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return 0, 0, fmt.Errorf("coordinates must be finite")
	}
	if x < 0 || x > 1000 || y < 0 || y > 1000 {
		return 0, 0, fmt.Errorf("coordinates must use the normalized 0-1000 scale, got x=%.2f y=%.2f", x, y)
	}

	// Get screen size
	size, err := p.screenSize(ctx)
	if err != nil {
		return 0, 0, err
	}

	// Convert normalized to pixel coordinates
	// ADB uses pixel coordinates directly
	pixelX := p.scaleToPixel(x, 1001, size.width)
	pixelY := p.scaleToPixel(y, 1001, size.height)

	return pixelX, pixelY, nil
}

func (p *ADBProvider) scaleToPixel(value float64, sourceSize int, targetSize int) int {
	if targetSize <= 1 || sourceSize <= 1 {
		return 0
	}
	maxSource := float64(sourceSize - 1)
	maxTarget := float64(targetSize - 1)
	clamped := clampFloat(value, 0, maxSource)
	return int(math.Round((clamped / maxSource) * maxTarget))
}

// ============================================================================
// Screen size resolution
// ============================================================================

func (p *ADBProvider) screenSize(ctx context.Context) (adbInputScreenSize, error) {
	// Try phone screen info first
	if size, ok := p.phoneScreenSize(); ok {
		return size, nil
	}

	// Check cache
	now := time.Now()
	p.mu.Lock()
	cachedSize := p.cachedScreenSize
	cachedExpiry := p.screenSizeExpires
	p.mu.Unlock()

	if cachedSize.width > 0 && cachedSize.height > 0 && now.Before(cachedExpiry) {
		return cachedSize, nil
	}

	// Query via adb
	out, err := p.runShellOutput(ctx, "wm", "size")
	if err == nil {
		if size, ok := p.parseWMSize(out); ok {
			p.mu.Lock()
			p.cachedScreenSize = size
			p.screenSizeExpires = now.Add(p.screenSizeTTL)
			p.mu.Unlock()
			return size, nil
		}
		err = fmt.Errorf("parse wm size: no screen size in %q", strings.TrimSpace(out))
	}

	// Fallback to screenshot dimensions
	if size, ok := p.fallbackScreenSize(); ok {
		return size, nil
	}

	return adbInputScreenSize{}, fmt.Errorf("resolve adb screen size: %w", err)
}

func (p *ADBProvider) phoneScreenSize() (adbInputScreenSize, bool) {
	if p.screen == nil {
		return adbInputScreenSize{}, false
	}

	info := p.screen.PhoneScreenInfo()
	if info.WidthPixels != nil && info.HeightPixels != nil && *info.WidthPixels > 0 && *info.HeightPixels > 0 {
		return adbInputScreenSize{width: *info.WidthPixels, height: *info.HeightPixels}, true
	}
	if info.NativeWidthPixels != nil && info.NativeHeightPixels != nil && *info.NativeWidthPixels > 0 && *info.NativeHeightPixels > 0 {
		return adbInputScreenSize{width: *info.NativeWidthPixels, height: *info.NativeHeightPixels}, true
	}

	return adbInputScreenSize{}, false
}

func (p *ADBProvider) fallbackScreenSize() (adbInputScreenSize, bool) {
	if p.screen == nil {
		return adbInputScreenSize{}, false
	}

	if width, height, active, age, ok := p.screen.ActiveAreaWithAge(); ok && age < adbScreenDimensionsStaleAfter {
		if active.Valid && active.Width > 0 && active.Height > 0 {
			return adbInputScreenSize{width: active.Width, height: active.Height}, true
		}
		if width > 0 && height > 0 {
			return adbInputScreenSize{width: width, height: height}, true
		}
	}

	return adbInputScreenSize{}, false
}

func (p *ADBProvider) parseWMSize(output string) (adbInputScreenSize, bool) {
	var size adbInputScreenSize
	for _, match := range adbWMSizePattern.FindAllStringSubmatch(output, -1) {
		if len(match) != 3 {
			continue
		}
		width, widthErr := strconv.Atoi(match[1])
		height, heightErr := strconv.Atoi(match[2])
		if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
			continue
		}
		size = adbInputScreenSize{width: width, height: height}
	}
	return size, size.width > 0 && size.height > 0
}

// ============================================================================
// Key resolution
// ============================================================================

func (p *ADBProvider) resolveKeycodes(rawKeys []string) ([]string, error) {
	keycodes := make([]string, 0, len(rawKeys))

	for _, rawKey := range rawKeys {
		normalized := strings.ToLower(strings.TrimSpace(rawKey))
		if normalized == "" {
			continue
		}

		if len(keycodes) >= 6 {
			return nil, fmt.Errorf("keypress supports at most 6 simultaneous keys")
		}

		keycode, err := p.resolveKeycode(normalized)
		if err != nil {
			return nil, err
		}
		keycodes = append(keycodes, keycode)
	}

	if len(keycodes) == 0 {
		return nil, fmt.Errorf("at least one key is required")
	}

	return keycodes, nil
}

func (p *ADBProvider) resolveKeycode(key string) (string, error) {
	// Check aliases first
	if keycode, ok := adbAndroidKeycodeAliases[key]; ok {
		return keycode, nil
	}

	// Check standard keyboard map
	if keycode, ok := adbKeyboardKeycodeMap[key]; ok {
		return keycode, nil
	}

	// Pass through KEYCODE_* format
	if strings.HasPrefix(strings.ToUpper(key), "KEYCODE_") {
		return strings.ToUpper(key), nil
	}

	// Reject HID-only keys
	if strings.HasPrefix(key, "key_usage_") {
		return "", fmt.Errorf("%s is a HID usage alias and has no adb keyevent equivalent", key)
	}

	return "", fmt.Errorf("unknown adb key: %q", key)
}

// ============================================================================
// ADB command execution
// ============================================================================

func (p *ADBProvider) runShell(ctx context.Context, args ...string) error {
	_, err := p.runShellOutput(ctx, args...)
	return err
}

func (p *ADBProvider) runShellWithTimeout(ctx context.Context, timeout time.Duration, args ...string) error {
	_, err := p.runShellOutputWithTimeout(ctx, timeout, args...)
	return err
}

func (p *ADBProvider) runShellOutput(ctx context.Context, args ...string) (string, error) {
	return p.runShellOutputWithTimeout(ctx, p.commandTimeout, args...)
}

func (p *ADBProvider) runShellOutputWithTimeout(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	shellArgs := make([]string, 0, len(args)+1)
	shellArgs = append(shellArgs, "shell")
	shellArgs = append(shellArgs, args...)
	return p.runWithTimeout(ctx, timeout, shellArgs...)
}

func (p *ADBProvider) runWithTimeout(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = p.commandTimeout
	}

	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Get adb path
	adbPath, err := p.client.ADBPath()
	if err != nil {
		return "", fmt.Errorf("adb not available: %w", err)
	}

	// Resolve device serial
	serial, err := p.client.ResolveSerial(cmdCtx, adbPath)
	if err != nil {
		return "", fmt.Errorf("resolve adb device: %w", err)
	}

	// Build adb arguments
	adbArgs := make([]string, 0, len(args)+2)
	if serial != "" {
		adbArgs = append(adbArgs, "-s", serial)
	}
	adbArgs = append(adbArgs, args...)

	// Execute command
	stdout, stderr, err := p.runADB(cmdCtx, adbPath, adbArgs...)
	if err != nil {
		p.client.InvalidateAutoSerial(serial)
		if trimmed := strings.TrimSpace(string(stderr)); trimmed != "" {
			return strings.TrimSpace(string(stdout)), fmt.Errorf("adb command failed: %s", trimmed)
		}
		return strings.TrimSpace(string(stdout)), err
	}

	out := strings.TrimSpace(string(stdout))
	if errText := strings.TrimSpace(string(stderr)); errText != "" {
		if out != "" {
			out += "\n"
		}
		out += errText
	}

	return out, nil
}

// ============================================================================
// Utility functions
// ============================================================================

func (p *ADBProvider) timeoutForDuration(durationMs int) time.Duration {
	if durationMs <= 0 {
		return p.commandTimeout
	}
	timeout := time.Duration(durationMs)*time.Millisecond + p.gestureTimeoutPad
	if timeout < p.commandTimeout {
		return p.commandTimeout
	}
	return timeout
}

func (p *ADBProvider) clampDuration(value, fallback, minimum int) int {
	if value <= 0 {
		value = fallback
	}
	if value < minimum {
		value = minimum
	}
	if value > p.maxDurationMs {
		value = p.maxDurationMs
	}
	return value
}

// ADBScreenClient discovers the adb executable and resolves a target device.
type ADBScreenClient struct {
	mu              sync.Mutex
	adbPath         string
	autoSerial      string
	autoSerialValid bool
	commandRunner   adbCommandRunner
}

func NewADBScreenClient() *ADBScreenClient {
	return &ADBScreenClient{}
}

func (c *ADBScreenClient) setCommandRunner(runner adbCommandRunner) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commandRunner = runner
}

func (c *ADBScreenClient) ADBPath() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.adbPath != "" {
		return c.adbPath, nil
	}

	if configured := strings.TrimSpace(os.Getenv("AIDEN_ADB_PATH")); configured != "" {
		c.adbPath = configured
		return c.adbPath, nil
	}

	// Try to find adb in PATH
	path, err := exec.LookPath("adb")
	if err != nil {
		return "", fmt.Errorf("adb not found in PATH: %w", err)
	}

	c.adbPath = path
	return path, nil
}

func (c *ADBScreenClient) ResolveSerial(ctx context.Context, adbPath string) (string, error) {
	for _, key := range []string{"AIDEN_ADB_SERIAL", "ANDROID_SERIAL"} {
		if serial := strings.TrimSpace(os.Getenv(key)); serial != "" {
			return serial, nil
		}
	}

	c.mu.Lock()
	if c.autoSerialValid && c.autoSerial != "" {
		serial := c.autoSerial
		c.mu.Unlock()
		return serial, nil
	}
	runner := c.commandRunner
	c.mu.Unlock()

	// Use injectable runner if available, otherwise fall back to package-level function
	if runner == nil {
		runner = runADBCommand
	}

	// Query connected devices
	stdout, stderr, err := runner(ctx, adbPath, "devices")
	if err != nil {
		return "", fmt.Errorf("adb devices failed: %w", err)
	}

	output := string(stdout)
	if errText := strings.TrimSpace(string(stderr)); errText != "" {
		output += "\n" + errText
	}

	// Parse device list
	var devices []string
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") || strings.HasPrefix(line, "*") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[1] == "device" {
			devices = append(devices, parts[0])
		}
	}

	if len(devices) == 0 {
		return "", fmt.Errorf("no adb devices connected")
	}

	if len(devices) > 1 {
		return "", fmt.Errorf("multiple adb devices connected; set AIDEN_ADB_SERIAL or ANDROID_SERIAL")
	}

	// There is exactly one connected device, so it is safe to cache it.
	serial := devices[0]

	c.mu.Lock()
	c.autoSerial = serial
	c.autoSerialValid = true
	c.mu.Unlock()

	return serial, nil
}

func (p *ADBProvider) androidSDKVersion(ctx context.Context) (int, error) {
	p.mu.Lock()
	if p.androidSDKValid {
		androidSDK := p.androidSDK
		p.mu.Unlock()
		return androidSDK, nil
	}
	p.mu.Unlock()

	output, err := p.runShellOutput(ctx, "getprop", "ro.build.version.sdk")
	if err != nil {
		return 0, fmt.Errorf("detect Android API level for key combination: %w", err)
	}
	androidSDK, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil || androidSDK <= 0 {
		return 0, fmt.Errorf("detect Android API level for key combination: invalid value %q", output)
	}

	p.mu.Lock()
	p.androidSDK = androidSDK
	p.androidSDKValid = true
	p.mu.Unlock()
	return androidSDK, nil
}

func (c *ADBScreenClient) InvalidateAutoSerial(serial string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.autoSerial == serial {
		c.autoSerialValid = false
	}
}

func runADBCommand(ctx context.Context, adbPath string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, adbPath, args...)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return []byte(stdout.String()), []byte(stderr.String()), err
}
