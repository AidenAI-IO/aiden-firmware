package agent

import (
	"aiden-agent/internal/agent/screen"
	"context"
	"image/color"
	"strings"
	"testing"
)

func TestImageDiffRejectsInvalidStoredJPEG(t *testing.T) {
	t.Run("previous screenshot", func(t *testing.T) {
		state := &screen.ScreenState{}
		state.UpdateScreenshotWithID(501, []byte("not-jpeg"), 40, 40)
		state.UpdateScreenshotWithID(502, solidImageDiffJPEG(t, color.White), 40, 40)

		result, err := (&ImageDiffTool{screen: state}).Call(context.Background(), `{"before_id":501,"after_id":502}`)
		if err != nil {
			t.Fatalf("Call returned error: %v", err)
		}
		if !strings.Contains(result, "decode previous screenshot JPEG") {
			t.Errorf("unexpected error message: %s", result)
		}
	})

	t.Run("latest screenshot", func(t *testing.T) {
		state := &screen.ScreenState{}
		state.UpdateScreenshotWithID(601, solidImageDiffJPEG(t, color.White), 40, 40)
		state.UpdateScreenshotWithID(602, []byte("not-jpeg"), 40, 40)

		result, err := (&ImageDiffTool{screen: state}).Call(context.Background(), `{"before_id":601,"after_id":602}`)
		if err != nil {
			t.Fatalf("Call returned error: %v", err)
		}
		if !strings.Contains(result, "decode latest screenshot JPEG") {
			t.Errorf("unexpected error message: %s", result)
		}
	})
}
