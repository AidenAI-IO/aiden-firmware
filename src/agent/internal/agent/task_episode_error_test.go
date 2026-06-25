package agent

import (
	"testing"

	"github.com/tmc/langchaingo/schema"
)

func TestEpisodeRecorderRecordsStructuredToolError(t *testing.T) {
	recorder := NewEpisodeRecorder(MemoryRetrieveRequest{}, MemoryContext{})
	action := schema.AgentAction{Tool: "contacts", ToolInput: `{"action":"query"}`}
	step := schema.AgentStep{Action: action, Observation: "contacts permission denied"}
	toolErr := NewToolError(CodePermissionDenied, "contacts permission denied")

	recorder.RecordExecution(roleExecutionResult{
		Action:    &action,
		Step:      &step,
		ToolError: toolErr,
	})
	episode := recorder.Finish("", nil, nil, nil, nil)

	for _, event := range episode.Events {
		if event.Type != "tool_result" {
			continue
		}
		if event.ToolError == nil || event.ToolError.Code != CodePermissionDenied {
			t.Fatalf("TaskEpisodeEvent.ToolError = %+v, want permission_denied", event.ToolError)
		}
		return
	}
	t.Fatal("missing tool_result event")
}
