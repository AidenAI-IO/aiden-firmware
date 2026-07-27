package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
	"time"

	"aiden-agent/internal/agent/screen"

	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestScreenToolResultObserverUpdatesStateFromBridgedScreenshot(t *testing.T) {
	output := `{"width":390,"height":844,"source_width":1920,"source_height":1080,"active_area":{"x":711,"y":0,"width":498,"height":1080,"valid":true},"format":"jpeg"}`
	remote := &stubTool{name: "screenshot", description: "Capture screenshot.", output: output, visual: true}
	bridge := newMockEnvironmentBridge(t, remote)
	defer bridge.Close()

	state := &screen.ScreenState{}
	result := executeToolCall(context.Background(), ToolCallExecution{
		Specs:                  NewToolSpecs([]langtools.Tool{&stubTool{name: "screenshot", description: "Capture screenshot.", visual: true}}),
		Action:                 schema.AgentAction{Tool: "screenshot", ToolInput: `{}`},
		EnvironmentBridge:      NewEnvironmentBridgeClient(bridge.URL),
		EnvironmentBridgeTools: []string{"screenshot"},
		ResultObserver:         newScreenToolResultObserver(state),
	})

	if result.Error != nil {
		t.Fatalf("execute bridged screenshot: %v", result.Error)
	}
	width, height, active, _, ok := state.ActiveAreaWithAge()
	if !ok || width != 1920 || height != 1080 {
		t.Fatalf("screen state = %dx%d ok=%v, want 1920x1080 true", width, height, ok)
	}
	want := screen.ScreenActiveArea{X: 711, Y: 0, Width: 498, Height: 1080, Valid: true}
	if active != want {
		t.Fatalf("active area = %+v, want %+v", active, want)
	}
}

func TestScreenToolResultObserverDoesNotDuplicateLocalScreenshotUpdate(t *testing.T) {
	state := &screen.ScreenState{}
	jpegData := []byte("local-jpeg")
	startedAt := time.Now()
	state.UpdateScreenshotWithID(99, jpegData, 390, 844)
	generation := state.ScreenshotGeneration()

	output := `{"width":390,"height":844,"screenshot_id":99,"format":"jpeg","data":"` + base64.StdEncoding.EncodeToString(jpegData) + `"}`
	newScreenToolResultObserver(state)(context.Background(), ToolCall{
		Spec:      ToolSpec{Name: "screenshot", Tool: &stubTool{name: "screenshot", visual: true}},
		StartedAt: startedAt,
	}, ToolResult{Output: output})

	if got := state.ScreenshotGeneration(); got != generation {
		t.Fatalf("screenshot generation = %d, want %d", got, generation)
	}
	if _, _, ok := state.LatestScreenshotPair(); ok {
		t.Fatal("local tool result was recorded as a second screenshot")
	}
}

func TestScreenToolResultObserverRecordsBridgedScreenshotObservation(t *testing.T) {
	state := &screen.ScreenState{}
	before := []byte("before-jpeg")
	after := []byte("after-jpeg")
	state.UpdateScreenshotWithID(99, before, 390, 844)

	output := `{"width":390,"height":844,"screenshot_id":100,"format":"jpeg","data":"` + base64.StdEncoding.EncodeToString(after) + `"}`
	newScreenToolResultObserver(state)(context.Background(), ToolCall{
		Spec:      ToolSpec{Name: "screenshot", Tool: &stubTool{name: "screenshot", visual: true}},
		StartedAt: time.Now().Add(time.Second),
	}, ToolResult{Output: output})

	gotBefore, gotAfter, ok := state.LatestScreenshotPair()
	if !ok || gotBefore.ID != 99 || gotAfter.ID != 100 || !bytes.Equal(gotBefore.JPEG, before) || !bytes.Equal(gotAfter.JPEG, after) {
		t.Fatalf("bridged screenshot pair = %#v -> %#v, ok=%v", gotBefore, gotAfter, ok)
	}
}
