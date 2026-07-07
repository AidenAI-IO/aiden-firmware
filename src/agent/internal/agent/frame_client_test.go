package agent

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFrameMetadataUnmarshalSupportsStringNumbers(t *testing.T) {
	input := []byte(`{
		"seq":"123",
		"width":"1920",
		"height":"1080",
		"source_width":"1280",
		"source_height":"720",
		"crop_x":"0",
		"crop_y":"72",
		"crop_width":"1280",
		"crop_height":"576",
		"pixel_format":"jpeg",
		"stride":"5760",
		"bytes":"456789",
		"stale":"false"
	}`)

	var meta frameMetadata
	if err := json.Unmarshal(input, &meta); err != nil {
		t.Fatalf("unmarshal frameMetadata: %v", err)
	}

	if meta.Seq != 123 {
		t.Fatalf("unexpected seq: %d", meta.Seq)
	}
	if meta.Width != 1920 {
		t.Fatalf("unexpected width: %d", meta.Width)
	}
	if meta.Height != 1080 {
		t.Fatalf("unexpected height: %d", meta.Height)
	}
	if meta.SourceWidth != 1280 || meta.SourceHeight != 720 {
		t.Fatalf("unexpected source dims: %dx%d", meta.SourceWidth, meta.SourceHeight)
	}
	if meta.CropX != 0 || meta.CropY != 72 || meta.CropWidth != 1280 || meta.CropHeight != 576 {
		t.Fatalf("unexpected crop rect: x=%d y=%d w=%d h=%d", meta.CropX, meta.CropY, meta.CropWidth, meta.CropHeight)
	}
	if meta.PixelFormat != "jpeg" {
		t.Fatalf("unexpected pixel format: %q", meta.PixelFormat)
	}
	if meta.Stride != 5760 {
		t.Fatalf("unexpected stride: %d", meta.Stride)
	}
	if meta.Bytes != 456789 {
		t.Fatalf("unexpected bytes: %d", meta.Bytes)
	}
	if meta.Stale {
		t.Fatalf("unexpected stale flag: %v", meta.Stale)
	}
}

func TestFrameMetadataUnmarshalAllowsOmittedSourceAndCropFields(t *testing.T) {
	input := []byte(`{
		"seq":"123",
		"width":"1920",
		"height":"1080",
		"pixel_format":"jpeg",
		"stride":"5760",
		"bytes":"456789",
		"stale":"false"
	}`)

	var meta frameMetadata
	if err := json.Unmarshal(input, &meta); err != nil {
		t.Fatalf("unmarshal frameMetadata: %v", err)
	}

	if meta.SourceWidth != 0 || meta.SourceHeight != 0 {
		t.Fatalf("unexpected source dims: %dx%d", meta.SourceWidth, meta.SourceHeight)
	}
	if meta.CropX != 0 || meta.CropY != 0 || meta.CropWidth != 0 || meta.CropHeight != 0 {
		t.Fatalf("unexpected crop rect: x=%d y=%d w=%d h=%d", meta.CropX, meta.CropY, meta.CropWidth, meta.CropHeight)
	}
}

