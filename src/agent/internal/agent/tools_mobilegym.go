package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
)

const mobileGymBridgeRequestTimeout = 30 * time.Second

const (
	defaultMobileGymScreenWidth  = 1080
	defaultMobileGymScreenHeight = 2400
)

type mobileGymBridgeClient struct {
	sessions *mobileGymSessionStore
	http     *http.Client
}

type mobileGymEpisodeRequest struct {
	EpisodeID string `json:"episode_id"`
}

type mobileGymScreenshotTool struct {
	client *mobileGymBridgeClient
	screen *screenState
}

type mobileGymActionResponse struct {
	OK         *bool             `json:"ok,omitempty"`
	Message    string            `json:"message,omitempty"`
	Screenshot *screenshotResult `json:"screenshot,omitempty"`
}

type mobileGymTouchGestureTool struct {
	client *mobileGymBridgeClient
	screen *screenState
}

type mobileGymMouseClickTool struct {
	client *mobileGymBridgeClient
	screen *screenState
}

type mobileGymMouseMoveTool struct {
	screen *screenState
}

type mobileGymMouseScrollTool struct {
	client *mobileGymBridgeClient
	screen *screenState
}

type mobileGymKeyboardTextTool struct {
	client *mobileGymBridgeClient
	screen *screenState
}

type mobileGymKeyboardTapTool struct {
	client *mobileGymBridgeClient
	screen *screenState
}

