package messages

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func TestApplyScreenshotDisplayMarkerDrawsHollowRedAndWhiteRings(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 160, 120))
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			img.Set(x, y, color.RGBA{R: 8, G: 8, B: 8, A: 255})
		}
	}
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode source jpeg: %v", err)
	}

	marked, err := ApplyScreenshotDisplayMarker(raw.Bytes(), ScreenshotDisplayMarker{Type: "tap", X: 500, Y: 500})
	if err != nil {
		t.Fatalf("ApplyScreenshotDisplayMarker() error = %v", err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(marked))
	if err != nil {
		t.Fatalf("decode marked jpeg: %v", err)
	}
	center := color.RGBAModel.Convert(decoded.At(80, 60)).(color.RGBA)
	if center.R > 80 || center.G > 80 || center.B > 80 {
		t.Fatalf("marker center is not hollow: %#v", center)
	}

	redFound := false
	whiteFound := false
	for y := 30; y <= 90; y++ {
		for x := 50; x <= 110; x++ {
			pixel := color.RGBAModel.Convert(decoded.At(x, y)).(color.RGBA)
			if pixel.R > 170 && pixel.R > pixel.G*2 && pixel.R > pixel.B*2 {
				redFound = true
			}
			if pixel.R > 180 && pixel.G > 180 && pixel.B > 180 {
				whiteFound = true
			}
		}
	}
	if !redFound || !whiteFound {
		t.Fatalf("ring colors found: red=%v white=%v", redFound, whiteFound)
	}
}

func TestApplyScreenshotDisplayMarkerRejectsOutOfRangeCoordinate(t *testing.T) {
	if _, err := ApplyScreenshotDisplayMarker([]byte("jpeg"), ScreenshotDisplayMarker{X: -1, Y: 500}); err == nil {
		t.Fatal("out-of-range marker coordinate was accepted")
	}
}
