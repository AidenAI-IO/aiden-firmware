package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
)

const screenshotJPEGQuality = 80

type adbDeviceInfo struct {
	Serial string `json:"serial,omitempty"`
	Name   string `json:"name,omitempty"`
	State  string `json:"state,omitempty"`
}

type screenCaptureInfo struct {
	Backend   string         `json:"capture_backend,omitempty"`
	ADBDevice *adbDeviceInfo `json:"adb_device,omitempty"`
}

type screenshotResult struct {
	Width          int               `json:"width"`
	Height         int               `json:"height"`
	ActiveArea     *screenActiveArea `json:"active_area,omitempty"`
	ActiveWidth    int               `json:"active_width,omitempty"`
	ActiveHeight   int               `json:"active_height,omitempty"`
	Format         string            `json:"format"`
	Size           int               `json:"size"`
	Data           string            `json:"data,omitempty"`
	ScreenshotRef  string            `json:"screenshot_ref,omitempty"`
	CaptureBackend string            `json:"capture_backend,omitempty"`
	ADBDevice      *adbDeviceInfo    `json:"adb_device,omitempty"`
}

type screenshotFrameClient interface {
	LatestFrameWithFormat(format string, quality int) (*frameMetadata, []byte, screenCaptureInfo, error)
}

func cloneADBDeviceInfo(info *adbDeviceInfo) *adbDeviceInfo {
	if info == nil {
		return nil
	}
	copy := *info
	return &copy
}

func cloneScreenCaptureInfo(info screenCaptureInfo) screenCaptureInfo {
	info.ADBDevice = cloneADBDeviceInfo(info.ADBDevice)
	return info
}

func applyScreenCaptureInfo(result *screenshotResult, info screenCaptureInfo) {
	if result == nil {
		return
	}
	result.CaptureBackend = info.Backend
	result.ADBDevice = cloneADBDeviceInfo(info.ADBDevice)
}

// ScreenshotTool captures a screenshot from the frame service.
type ScreenshotTool struct {
	client screenshotFrameClient
	screen *screenState
}

func frameMetadataSourceActiveArea(meta *frameMetadata) (sourceWidth, sourceHeight int, active screenActiveArea, ok bool) {
	if meta == nil || meta.SourceWidth == 0 || meta.SourceHeight == 0 {
		return 0, 0, screenActiveArea{}, false
	}
	sourceWidth = int(meta.SourceWidth)
	sourceHeight = int(meta.SourceHeight)
	active = screenActiveArea{
		X:      int(meta.CropX),
		Y:      int(meta.CropY),
		Width:  int(meta.CropWidth),
		Height: int(meta.CropHeight),
		Valid:  meta.CropWidth > 0 && meta.CropHeight > 0,
	}
	if !active.Valid {
		active = screenActiveArea{
			X:      0,
			Y:      0,
			Width:  sourceWidth,
			Height: sourceHeight,
			Valid:  true,
		}
	}
	if active.X < 0 || active.Y < 0 || active.Width <= 0 || active.Height <= 0 ||
		active.X+active.Width > sourceWidth || active.Y+active.Height > sourceHeight {
		return 0, 0, screenActiveArea{}, false
	}
	return sourceWidth, sourceHeight, active, true
}

func NewScreenshotTool(socketPath string, screen *screenState) *ScreenshotTool {
	return &ScreenshotTool{
		client: NewScreenCaptureClient(socketPath),
		screen: screen,
	}
}

func (t *ScreenshotTool) Name() string { return "screenshot" }

func (t *ScreenshotTool) ReturnsVisualObservation() bool { return true }

func (t *ScreenshotTool) Description() string {
	return `Capture a screenshot from the connected display. No input required (pass empty JSON {} or ""). ` +
		`Returns a JSON object with width, height, and base64-encoded JPEG image data. ` +
		`The returned width and height define coord_space:"screenshot": use pixel coordinates exactly as measured in this returned image.`
}

func (t *ScreenshotTool) ArgsSchema() map[string]any {
	return objectArgsSchema(nil)
}

