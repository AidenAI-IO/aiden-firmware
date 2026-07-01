package agent

import (
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
	if !strings.Contains(abortAction.ToolInput, "already failed in this step") ||
		!strings.Contains(abortAction.ToolInput, "Aiden stayed backgrounded") {
		t.Fatalf("abort input = %s, want foreground failure reason", abortAction.ToolInput)
	}
}
