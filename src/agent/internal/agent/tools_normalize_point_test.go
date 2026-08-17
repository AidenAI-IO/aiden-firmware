package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizePointToolConvertsScreenshotPixels(t *testing.T) {
	tool := &NormalizePointTool{}
	out, err := tool.Call(context.Background(), `{"pixel_x":130,"pixel_y":202,"source_width":447,"source_height":972}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	point, ok := got["point"].(map[string]any)
	if !ok || got["coordinate"] != "normalized" || point["x"] != float64(291) || point["y"] != float64(208) {
		t.Fatalf("output = %#v, want normalized (291,208)", got)
	}
}

func TestNormalizePointToolMapsBottomRightPixelToNormalizedEdge(t *testing.T) {
	tool := &NormalizePointTool{}
	out, err := tool.Call(context.Background(), `{"pixel_x":446,"pixel_y":971,"source_width":447,"source_height":972}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !strings.Contains(out, `"point":{"x":1000,"y":1000}`) {
		t.Fatalf("output = %q, want normalized bottom-right edge", out)
	}
}

func TestNormalizePointToolRejectsOutOfRangePixels(t *testing.T) {
	tool := &NormalizePointTool{}
	out, err := tool.Call(context.Background(), `{"pixel_x":447,"pixel_y":202,"source_width":447,"source_height":972}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !strings.Contains(out, "inside the model-visible screenshot bounds") {
		t.Fatalf("output = %q, want a model-visible bounds error", out)
	}
}

func TestNormalizePointToolConvertsModelVisiblePixelsForLargeSourceImage(t *testing.T) {
	tool := NewNormalizePointTool("anthropic", "claude-opus-5")
	out, err := tool.Call(context.Background(), `{"pixel_x":613,"pixel_y":763,"source_width":1179,"source_height":2556}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !strings.Contains(out, `"point":{"x":849,"y":487}`) {
		t.Fatalf("output = %q, want normalized large-image point (849,487)", out)
	}
}

func TestNormalizePointToolLeavesOtherProviderPixelsInSourceFrame(t *testing.T) {
	tool := NewNormalizePointTool("openai", "gpt-5.5")
	out, err := tool.Call(context.Background(), `{"pixel_x":1000,"pixel_y":1256,"source_width":1179,"source_height":2556}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !strings.Contains(out, `"point":{"x":849,"y":492}`) {
		t.Fatalf("output = %q, want normalized source-frame point (849,492)", out)
	}
}

func TestBuiltinToolSetConfiguresNormalizePointForAnthropic(t *testing.T) {
	toolSet := NewBuiltinToolSetFromConfig(Config{Model: ModelConfig{Provider: "anthropic", Model: "claude-opus-5"}}, ProxyConfig{})
	tool, ok := toolSet.tools["normalize_point"].(*NormalizePointTool)
	if !ok {
		t.Fatalf("normalize_point tool = %T, want *NormalizePointTool", toolSet.tools["normalize_point"])
	}
	if tool.maxImageEdge != modelVisibleImageMaxEdge {
		t.Fatalf("maxImageEdge = %d, want %d", tool.maxImageEdge, modelVisibleImageMaxEdge)
	}
}
