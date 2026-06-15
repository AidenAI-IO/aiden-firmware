package agent

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

type coordinateDebugScreenshotOptions struct {
	CropBlackBars bool
}

func parseCoordinateDebugScreenshotOptions(r *http.Request) coordinateDebugScreenshotOptions {
	options := coordinateDebugScreenshotOptions{CropBlackBars: true}
	if r == nil {
		return options
	}
	raw := strings.TrimSpace(r.URL.Query().Get("crop_black_bars"))
	if raw == "" {
		return options
	}
	switch strings.ToLower(raw) {
	case "0", "false", "no", "off":
		options.CropBlackBars = false
	}
	return options
}

func (s *Server) captureCoordinateDebugScreenshot(options coordinateDebugScreenshotOptions) (*frameMetadata, []byte, error) {
	if s == nil || s.runtime == nil {
		return nil, nil, fmt.Errorf("runtime not configured")
	}

	client := NewFrameServiceClient(s.runtime.config.HID.FrameSocketOrDefault())
	if options.CropBlackBars {
		return client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality)
	}

	meta, frameData, err := client.LatestFrame()
	if err != nil {
		return nil, nil, err
	}
	jpegData, err := encodeFrameAsJPEG(meta, frameData, screenshotJPEGQuality)
	if err != nil {
		return nil, nil, err
	}

	encodedMeta := *meta
	encodedMeta.PixelFormat = "jpeg"
	encodedMeta.Stride = 0
	encodedMeta.Bytes = uint64(len(jpegData))
	return &encodedMeta, jpegData, nil
}

func (s *Server) captureCoordinateDebugScreenshotResult(options coordinateDebugScreenshotOptions) (*screenshotResult, error) {
	meta, jpegData, err := s.captureCoordinateDebugScreenshot(options)
	if err != nil {
		return nil, err
	}
	if meta.PixelFormat != "jpeg" {
		return nil, fmt.Errorf("expected jpeg format, got %s", meta.PixelFormat)
	}

	active := detectScreenshotActiveArea(jpegData, int(meta.Width), int(meta.Height))
	result := &screenshotResult{
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
	return result, nil
}
