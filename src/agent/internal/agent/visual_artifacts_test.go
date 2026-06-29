package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestExecuteToolCallExternalizesVisualObservation(t *testing.T) {
	imageBytes := []byte("visual-artifact-bytes")
	output := `{"width":320,"height":240,"format":"jpeg","size":21,"data":"` +
		base64.StdEncoding.EncodeToString(imageBytes) + `"}`
	tool := &stubTool{name: "screenshot", output: output, visual: true}
	store := &visualArtifactStore{rootDir: t.TempDir()}
	callback := &toolExecutionCallbackRecorder{}

	result := executeToolCall(context.Background(), ToolCallExecution{
		Specs:           NewToolSpecs([]langtools.Tool{tool}),
		Action:          schema.AgentAction{Tool: "screenshot", ToolInput: "{}"},
		Callback:        callback,
		VisualArtifacts: store,
	})

	if strings.Contains(result.Step.Observation, `"data"`) ||
		strings.Contains(result.Step.Observation, base64.StdEncoding.EncodeToString(imageBytes)) {
		t.Fatalf("step retained inline image data: %s", result.Step.Observation)
	}
	var observation postActionScreenshotResult
	if err := json.Unmarshal([]byte(result.Step.Observation), &observation); err != nil {
		t.Fatalf("decode step observation: %v", err)
	}
	if observation.ScreenshotRef == "" {
		t.Fatalf("step is missing screenshot_ref: %s", result.Step.Observation)
	}
	stored, err := os.ReadFile(filepath.Join(store.rootDir, filepath.FromSlash(observation.ScreenshotRef)))
	if err != nil {
		t.Fatalf("read visual artifact: %v", err)
	}
	if string(stored) != string(imageBytes) {
		t.Fatalf("stored image = %q, want %q", stored, imageBytes)
	}
	if len(callback.results) != 1 || strings.Contains(callback.results[0].Output, `"data"`) {
		t.Fatalf("callback retained inline image data: %#v", callback.results)
	}
}

func TestExecuteToolCallFailsWhenVisualObservationCannotBeExternalized(t *testing.T) {
	imageBytes := []byte("visual-artifact-bytes")
	encoded := base64.StdEncoding.EncodeToString(imageBytes)
	output := `{"width":320,"height":240,"format":"jpeg","size":21,"data":"` + encoded + `"}`
	tool := &stubTool{name: "screenshot", output: output, visual: true}
	rootFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(rootFile, []byte("file"), 0o644); err != nil {
		t.Fatalf("write root file: %v", err)
	}
	store := &visualArtifactStore{rootDir: rootFile}
	callback := &toolExecutionCallbackRecorder{}

	result := executeToolCall(context.Background(), ToolCallExecution{
		Specs:           NewToolSpecs([]langtools.Tool{tool}),
		Action:          schema.AgentAction{Tool: "screenshot", ToolInput: "{}"},
		Callback:        callback,
		VisualArtifacts: store,
	})

	if !result.Result.IsError() {
		t.Fatal("result.IsError = false, want true")
	}
	if result.Result.Error == nil {
		t.Fatal("result.Error = nil, want visual artifact error")
	}
	if !strings.Contains(result.Result.Output, "failed to store visual artifact:") {
		t.Fatalf("result output = %q, want visual artifact failure", result.Result.Output)
	}
	if strings.Contains(result.Step.Observation, encoded) || strings.Contains(result.Result.Output, encoded) {
		t.Fatalf("inline image data retained after externalization failure: step=%q output=%q", result.Step.Observation, result.Result.Output)
	}
	if result.Result.Summary != result.Result.Output {
		t.Fatalf("summary = %q, want output %q", result.Result.Summary, result.Result.Output)
	}
	if len(callback.results) != 1 {
		t.Fatalf("callback results = %d, want 1", len(callback.results))
	}
	if strings.Contains(callback.results[0].Output, encoded) || !callback.results[0].IsError() {
		t.Fatalf("callback result did not surface sanitized failure: %#v", callback.results[0])
	}
}

func TestFunctionAgentRehydratesScreenshotReference(t *testing.T) {
	imageBytes := []byte("referenced-image")
	store := &visualArtifactStore{rootDir: t.TempDir()}
	ref, err := store.write("jpeg", imageBytes)
	if err != nil {
		t.Fatalf("write visual artifact: %v", err)
	}
	observation, err := json.Marshal(postActionScreenshotResult{
		screenshotResult: screenshotResult{
			Width:         320,
			Height:        240,
			Format:        "jpeg",
			Size:          len(imageBytes),
			ScreenshotRef: ref,
		},
	})
	if err != nil {
		t.Fatalf("encode observation: %v", err)
	}
	agent := &FunctionAgent{
		Tools:           []langtools.Tool{&stubTool{name: "screenshot", visual: true}},
		VisualArtifacts: store,
	}

	_, followups := agent.observationMessagesForStep(schema.AgentStep{
		Action:      schema.AgentAction{Tool: "screenshot"},
		Observation: string(observation),
	}, true)

	wantURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(imageBytes)
	found := false
	for _, message := range followups {
		for _, part := range message.Parts {
			if image, ok := part.(llms.ImageURLContent); ok && image.URL == wantURL {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("referenced screenshot was not attached: %#v", followups)
	}
}

func TestWorldStateRetainsScreenshotReferenceInsteadOfBytes(t *testing.T) {
	imageBytes := []byte("world-state-image")
	store := &visualArtifactStore{rootDir: t.TempDir()}
	ref, err := store.write("jpeg", imageBytes)
	if err != nil {
		t.Fatalf("write visual artifact: %v", err)
	}
	observation := `{"width":320,"height":240,"format":"jpeg","size":17,"screenshot_ref":"` + ref + `"}`
	state := worldState{}

	state.UpdateFromStep(schema.AgentStep{
		Action:      schema.AgentAction{Tool: "screenshot"},
		Observation: observation,
	}, 1, []langtools.Tool{&stubTool{name: "screenshot", visual: true}}, store)

	if state.LatestScreenshot == nil {
		t.Fatal("world state is missing screenshot metadata")
	}
	if state.LatestScreenshot.ScreenshotRef != ref {
		t.Fatalf("screenshot_ref = %q, want %q", state.LatestScreenshot.ScreenshotRef, ref)
	}
	if len(state.LatestScreenshot.Data) != 0 {
		t.Fatalf("world state retained %d image bytes", len(state.LatestScreenshot.Data))
	}
}

func TestMaterializeEventArtifactUsesExistingReference(t *testing.T) {
	store := NewTaskEpisodeStore(t.TempDir())
	dir := filepath.Join(t.TempDir(), "episode")
	ref := "artifacts/visual_000001.jpeg"
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
