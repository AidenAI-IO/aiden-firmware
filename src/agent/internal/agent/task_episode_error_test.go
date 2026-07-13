package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/schema"
)

func TestEpisodeRecorderRecordsStructuredToolError(t *testing.T) {
	recorder := NewEpisodeRecorder(MemoryRetrieveRequest{}, MemoryContext{})
	action := schema.AgentAction{Tool: "bridge_contacts", ToolInput: `{"action":"query"}`}
	step := schema.AgentStep{Action: action, Observation: "contacts permission denied"}
	toolErr := NewToolError(CodePermissionDenied, "contacts permission denied")

	recorder.RecordExecution(ToolCallExecutionResult{
		Call:   ToolCall{Action: action},
		Step:   step,
		Result: ToolResult{Output: toolErr.Message, Error: toolErr},
	})
	episode := recorder.Finish("", nil, nil, nil, nil)

	for _, event := range episode.Events {
		if event.Type != "tool_result" {
			continue
		}
		if event.ToolError == nil || event.ToolError.Code != CodePermissionDenied {
			t.Fatalf("TaskEpisodeEvent.ToolError = %+v, want permission_denied", event.ToolError)
		}
		if !event.IsError {
			t.Fatalf("TaskEpisodeEvent.IsError = false, want true")
		}
		if event.ToolError.Message != "contacts permission denied" {
			t.Fatalf("TaskEpisodeEvent.ToolError.Message = %q", event.ToolError.Message)
		}
		return
	}
	t.Fatal("missing tool_result event")
}

func TestMaterializeEventArtifactUsesExistingReference(t *testing.T) {
	store := NewTaskEpisodeStore(t.TempDir())
	dir := filepath.Join(t.TempDir(), "episode")
	ref := "artifacts/step_002.jpeg"
	event := TaskEpisodeEvent{
		RawObservation: `{"width":320,"height":240,"format":"jpeg","size":10,"screenshot_ref":"` + ref + `"}`,
	}

	if err := store.materializeEventArtifact(dir, &event, 1); err != nil {
		t.Fatalf("materialize referenced artifact: %v", err)
	}
	if event.ScreenshotRef != ref {
		t.Fatalf("screenshot_ref = %q, want %q", event.ScreenshotRef, ref)
	}
	if !strings.Contains(event.Observation, `"screenshot_ref":"`+ref+`"`) {
		t.Fatalf("compact observation is missing reference: %s", event.Observation)
	}
}
