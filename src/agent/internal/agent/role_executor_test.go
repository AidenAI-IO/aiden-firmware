package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/schema"
)

func TestExecutorAutoAbortsRepeatedPhoneBridgeForegroundFailure(t *testing.T) {
	state := &roleLoopState{
		StepExecutionResults: []roleExecutionResult{{
			Action:    &schema.AgentAction{Tool: "contacts", ToolInput: `{"action":"query","query":"Sample Contact","limit":5}`},
			Step:      &schema.AgentStep{Observation: "phone bridge did not return to foreground within 8s"},
			ToolError: NewToolError(CodeAppBackgrounded, "phone bridge did not return to foreground within 8s"),
		}},
	}
	action := schema.AgentAction{
		Tool:      "contacts",
		ToolInput: `{"action":"query","query":"Sample Contact","limit":5}`,
	}
	abortAction, ok := state.autoAbortRepeatedPhoneBridgeForegroundFailure(action)
	if !ok {
		t.Fatal("autoAbortRepeatedPhoneBridgeForegroundFailure() ok=false, want abort")
	}
	if abortAction.Tool != toolAbortStep {
		t.Fatalf("abort tool = %q, want %q", abortAction.Tool, toolAbortStep)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(abortAction.ToolInput), &payload); err != nil {
		t.Fatalf("abort input is not valid JSON: %v", err)
	}
	reason := payload["reason"]
	if !strings.Contains(reason, "already failed in this step") ||
		!strings.Contains(reason, "Aiden stayed backgrounded") {
		t.Fatalf("abort reason = %q, want foreground failure reason", reason)
	}
}
