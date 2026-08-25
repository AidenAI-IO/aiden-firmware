package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStripScreenshotDataPreservesStableScreenStatus(t *testing.T) {
	content := `{"width":320,"height":240,"format":"jpeg","size":4,"data":"ZmFrZQ==","action_output":" completed ","screen_stable":false,"stable_wait_ms":0,"screen_changed":true,"last_diff":1.25}`

	stripped := stripScreenshotData(content)

	if strings.Contains(stripped, "ZmFrZQ==") || strings.Contains(stripped, `"data"`) {
		t.Fatalf("screenshot data was not stripped: %s", stripped)
	}
	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(stripped), &result); err != nil {
		t.Fatalf("decode stripped result: %v", err)
	}
	if result.Width != 320 || result.Height != 240 || result.Format != "jpeg" || result.Size != 4 {
		t.Fatalf("screenshot metadata = %#v", result)
	}
	if result.ActionOutput != "completed" {
		t.Fatalf("ActionOutput = %q, want completed", result.ActionOutput)
	}
	if result.ScreenStable == nil || *result.ScreenStable {
		t.Fatalf("ScreenStable = %#v, want false", result.ScreenStable)
	}
	if result.StableWaitMs == nil || *result.StableWaitMs != 0 {
		t.Fatalf("StableWaitMs = %#v, want 0", result.StableWaitMs)
	}
	if result.ScreenChanged == nil || !*result.ScreenChanged {
		t.Fatalf("ScreenChanged = %#v, want true", result.ScreenChanged)
	}
	if result.LastDiff == nil || *result.LastDiff != 1.25 {
		t.Fatalf("LastDiff = %#v, want 1.25", result.LastDiff)
	}
}
