package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

const screenshotJPEGQuality = 80

// ScreenshotTool captures a screenshot from the frame service.
type ScreenshotTool struct {
	client *FrameServiceClient
}

func NewScreenshotTool(socketPath string) *ScreenshotTool {
	return &ScreenshotTool{client: NewFrameServiceClient(socketPath)}
}

func (t *ScreenshotTool) Name() string { return "screenshot" }

func (t *ScreenshotTool) Description() string {
	return `Capture a screenshot from the connected display. No input required (pass empty JSON {} or ""). ` +
		`Returns a JSON object with width, height, and base64-encoded JPEG image data. ` +
		`Black borders are automatically cropped.`
}

func (t *ScreenshotTool) Call(_ context.Context, _ string) (string, error) {
	meta, frameData, err := t.client.LatestFrame()
	if err != nil {
		return "", fmt.Errorf("capture screenshot: %w", err)
	}

	rgb, err := convertFrameToRGB(meta, frameData)
	if err != nil {
		return "", fmt.Errorf("convert frame: %w", err)
	}

	w, h := int(meta.Width), int(meta.Height)

	// Crop black bars
	rgb, w, h = cropBlackBars(rgb, w, h, defaultBlackThreshold)

	// Encode to JPEG
	jpegData, err := encodeJPEG(rgb, w, h, screenshotJPEGQuality)
	if err != nil {
		return "", fmt.Errorf("encode jpeg: %w", err)
	}

	result := struct {
		Width  int    `json:"width"`
		Height int    `json:"height"`
		Format string `json:"format"`
		Size   int    `json:"size"`
		Data   string `json:"data"`
	}{
		Width:  w,
		Height: h,
		Format: "jpeg",
		Size:   len(jpegData),
		Data:   base64.StdEncoding.EncodeToString(jpegData),
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}
