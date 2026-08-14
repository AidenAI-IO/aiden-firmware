package agent

import (
	"context"
	"testing"

	"aiden-agent/internal/agent/screen"

	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestScreenToolResultObserverUpdatesStateFromBridgedScreenshot(t *testing.T) {
	output := `{"width":390,"height":844,"source_width":1920,"source_height":1080,"active_area":{"x":711,"y":0,"width":498,"height":1080,"valid":true},"format":"jpeg"}`
	remote := &stubTool{name: "touch_gesture", description: "Touch.", output: output, visual: true}
	bridge := newMockEnvironmentBridge(t, remote)
	defer bridge.Close()

	state := &screen.ScreenState{}
	result := executeToolCall(context.Background(), ToolCallExecution{
		Specs:                  NewToolSpecs([]langtools.Tool{&stubTool{name: "touch_gesture", description: "Touch.", visual: true}}),
		Action:                 schema.AgentAction{Tool: "touch_gesture", ToolInput: `{}`},
		EnvironmentBridge:      NewEnvironmentBridgeClient(bridge.URL),
		EnvironmentBridgeTools: []string{"touch_gesture"},
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
