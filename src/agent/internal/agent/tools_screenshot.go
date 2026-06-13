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

type screenshotResult struct {
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	ActiveArea   *screenActiveArea `json:"active_area,omitempty"`
	ActiveWidth  int               `json:"active_width,omitempty"`
	ActiveHeight int               `json:"active_height,omitempty"`
	Format       string            `json:"format"`
	Size         int               `json:"size"`
	Data         string            `json:"data"`
}

// ScreenshotTool captures a screenshot from the frame service.
type ScreenshotTool struct {
	client *FrameServiceClient
	screen *screenState
}

func NewScreenshotTool(socketPath string, screen *screenState) *ScreenshotTool {
	return &ScreenshotTool{
		client: NewFrameServiceClient(socketPath),
		screen: screen,
	}
}

func (t *ScreenshotTool) Name() string { return "screenshot" }

func (t *ScreenshotTool) ReturnsVisualObservation() bool { return true }

func (t *ScreenshotTool) Description() string {
	return `Capture a screenshot from the connected display. No input required (pass empty JSON {} or ""). ` +
		`Returns a JSON object with width, height, and base64-encoded JPEG image data.`
}

func (t *ScreenshotTool) ArgsSchema() map[string]any {
	return objectArgsSchema(nil)
}

func (t *ScreenshotTool) Call(_ context.Context, _ string) (string, error) {
	// Request JPEG format directly from frame_service (hardware-encoded)
	meta, jpegData, err := t.client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality)
	if err != nil {
		return "", err
	}

	if meta.PixelFormat != "jpeg" {
		return "", fmt.Errorf("expected jpeg format, got %s", meta.PixelFormat)
	}
	active := detectScreenshotActiveArea(jpegData, int(meta.Width), int(meta.Height))
	if t.screen != nil {
		t.screen.UpdateActiveArea(int(meta.Width), int(meta.Height), active)
	}

	result := screenshotResult{
		Width:  int(meta.Width),
		Height: int(meta.Height),
		Format: "jpeg",
		Size:   len(jpegData),
		Data:   base64.StdEncoding.EncodeToString(jpegData),
	}
	if active.Valid && (active.X != 0 || active.Y != 0 || active.Width != result.Width || active.Height != result.Height) {
		result.ActiveArea = &active
		result.ActiveWidth = active.Width
		result.ActiveHeight = active.Height
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}

func detectScreenshotActiveArea(jpegData []byte, expectedWidth, expectedHeight int) screenActiveArea {
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return screenActiveArea{}
	}
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