func (t *ScreenshotTool) Call(_ context.Context, _ string) (string, error) {
	// Request JPEG format directly from frame_service (hardware-encoded)
	meta, jpegData, captureInfo, err := t.client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality)
	if err != nil {
		return "", err
	}
	if meta.Stale {
		return "", fmt.Errorf("frame service: STALE_FRAME")
	}
	if meta.PixelFormat != "jpeg" {
		return "", fmt.Errorf("expected jpeg format, got %s", meta.PixelFormat)
	}
	if touchscreenRCADebugEnabledCached() {
		touchscreenRCALogf("screenshot frame meta=%s capture_backend=%q mapping_before={%s}", formatTouchscreenRCAMetadata(meta), captureInfo.Backend, formatTouchscreenRCAScreenMapping(t.screen))
	}
	active := screenActiveArea{}
	sourceWidth := int(meta.Width)
	sourceHeight := int(meta.Height)
	alreadyCropped := false
	if fullWidth, fullHeight, sourceActive, ok := frameMetadataSourceActiveArea(meta); ok {
		sourceWidth = fullWidth
		sourceHeight = fullHeight
		active = sourceActive
		alreadyCropped = true
	} else {
		active = detectScreenshotActiveAreaForScreen(t.screen, jpegData, int(meta.Width), int(meta.Height))
	}
	if touchscreenRCADebugEnabledCached() {
		touchscreenRCALogf(
			"screenshot resolved active_area source=%dx%d active=%s already_cropped=%v jpeg_dimensions=%dx%d mapping_before_update={%s}",
			sourceWidth,
			sourceHeight,
			formatTouchscreenRCAActiveArea(active),
			alreadyCropped,
			meta.Width,
			meta.Height,
			formatTouchscreenRCAScreenMapping(t.screen),
		)
	}
	if t.screen != nil {
		t.screen.UpdateActiveArea(sourceWidth, sourceHeight, active)
	}

	// Crop to active_area so LLM sees only the mirrored phone touch region.
	displayWidth := int(meta.Width)
	displayHeight := int(meta.Height)
	displayData := jpegData
	if !alreadyCropped && active.Valid && (active.X != 0 || active.Y != 0 || active.Width != displayWidth || active.Height != displayHeight) {
		croppedData, croppedWidth, croppedHeight, err := cropJPEGToActiveArea(jpegData, active, screenshotJPEGQuality)
		if err != nil {
			return "", fmt.Errorf("crop screenshot to active area: %w", err)
		}
		displayWidth = croppedWidth
		displayHeight = croppedHeight
		displayData = croppedData
	}

	result := screenshotResult{
		Width:  displayWidth,
		Height: displayHeight,
		Format: "jpeg",
		Size:   len(displayData),
		Data:   base64.StdEncoding.EncodeToString(displayData),
	}
	applyScreenCaptureInfo(&result, captureInfo)
	if touchscreenRCADebugEnabledCached() {
		touchscreenRCALogf(
			"screenshot result display=%dx%d size=%d capture_backend=%q mapping_after={%s}",
			displayWidth,
			displayHeight,
			len(displayData),
			result.CaptureBackend,
			formatTouchscreenRCAScreenMapping(t.screen),
		)
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}

func detectScreenshotActiveArea(jpegData []byte, expectedWidth, expectedHeight int) screenActiveArea {
	return detectScreenshotActiveAreaWithPhoneScreen(jpegData, expectedWidth, expectedHeight, nil)
}

func detectScreenshotActiveAreaForScreen(screen *screenState, jpegData []byte, expectedWidth, expectedHeight int) screenActiveArea {
	if screen == nil {
		return detectScreenshotActiveArea(jpegData, expectedWidth, expectedHeight)
	}
	phoneScreen := screen.PhoneScreenInfo()
	return detectScreenshotActiveAreaWithPhoneScreen(jpegData, expectedWidth, expectedHeight, &phoneScreen)
}

func detectScreenshotActiveAreaWithPhoneScreen(jpegData []byte, expectedWidth, expectedHeight int, phoneScreen *PhoneScreenInfo) screenActiveArea {
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return screenActiveArea{}
	}
	approx := detectImageActiveArea(img, expectedWidth, expectedHeight)
	if phoneScreen != nil {
		if derived, ok := deriveActiveAreaFromPhoneScreen(expectedWidth, expectedHeight, *phoneScreen, approx); ok {
			return derived
		}
	}
	return approx
}

