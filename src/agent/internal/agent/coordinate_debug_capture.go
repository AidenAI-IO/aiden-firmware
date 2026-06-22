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
	SourceWidth                int               `json:"source_width"`
	SourceHeight               int               `json:"source_height"`
	SourceActiveArea           *screenActiveArea `json:"source_active_area,omitempty"`
	OriginalScreenWidthPixels  *int              `json:"original_screen_width_pixels,omitempty"`
	OriginalScreenHeightPixels *int              `json:"original_screen_height_pixels,omitempty"`
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
	_ = s.bridgeEnvironment()

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
	var screen *screenState
	if s.runtime.tools != nil {
		screen = s.runtime.tools.screen
	}
	sourceActive := detectScreenshotActiveAreaForScreen(screen, rawJPEGData, sourceWidth, sourceHeight)
	if screen != nil {
		screen.UpdateActiveArea(sourceWidth, sourceHeight, sourceActive)
	}
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
	if screen != nil {
		if width, height, ok := preferredPhoneScreenPixels(screen.PhoneScreenInfo()); ok {
			result.OriginalScreenWidthPixels = &width
			result.OriginalScreenHeightPixels = &height
		}
	}
	if displayActiveArea != nil {
		result.SourceActiveArea = displayActiveArea
	}
	return result, displayJPEGData, nil
}

func preferredPhoneScreenPixels(screen PhoneScreenInfo) (int, int, bool) {
	if screen.NativeWidthPixels != nil && screen.NativeHeightPixels != nil &&
		*screen.NativeWidthPixels > 0 && *screen.NativeHeightPixels > 0 {
		return *screen.NativeWidthPixels, *screen.NativeHeightPixels, true
	}
	if screen.WidthPixels != nil && screen.HeightPixels != nil &&
		*screen.WidthPixels > 0 && *screen.HeightPixels > 0 {
		return *screen.WidthPixels, *screen.HeightPixels, true
	}
	return 0, 0, false
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
