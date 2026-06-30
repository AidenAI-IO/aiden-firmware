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

func (s *Server) coordinateDebugScreen() *screenState {
	if s == nil || s.runtime == nil || s.runtime.tools == nil {
		return nil
	}
	return s.runtime.tools.screen
}

func (s *Server) coordinateDebugOriginalScreenSize() (*int, *int) {
	screen := s.coordinateDebugScreen()
	if screen == nil {
		return nil, nil
	}
	width, height, ok := preferredPhoneScreenPixels(screen.PhoneScreenInfo())
	if !ok {
		return nil, nil
	}
	return &width, &height
}

func (s *Server) newCoordinateDebugScreenshotResult(display screenshotResult, sourceWidth, sourceHeight int, sourceActive *screenActiveArea) *coordinateDebugScreenshotResult {
	if sourceWidth <= 0 {
		sourceWidth = display.Width
	}
	if sourceHeight <= 0 {
		sourceHeight = display.Height
	}
	result := &coordinateDebugScreenshotResult{
		screenshotResult: display,
		SourceWidth:      sourceWidth,
		SourceHeight:     sourceHeight,
	}
	if sourceActive != nil {
		result.SourceActiveArea = sourceActive
	}
	result.OriginalScreenWidthPixels, result.OriginalScreenHeightPixels = s.coordinateDebugOriginalScreenSize()
	return result
}

func (s *Server) coordinateDebugScreenshotResultFromScreenState(display screenshotResult) *coordinateDebugScreenshotResult {
	sourceWidth := display.Width
	sourceHeight := display.Height
	var sourceActive *screenActiveArea
	screen := s.coordinateDebugScreen()
	if screen != nil {
		if width, height, active, age, ok := screen.ActiveAreaWithAge(); ok && age < screenDimensionsStaleAfter {
			sourceWidth = width
			sourceHeight = height
			if active.Valid && (active.X != 0 || active.Y != 0 || active.Width != width || active.Height != height) {
				activeCopy := active
				sourceActive = &activeCopy
			}
		}
	}
	return s.newCoordinateDebugScreenshotResult(display, sourceWidth, sourceHeight, sourceActive)
}

func coordinateDebugScreenshotMatchesMapping(display screenshotResult, state screenMappingState) bool {
	if state.width <= 0 || state.height <= 0 {
		return false
	}
	active := state.active
	if !active.Valid {
		active = screenActiveArea{
			X:      0,
			Y:      0,
			Width:  state.width,
			Height: state.height,
			Valid:  true,
		}
	}
	if active.X < 0 || active.Y < 0 || active.Width <= 0 || active.Height <= 0 ||
		active.X+active.Width > state.width || active.Y+active.Height > state.height {
		return false
	}
	return display.Width == active.Width && display.Height == active.Height
}

func coordinateDebugDisplayScreenshot(jpegData []byte, width, height int) screenshotResult {
	return screenshotResult{
		Width:  width,
		Height: height,
		Format: "jpeg",
		Size:   len(jpegData),
		Data:   base64.StdEncoding.EncodeToString(jpegData),
	}
}

func coordinateDebugSourceActiveArea(active screenActiveArea, sourceWidth, sourceHeight int) *screenActiveArea {
	if !active.Valid {
		return nil
	}
	if active.X == 0 && active.Y == 0 && active.Width == sourceWidth && active.Height == sourceHeight {
		return nil
	}
	activeCopy := active
	return &activeCopy
}

func (s *Server) captureCoordinateDebugScreenshot(options coordinateDebugScreenshotOptions) (*coordinateDebugScreenshotResult, []byte, error) {
	if s == nil || s.runtime == nil {
		return nil, nil, fmt.Errorf("runtime not configured")
	}
	_ = s.bridgeEnvironment()

	client := NewScreenCaptureClient(s.runtime.config.HID.FrameSocketOrDefault())
	if options.CropBlackBars {
		meta, jpegData, err := client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality)
		if err == nil && meta != nil {
			if meta.PixelFormat == "jpeg" {
				if sourceWidth, sourceHeight, sourceActive, ok := frameMetadataSourceActiveArea(meta); ok {
					screen := s.coordinateDebugScreen()
					if screen != nil {
						screen.UpdateActiveArea(sourceWidth, sourceHeight, sourceActive)
					}
					display := coordinateDebugDisplayScreenshot(jpegData, int(meta.Width), int(meta.Height))
					applyScreenCaptureInfo(&display, client.LastCaptureInfo())
					result := s.newCoordinateDebugScreenshotResult(
						display,
						sourceWidth,
						sourceHeight,
						coordinateDebugSourceActiveArea(sourceActive, sourceWidth, sourceHeight),
					)
					return result, jpegData, nil
				}
			}
		}
	}

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
	screen := s.coordinateDebugScreen()
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

	result := s.newCoordinateDebugScreenshotResult(
		func() screenshotResult {
			display := coordinateDebugDisplayScreenshot(displayJPEGData, displayWidth, displayHeight)
			applyScreenCaptureInfo(&display, client.LastCaptureInfo())
			return display
		}(),
		sourceWidth,
		sourceHeight,
		displayActiveArea,
	)
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