func deriveActiveAreaFromPhoneScreen(frameWidth, frameHeight int, phoneScreen PhoneScreenInfo, approx screenActiveArea) (screenActiveArea, bool) {
	if frameWidth <= 0 || frameHeight <= 0 {
		return screenActiveArea{}, false
	}
	candidates := phoneScreenAspectRatioCandidates(phoneScreen)
	if len(candidates) == 0 {
		return screenActiveArea{}, false
	}
	if len(candidates) > 1 && !approx.Valid {
		return screenActiveArea{}, false
	}

	best := screenActiveArea{}
	bestScore := math.MaxFloat64
	for _, aspectRatio := range candidates {
		candidate, ok := projectAspectRatioToFrame(frameWidth, frameHeight, aspectRatio)
		if !ok {
			continue
		}
		score := scoreActiveAreaCandidate(candidate, approx, frameWidth, frameHeight)
		if score < bestScore {
			best = candidate
			bestScore = score
		}
	}
	if !best.Valid {
		return screenActiveArea{}, false
	}
	return best, true
}

func phoneScreenAspectRatioCandidates(phoneScreen PhoneScreenInfo) []float64 {
	candidates := make([]float64, 0, 2)
	if width, height, ok := currentPhoneScreenDimensions(phoneScreen); ok {
		candidates = appendAspectRatioOrientations(candidates, width, height)
	}
	if width, height, ok := nativePhoneScreenDimensions(phoneScreen); ok {
		candidates = appendAspectRatioOrientations(candidates, width, height)
	}
	return candidates
}

func currentPhoneScreenDimensions(phoneScreen PhoneScreenInfo) (float64, float64, bool) {
	switch {
	case phoneScreen.WidthPixels != nil && phoneScreen.HeightPixels != nil && *phoneScreen.WidthPixels > 0 && *phoneScreen.HeightPixels > 0:
		return float64(*phoneScreen.WidthPixels), float64(*phoneScreen.HeightPixels), true
	case phoneScreen.Width != nil && phoneScreen.Height != nil && *phoneScreen.Width > 0 && *phoneScreen.Height > 0:
		return *phoneScreen.Width, *phoneScreen.Height, true
	default:
		return 0, 0, false
	}
}

func nativePhoneScreenDimensions(phoneScreen PhoneScreenInfo) (float64, float64, bool) {
	if phoneScreen.NativeWidthPixels != nil && phoneScreen.NativeHeightPixels != nil &&
		*phoneScreen.NativeWidthPixels > 0 && *phoneScreen.NativeHeightPixels > 0 {
		return float64(*phoneScreen.NativeWidthPixels), float64(*phoneScreen.NativeHeightPixels), true
	}
	return 0, 0, false
}

func appendUniqueAspectCandidate(candidates []float64, aspectRatio float64) []float64 {
	if aspectRatio <= 0 || math.IsNaN(aspectRatio) || math.IsInf(aspectRatio, 0) {
		return candidates
	}
	for _, candidate := range candidates {
		if math.Abs(candidate-aspectRatio) < 1e-6 {
			return candidates
		}
	}
	return append(candidates, aspectRatio)
}

func appendAspectRatioOrientations(candidates []float64, width, height float64) []float64 {
	if width <= 0 || height <= 0 {
		return candidates
	}
	candidates = appendUniqueAspectCandidate(candidates, width/height)
	candidates = appendUniqueAspectCandidate(candidates, height/width)
	return candidates
}