type mobileGymTapRequest struct {
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Count      int    `json:"count,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Button     string `json:"button,omitempty"`
	HoldMs     int    `json:"hold_ms,omitempty"`
	DurationMs int    `json:"duration_ms,omitempty"`
}

type mobileGymSwipeRequest struct {
	StartX       int `json:"start_x"`
	StartY       int `json:"start_y"`
	EndX         int `json:"end_x"`
	EndY         int `json:"end_y"`
	DurationMs   int `json:"duration_ms,omitempty"`
	HoldBeforeMs int `json:"hold_before_ms,omitempty"`
	HoldAfterMs  int `json:"hold_after_ms,omitempty"`
	Steps        int `json:"steps,omitempty"`
}

type mobileGymTextRequest struct {
	Text string `json:"text"`
}

type mobileGymKeyRequest struct {
	Key       string   `json:"key"`
	Modifiers []string `json:"modifiers,omitempty"`
}

type mobileGymPoint struct {
	x int
	y int
}

func newMobileGymToolSet(cfg Config, proxyCfg ProxyConfig, mobileGym *mobileGymSessionStore, options ...BuiltinToolSetOption) *ToolSet {
	toolOptions := builtinToolSetOptions{}
	for _, option := range options {
		if option != nil {
			option(&toolOptions)
		}
	}
	if mobileGym == nil {
		mobileGym = &mobileGymSessionStore{}
	}
	screen := &screenState{}
	client := &mobileGymBridgeClient{sessions: mobileGym, http: &http.Client{Timeout: mobileGymBridgeRequestTimeout}}
	screenshot := &mobileGymScreenshotTool{client: client, screen: screen}

	tools := map[string]langtools.Tool{
		"keyboard_tap":  &mobileGymKeyboardTapTool{client: client, screen: screen},
		"keyboard_text": &mobileGymKeyboardTextTool{client: client, screen: screen},
		"mouse_click":   &mobileGymMouseClickTool{client: client, screen: screen},
		"mouse_move":    &mobileGymMouseMoveTool{screen: screen},
		"mouse_scroll":  &mobileGymMouseScrollTool{client: client, screen: screen},
		"touch_gesture": &mobileGymTouchGestureTool{client: client, screen: screen},
		"screenshot":    screenshot,
		"current_time":  NewCurrentTimeTool(),
		"calculator":    NewCalculatorTool(),
	}
	if toolOptions.sleepController != nil {
		tools["enter_sleep"] = NewEnterSleepTool(toolOptions.sleepController)
	}
	return &ToolSet{tools: tools, screen: screen}
}

func (t *mobileGymScreenshotTool) Name() string { return "screenshot" }

func (t *mobileGymScreenshotTool) ReturnsVisualObservation() bool { return true }

func (t *mobileGymScreenshotTool) Description() string {
	return `Capture a screenshot from the MobileGym simulator. No input required (pass empty JSON {} or ""). ` +
		`Returns a JSON object with width, height, and base64-encoded JPEG image data.`
}

func (t *mobileGymScreenshotTool) ArgsSchema() map[string]any {
	return (&ScreenshotTool{}).ArgsSchema()
}

func (t *mobileGymScreenshotTool) Call(ctx context.Context, _ string) (string, error) {
	var result screenshotResult
	if err := t.client.post(ctx, "/screenshot", mobileGymEpisodeRequest{}, &result); err != nil {
		return "", err
	}
	if err := normalizeMobileGymScreenshotResult(t.screen, &result); err != nil {
		return "", err
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}

func (t *mobileGymTouchGestureTool) Name() string { return "touch_gesture" }

func (t *mobileGymTouchGestureTool) ReturnsVisualObservation() bool { return true }

func (t *mobileGymTouchGestureTool) Description() string {
	return (&TouchGestureTool{}).Description() + " MobileGym backend sends gestures through the simulator bridge."
}

func (t *mobileGymTouchGestureTool) ArgsSchema() map[string]any {
	return (&TouchGestureTool{}).ArgsSchema()
}

func (t *mobileGymTouchGestureTool) Call(ctx context.Context, input string) (string, error) {
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

	switch gestureType {
	case "tap":
		point, err := resolveRequiredMobileGymPoint(t.screen, args.Point, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		return callMobileGymAction(ctx, t.client, t.screen, "/tap", mobileGymTapRequest{X: point.x, Y: point.y, Button: normalizeMobileGymButton(args.Button), HoldMs: intOrDefault(args.HoldMs, defaultTapHoldMs)})
	case "double_tap":
		point, err := resolveRequiredMobileGymPoint(t.screen, args.Point, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		return callMobileGymAction(ctx, t.client, t.screen, "/tap", mobileGymTapRequest{X: point.x, Y: point.y, Count: 2, Button: normalizeMobileGymButton(args.Button), HoldMs: intOrDefault(args.HoldMs, defaultTapHoldMs)})
	case "long_press":
		point, err := resolveRequiredMobileGymPoint(t.screen, args.Point, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		return callMobileGymAction(ctx, t.client, t.screen, "/tap", mobileGymTapRequest{X: point.x, Y: point.y, Kind: "long_press", Button: normalizeMobileGymButton(args.Button), DurationMs: intOrDefault(args.DurationMs, 500)})
	case "swipe":
		start, err := resolveRequiredMobileGymPoint(t.screen, args.Start, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		end, err := resolveRequiredMobileGymPoint(t.screen, args.End, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		return callMobileGymSwipe(ctx, t.client, t.screen, start, end, intOrDefault(args.DurationMs, defaultSwipeDurationMs), intOrDefault(args.HoldBeforeMs, defaultSwipeHoldBeforeMs), intOrDefault(args.HoldAfterMs, defaultSwipeHoldAfterMs), positiveIntOrDefault(args.Steps, defaultSwipeSteps), "/swipe")
	case "drag":
		start, err := resolveRequiredMobileGymPoint(t.screen, args.Start, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		end, err := resolveRequiredMobileGymPoint(t.screen, args.End, coordSpace)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		return callMobileGymSwipe(ctx, t.client, t.screen, start, end, intOrDefault(args.DurationMs, 250), intOrDefault(args.HoldBeforeMs, 0), intOrDefault(args.HoldAfterMs, 0), positiveIntOrDefault(args.Steps, 12), "/drag")
	case "swipe_left", "swipe_right", "swipe_up", "swipe_down":
		preset, err := directionalSwipePreset(args.Strength)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		start, end, err := mobileGymDirectionalSwipePoints(t.screen, gestureType, args.Distance, args.Anchor, preset)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		return callMobileGymSwipe(ctx, t.client, t.screen, start, end, intOrDefault(args.DurationMs, preset.durationMs), intOrDefault(args.HoldBeforeMs, preset.holdBeforeMs), intOrDefault(args.HoldAfterMs, preset.holdAfterMs), positiveIntOrDefault(args.Steps, preset.steps), "/swipe")
	case "back", "edge_back", "left_edge_back":
		return callMobileGymAction(ctx, t.client, t.screen, "/back", struct{}{})
	case "home", "home_swipe", "bottom_edge_home":
		return callMobileGymAction(ctx, t.client, t.screen, "/home", struct{}{})
	default:
		return fmt.Sprintf("error: unsupported gesture type: %q", args.Type), nil
	}
}

func (t *mobileGymMouseClickTool) Name() string { return "mouse_click" }

func (t *mobileGymMouseClickTool) ReturnsVisualObservation() bool { return true }

func (t *mobileGymMouseClickTool) Description() string {
	return (&MouseClickTool{}).Description() + " MobileGym backend converts the click to a simulator tap."
}

func (t *mobileGymMouseClickTool) ArgsSchema() map[string]any {
	return (&MouseClickTool{}).ArgsSchema()
}

func (t *mobileGymMouseClickTool) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		X          pointerCoordinate `json:"x"`
		Y          pointerCoordinate `json:"y"`
		Button     string            `json:"button"`
		CoordSpace string            `json:"coord_space"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v", err), nil
	}
	x, y, err := resolveMobileGymPosition(t.screen, args.X.Float64(), args.Y.Float64(), args.CoordSpace, coordinateSpaceAuto)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	return callMobileGymAction(ctx, t.client, t.screen, "/tap", mobileGymTapRequest{X: x, Y: y, Button: normalizeMobileGymButton(args.Button), HoldMs: defaultTapHoldMs})
}