func TestFrameHealthResponseSupportsStringNumbers(t *testing.T) {
	input := []byte(`{
		"status":"OK",
		"state":"RUNNING",
		"latest_seq":"13068",
		"frame_age_ms":"122",
		"ring_buffer_size":"3",
		"ring_buffer_used":"3",
		"consecutive_failures":"0",
		"last_error":"capture failed",
		"last_recovery_ts":"28099571095",
		"avg_frame_serve_latency_ms":645.125,
		"avg_capture_copy_latency_ms":18.609
	}`)

	var resp frameHealthResponse
	if err := json.Unmarshal(input, &resp); err != nil {
		t.Fatalf("unmarshal frameHealthResponse: %v", err)
	}

	latestSeq, err := parseFlexibleUint64(resp.LatestSeq)
	if err != nil {
		t.Fatalf("parse latest_seq: %v", err)
	}
	if latestSeq != 13068 {
		t.Fatalf("latest_seq = %d, want 13068", latestSeq)
	}

	frameAgeMs, err := parseFlexibleUint64(resp.FrameAgeMs)
	if err != nil {
		t.Fatalf("parse frame_age_ms: %v", err)
	}
	if frameAgeMs != 122 {
		t.Fatalf("frame_age_ms = %d, want 122", frameAgeMs)
	}

	ringBufferSize, err := parseFlexibleUint32(resp.RingBufferSize)
	if err != nil {
		t.Fatalf("parse ring_buffer_size: %v", err)
	}
	if ringBufferSize != 3 {
		t.Fatalf("ring_buffer_size = %d, want 3", ringBufferSize)
	}

	ringBufferUsed, err := parseFlexibleUint32(resp.RingBufferUsed)
	if err != nil {
		t.Fatalf("parse ring_buffer_used: %v", err)
	}
	if ringBufferUsed != 3 {
		t.Fatalf("ring_buffer_used = %d, want 3", ringBufferUsed)
	}

	consecutiveFailures, err := parseFlexibleUint32(resp.ConsecutiveFailures)
	if err != nil {
		t.Fatalf("parse consecutive_failures: %v", err)
	}
	if consecutiveFailures != 0 {
		t.Fatalf("consecutive_failures = %d, want 0", consecutiveFailures)
	}

	lastRecoveryTs, err := parseFlexibleUint64(resp.LastRecoveryTs)
	if err != nil {
		t.Fatalf("parse last_recovery_ts: %v", err)
	}
	if lastRecoveryTs != 28099571095 {
		t.Fatalf("last_recovery_ts = %d, want 28099571095", lastRecoveryTs)
	}

	if resp.AvgFrameServeLatencyMs != 645.125 {
		t.Fatalf("avg_frame_serve_latency_ms = %v, want 645.125", resp.AvgFrameServeLatencyMs)
	}
	if resp.AvgCaptureCopyLatencyMs != 18.609 {
		t.Fatalf("avg_capture_copy_latency_ms = %v, want 18.609", resp.AvgCaptureCopyLatencyMs)
	}
}

func TestParseCoordinateDebugScreenshotOptionsDefaultsToCropping(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/screenshot.jpg", nil)
	options := parseCoordinateDebugScreenshotOptions(req)
	if !options.CropBlackBars {
		t.Fatal("expected crop_black_bars to default to true")
	}
}

func TestParseCoordinateDebugScreenshotOptionsCanDisableCropping(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/screenshot.jpg?crop_black_bars=false", nil)
	options := parseCoordinateDebugScreenshotOptions(req)
	if options.CropBlackBars {
		t.Fatal("expected crop_black_bars=false to disable cropping")
	}
}