func projectAspectRatioToFrame(frameWidth, frameHeight int, aspectRatio float64) (screenActiveArea, bool) {
	if frameWidth <= 0 || frameHeight <= 0 || aspectRatio <= 0 || math.IsNaN(aspectRatio) || math.IsInf(aspectRatio, 0) {
		return screenActiveArea{}, false
	}
	frameAspectRatio := float64(frameWidth) / float64(frameHeight)
	if frameAspectRatio > aspectRatio {
		activeWidth := int(math.Round(float64(frameHeight) * aspectRatio))
		if activeWidth < 1 {
			return screenActiveArea{}, false
		}
		if activeWidth > frameWidth {
			activeWidth = frameWidth
		}
		return screenActiveArea{
			X:      (frameWidth - activeWidth) / 2,
			Y:      0,
			Width:  activeWidth,
			Height: frameHeight,
			Valid:  true,
		}, true
	}

	activeHeight := int(math.Round(float64(frameWidth) / aspectRatio))
	if activeHeight < 1 {
		return screenActiveArea{}, false
	}
	if activeHeight > frameHeight {
		activeHeight = frameHeight
	}
	return screenActiveArea{
		X:      0,
		Y:      (frameHeight - activeHeight) / 2,
		Width:  frameWidth,
		Height: activeHeight,
		Valid:  true,
	}, true
}

func scoreActiveAreaCandidate(candidate, approx screenActiveArea, frameWidth, frameHeight int) float64 {
	if !approx.Valid || frameWidth <= 0 || frameHeight <= 0 {
		return 0
	}
	return math.Abs(float64(candidate.X-approx.X))/float64(frameWidth) +
		math.Abs(float64(candidate.Y-approx.Y))/float64(frameHeight) +
		math.Abs(float64(candidate.Width-approx.Width))/float64(frameWidth) +
		math.Abs(float64(candidate.Height-approx.Height))/float64(frameHeight)
}

func detectImageActiveArea(img image.Image, expectedWidth, expectedHeight int) screenActiveArea {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return screenActiveArea{}
	}
	if expectedWidth > 0 && expectedHeight > 0 && (width != expectedWidth || height != expectedHeight) {
		width = expectedWidth
		height = expectedHeight
	}

	threshold := 10.0
	left := 0
	for left < width && imageColumnDark(img, bounds.Min.X+left, bounds.Min.Y, height, threshold) {
		left++
	}
	right := width - 1
	for right >= left && imageColumnDark(img, bounds.Min.X+right, bounds.Min.Y, height, threshold) {
		right--
	}
	top := 0
	for top < height && imageRowDark(img, bounds.Min.Y+top, bounds.Min.X, width, threshold) {
		top++
	}
	bottom := height - 1
	for bottom >= top && imageRowDark(img, bounds.Min.Y+bottom, bounds.Min.X, width, threshold) {
		bottom--
	}

	activeWidth := right - left + 1
	activeHeight := bottom - top + 1
	if activeWidth <= 0 || activeHeight <= 0 {
		return screenActiveArea{}
	}
	if activeWidth > width*95/100 && activeHeight > height*95/100 {
		return screenActiveArea{}
	}
	if activeWidth < width/5 && activeHeight < height/5 {
		return screenActiveArea{}
	}
	return screenActiveArea{X: left, Y: top, Width: activeWidth, Height: activeHeight, Valid: true}
}

func imageColumnDark(img image.Image, x, minY, height int, threshold float64) bool {
	samples := 64
	if height < samples {
		samples = height
	}
	if samples <= 0 {
		return true
	}
	if samples == 1 {
		return pixelBrightness(img.At(x, minY)) <= threshold
	}
	bright := 0
	for i := 0; i < samples; i++ {
		y := minY + int(math.Round(float64(i)*float64(height-1)/float64(samples-1)))
		if pixelBrightness(img.At(x, y)) > threshold {
			bright++
		}
	}
	return bright <= samples/20
}

func imageRowDark(img image.Image, y, minX, width int, threshold float64) bool {
	samples := 64
	if width < samples {
		samples = width
	}
	if samples <= 0 {
		return true
	}
	if samples == 1 {
		return pixelBrightness(img.At(minX, y)) <= threshold
	}
	bright := 0
	for i := 0; i < samples; i++ {
		x := minX + int(math.Round(float64(i)*float64(width-1)/float64(samples-1)))
		if pixelBrightness(img.At(x, y)) > threshold {
			bright++
		}
	}
	return bright <= samples/20
}

func pixelBrightness(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	return (0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8))
}
