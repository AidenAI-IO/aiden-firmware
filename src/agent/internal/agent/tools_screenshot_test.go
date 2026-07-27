package agent

import (
	"aiden-agent/internal/agent/screen"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
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

func TestScreenshotDescriptionDocumentsOpaqueIDs(t *testing.T) {
	desc := (&ScreenshotTool{}).Description()
	for _, want := range []string{"opaque previous_screenshot_id", "Copy screenshot IDs exactly", "never derive them from frame sequence numbers"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
}

func TestScreenshotToolUsesJPEGSourceMetadataForSharedScreenState(t *testing.T) {
	jpegData, err := encodeJPEG([]byte{
		255, 255, 255, 255, 255, 255,
		255, 255, 255, 255, 255, 255,
	}, 2, 2, screenshotJPEGQuality)
	if err != nil {
		t.Fatalf("encodeJPEG() error = %v", err)
	}

	screenState := &screen.ScreenState{}
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
		screen: screenState,
	}
	_, initialScreenshotID := screenState.UpdateScreenshot(jpegData, 2, 2)

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
	if result.ScreenshotID == 0 || result.ScreenshotID == initialScreenshotID {
		t.Fatalf("screenshot_id = %d, want a new non-zero ID after %d", result.ScreenshotID, initialScreenshotID)
	}
	if result.PreviousScreenshotID != initialScreenshotID {
		t.Fatalf("previous_screenshot_id = %d, want %d", result.PreviousScreenshotID, initialScreenshotID)
	}
	want := screen.ScreenActiveArea{X: 5, Y: 0, Width: 5, Height: 9, Valid: true}
	if result.SourceWidth != 16 || result.SourceHeight != 9 || result.ActiveArea == nil || *result.ActiveArea != want || result.ActiveWidth != want.Width || result.ActiveHeight != want.Height {
		t.Fatalf("unexpected source mapping metadata: %#v", result)
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

	secondOut, err := tool.Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("second Call() error = %v", err)
	}
	var secondResult screenshotResult
	if err := json.Unmarshal([]byte(secondOut), &secondResult); err != nil {
		t.Fatalf("second output is not valid screenshot JSON: %v", err)
	}
	if secondResult.PreviousScreenshotID != result.ScreenshotID || secondResult.ScreenshotID == 0 || secondResult.ScreenshotID == result.ScreenshotID {
		t.Fatalf("repeated frame seq produced screenshot pair %d -> %d after %d", secondResult.PreviousScreenshotID, secondResult.ScreenshotID, result.ScreenshotID)
	}
	if client.calls != 2 {
		t.Fatalf("LatestFrameWithFormat call count = %d, want 2", client.calls)
	}

	width, height, active, _, ok := screenState.ActiveAreaWithAge()
	if !ok || width != 16 || height != 9 {
		t.Fatalf("screen dimensions = %dx%d ok=%v, want 16x9 true", width, height, ok)
	}
	if active != want {
		t.Fatalf("active area = %+v, want %+v", active, want)
	}
	latest, latestWidth, latestHeight, _, ok := screenState.LatestScreenshot(screenDimensionsStaleAfter)
	if !ok || latestWidth != 2 || latestHeight != 2 || !bytes.Equal(latest, jpegData) {
		t.Fatalf("latest screenshot = %dx%d bytes=%d ok=%v, want 2x2 bytes=%d true", latestWidth, latestHeight, len(latest), ok, len(jpegData))
	}
}
