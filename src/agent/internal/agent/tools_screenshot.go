package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

const screenshotJPEGQuality = 80

type screenshotResult struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Format string `json:"format"`
	Size   int    `json:"size"`
	Data   string `json:"data"`
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

func (t *ScreenshotTool) Description() string {
	return `Capture a screenshot from the connected display. No input required (pass empty JSON {} or ""). ` +
		`Returns a JSON object with width, height, and base64-encoded JPEG image data.`
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
	if t.screen != nil {
		t.screen.Update(int(meta.Width), int(meta.Height))
	}

	result := screenshotResult{
		Width:  int(meta.Width),
		Height: int(meta.Height),
		Format: "jpeg",
		Size:   len(jpegData),
		Data:   base64.StdEncoding.EncodeToString(jpegData),
	}

	out, _ := json.Marshal(result)
	return string(out), nil
}
