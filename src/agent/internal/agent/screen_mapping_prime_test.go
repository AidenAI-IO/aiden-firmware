package agent

import (
	"context"
	"testing"

	langtools "github.com/tmc/langchaingo/tools"
)

func TestToolSetPrimeScreenMappingCapturesScreenshotMetadata(t *testing.T) {
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
			Seq:          42,
			Width:        2,
			Height:       2,
			SourceWidth:  1920,
			SourceHeight: 1080,
			CropX:        711,
			CropY:        0,
			CropWidth:    497,
			CropHeight:   1080,
			PixelFormat:  "jpeg",
			Bytes:        uint64(len(jpegData)),
		},
		data: jpegData,
	}
	tools := &ToolSet{
		tools: map[string]langtools.Tool{
			"screenshot": &ScreenshotTool{client: client, screen: screen},
		},
		screen: screen,
	}

	if err := tools.PrimeScreenMapping(context.Background()); err != nil {
		t.Fatalf("PrimeScreenMapping() error = %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("LatestFrameWithFormat calls = %d, want 1", client.calls)
	}
	width, height, active, _, ok := screen.ActiveAreaWithAge()
	if !ok {
		t.Fatal("screen mapping was not established")
	}
	if width != 1920 || height != 1080 {
		t.Fatalf("source dimensions = %dx%d, want 1920x1080", width, height)
	}
	wantActive := screenActiveArea{X: 711, Y: 0, Width: 497, Height: 1080, Valid: true}
	if active != wantActive {
		t.Fatalf("active area = %+v, want %+v", active, wantActive)
	}
}
