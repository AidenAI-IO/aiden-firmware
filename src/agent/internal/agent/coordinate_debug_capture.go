package agent

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"strings"
)

type coordinateDebugScreenshotOptions struct {
	CropBlackBars bool
}

type coordinateDebugScreenshotResult struct {
	screenshotResult
	SourceWidth      int               `json:"source_width"`
	SourceHeight     int               `json:"source_height"`
	SourceActiveArea *screenActiveArea `json:"source_active_area,omitempty"`
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

func (s *Server) captureCoordinateDebugScreenshot(options coordinateDebugScreenshotOptions) (*coordinateDebugScreenshotResult, []byte, error) {
	if s == nil || s.runtime == nil {
		return nil, nil, fmt.Errorf("runtime not configured")
	}

	client := NewFrameServiceClient(s.runtime.config.HID.FrameSocketOrDefault())
	meta, frameData, err := client.LatestFrame()
	if err != nil {
		return nil, nil, err
	}
	rawJPEGData, err := encodeFrameAsJPEG(meta, frameData, screenshotJPEGQuality)
	if err != nil {
		return nil, nil, err
	}
	sourceWidth := int(meta.Width)
	sourceHeight := int(meta.Height)
	sourceActive := detectScreenshotActiveArea(rawJPEGData, sourceWidth, sourceHeight)
	displayJPEGData := rawJPEGData
	displayWidth := sourceWidth
	displayHeight := sourceHeight
	var displayActiveArea *screenActiveArea

	if options.CropBlackBars && sourceActive.Valid {
		croppedJPEGData, croppedWidth, croppedHeight, err := cropJPEGToActiveArea(rawJPEGData, sourceActive, screenshotJPEGQuality)
		if err != nil {
			return nil, nil, err
		}
		displayJPEGData = croppedJPEGData
		displayWidth = croppedWidth
		displayHeight = croppedHeight
	}
	if sourceActive.Valid && (!options.CropBlackBars || sourceActive.X != 0 || sourceActive.Y != 0 || sourceActive.Width != sourceWidth || sourceActive.Height != sourceHeight) {
		activeCopy := sourceActive
		displayActiveArea = &activeCopy
	}

	result := &coordinateDebugScreenshotResult{
		screenshotResult: screenshotResult{
			Width:  displayWidth,
			Height: displayHeight,
			Format: "jpeg",
			Size:   len(displayJPEGData),
			Data:   base64.StdEncoding.EncodeToString(displayJPEGData),
		},
		SourceWidth:  sourceWidth,
		SourceHeight: sourceHeight,
	}
	if displayActiveArea != nil {
		result.SourceActiveArea = displayActiveArea
	}
	return result, displayJPEGData, nil
}

func (s *Server) captureCoordinateDebugScreenshotResult(options coordinateDebugScreenshotOptions) (*coordinateDebugScreenshotResult, error) {
	result, _, err := s.captureCoordinateDebugScreenshot(options)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func cropJPEGToActiveArea(jpegData []byte, active screenActiveArea, quality int) ([]byte, int, int, error) {
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode jpeg for crop: %w", err)
	}
	bounds := img.Bounds()
	if active.X < 0 || active.Y < 0 || active.Width <= 0 || active.Height <= 0 ||
		active.X+active.Width > bounds.Dx() || active.Y+active.Height > bounds.Dy() {
		return nil, 0, 0, fmt.Errorf("invalid active area for crop: %+v within %dx%d", active, bounds.Dx(), bounds.Dy())
	}

	rgba := image.NewRGBA(image.Rect(0, 0, active.Width, active.Height))
	for y := 0; y < active.Height; y++ {
		for x := 0; x < active.Width; x++ {
			rgba.Set(x, y, img.At(bounds.Min.X+active.X+x, bounds.Min.Y+active.Y+y))
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: quality}); err != nil {
		return nil, 0, 0, fmt.Errorf("encode cropped jpeg: %w", err)
	}
	return buf.Bytes(), active.Width, active.Height, nil
}

func decodeCoordinateDebugScreenshotBase64(data string) (image.Image, error) {
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}
	return jpeg.Decode(bytes.NewReader(decoded))
}

func imageEqualColorAt(img image.Image, x, y int, want color.Color) bool {
	r1, g1, b1, a1 := img.At(x, y).RGBA()
	r2, g2, b2, a2 := want.RGBA()
	return r1 == r2 && g1 == g2 && b1 == b2 && a1 == a2
}