func TestEncodeFrameAsJPEGPassthroughForJPEG(t *testing.T) {
	srcImage := image.NewRGBA(image.Rect(0, 0, 2, 1))
	srcImage.Set(0, 0, color.RGBA{R: 255, A: 255})
	srcImage.Set(1, 0, color.RGBA{G: 255, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, srcImage, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatalf("encode source jpeg: %v", err)
	}

	meta := &frameMetadata{Width: 2, Height: 1, PixelFormat: "jpeg"}
	out, err := encodeFrameAsJPEG(meta, buf.Bytes(), 80)
	if err != nil {
		t.Fatalf("encodeFrameAsJPEG: %v", err)
	}
	if !bytes.Equal(out, buf.Bytes()) {
		t.Fatal("expected jpeg input to pass through unchanged")
	}
}

func TestConvertFrameToRGBSupportsPNG(t *testing.T) {
	srcImage := image.NewRGBA(image.Rect(0, 0, 2, 1))
	srcImage.Set(0, 0, color.RGBA{R: 255, A: 255})
	srcImage.Set(1, 0, color.RGBA{G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, srcImage); err != nil {
		t.Fatalf("encode source png: %v", err)
	}

	meta := &frameMetadata{Width: 2, Height: 1, PixelFormat: "png"}
	rgb, err := convertFrameToRGB(meta, buf.Bytes())
	if err != nil {
		t.Fatalf("convertFrameToRGB: %v", err)
	}
	want := []byte{255, 0, 0, 0, 255, 0}
	if !bytes.Equal(rgb, want) {
		t.Fatalf("rgb = %v, want %v", rgb, want)
	}
}

func TestEncodeFrameAsJPEGConvertsPNG(t *testing.T) {
	srcImage := image.NewRGBA(image.Rect(0, 0, 2, 1))
	srcImage.Set(0, 0, color.RGBA{R: 255, A: 255})
	srcImage.Set(1, 0, color.RGBA{G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, srcImage); err != nil {
		t.Fatalf("encode source png: %v", err)
	}

	meta := &frameMetadata{Width: 2, Height: 1, PixelFormat: "png"}
	out, err := encodeFrameAsJPEG(meta, buf.Bytes(), 80)
	if err != nil {
		t.Fatalf("encodeFrameAsJPEG: %v", err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode converted jpeg: %v", err)
	}
	if bounds := decoded.Bounds(); bounds.Dx() != 2 || bounds.Dy() != 1 {
		t.Fatalf("decoded bounds = %v, want 2x1", bounds)
	}
}

func TestCropJPEGToActiveArea(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 40), G: uint8(y * 40), B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatalf("encode source jpeg: %v", err)
	}

	cropped, width, height, err := cropJPEGToActiveArea(buf.Bytes(), screenActiveArea{X: 1, Y: 1, Width: 2, Height: 2, Valid: true}, 100)
	if err != nil {
		t.Fatalf("cropJPEGToActiveArea: %v", err)
	}
	if width != 2 || height != 2 {
		t.Fatalf("crop size = %dx%d, want 2x2", width, height)
	}

	decoded, err := jpeg.Decode(bytes.NewReader(cropped))
	if err != nil {
		t.Fatalf("decode cropped jpeg: %v", err)
	}
	if bounds := decoded.Bounds(); bounds.Dx() != 2 || bounds.Dy() != 2 {
		t.Fatalf("decoded bounds = %v, want 2x2", bounds)
	}
}

func TestDeriveActiveAreaFromPhoneScreenUsesReportedAspectRatio(t *testing.T) {
	approx := screenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true}
	active, ok := deriveActiveAreaFromPhoneScreen(1920, 1080, PhoneScreenInfo{
		WidthPixels:  intPtr(1080),
		HeightPixels: intPtr(1920),
	}, approx)
	if !ok {
		t.Fatal("expected aspect-ratio derived active area")
	}
	want := screenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true}
	if active != want {
		t.Fatalf("active area = %+v, want %+v", active, want)
	}
}

func TestDeriveActiveAreaFromNativePhoneScreenUsesApproxToChooseOrientation(t *testing.T) {
	approx := screenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true}
	active, ok := deriveActiveAreaFromPhoneScreen(1920, 1080, PhoneScreenInfo{
		NativeWidthPixels:  intPtr(1920),
		NativeHeightPixels: intPtr(1080),
	}, approx)
	if !ok {
		t.Fatal("expected active area derived from native screen dimensions")
	}
	want := screenActiveArea{X: 656, Y: 0, Width: 608, Height: 1080, Valid: true}
	if active != want {
		t.Fatalf("active area = %+v, want %+v", active, want)
	}
}

func TestDeriveActiveAreaFromPhoneScreenConsidersNativeDimensionsAlongsideCurrent(t *testing.T) {
	approx := screenActiveArea{X: 717, Y: 0, Width: 486, Height: 1080, Valid: true}
	active, ok := deriveActiveAreaFromPhoneScreen(1920, 1080, PhoneScreenInfo{
		WidthPixels:        intPtr(1080),
		HeightPixels:       intPtr(1920),
		NativeWidthPixels:  intPtr(1080),
		NativeHeightPixels: intPtr(2400),
	}, approx)
	if !ok {
		t.Fatal("expected active area derived from current and native screen dimensions")
	}
	want := screenActiveArea{X: 717, Y: 0, Width: 486, Height: 1080, Valid: true}
	if active != want {
		t.Fatalf("active area = %+v, want %+v", active, want)
	}
}

func TestDeriveActiveAreaFromReportedPhoneScreenCanChooseRotatedOrientation(t *testing.T) {
	approx := screenActiveArea{X: 0, Y: 97, Width: 1920, Height: 886, Valid: true}
	active, ok := deriveActiveAreaFromPhoneScreen(1920, 1080, PhoneScreenInfo{
		WidthPixels:  intPtr(1080),
		HeightPixels: intPtr(2340),
	}, approx)
	if !ok {
		t.Fatal("expected rotated active area derived from reported screen dimensions")
	}
	want := screenActiveArea{X: 0, Y: 97, Width: 1920, Height: 886, Valid: true}
	if active != want {
		t.Fatalf("active area = %+v, want %+v", active, want)
	}
}
