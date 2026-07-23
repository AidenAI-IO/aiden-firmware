package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
)

type fakeScreenshotFrameClient struct {
	meta        frameMetadata
	data        []byte
	calls       int
	captureInfo screenCaptureInfo
}

func (c *fakeScreenshotFrameClient) LatestFrameWithFormat(format string, quality int) (*frameMetadata, []byte, screenCaptureInfo, error) {
	if format != "jpeg" {
		return nil, nil, screenCaptureInfo{}, fmt.Errorf("unexpected format %q", format)
	}
	if quality != screenshotJPEGQuality {
		return nil, nil, screenCaptureInfo{}, fmt.Errorf("quality = %d, want %d", quality, screenshotJPEGQuality)
	}
	c.calls++
	meta := c.meta
	return &meta, append([]byte(nil), c.data...), cloneScreenCaptureInfo(c.captureInfo), nil
}

func TestScreenshotToolUsesJPEGSourceMetadataForSharedScreenState(t *testing.T) {
	jpegData, err := encodeJPEG([]byte{
		255, 255, 255, 255, 255, 255,
		255, 255, 255, 255, 255, 255,
	}, 2, 2, screenshotJPEGQuality)
	if err != nil {
		t.Fatalf("encodeJPEG() error = %v", err)
	}

	screen := &screenState{}
	client := &fakeScreenshotFrameClient{
		meta: frameMetadata{
			Seq:          99,
			Width:        2,
			Height:       2,
			SourceWidth:  16,
			SourceHeight: 9,
			CropX:        5,
			CropY:        0,
			CropWidth:    5,
			CropHeight:   9,
			PixelFormat:  "jpeg",
			Bytes:        uint64(len(jpegData)),
		},
		data: jpegData,
		captureInfo: screenCaptureInfo{
			Backend: "adb",
			ADBDevice: &adbDeviceInfo{
				Serial: "serial123",
				Name:   "Pixel 9",
				State:  "device",
			},
		},
	}
	tool := &ScreenshotTool{
		client: client,
		screen: screen,
	}

	out, err := tool.Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	var result screenshotResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not valid screenshot JSON: %v", err)
	}
	if result.Width != 2 || result.Height != 2 || result.Format != "jpeg" || result.Size != len(jpegData) {
		t.Fatalf("unexpected screenshot metadata: %#v", result)
	}
	if result.ActiveArea != nil {
		t.Fatalf("expected no active_area in already-cropped jpeg response, got %#v", result.ActiveArea)
	}
	if result.CaptureBackend != "adb" {
		t.Fatalf("capture backend = %q, want adb", result.CaptureBackend)
	}
	if result.ADBDevice == nil || result.ADBDevice.Serial != "serial123" || result.ADBDevice.Name != "Pixel 9" || result.ADBDevice.State != "device" {
		t.Fatalf("unexpected adb device info: %#v", result.ADBDevice)
	}
	if result.Data != base64.StdEncoding.EncodeToString(jpegData) {
		t.Fatalf("unexpected screenshot data: %q", result.Data)
	}
	if client.calls != 1 {
		t.Fatalf("LatestFrameWithFormat call count = %d, want 1", client.calls)
	}

	width, height, active, _, ok := screen.ActiveAreaWithAge()
	if !ok || width != 16 || height != 9 {
		t.Fatalf("screen dimensions = %dx%d ok=%v, want 16x9 true", width, height, ok)
	}
	want := screenActiveArea{X: 5, Y: 0, Width: 5, Height: 9, Valid: true}
	if active != want {
		t.Fatalf("active area = %+v, want %+v", active, want)
	}
	latest, latestWidth, latestHeight, _, ok := screen.LatestScreenshot(screenDimensionsStaleAfter)
	if !ok || latestWidth != 2 || latestHeight != 2 || !bytes.Equal(latest, jpegData) {
		t.Fatalf("latest screenshot = %dx%d bytes=%d ok=%v, want 2x2 bytes=%d true", latestWidth, latestHeight, len(latest), ok, len(jpegData))
	}
}
