package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/schema"
)

func TestEpisodeRecorderRecordsStructuredToolError(t *testing.T) {
	recorder := NewEpisodeRecorder(MemoryRetrieveRequest{}, MemoryContext{})
	action := schema.AgentAction{Tool: "non_sensitive_tool", ToolInput: `{"action":"query"}`}
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

func TestEpisodeRecorderDoesNotPersistSensitiveToolResultContent(t *testing.T) {
	root := t.TempDir()
	recorder := NewPersistentEpisodeRecorder(
		MemoryRetrieveRequest{Input: "read clipboard"},
		MemoryContext{},
		NewTaskEpisodeStore(root),
	)
	secret := "SENSITIVE_EPISODE_MARKER_8f2d"
	action := schema.AgentAction{Tool: toolBridgeClipboard, ToolInput: `{"action":"get"}`}
	recorder.RecordExecution(ToolCallExecutionResult{
		Call: ToolCall{
			Spec:   ToolSpec{Name: toolBridgeClipboard},
			Action: action,
		},
		Step: schema.AgentStep{Action: action, Observation: secret},
		Result: ToolResult{
			Output:  secret,
			Summary: secret,
			Error: NewToolErrorWithDetails(
				CodeToolExecutionFailed,
				secret,
				map[string]any{"raw": secret},
			),
		},
	})
	episode := recorder.Finish("", nil, nil, nil, nil)

	foundRedactedResult := false
	for _, event := range episode.Events {
		if event.Type != "tool_result" {
			continue
		}
		foundRedactedResult = true
		if event.Content != sensitiveToolResultRedaction || event.RawObservation != "" {
			t.Fatalf("sensitive tool result event = %#v", event)
		}
		if event.ToolError == nil || event.ToolError.Message != sensitiveToolResultRedaction || len(event.ToolError.Details) != 0 {
			t.Fatalf("sensitive tool error = %#v", event.ToolError)
		}
	}
	if !foundRedactedResult {
		t.Fatal("missing sensitive tool_result event")
	}

	filesScanned := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		filesScanned++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), secret) {
			t.Fatalf("sensitive marker persisted in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk episode store: %v", err)
	}
	if filesScanned == 0 {
		t.Fatal("expected persisted episode files")
	}
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
