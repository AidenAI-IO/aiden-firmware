package agent

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFrameMetadataUnmarshalSupportsStringNumbers(t *testing.T) {
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

	if meta.Seq != 123 {
		t.Fatalf("unexpected seq: %d", meta.Seq)
	}
	if meta.Width != 1920 {
		t.Fatalf("unexpected width: %d", meta.Width)
	}
	if meta.Height != 1080 {
		t.Fatalf("unexpected height: %d", meta.Height)
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