func (t *mobileGymMouseMoveTool) Name() string { return "mouse_move" }

func (t *mobileGymMouseMoveTool) Description() string {
	return (&MouseMoveTool{}).Description() + " MobileGym backend treats this as a logical cursor update and does not mutate the simulator."
}

func (t *mobileGymMouseMoveTool) ArgsSchema() map[string]any {
	return (&MouseMoveTool{}).ArgsSchema()
}

func (t *mobileGymMouseMoveTool) Call(_ context.Context, input string) (string, error) {
	var args struct {
		X          pointerCoordinate `json:"x"`
		Y          pointerCoordinate `json:"y"`
		CoordSpace string            `json:"coord_space"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v", err), nil
	}
	if _, _, err := resolveMobileGymPosition(t.screen, args.X.Float64(), args.Y.Float64(), args.CoordSpace, coordinateSpaceAuto); err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	return "ok", nil
}

func (t *mobileGymMouseScrollTool) Name() string { return "mouse_scroll" }

func (t *mobileGymMouseScrollTool) ReturnsVisualObservation() bool { return true }

func (t *mobileGymMouseScrollTool) Description() string {
	return (&MouseScrollTool{}).Description() + " MobileGym backend converts wheel deltas to vertical swipe gestures."
}

func (t *mobileGymMouseScrollTool) ArgsSchema() map[string]any {
	return (&MouseScrollTool{}).ArgsSchema()
}

func (t *mobileGymMouseScrollTool) Call(ctx context.Context, input string) (string, error) {
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

	distance := clampFloat(math.Abs(float64(args.Delta))*80, 120, 600)
	half := distance / 2
	startY, endY := 500-half, 500+half
	if args.Delta < 0 {
		startY, endY = 500+half, 500-half
	}
	start, err := resolveMobileGymNormalizedPoint(t.screen, 500, startY)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	end, err := resolveMobileGymNormalizedPoint(t.screen, 500, endY)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	return callMobileGymSwipe(ctx, t.client, t.screen, start, end, defaultSwipeDurationMs, defaultSwipeHoldBeforeMs, defaultSwipeHoldAfterMs, defaultSwipeSteps, "/swipe")
}

func (t *mobileGymKeyboardTextTool) Name() string { return "keyboard_text" }

func (t *mobileGymKeyboardTextTool) ReturnsVisualObservation() bool { return true }

func (t *mobileGymKeyboardTextTool) Description() string {
	return (&KeyboardTextTool{}).Description() + " MobileGym backend types the text through the simulator bridge."
}

func (t *mobileGymKeyboardTextTool) ArgsSchema() map[string]any {
	return (&KeyboardTextTool{}).ArgsSchema()
}

func (t *mobileGymKeyboardTextTool) Call(ctx context.Context, input string) (string, error) {
	text, errText := parseKeyboardTextInput(input)
	if errText != "" {
		return errText, nil
	}
	return callMobileGymAction(ctx, t.client, t.screen, "/type_text", mobileGymTextRequest{Text: text})
}

func (t *mobileGymKeyboardTapTool) Name() string { return "keyboard_tap" }

func (t *mobileGymKeyboardTapTool) ReturnsVisualObservation() bool { return true }

func (t *mobileGymKeyboardTapTool) Description() string {
	return (&KeyboardTapTool{}).Description() + " MobileGym backend sends common keys through the simulator bridge; meta+h maps to Android home."
}

func (t *mobileGymKeyboardTapTool) ArgsSchema() map[string]any {
	return (&KeyboardTapTool{}).ArgsSchema()
}

func (t *mobileGymKeyboardTapTool) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v", err), nil
	}
	if len(args.Keys) == 0 {
		return "error: keys array is required", nil
	}

	modifiers := make([]string, 0, len(args.Keys))
	keys := make([]string, 0, len(args.Keys))
	hasMeta := false
	for _, key := range args.Keys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if _, ok := hidModifierMap[key]; ok {
			modifiers = append(modifiers, key)
			if key == "meta" || key == "lmeta" || key == "rmeta" || key == "super" || key == "win" || key == "cmd" {
				hasMeta = true
			}
			continue
		}
		if _, ok := hidKeyboardMap[key]; !ok {
			return fmt.Sprintf("error: unknown key: %q", key), nil
		}
		keys = append(keys, key)
	}
	if hasMeta && len(keys) == 1 && keys[0] == "h" {
		return callMobileGymAction(ctx, t.client, t.screen, "/home", struct{}{})
	}
	if len(keys) == 0 {
		return "error: non-modifier key is required", nil
	}
	if len(keys) > 1 {
		return "error: mobilegym keyboard_tap supports one non-modifier key at a time", nil
	}
	return callMobileGymAction(ctx, t.client, t.screen, "/key", mobileGymKeyRequest{Key: keys[0], Modifiers: modifiers})
}

func normalizeMobileGymScreenshotResult(screen *screenState, result *screenshotResult) error {
	if result == nil {
		return fmt.Errorf("mobilegym screenshot result is missing")
	}
	if result.Width <= 0 || result.Height <= 0 {
		return fmt.Errorf("mobilegym screenshot returned invalid dimensions %dx%d", result.Width, result.Height)
	}
	if strings.TrimSpace(result.Data) == "" {
		return fmt.Errorf("mobilegym screenshot payload is empty")
	}
	if result.Format == "" {
		result.Format = "jpeg"
	}
	if result.Size == 0 && result.Data != "" {
		result.Size = len(result.Data)
	}
	if result.Size == 0 {
		return fmt.Errorf("mobilegym screenshot has zero size")
	}
	if screen != nil {
		screen.Update(result.Width, result.Height)
	}
	return nil
}

func callMobileGymSwipe(ctx context.Context, client *mobileGymBridgeClient, screen *screenState, start, end mobileGymPoint, durationMs, holdBeforeMs, holdAfterMs, steps int, path string) (string, error) {
	return callMobileGymAction(ctx, client, screen, path, mobileGymSwipeRequest{
		StartX:       start.x,
		StartY:       start.y,
		EndX:         end.x,
		EndY:         end.y,
		DurationMs:   durationMs,
		HoldBeforeMs: holdBeforeMs,
		HoldAfterMs:  holdAfterMs,
		Steps:        steps,
	})
}

func callMobileGymAction(ctx context.Context, client *mobileGymBridgeClient, screen *screenState, path string, payload any) (string, error) {
	var response mobileGymActionResponse
	if err := client.post(ctx, path, payload, &response); err != nil {
		return "", err
	}
	return formatMobileGymActionOutput(screen, response), nil
}

func formatMobileGymActionOutput(screen *screenState, response mobileGymActionResponse) string {
	message := strings.TrimSpace(response.Message)
	if response.OK != nil && !*response.OK {
		if message == "" {
			message = "mobilegym bridge action failed"
		}
		return "error: " + message
	}
	if message == "" {
		message = "ok"
	}
	if response.Screenshot == nil {
		return message
	}
	if err := normalizeMobileGymScreenshotResult(screen, response.Screenshot); err != nil {
		return fmt.Sprintf("error: action completed with output %q, but post-action screenshot was invalid: %v", message, err)
	}
	out, _ := json.Marshal(postActionScreenshotResult{
		screenshotResult: *response.Screenshot,
		ActionOutput:     message,
	})
	return string(out)
}

func resolveRequiredMobileGymPoint(screen *screenState, point *pointerPoint, coordSpace string) (mobileGymPoint, error) {
	if point == nil {
		return mobileGymPoint{}, fmt.Errorf("point is required")
	}
	x, y, err := resolveMobileGymPosition(screen, point.X.Float64(), point.Y.Float64(), coordSpace, coordinateSpaceNormalized)
	if err != nil {
		return mobileGymPoint{}, err
	}
	return mobileGymPoint{x: x, y: y}, nil
}

func resolveMobileGymPosition(screen *screenState, x, y float64, coordSpace string, defaultSpace string) (int, int, error) {
	space, err := normalizeCoordinateSpace(coordSpace, defaultSpace)
	if err != nil {
		return 0, 0, err
	}
	switch space {
	case coordinateSpaceAuto:
		if looksLikeNormalizedPoint(x, y) {
			point, err := resolveMobileGymNormalizedPoint(screen, x, y)
			return point.x, point.y, err
		}
		return resolveMobileGymPixelPosition(screen, x, y)
	case coordinateSpaceNormalized:
		point, err := resolveMobileGymNormalizedPoint(screen, x, y)
		return point.x, point.y, err
	case coordinateSpacePixel:
		return resolveMobileGymPixelPosition(screen, x, y)
	case coordinateSpaceAbsolute:
		width, height, ok := freshMobileGymScreenDimensions(screen)
		if !ok {
			return 0, 0, fmt.Errorf("absolute coordinates require known screen dimensions; call screenshot first or use coord_space normalized")
		}
		return scaleAbsoluteToMobileGymPixel(x, width), scaleAbsoluteToMobileGymPixel(y, height), nil
	}
	return 0, 0, fmt.Errorf("unsupported coord_space: %q", coordSpace)
}

func resolveMobileGymNormalizedPoint(screen *screenState, x, y float64) (mobileGymPoint, error) {
	width, height, ok := freshMobileGymScreenDimensions(screen)
	if !ok {
		width, height = defaultMobileGymScreenWidth, defaultMobileGymScreenHeight
	}
	return mobileGymPoint{x: scaleNormalizedToMobileGymPixel(x, width), y: scaleNormalizedToMobileGymPixel(y, height)}, nil
}

func resolveMobileGymPixelPosition(screen *screenState, x, y float64) (int, int, error) {
	width, height, ok := freshMobileGymScreenDimensions(screen)
	if !ok {
		return 0, 0, fmt.Errorf("pixel coordinates require known screen dimensions; call screenshot first or use coord_space normalized")
	}
	if x < 0 || y < 0 || x > float64(width-1) || y > float64(height-1) {
		return 0, 0, fmt.Errorf("pixel coordinates x=%.2f y=%.2f are outside cached screenshot bounds %dx%d; use coord_space normalized with 0-1000 coordinates, where 500,500 is center, or refresh the screenshot dimensions", x, y, width, height)
	}
	return int(math.Round(x)), int(math.Round(y)), nil
}

func freshMobileGymScreenDimensions(screen *screenState) (int, int, bool) {
	if screen == nil {
		return 0, 0, false
	}
	width, height, age, ok := screen.DimensionsWithAge()
	if !ok || age >= screenDimensionsStaleAfter {
		return 0, 0, false
	}
	return width, height, true
}

func scaleNormalizedToMobileGymPixel(value float64, size int) int {
	if size <= 1 {
		return 0
	}
	return int(math.Round(clampFloat(value, 0, 1000) / 1000.0 * float64(size-1)))
}

func scaleAbsoluteToMobileGymPixel(value float64, size int) int {
	if size <= 1 {
		return 0
	}
	return int(math.Round((clampFloat(value, 0, absMouseMaxPos) / absMouseMaxPos) * float64(size-1)))
}

func mobileGymDirectionalSwipePoints(screen *screenState, gestureType string, distance, anchor *float64, preset directionalSwipeSettings) (mobileGymPoint, mobileGymPoint, error) {
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
		return mobileGymPoint{}, mobileGymPoint{}, fmt.Errorf("unsupported directional swipe: %q", gestureType)
	}

	start, err := resolveMobileGymNormalizedPoint(screen, startX, startY)
	if err != nil {
		return mobileGymPoint{}, mobileGymPoint{}, err
	}
	end, err := resolveMobileGymNormalizedPoint(screen, endX, endY)
	if err != nil {
		return mobileGymPoint{}, mobileGymPoint{}, err
	}
	return start, end, nil
}

func normalizeMobileGymButton(button string) string {
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "right", "middle":
		return strings.ToLower(strings.TrimSpace(button))
	default:
		return "left"
	}
}

func (c *mobileGymBridgeClient) post(ctx context.Context, path string, payload any, out any) error {
	if c == nil || c.sessions == nil {
		return fmt.Errorf("mobilegym bridge session is not configured")
	}
	session, ok := c.sessions.Get()
	if !ok {
		return fmt.Errorf("mobilegym bridge episode is not active")
	}
	if session.BridgeURL == "" || session.BridgeToken == "" || session.EpisodeID == "" {
		return fmt.Errorf("mobilegym bridge session is incomplete")
	}

	data, err := json.Marshal(withMobileGymEpisodeID(payload, session.EpisodeID))
	if err != nil {
		return err
	}
	url := strings.TrimRight(session.BridgeURL, "/") + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+session.BridgeToken)
	client := c.http
	if client == nil {
		client = &http.Client{Timeout: mobileGymBridgeRequestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mobilegym bridge %s returned HTTP %d", path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func withMobileGymEpisodeID(payload any, episodeID string) any {
	data, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"episode_id": episodeID}
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		obj = map[string]any{}
	}
	obj["episode_id"] = episodeID
	return obj
}
