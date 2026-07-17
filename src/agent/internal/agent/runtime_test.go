package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aiden-agent/internal/agent/agentpath"
	"aiden-agent/internal/agent/contextmanager"
	"aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	fakellm "github.com/tmc/langchaingo/llms/fake"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestEffectiveMaxIterationsDefaultsAndUnlimited(t *testing.T) {
	if got := effectiveMaxIterations(-1); got != math.MaxInt {
		t.Fatalf("effectiveMaxIterations(-1) = %d, want math.MaxInt", got)
	}
	if got := effectiveMaxIterations(0); got != math.MaxInt {
		t.Fatalf("effectiveMaxIterations(0) = %d, want math.MaxInt", got)
	}
	if got := effectiveMaxIterations(10); got != 10 {
		t.Fatalf("effectiveMaxIterations(10) = %d, want 10", got)
	}
}

func TestRuntimeUsesConfiguredTerminationPolicy(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		{
			Choices: []*llms.ContentChoice{{
				ToolCalls: []llms.ToolCall{{ID: "invalid-tool-call"}},
			}},
		},
		contentResponse("should not be reached"),
	}}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:             ModelConfig{Provider: "fake"},
			Instruction:       "Answer directly.",
			TerminationPolicy: TerminationPolicyConfig{ParseFailureLimit: 1},
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "test configured parse limit"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(result.Output, string(StopReasonParseFailure)) {
		t.Fatalf("output = %q, want configured parse-failure termination", result.Output)
	}
	if model.callCount != 1 {
		t.Fatalf("model call count = %d, want 1", model.callCount)
	}
}

type testModelResolver struct {
	model llms.Model
	err   error
	calls int
	spec  model.ModelSpec
}

func (r *testModelResolver) Get() (llms.Model, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.model, nil
}

func (r *testModelResolver) CallOptions() []chains.ChainCallOption {
	return nil
}

func (r *testModelResolver) Spec() model.ModelSpec {
	return r.spec
}

func TestRuntimeRun(t *testing.T) {
	cfg := Config{
		Model:       ModelConfig{Provider: "fake"},
		Instruction: "Answer directly.",
	}
	cfg = withTestConfigDir(t, cfg)

	resolver := &testModelResolver{
		model: &scriptedModel{responses: roleDirectResponses("completed")},
	}

	runtime := NewRuntimeWithDeps(cfg, resolver, NewMemoryManager(""), NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}), NewSkillIndex())
	result, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "completed" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(result.Memory) != 0 {
		t.Fatalf("expected empty memory snapshot without a storage dir, got %#v", result.Memory)
	}
}

func TestRuntimeRunMarksMainAgentModelCallsForRawHTTPLog(t *testing.T) {
	model := &scriptedModel{responses: roleDirectResponses("completed")}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Answer directly.",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.rawHTTPLogEnabled) != 1 {
		t.Fatalf("expected one model call, got %d", len(model.rawHTTPLogEnabled))
	}
	if !model.rawHTTPLogEnabled[0] {
		t.Fatal("main agent model call was not marked for raw HTTP logging")
	}
}

func TestRuntimeRunExportsFailedTraceWhenModelBuildFails(t *testing.T) {
	ingestCh := make(chan langfuseIngestionRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/public/ingestion" {
			http.NotFound(w, r)
			return
		}
		var req langfuseIngestionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		select {
		case ingestCh <- req:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"successes":[]}`))
	}))
	defer server.Close()

	configDir := ensureTestConfigDir(t, t.TempDir())
	buildErr := errors.New("model unavailable")
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir: configDir,
			Telemetry: TelemetryConfig{
				Enabled:          boolPtr(true),
				BaseURL:          server.URL,
				PublicKey:        "pk-test",
				SecretKey:        "sk-test",
				UploadTimeoutSec: 1,
			},
		},
		&testModelResolver{err: buildErr},
		NewMemoryManager(filepath.Join(configDir, "memory")),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	_, err := runtime.Run(context.Background(), RunRequest{Input: "turn that should be traced"})
	if !errors.Is(err, buildErr) {
		t.Fatalf("Run() error = %v, want %v", err, buildErr)
	}

	var ingest langfuseIngestionRequest
	select {
	case ingest = <-ingestCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected Langfuse ingestion for failed chat")
	}

	var traceBody map[string]any
	for _, event := range ingest.Batch {
		if event.Type != "trace-create" {
			continue
		}
		if err := json.Unmarshal(event.Body, &traceBody); err != nil {
			t.Fatalf("decode trace body: %v", err)
		}
		break
	}
	if traceBody == nil {
		t.Fatalf("ingestion batch missing trace-create: %#v", ingest.Batch)
	}
	if got := traceBody["input"]; got != "turn that should be traced" {
		t.Fatalf("trace input = %#v, want original user input", got)
	}
	metadata, ok := traceBody["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("trace metadata missing or invalid: %#v", traceBody["metadata"])
	}
	if got := metadata["failure_reason"]; got != buildErr.Error() {
		t.Fatalf("failure_reason = %#v, want %q", got, buildErr.Error())
	}
}

func TestRuntimeRunWaitForWakeupTerminatesRoleLoop(t *testing.T) {
	const wakeupMessage = "The current agent run is ending now, and the voice interaction will wait for the next wakeup event."
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("wait_1", "wait_for_wakeup", `{"reason":"user asked"}`),
		toolCallResponse("wait_2", "wait_for_wakeup", `{"reason":"still awake"}`),
	}}
	controller := NewWaitForWakeupController()
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools.", MaxIterations: 2}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"wait_for_wakeup": NewWaitForWakeupTool(controller),
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "go to sleep"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.WaitForWakeupRequested {
		t.Fatal("WaitForWakeupRequested = false, want true")
	}
	if result.WaitForWakeupReason != "user asked" {
		t.Fatalf("WaitForWakeupReason = %q, want user asked", result.WaitForWakeupReason)
	}
	if !result.SleepRequested {
		t.Fatal("SleepRequested = false, want deprecated alias to mirror WaitForWakeupRequested")
	}
	if result.SleepReason != "user asked" {
		t.Fatalf("SleepReason = %q, want deprecated alias to mirror WaitForWakeupReason", result.SleepReason)
	}
	if result.Output != wakeupMessage {
		t.Fatalf("Output = %q, want wait-for-wakeup observation message", result.Output)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal RunResult: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("Unmarshal RunResult JSON: %v", err)
	}
	if payload["wait_for_wakeup_requested"] != true || payload["sleep_requested"] != true {
		t.Fatalf("RunResult JSON missing wakeup aliases: %s", encoded)
	}
	if payload["wait_for_wakeup_reason"] != "user asked" || payload["sleep_reason"] != "user asked" {
		t.Fatalf("RunResult JSON missing wakeup reason aliases: %s", encoded)
	}
	if model.callCount != 1 {
		t.Fatalf("model call count = %d, want role loop to stop after wait_for_wakeup", model.callCount)
	}
}

func TestRuntimeRunStopsTouchGestureOnPointerModeMismatch(t *testing.T) {
	tests := []struct {
		name        string
		platform    string
		pointerMode string
		want        string
	}{
		{
			name:        "android absolute",
			platform:    "android",
			pointerMode: "absolute",
			want:        `hid.pointer_mode to "touchscreen"`,
		},
		{
			name:        "ios touchscreen",
			platform:    "ios",
			pointerMode: "touchscreen",
			want:        `hid.pointer_mode to "absolute"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := &scriptedModel{responses: roleToolResponses("touch_gesture", `{"type":"tap","point":{"x":500,"y":500}}`, "should not be used")}
			tool := &stubTool{
				name:        "touch_gesture",
				description: "Touch.",
				output:      `{"screen_changed":false}`,
			}
			runtime := NewRuntimeWithDeps(
				withTestConfigDir(t, Config{
					Model:           ModelConfig{Provider: "fake"},
					Instruction:     "Use tools.",
					DefaultPlatform: tc.platform,
					HID:             HIDConfig{PointerMode: tc.pointerMode},
				}),
				&testModelResolver{model: model},
				NewMemoryManager(""),
				&ToolSet{tools: map[string]langtools.Tool{"touch_gesture": tool}},
				NewSkillIndex(),
			)

			result, err := runtime.Run(context.Background(), RunRequest{Input: "tap the screen"})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if !strings.Contains(result.Output, "touch_gesture produced no visible screen change") {
				t.Fatalf("output missing no-change stop reason: %q", result.Output)
			}
			if !strings.Contains(result.Output, tc.want) {
				t.Fatalf("output missing pointer mode guidance %q: %q", tc.want, result.Output)
			}
			if model.callCount != 1 {
				t.Fatalf("model call count = %d, want stop before second model call", model.callCount)
			}
			if len(tool.inputs) != 1 {
				t.Fatalf("touch_gesture calls = %d, want 1", len(tool.inputs))
			}
		})
	}
}

func TestRuntimeRunStopsPointerModeMismatchContentBeforeRetryToolCall(t *testing.T) {
	stopMessage := `touch_gesture produced no visible screen change, and the device is configured as Android with hid.pointer_mode="absolute". Stop operation here because the touch mode likely does not match the target. Please switch hid.pointer_mode to "touchscreen", restart the agent, and retry.`
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponseWithContent("call_1", "touch_gesture", `{"type":"long_press","point":{"x":500,"y":500}}`, stopMessage),
		contentResponse("should not be used"),
	}}
	tool := &stubTool{
		name:        "touch_gesture",
		description: "Touch.",
		output:      `{"screen_changed":false}`,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:           ModelConfig{Provider: "fake"},
			Instruction:     "Use tools.",
			DefaultPlatform: "android",
			HID:             HIDConfig{PointerMode: "absolute"},
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"touch_gesture": tool}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "tap the screen"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != stopMessage {
		t.Fatalf("output = %q, want stop message", result.Output)
	}
	if model.callCount != 1 {
		t.Fatalf("model call count = %d, want stop before second model call", model.callCount)
	}
	if len(tool.inputs) != 0 {
		t.Fatalf("touch_gesture calls = %d, want 0", len(tool.inputs))
	}
}

func TestRuntimeRunDoesNotStopPointerModeMismatchContentForOtherTool(t *testing.T) {
	stopMessage := `touch_gesture produced no visible screen change, and the device is configured as Android with hid.pointer_mode="absolute". Stop operation here because the touch mode likely does not match the target. Please switch hid.pointer_mode to "touchscreen", restart the agent, and retry.`
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponseWithContent("call_1", "screenshot", `{}`, stopMessage),
		contentResponse("continued after screenshot"),
	}}
	tool := &stubTool{
		name:        "screenshot",
		description: "Screenshot.",
		output:      `{"ok":true}`,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:           ModelConfig{Provider: "fake"},
			Instruction:     "Use tools.",
			DefaultPlatform: "android",
			HID:             HIDConfig{PointerMode: "absolute"},
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"screenshot": tool}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "take a screenshot"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "continued after screenshot" {
		t.Fatalf("output = %q, want model final answer", result.Output)
	}
	if model.callCount != 2 {
		t.Fatalf("model call count = %d, want continue to second model call", model.callCount)
	}
	if len(tool.inputs) != 1 {
		t.Fatalf("screenshot calls = %d, want 1", len(tool.inputs))
	}
}

func TestRuntimeRunContinuesTouchGestureWhenPointerModeMatches(t *testing.T) {
	model := &scriptedModel{responses: roleToolResponses("touch_gesture", `{"type":"tap","point":{"x":500,"y":500}}`, "verified after inspection")}
	tool := &stubTool{
		name:        "touch_gesture",
		description: "Touch.",
		output:      `{"screen_changed":false}`,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:           ModelConfig{Provider: "fake"},
			Instruction:     "Use tools.",
			DefaultPlatform: "android",
			HID:             HIDConfig{PointerMode: "touchscreen"},
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"touch_gesture": tool}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "tap the screen"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "verified after inspection" {
		t.Fatalf("output = %q, want model final answer", result.Output)
	}
	if model.callCount != 2 {
		t.Fatalf("model call count = %d, want continue to second model call", model.callCount)
	}
	if len(tool.inputs) != 1 {
		t.Fatalf("touch_gesture calls = %d, want 1", len(tool.inputs))
	}
}

func TestRuntimeRunWaitForWakeupAppendsToolResultBeforeFinishing(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("wait_1", "wait_for_wakeup", `{"reason":"user asked"}`),
		contentResponse("resumed"),
	}}
	controller := NewWaitForWakeupController()
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools.", LoadAllTools: true}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"wait_for_wakeup": NewWaitForWakeupTool(controller),
		}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "go to sleep"}); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if _, err := runtime.Run(context.Background(), RunRequest{Input: "continue"}); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if len(model.messages) < 2 {
		t.Fatalf("model calls = %d, want second run planner prompt", len(model.messages))
	}

	var foundToolCall, foundToolResponse bool
	for _, msg := range model.messages[1] {
		for _, part := range msg.Parts {
			switch typed := part.(type) {
			case llms.ToolCall:
				if msg.Role == llms.ChatMessageTypeAI &&
					typed.ID == "wait_1" &&
					typed.FunctionCall != nil &&
					typed.FunctionCall.Name == "wait_for_wakeup" {
					foundToolCall = true
				}
			case llms.ToolCallResponse:
				if msg.Role == llms.ChatMessageTypeTool &&
					typed.ToolCallID == "wait_1" &&
					strings.Contains(typed.Content, "wait_for_wakeup_requested") {
					foundToolResponse = true
				}
			}
		}
	}
	if !foundToolCall || !foundToolResponse {
		t.Fatalf("second run prompt missing paired wait_for_wakeup scratchpad: found call=%v response=%v messages=%#v",
			foundToolCall, foundToolResponse, model.messages[1])
	}
}

func TestRuntimeRunWaitForWakeupDoesNotStreamWithoutModelText(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("wait_1", "wait_for_wakeup", `{"reason":"user asked"}`),
	}}
	controller := NewWaitForWakeupController()
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"wait_for_wakeup": NewWaitForWakeupTool(controller),
		}},
		NewSkillIndex(),
	)

	var stream strings.Builder
	result, err := runtime.Run(context.Background(), RunRequest{
		Input:        "go to sleep",
		StreamWriter: &stream,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.WaitForWakeupRequested {
		t.Fatal("WaitForWakeupRequested = false, want true")
	}
	if stream.String() != "" {
		t.Fatalf("stream = %q, want no spoken final answer for wait_for_wakeup", stream.String())
	}
}

func TestRuntimeRunInjectsCurrentDateIntoPlannerPrompt(t *testing.T) {
	originalNow := promptNow
	promptNow = func() time.Time {
		return time.Date(2026, time.June, 15, 8, 0, 0, 0, time.UTC)
	}
	t.Cleanup(func() { promptNow = originalNow })

	model := &scriptedModel{responses: roleDirectResponses("completed")}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) == 0 || len(model.messages[0]) == 0 {
		t.Fatalf("expected model to receive planner prompt")
	}
	systemPrompt := messageText(model.messages[0][:1])
	want := "Current date: 2026-06-15 (星期一)"
	if !strings.Contains(systemPrompt, want) {
		t.Fatalf("planner system prompt missing current date %q:\n%s", want, systemPrompt)
	}
}

func TestRuntimeRunAllowsNilMemoryManager(t *testing.T) {
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1}),
		&testModelResolver{model: model},
		nil,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "ok" {
		t.Fatalf("output = %q, want ok", result.Output)
	}
}

func TestRuntimeRunContinuesWhenPersistedMemoryCannotLoad(t *testing.T) {
	memoryDir := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(filepath.Join(memoryDir, "session"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "session", "events.jsonl"), []byte("{not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	manager := NewMemoryManager(memoryDir)
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1}),
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	t.Cleanup(func() { _ = runtime.Close() })

	result, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "ok" {
		t.Fatalf("output = %q, want ok", result.Output)
	}
}

func TestRuntimeRunUsesSessionManager(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponseWithInfo("Old answer.", map[string]any{"prompt_tokens": 321}),
		},
	}
	manager := &recordingSessionManager{
		beginResult: SessionBeginResult{
			Boundary: sessionBoundaryTelemetry{Decision: BoundaryContinue, Reason: BoundaryReasonTimeGapShort},
		},
		result: SessionCommitResult{
			Memory: []MessageRecord{{Role: "human", Content: "committed snapshot"}},
		},
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.sessionManager = manager

	result, err := runtime.Run(context.Background(), RunRequest{Input: "original request"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if manager.beginCalls != 1 {
		t.Fatalf("expected one session begin, got %d", manager.beginCalls)
	}
	if manager.beginReq.Input != "original request" {
		t.Fatalf("begin input = %q, want original request", manager.beginReq.Input)
	}
	if manager.beginReq.CurrentHints != (CurrentEnvironmentHints{}) {
		t.Fatalf("unexpected begin hints: %#v", manager.beginReq.CurrentHints)
	}
	if manager.commitCalls != 1 {
		t.Fatalf("expected one session commit, got %d", manager.commitCalls)
	}
	if manager.commitReq.AgentName != "default" {
		t.Fatalf("agent name = %q, want default", manager.commitReq.AgentName)
	}
	if manager.commitReq.Input != "original request" || manager.commitReq.Output != "Old answer." {
		t.Fatalf("unexpected commit request: %#v", manager.commitReq)
	}
	if len(manager.commitReq.Steers) != 0 {
		t.Fatalf("unexpected commit steers: %#v", manager.commitReq.Steers)
	}
	if manager.commitReq.Metrics == nil || manager.commitReq.Metrics.LastPromptTokens != 321 {
		t.Fatalf("commit metrics missing prompt tokens: %#v", manager.commitReq.Metrics)
	}
	assertMemoryRecords(t, result.Memory, manager.result.Memory)
}

func TestRuntimeRunContinuesWhenSessionBeginFails(t *testing.T) {
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	manager := &recordingSessionManager{beginErr: errors.New("session append failed")}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.sessionManager = manager

	result, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "ok" {
		t.Fatalf("output = %q, want ok", result.Output)
	}
	if manager.beginCalls != 1 {
		t.Fatalf("begin calls = %d, want 1", manager.beginCalls)
	}
}

func TestRuntimeRunReturnsOutputWhenSessionCommitFails(t *testing.T) {
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	manager := &recordingSessionManager{err: errors.New("session commit failed")}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.sessionManager = manager

	result, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "ok" {
		t.Fatalf("output = %q, want ok", result.Output)
	}
	if manager.commitCalls != 1 {
		t.Fatalf("commit calls = %d, want 1", manager.commitCalls)
	}
}

type recordingSessionManager struct {
	beginCalls  int
	beginReq    SessionBeginRequest
	beginResult SessionBeginResult
	beginErr    error
	commitCalls int
	commitReq   SessionCommitRequest
	result      SessionCommitResult
	err         error
}

func (m *recordingSessionManager) BeginRun(ctx context.Context, req SessionBeginRequest) (SessionBeginResult, error) {
	m.beginCalls++
	m.beginReq = req
	if m.beginErr != nil {
		return SessionBeginResult{}, m.beginErr
	}
	return m.beginResult, nil
}

func (m *recordingSessionManager) CommitRun(ctx context.Context, req SessionCommitRequest) (SessionCommitResult, error) {
	m.commitCalls++
	m.commitReq = req
	if m.err != nil {
		return SessionCommitResult{}, m.err
	}
	return m.result, nil
}

type blockingEpisodeMaintenancePlane struct {
	traceCommitted     atomic.Bool
	maintenanceStarted atomic.Bool
	released           atomic.Bool
	started            chan struct{}
	release            chan struct{}
}

func newBlockingEpisodeMaintenancePlane() *blockingEpisodeMaintenancePlane {
	return &blockingEpisodeMaintenancePlane{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (p *blockingEpisodeMaintenancePlane) Retrieve(context.Context, MemoryRetrieveRequest) (MemoryContext, error) {
	return MemoryContext{}, nil
}

func (p *blockingEpisodeMaintenancePlane) NewEpisodeRecorder(req MemoryRetrieveRequest, retrieved MemoryContext) *EpisodeRecorder {
	return NewEpisodeRecorder(req, retrieved)
}

func (p *blockingEpisodeMaintenancePlane) CommitEpisode(context.Context, TaskEpisode) error {
	return errors.New("sync CommitEpisode should not be called")
}

func (p *blockingEpisodeMaintenancePlane) commitEpisodeTrace(context.Context, TaskEpisode) error {
	p.traceCommitted.Store(true)
	return nil
}

func (p *blockingEpisodeMaintenancePlane) commitEpisodeMaintenance(ctx context.Context, episode TaskEpisode) {
	if p.maintenanceStarted.CompareAndSwap(false, true) {
		close(p.started)
	}
	select {
	case <-ctx.Done():
	case <-p.release:
	}
}

func (p *blockingEpisodeMaintenancePlane) releaseMaintenance() {
	if p.released.CompareAndSwap(false, true) {
		close(p.release)
	}
}

func TestRuntimeRunAsyncEpisodeMaintenanceDoesNotBlock(t *testing.T) {
	plane := newBlockingEpisodeMaintenancePlane()
	defer plane.releaseMaintenance()
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1}),
		&testModelResolver{model: &scriptedModel{responses: roleDirectResponses("ok")}},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = plane

	done := make(chan error, 1)
	go func() {
		_, err := runtime.Run(context.Background(), RunRequest{
			Input:                   "hello",
			AsyncEpisodeMaintenance: true,
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run() blocked on async episode maintenance")
	}
	if !plane.traceCommitted.Load() {
		t.Fatal("episode trace was not committed before Run returned")
	}
	select {
	case <-plane.started:
	case <-time.After(time.Second):
		t.Fatal("async episode maintenance did not start")
	}
}

type capturingEpisodePlane struct {
	episode       TaskEpisode
	retrieveDelay time.Duration
}

func (p *capturingEpisodePlane) Retrieve(context.Context, MemoryRetrieveRequest) (MemoryContext, error) {
	if p.retrieveDelay > 0 {
		time.Sleep(p.retrieveDelay)
	}
	return MemoryContext{}, nil
}

func (p *capturingEpisodePlane) NewEpisodeRecorder(req MemoryRetrieveRequest, retrieved MemoryContext) *EpisodeRecorder {
	return NewEpisodeRecorder(req, retrieved)
}

func (p *capturingEpisodePlane) CommitEpisode(_ context.Context, episode TaskEpisode) error {
	p.episode = episode
	return nil
}

func TestRuntimeRunCommitsTimingEventsBeforeEpisodeCommit(t *testing.T) {
	plane := &capturingEpisodePlane{retrieveDelay: 20 * time.Millisecond}
	model := &scriptedModel{responses: roleToolResponses("echo", `{"__arg1":"hello"}`, "ok")}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools.", MaxIterations: 3}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"echo": &stubTool{name: "echo", description: "Echo.", output: "tool output"},
		}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = plane

	result, err := runtime.Run(context.Background(), RunRequest{Input: "use echo"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "ok" {
		t.Fatalf("output = %q, want ok", result.Output)
	}

	var (
		hasSessionBegin   bool
		hasMemoryRetrieve bool
		startByIteration  = map[int]int{}
		endByIteration    = map[int]int{}
	)
	for index, event := range plane.episode.Events {
		switch event.Type {
		case runEventSessionBegin:
			hasSessionBegin = true
		case runEventMemoryRetrieve:
			hasMemoryRetrieve = true
		case runEventIterationStart:
			startByIteration[eventMetadataInt(event, "iteration")] = index
		case runEventIterationEnd:
			endByIteration[eventMetadataInt(event, "iteration")] = index
		}
	}
	if !hasSessionBegin {
		t.Fatal("committed episode missing session_begin event")
	}
	if !hasMemoryRetrieve {
		t.Fatal("committed episode missing memory_retrieve event")
	}
	if _, ok := startByIteration[1]; !ok {
		t.Fatalf("committed episode missing iteration 1 start: %#v", plane.episode.Events)
	}
	if _, ok := endByIteration[1]; !ok {
		t.Fatalf("committed episode missing iteration 1 end: %#v", plane.episode.Events)
	}
	if _, ok := startByIteration[2]; !ok {
		t.Fatalf("committed episode missing iteration 2 start: %#v", plane.episode.Events)
	}
	if _, ok := endByIteration[2]; !ok {
		t.Fatalf("committed episode missing iteration 2 end: %#v", plane.episode.Events)
	}
	if endByIteration[1] > startByIteration[2] {
		t.Fatalf("iteration 1 end index = %d, want before iteration 2 start index %d", endByIteration[1], startByIteration[2])
	}

	episodeStart := parseEpisodeTime(plane.episode.StartedAt, time.Time{})
	if episodeStart.IsZero() {
		t.Fatalf("committed episode missing StartedAt: %#v", plane.episode)
	}
	for _, event := range plane.episode.Events {
		switch event.Type {
		case runEventSessionBegin, runEventMemoryRetrieve:
			eventTime := parseEpisodeTime(event.Ts, time.Time{})
			if eventTime.Before(episodeStart) {
				t.Fatalf("%s event time %s is before episode start %s", event.Type, eventTime.Format(time.RFC3339Nano), episodeStart.Format(time.RFC3339Nano))
			}
		}
	}
}

func TestRuntimeRunCommitsTurnTelemetryEvents(t *testing.T) {
	plane := &capturingEpisodePlane{}
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = plane

	startedAt := time.Now().UTC().Add(-2 * time.Second)
	durationMs := int64(321)
	telemetryEvent := TaskEpisodeEvent{
		Type:       runEventSTTTranscription,
		Ts:         startedAt.Format(time.RFC3339Nano),
		Content:    "hello from voice",
		DurationMs: &durationMs,
		Metadata: map[string]interface{}{
			"provider":          "qwen-asr",
			"fallback_one_shot": false,
		},
	}

	_, err := runtime.Run(context.Background(), RunRequest{
		Input: "hello from voice",
		Turn: TurnInput{
			InputText:       "hello from voice",
			Modality:        TurnModalitySTT,
			Transcript:      "hello from voice",
			TelemetryEvents: []TaskEpisodeEvent{telemetryEvent},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(plane.episode.Events) == 0 || plane.episode.Events[0].Type != runEventSTTTranscription {
		t.Fatalf("first episode event = %#v, want STT telemetry first", plane.episode.Events)
	}
	if plane.episode.Events[0].Content != "hello from voice" {
		t.Fatalf("STT event content = %q", plane.episode.Events[0].Content)
	}
	episodeStart := parseEpisodeTime(plane.episode.StartedAt, time.Time{})
	if episodeStart.After(startedAt) {
		t.Fatalf("episode start = %s, want no later than STT start %s", episodeStart, startedAt)
	}
}

func eventMetadataInt(event TaskEpisodeEvent, key string) int {
	if event.Metadata == nil {
		return 0
	}
	switch value := event.Metadata[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func TestRuntimeRunAttachesPendingSteerToNextToolCall(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses(
			"echo", `{"__arg1":"original action"}`,
			"Adjusted based on feedback.",
		),
	}
	tool := &stubTool{
		name:        "echo",
		description: "Echo.",
		output:      "tool output",
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"echo": tool}},
		NewSkillIndex(),
	)

	var steerCalls int32
	var events []RunEvent
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "do the original action",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
		SteerProvider: func(ctx context.Context) (RunSteerMessage, bool) {
			if atomic.AddInt32(&steerCalls, 1) != 1 {
				return RunSteerMessage{}, false
			}
			return RunSteerMessage{
				ID:        "steer-1",
				RequestID: "req-1",
				Content:   "Use the updated instruction instead.",
				Timestamp: time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC),
			}, true
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "Adjusted based on feedback." {
		t.Fatalf("output = %q, want steer-adjusted response", result.Output)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "original action" {
		t.Fatalf("tool inputs = %#v, want single original action", tool.inputs)
	}

	if steerCalls == 0 {
		t.Fatal("SteerProvider was not polled")
	}
	toolResult, ok := firstRunEventOfType(events, "tool_result")
	if !ok {
		t.Fatalf("missing tool_result event: %#v", events)
	}
	if toolResult.Content != "tool output" || toolResult.IsError {
		t.Fatalf("unexpected tool result: %#v", toolResult)
	}
	// Second LLM call should include steer content
	if len(model.messages) < 2 {
		t.Fatalf("expected second LLM call after steer, got %#v", model.messages)
	}
	if !runtimeModelCallContains(model.messages[1], "Use the updated instruction instead.") {
		t.Fatalf("second LLM call missing steer: %#v", model.messages[1])
	}
	if !runtimeModelCallToolResponseContains(model.messages[1], "tool output") {
		t.Fatalf("second LLM call missing tool result: %#v", model.messages[1])
	}
	assertMemoryRecords(t, result.Memory, []MessageRecord{{Role: "human", Content: "Use the updated instruction instead."}})
}

func TestRuntimeRunSteerInterruptDoesNotPauseAfterNonCancelableTool(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("slow", `{"__arg1":"original action"}`, "Changed course."),
	}
	toolStarted := make(chan struct{})
	releaseTool := make(chan struct{})
	tool := &blockingTool{
		name:          "slow",
		description:   "Slow tool.",
		output:        "tool output",
		started:       toolStarted,
		release:       releaseTool,
		interruptSeen: make(chan struct{}),
		ignoreCancel:  true,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"slow": tool}},
		NewSkillIndex(),
	)

	interruptCh := make(chan struct{})
	waitCalled := make(chan struct{})
	resultCh := make(chan struct {
		result RunResult
		err    error
	}, 1)
	go func() {
		result, err := runtime.Run(context.Background(), RunRequest{
			Input: "do the original action",
			SteerInterrupt: func() <-chan struct{} {
				return interruptCh
			},
			SteerWaiter: func(ctx context.Context) (RunSteerMessage, bool, error) {
				close(waitCalled)
				return RunSteerMessage{ID: "steer-1", Content: "Use the updated instruction instead."}, true, nil
			},
		})
		resultCh <- struct {
			result RunResult
			err    error
		}{result: result, err: err}
	}()

	select {
	case <-toolStarted:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	close(interruptCh)
	select {
	case <-tool.interruptSeen:
	case <-time.After(time.Second):
		t.Fatal("tool context was not canceled after steer interrupt")
	}
	close(releaseTool)

	var runResult struct {
		result RunResult
		err    error
	}
	select {
	case runResult = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("runtime did not finish after interrupted non-cancelable tool returned")
	}
	select {
	case <-waitCalled:
		t.Fatal("SteerWaiter was called by context-manager loop")
	default:
	}
	if runResult.err != nil {
		t.Fatalf("Run() error = %v", runResult.err)
	}
	if runResult.result.Output != "Changed course." {
		t.Fatalf("output = %q, want Changed course.", runResult.result.Output)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "original action" {
		t.Fatalf("tool inputs = %#v, want only original action", tool.inputs)
	}
	if len(model.messages) < 2 {
		t.Fatalf("expected second model call after tool result, got %#v", model.messages)
	}
	if runtimeModelCallContains(model.messages[1], "Use the updated instruction instead.") {
		t.Fatalf("second model call unexpectedly contains steer message: %#v", model.messages[1])
	}
}

func TestRuntimeRunSteerInterruptWaitsThenContinuesWithoutInput(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("slow", `{"__arg1":"original action"}`, "Original done."),
	}
	toolStarted := make(chan struct{})
	toolCanceled := make(chan struct{})
	tool := &blockingTool{
		name:        "slow",
		description: "Slow tool.",
		output:      "tool output",
		started:     toolStarted,
		canceled:    toolCanceled,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"slow": tool}},
		NewSkillIndex(),
	)

	interruptCh := make(chan struct{})
	waitCalled := make(chan struct{})
	resultCh := make(chan struct {
		result RunResult
		err    error
	}, 1)
	go func() {
		result, err := runtime.Run(context.Background(), RunRequest{
			Input: "do the original action",
			SteerInterrupt: func() <-chan struct{} {
				return interruptCh
			},
			SteerWaiter: func(ctx context.Context) (RunSteerMessage, bool, error) {
				close(waitCalled)
				return RunSteerMessage{}, false, nil
			},
		})
		resultCh <- struct {
			result RunResult
			err    error
		}{result: result, err: err}
	}()

	select {
	case <-toolStarted:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	close(interruptCh)
	select {
	case <-toolCanceled:
	case <-time.After(time.Second):
		t.Fatal("tool context was not canceled after steer interrupt")
	}

	var runResult struct {
		result RunResult
		err    error
	}
	select {
	case runResult = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("runtime did not return after cancelable tool was canceled")
	}
	if runResult.err != nil {
		t.Fatalf("Run() error = %v", runResult.err)
	}
	if runResult.result.Output != "Original done." {
		t.Fatalf("output = %q, want Original done.", runResult.result.Output)
	}
	select {
	case <-waitCalled:
		// Expected: the canceled tool waits briefly for steering capture.
	default:
		t.Fatal("SteerWaiter was not called after tool cancellation")
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "original action" {
		t.Fatalf("tool inputs = %#v, want only original action", tool.inputs)
	}
}

func TestRuntimeRunCanceledToolDoesNotPoisonNextRunToolHistory(t *testing.T) {
	model := &strictToolPairModel{}
	toolStarted := make(chan struct{})
	toolCanceled := make(chan struct{})
	tool := &blockingTool{
		name:        "slow",
		description: "Slow tool.",
		output:      "tool output",
		started:     toolStarted,
		canceled:    toolCanceled,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"slow": tool}},
		NewSkillIndex(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := runtime.Run(ctx, RunRequest{Input: "do the original action"})
		firstDone <- err
	}()

	select {
	case <-toolStarted:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	cancel()
	select {
	case <-toolCanceled:
	case <-time.After(time.Second):
		t.Fatal("tool context was not canceled")
	}
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Run() error = %v, want context canceled", err)
	}

	result, err := runtime.Run(context.Background(), RunRequest{Input: "continue"})
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if result.Output != "continued" {
		t.Fatalf("second Run() output = %q, want continued", result.Output)
	}
}

type strictToolPairModel struct {
	calls atomic.Int64
}

func (m *strictToolPairModel) GenerateContent(_ context.Context, messages []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	if m.calls.Add(1) == 1 {
		return toolCallResponseWithContent("call_1", "slow", `{"__arg1":"original action"}`, ""), nil
	}
	if err := validateStrictToolMessageSequence(messages); err != nil {
		return nil, err
	}
	return contentResponse("continued"), nil
}

func (m *strictToolPairModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	panic("unexpected Call invocation")
}

func validateStrictToolMessageSequence(messages []llms.MessageContent) error {
	pending := map[string]struct{}{}
	for i, message := range messages {
		if len(pending) > 0 && message.Role != llms.ChatMessageTypeTool && message.Role != llms.ChatMessageTypeFunction {
			return fmt.Errorf("message %d arrived before tool responses for %v", i, pending)
		}
		switch message.Role {
		case llms.ChatMessageTypeAI:
			for _, part := range message.Parts {
				if call, ok := part.(llms.ToolCall); ok {
					pending[strings.TrimSpace(call.ID)] = struct{}{}
				}
			}
		case llms.ChatMessageTypeTool, llms.ChatMessageTypeFunction:
			if len(pending) == 0 {
				return fmt.Errorf("message %d contains an orphan tool result", i)
			}
			for _, part := range message.Parts {
				response, ok := part.(llms.ToolCallResponse)
				if !ok {
					continue
				}
				id := strings.TrimSpace(response.ToolCallID)
				if _, ok := pending[id]; !ok {
					return fmt.Errorf("message %d responds to unknown tool call %q", i, id)
				}
				delete(pending, id)
			}
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("missing tool responses for %v", pending)
	}
	return nil
}

func TestRuntimeRunConsumesPendingSteerBeforeFinalAnswer(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponse("Old answer."),
		},
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	var steerCalls int32
	var events []RunEvent
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "answer the old request",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
		SteerProvider: func(ctx context.Context) (RunSteerMessage, bool) {
			if atomic.AddInt32(&steerCalls, 1) != 1 {
				return RunSteerMessage{}, false
			}
			return RunSteerMessage{
				ID:        "steer-1",
				RequestID: "req-1",
				Content:   "Actually change direction before answering.",
				Timestamp: time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC),
			}, true
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "Old answer." {
		t.Fatalf("output = %q, want Old answer.", result.Output)
	}
	if steerCalls == 0 {
		t.Fatal("SteerProvider was not polled")
	}
	if steerEvent, ok := firstRunEventOfType(events, "steer"); !ok || steerEvent.Content != "Actually change direction before answering." {
		t.Fatalf("missing steer event: %#v", events)
	}
	if len(model.messages) != 1 {
		t.Fatalf("model calls = %d, want direct final answer in one call", len(model.messages))
	}
	assertMemoryRecords(t, result.Memory, []MessageRecord{{Role: "human", Content: "Actually change direction before answering."}})
}

func TestRuntimeRunPersistsConsumedSteerAsConversationMessage(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponse("Old answer."),
		},
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."}),
		&testModelResolver{model: model},
		NewMemoryManager(storageDir),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	t.Cleanup(func() { _ = runtime.Close() })

	var steerCalls int32
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "original persisted request",
		SteerProvider: func(ctx context.Context) (RunSteerMessage, bool) {
			if atomic.AddInt32(&steerCalls, 1) != 1 {
				return RunSteerMessage{}, false
			}
			return RunSteerMessage{
				ID:      "steer-1",
				Content: "persist this steering message",
			}, true
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "Old answer." {
		t.Fatalf("output = %q, want Old answer.", result.Output)
	}
	if steerCalls == 0 {
		t.Fatal("SteerProvider was not polled")
	}
	assertMemoryRecords(t, result.Memory, []MessageRecord{{Role: "human", Content: "persist this steering message"}})

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	if !sessionEventsContain(events, func(event SessionEvent) bool {
		return event.Type == "role_output" && event.Role == "agent" && event.Content == "Old answer."
	}) {
		t.Fatalf("expected agent role_output to be persisted in session events: %#v", events)
	}
	chatEvents := sessionEventsOfTypes(events, "user_input", "steer", "assistant_output")
	if sessionEventCount(chatEvents, "steer", "", "") != 1 {
		t.Fatalf("expected one persisted steer event: %#v", events)
	}
	if !sessionEventExists(chatEvents, "user_input", "user", "original persisted request") {
		t.Fatalf("expected original user input to be persisted; all events: %#v", events)
	}
	if !sessionEventExists(chatEvents, "assistant_output", "assistant", "Old answer.") {
		t.Fatalf("expected assistant output to be persisted; all events: %#v", events)
	}
}

func TestRuntimeRunPersistsAssistantOutputOnce(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	memoryManager := NewMemoryManager(storageDir)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := memoryManager.WaitMaintenance(ctx); err != nil {
			t.Fatalf("wait memory maintenance cleanup: %v", err)
		}
	})
	ctx := context.Background()

	model := &scriptedModel{responses: roleDirectResponses("hello answer")}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer."}),
		&testModelResolver{model: model},
		memoryManager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	t.Cleanup(func() { _ = runtime.Close() })

	if _, err := runtime.Run(ctx, RunRequest{Input: "hi"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	if got := sessionEventCount(events, "assistant_output", "assistant", "hello answer"); got != 1 {
		t.Fatalf("assistant_output count = %d, want 1; events=%#v", got, events)
	}
}

func TestRuntimeRunKeepsCurrentExchangeWhenSnapshotWindowIsFull(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	memoryManager := NewMemoryManager(storageDir)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := memoryManager.WaitMaintenance(ctx); err != nil {
			t.Fatalf("wait memory maintenance cleanup: %v", err)
		}
	})
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := memoryManager.AppendExchange(ctx, "default", fmt.Sprintf("prior user %02d", i), fmt.Sprintf("prior assistant %02d", i)); err != nil {
			t.Fatalf("AppendExchange(%d) error = %v", i, err)
		}
	}

	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponse("Old answer."),
		},
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."}),
		&testModelResolver{model: model},
		memoryManager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	var steerCalls int32
	result, err := runtime.Run(ctx, RunRequest{
		Input: "windowed request",
		SteerProvider: func(ctx context.Context) (RunSteerMessage, bool) {
			if atomic.AddInt32(&steerCalls, 1) != 1 {
				return RunSteerMessage{}, false
			}
			return RunSteerMessage{
				ID:      "steer-1",
				Content: "persist even when the hot window is full",
			}, true
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "Old answer." {
		t.Fatalf("output = %q, want Old answer.", result.Output)
	}
	if steerCalls == 0 {
		t.Fatal("SteerProvider was not polled")
	}

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	chatEvents := sessionEventsOfTypes(events, "user_input", "steer", "assistant_output")
	if sessionEventCount(chatEvents, "steer", "", "") != 1 {
		t.Fatalf("expected one persisted steer event: %#v", events)
	}
	if len(chatEvents) < 22 {
		t.Fatalf("expected at least 22 chat-like session events, got %d: %#v", len(chatEvents), events)
	}
	if !sessionEventExists(chatEvents, "user_input", "user", "windowed request") {
		t.Fatalf("expected current user input in session events: %#v", events)
	}
	if !sessionEventExists(chatEvents, "assistant_output", "assistant", "Old answer.") {
		t.Fatalf("expected current assistant output in session events: %#v", events)
	}
}

func TestRuntimeRunPersistsRootInputBeforeModelFailure(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."}),
		&testModelResolver{model: failingGenerateModel{err: errors.New("model unavailable")}},
		NewMemoryManager(storageDir),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	t.Cleanup(func() { _ = runtime.Close() })

	_, err := runtime.Run(context.Background(), RunRequest{
		Input:     "打开微信，进入 den 群，发送100块钱红包",
		EpisodeID: "ep_red_packet_failure",
		RequestID: "req-red-packet-failure",
	})
	if err == nil {
		t.Fatalf("Run() error = nil, want model failure")
	}

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	if len(events) == 0 {
		t.Fatalf("expected root user_input to be persisted before model failure")
	}
	root := events[0]
	if root.Type != "user_input" || root.Role != "user" || root.Content != "打开微信，进入 den 群，发送100块钱红包" {
		t.Fatalf("first session event = %#v", root)
	}
	if root.Modality != "text" || root.OriginalText != "打开微信，进入 den 群，发送100块钱红包" {
		t.Fatalf("root event missing text modality/original text: %#v", root)
	}
	if root.EpisodeID != "ep_red_packet_failure" || root.RequestID != "req-red-packet-failure" {
		t.Fatalf("root event missing episode/request metadata: %#v", root)
	}
}

func TestRuntimeRunPersistsCanonicalVoiceInputBeforeModelFailure(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."}),
		&testModelResolver{model: failingGenerateModel{err: errors.New("model unavailable")}},
		NewMemoryManager(storageDir),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	t.Cleanup(func() { _ = runtime.Close() })

	_, err := runtime.Run(context.Background(), RunRequest{
		Turn: TurnInput{
			InputText:    "打开微信发消息",
			OriginalText: "按住说话",
			Modality:     "stt",
			Source:       "voice",
			Transcript:   "打开微信发消息",
			Artifacts: []InputArtifact{{
				Kind:       AttachmentKindAudio,
				MIMEType:   "audio/wav",
				Path:       "/userdata/agent/audio/msg_123.wav",
				DurationMS: 3200,
				Size:       102400,
			}},
		},
		EpisodeID: "ep_voice_failure",
		RequestID: "req-voice-failure",
	})
	if err == nil {
		t.Fatalf("Run() error = nil, want model failure")
	}

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	if len(events) == 0 {
		t.Fatalf("expected canonical user_input to be persisted before model failure")
	}
	root := events[0]
	if root.Type != "user_input" || root.Role != "user" || root.Content != "打开微信发消息" {
		t.Fatalf("first session event = %#v", root)
	}
	if root.Modality != "stt" || root.Source != "voice" || root.OriginalText != "按住说话" || root.Transcript != "打开微信发消息" {
		t.Fatalf("root event missing voice metadata: %#v", root)
	}
	if len(root.Artifacts) != 1 {
		t.Fatalf("root artifacts = %#v, want one audio artifact", root.Artifacts)
	}
	artifact := root.Artifacts[0]
	if artifact.Kind != AttachmentKindAudio || artifact.MIMEType != "audio/wav" || artifact.Path == "" {
		t.Fatalf("audio artifact metadata = %#v", artifact)
	}
	if artifact.DurationMS != 3200 || artifact.Size != 102400 {
		t.Fatalf("audio artifact size/duration = %#v", artifact)
	}
	if len(artifact.Data) != 0 {
		t.Fatalf("session artifact must not contain binary data: %#v", artifact)
	}
	if root.EpisodeID != "ep_voice_failure" || root.RequestID != "req-voice-failure" {
		t.Fatalf("root event missing episode/request metadata: %#v", root)
	}
}

func TestRuntimeRunPersistsAttachmentArtifactsWithoutBinary(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."}),
		&testModelResolver{model: failingGenerateModel{err: errors.New("model unavailable")}},
		NewMemoryManager(storageDir),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	t.Cleanup(func() { _ = runtime.Close() })

	_, err := runtime.Run(context.Background(), RunRequest{
		Input: "Describe the uploaded image.",
		Attachments: []InputAttachment{{
			Kind:     AttachmentKindImage,
			Name:     "screen.png",
			MIMEType: "image/png",
			Data:     []byte{0x89, 0x50, 0x4e, 0x47},
		}},
		EpisodeID: "ep_image_failure",
		RequestID: "req-image-failure",
	})
	if err == nil {
		t.Fatalf("Run() error = nil, want model failure")
	}

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	if len(events) == 0 {
		t.Fatalf("expected user_input to be persisted before model failure")
	}
	root := events[0]
	if root.Type != "user_input" || root.Content != "Describe the uploaded image." || root.Modality != "text" {
		t.Fatalf("first session event = %#v", root)
	}
	if root.OriginalText != "Describe the uploaded image." {
		t.Fatalf("root original_text = %q", root.OriginalText)
	}
	if len(root.Artifacts) != 1 {
		t.Fatalf("root artifacts = %#v, want one image artifact", root.Artifacts)
	}
	artifact := root.Artifacts[0]
	if artifact.Kind != AttachmentKindImage || artifact.Name != "screen.png" || artifact.MIMEType != "image/png" || artifact.Size != 4 {
		t.Fatalf("image artifact metadata = %#v", artifact)
	}
	if len(artifact.Data) != 0 {
		t.Fatalf("session artifact must not contain binary data: %#v", artifact)
	}
}
func TestRuntimeRunIncludesAvailableSkillCatalog(t *testing.T) {
	index := NewSkillIndex()
	index.skills["planner"] = &SkillDefinition{
		Name:         "planner",
		Description:  "Plan before acting",
		Instructions: "Make a plan.",
	}
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		index,
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !runtimeModelCallContains(model.messages[0], "## Available skills") {
		t.Fatalf("run missing available skills heading")
	}
	if !runtimeModelCallContains(model.messages[0], "- planner: Plan before acting") {
		t.Fatalf("run missing planner skill catalog entry")
	}
	if runtimeModelCallContains(model.messages[0], "[planner] Make a plan.") {
		t.Fatalf("inactive skill instructions should not be injected")
	}
}

func TestRuntimeRunOmitsArchivedSkillsFromAvailableCatalog(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	skillsDir := filepath.Join(configDir, "skills")
	writeSKILL(t, skillsDir, "alpha", testSkillA)
	writeSKILL(t, skillsDir, "beta", testSkillB)
	saveSkillUsage(filepath.Join(configDir, "skill-state", "usage.json"), map[string]SkillUsageEntry{
		"beta": {State: SkillUsageStateArchived},
	})
	index, err := LoadSkillsFromDirs([]string{skillsDir})
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	runtime := NewRuntimeWithDeps(
		Config{ConfigDir: configDir, SkillsDirs: []string{skillsDir}, Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		index,
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !runtimeModelCallContains(model.messages[0], "- alpha: Alpha skill") {
		t.Fatalf("run missing active alpha skill catalog entry")
	}
	if runtimeModelCallContains(model.messages[0], "- beta: Beta skill") {
		t.Fatalf("run included archived beta skill catalog entry")
	}
}

func TestToolDescriptorsIncludeSkillToolMetadata(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	tools := &ToolSet{tools: map[string]langtools.Tool{}}
	tools.RegisterSkillTools(filepath.Join(configDir, "skills"), filepath.Join(configDir, "skill-state", ".bundled_manifest.json"))
	runtime := NewRuntimeWithDeps(Config{}, nil, nil, tools, NewSkillIndex())

	for _, name := range []string{"skill_list", "skill_read"} {
		desc, ok := runtime.ToolDescriptorByName(name)
		if !ok {
			t.Fatalf("expected descriptor for %s", name)
		}
		if desc.Category != "skills" {
			t.Fatalf("%s category = %q, want skills", name, desc.Category)
		}
		if desc.InputMode != toolInputModeJSON {
			t.Fatalf("%s input mode = %q, want json", name, desc.InputMode)
		}
		if strings.TrimSpace(desc.ExampleInput) == "" {
			t.Fatalf("%s missing example input", name)
		}
	}
	if _, ok := runtime.ToolDescriptorByName("skill_mark_used"); ok {
		t.Fatal("skill_mark_used should remain hidden from the HTTP catalog")
	}
}

func TestParseChunkStructuredSummaryJSONRejectsProseWrappedJSON(t *testing.T) {
	_, err := parseChunkStructuredSummaryJSON(`Here is the JSON:
{"summary":"summary","decisions":["decision"]}`)
	if err == nil {
		t.Fatalf("expected prose-wrapped structured summary JSON to be rejected")
	}
}

func TestToolDescriptorsIncludeMemoryToolMetadata(t *testing.T) {
	tools := &ToolSet{tools: map[string]langtools.Tool{}}
	tools.RegisterMemoryTools(t.TempDir(), 3, nil)
	runtime := NewRuntimeWithDeps(Config{}, nil, nil, tools, NewSkillIndex())

	for _, name := range []string{"recall_device_memory", "inspect_episode"} {
		desc, ok := runtime.ToolDescriptorByName(name)
		if !ok {
			t.Fatalf("expected descriptor for %s", name)
		}
		if desc.Category != "memory" {
			t.Fatalf("%s category = %q, want memory", name, desc.Category)
		}
		if desc.InputMode != toolInputModeJSON {
			t.Fatalf("%s input mode = %q, want json", name, desc.InputMode)
		}
		if strings.TrimSpace(desc.ExampleInput) == "" {
			t.Fatalf("%s missing example input", name)
		}
	}
}

func TestSkillCatalogSummaryLimitsEntriesAndDescriptionLength(t *testing.T) {
	index := NewSkillIndex()
	longDesc := strings.Repeat("长", maxSkillCatalogDescriptionRunes+10)
	for i := 0; i < maxSkillCatalogEntries+2; i++ {
		name := fmt.Sprintf("skill-%02d", i)
		index.skills[name] = &SkillDefinition{Name: name, Description: longDesc}
	}
	manager := NewSkillManager(index)
	catalog := manager.CatalogSummary()
	if strings.Count(catalog, "- skill-") != maxSkillCatalogEntries {
		t.Fatalf("expected %d catalog entries, got catalog:\n%s", maxSkillCatalogEntries, catalog)
	}
	if !strings.Contains(catalog, "more skills hidden. Use skill_list to search") {
		t.Fatalf("expected hidden skills hint, got:\n%s", catalog)
	}
	if strings.Contains(catalog, strings.Repeat("长", maxSkillCatalogDescriptionRunes+1)) {
		t.Fatalf("expected long descriptions to be truncated")
	}
}

func TestRuntimeRunSnapshotUnaffectedByConcurrentReload(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	skillsDir := filepath.Join(configDir, "skills")
	v1 := "---\nname: alpha\ndescription: Alpha\n---\n\nUse alpha v1.\n"
	v2 := "---\nname: alpha\ndescription: Alpha\n---\n\nUse alpha v2.\n"
	writeSKILL(t, skillsDir, "alpha", v1)
	index, err := LoadSkillsFromDirs([]string{skillsDir})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntimeWithDeps(
		Config{ConfigDir: configDir, SkillsDirs: []string{skillsDir}},
		&testModelResolver{model: fakellm.NewFakeLLM([]string{"unused"})},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		index,
	)

	if err := runtime.reloadSkillsIfDirty(); err != nil {
		t.Fatal(err)
	}
	runSkills := runtime.skills.Snapshot()
	if err := runSkills.Activate(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}

	writeSKILL(t, skillsDir, "alpha", v2)
	runtime.MarkSkillsDirty()
	if err := runtime.reloadSkillsIfDirty(); err != nil {
		t.Fatal(err)
	}

	resolved, err := runSkills.Resolve([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resolved.CombinedInstructions(), "Use alpha v1.") {
		t.Fatalf("in-progress run snapshot lost v1 instructions: %s", resolved.CombinedInstructions())
	}
	if strings.Contains(resolved.CombinedInstructions(), "Use alpha v2.") {
		t.Fatalf("in-progress run snapshot saw reloaded v2 instructions: %s", resolved.CombinedInstructions())
	}
}

func runtimeModelCallContains(messages []llms.MessageContent, want string) bool {
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if text, ok := part.(llms.TextContent); ok && strings.Contains(text.Text, want) {
				return true
			}
		}
	}
	return false
}

func runtimeModelCallToolResponseContains(messages []llms.MessageContent, want string) bool {
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if response, ok := part.(llms.ToolCallResponse); ok && strings.Contains(response.Content, want) {
				return true
			}
		}
	}
	return false
}

func runtimeLastMessageText(messages []llms.MessageContent) (llms.ChatMessageType, string, bool) {
	if len(messages) == 0 {
		return "", "", false
	}
	last := messages[len(messages)-1]
	var builder strings.Builder
	for _, part := range last.Parts {
		if text, ok := part.(llms.TextContent); ok {
			builder.WriteString(text.Text)
		}
	}
	return last.Role, builder.String(), true
}

func assertMemoryRecords(t *testing.T, got []MessageRecord, want []MessageRecord) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("memory records length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("memory record %d = %#v, want %#v; all records: %#v", i, got[i], want[i], got)
		}
	}
}

func sessionEventsContain(events []SessionEvent, predicate func(SessionEvent) bool) bool {
	for _, event := range events {
		if predicate(event) {
			return true
		}
	}
	return false
}

func sessionEventExists(events []SessionEvent, typ, role, content string) bool {
	return sessionEventCount(events, typ, role, content) > 0
}

func sessionEventCount(events []SessionEvent, typ, role, content string) int {
	count := 0
	for _, event := range events {
		if typ != "" && event.Type != typ {
			continue
		}
		if role != "" && event.Role != role {
			continue
		}
		if content != "" && event.Content != content {
			continue
		}
		count++
	}
	return count
}

func messageRecordExists(records []MessageRecord, role, content string) bool {
	for _, record := range records {
		if record.Role == role && record.Content == content {
			return true
		}
	}
	return false
}

func readSessionEventObjects(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile session events: %v", err)
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode raw session event: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func sessionEventsOfTypes(events []SessionEvent, types ...string) []SessionEvent {
	wanted := map[string]bool{}
	for _, typ := range types {
		wanted[typ] = true
	}
	var filtered []SessionEvent
	for _, event := range events {
		if wanted[event.Type] {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

type scriptedModel struct {
	responses         []*llms.ContentResponse
	streamChunks      [][]string
	callCount         int
	sawStreaming      []bool
	messages          [][]llms.MessageContent
	tools             [][]llms.Tool
	rawHTTPLogEnabled []bool
}

type blockingFinalWriter struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce atomic.Bool
}

func (w *blockingFinalWriter) Write(p []byte) (int, error) {
	if w.startedOnce.CompareAndSwap(false, true) {
		close(w.started)
	}
	<-w.release
	return len(p), nil
}

type staticTool struct {
	name   string
	output string
}

func (t *staticTool) Name() string { return t.name }

func (t *staticTool) Description() string { return "static test tool" }

func (t *staticTool) Call(context.Context, string) (string, error) { return t.output, nil }

type failingGenerateModel struct {
	err error
}

func (m failingGenerateModel) GenerateContent(context.Context, []llms.MessageContent, ...llms.CallOption) (*llms.ContentResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return nil, errors.New("generate failed")
}

func (m failingGenerateModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return "", errors.New("call failed")
}

func (m *scriptedModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	var callOptions llms.CallOptions
	for _, option := range options {
		option(&callOptions)
	}
	m.sawStreaming = append(m.sawStreaming, callOptions.StreamingFunc != nil)
	m.messages = append(m.messages, messages)
	m.tools = append(m.tools, callOptions.Tools)
	m.rawHTTPLogEnabled = append(m.rawHTTPLogEnabled, rawHTTPLogEnabled(ctx))

	if callOptions.StreamingFunc != nil && m.callCount < len(m.responses) {
		if m.streamChunks != nil && m.callCount < len(m.streamChunks) {
			for _, chunk := range m.streamChunks[m.callCount] {
				if err := callOptions.StreamingFunc(ctx, []byte(chunk)); err != nil {
					return nil, err
				}
			}
		} else {
			content := m.responses[m.callCount].Choices[0].Content
			if content != "" {
				if err := callOptions.StreamingFunc(ctx, []byte("chunk:"+content)); err != nil {
					return nil, err
				}
			}
		}
	}

	if m.callCount >= len(m.responses) {
		t := &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: ""}}}
		return t, nil
	}

	response := m.responses[m.callCount]
	m.callCount++
	return response, nil
}

func (m *scriptedModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	panic("unexpected Call invocation")
}

func contentResponse(content string) *llms.ContentResponse {
	return contentResponseWithInfo(content, nil)
}

func TestRuntimeRunCompactsWithoutLogger(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	sessionFolder := agentpath.ContextManagerSessionFolder(configDir)
	// Runtime compaction uses a minimum context window of 8192 tokens, so this
	// fixture must comfortably exceed the ~80% threshold to force compaction.
	manager, err := contextmanager.NewContextManagerFromMessageList(sessionFolder, []contextmanager.Message{
		{Role: contextmanager.MessageRoleSystem, Content: "Answer directly."},
		{Role: contextmanager.MessageRoleUser, Content: strings.Repeat("user ", 1600)},
		{Role: contextmanager.MessageRoleAssistant, Content: strings.Repeat("assistant ", 1600)},
		{
			Role: contextmanager.MessageRoleToolCall,
			ToolCalls: []contextmanager.ToolCall{{
				ID:        "call_1",
				Name:      "echo",
				Arguments: `{"input":"` + strings.Repeat("x", 4000) + `"}`,
			}},
		},
		{
			Role: contextmanager.MessageRoleToolResult,
			ToolResults: []contextmanager.ToolResult{{
				ToolCallID: "call_1",
				Name:       "echo",
				Content:    strings.Repeat("result ", 1600),
			}},
		},
		{Role: contextmanager.MessageRoleAssistant, Content: "recent tail"},
	})
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}

	llmModel := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponse("compacted summary"),
			contentResponse("ok"),
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{
			model: llmModel,
			spec:  model.ModelSpec{ContextWindow: 100},
		},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.contextManager = manager
	runtime.logger = nil

	result, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "ok" {
		t.Fatalf("output = %q, want ok", result.Output)
	}
	if len(llmModel.messages) != 2 {
		t.Fatalf("model call count = %d, want 2 (summary + planner)", len(llmModel.messages))
	}
}

func contentResponseWithInfo(content string, info map[string]any) *llms.ContentResponse {
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content:        content,
			GenerationInfo: info,
		}},
	}
}

func plannerResponse(nextStep string, plan ...string) *llms.ContentResponse {
	if len(plan) == 0 {
		plan = []string{nextStep}
	}
	payload, _ := json.Marshal(map[string]any{
		"objective":           "test objective",
		"completion_criteria": []string{"test request is satisfied"},
		"plan":                plan,
		"next_step":           nextStep,
		"reason":              "test plan",
	})
	return contentResponse(string(payload))
}

func verifierFinishResponse(finalAnswer string) *llms.ContentResponse {
	return verifierFinishResponseWithInfo(finalAnswer, nil)
}

func verifierFinishResponseWithInfo(finalAnswer string, info map[string]any) *llms.ContentResponse {
	return contentResponseWithInfo(verifierFinishJSON(finalAnswer), info)
}

func verifierFinishJSON(finalAnswer string) string {
	payload, _ := json.Marshal(map[string]any{
		"can_finish":   true,
		"final_answer": finalAnswer,
		"reason":       "test verified",
	})
	return string(payload)
}

func structuredVerifierFinishResponse(speechText, output string) *llms.ContentResponse {
	return contentResponse(structuredVerifierFinishJSON(speechText, output))
}

func structuredVerifierFinishJSON(speechText, output string) string {
	payload, _ := json.Marshal(map[string]any{
		"can_finish":   true,
		"speech":       speechText,
		"text":         output,
		"final_answer": output,
		"reason":       "test verified",
	})
	return string(payload)
}

func verifierContinueResponse(reason string) *llms.ContentResponse {
	return contentResponse(verifierContinueJSON(reason))
}

func verifierContinueJSON(reason string) string {
	payload, _ := json.Marshal(map[string]any{
		"can_finish":   false,
		"needs_replan": true,
		"reason":       reason,
	})
	return string(payload)
}

func verifierStepContinueResponse(reason string) *llms.ContentResponse {
	payload, _ := json.Marshal(map[string]any{
		"can_finish":   false,
		"needs_replan": false,
		"reason":       reason,
	})
	return contentResponse(string(payload))
}

func toolCallResponse(id, name, arguments string) *llms.ContentResponse {
	return toolCallResponseWithContent(id, name, arguments, "")
}

func toolCallResponseWithContent(id, name, arguments, content string) *llms.ContentResponse {
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content: content,
			ToolCalls: []llms.ToolCall{{
				ID:   id,
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      name,
					Arguments: arguments,
				},
			}},
		}},
	}
}

func multiToolCallResponse(calls ...llms.ToolCall) *llms.ContentResponse {
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			ToolCalls: calls,
		}},
	}
}

func roleDirectResponses(finalAnswer string) []*llms.ContentResponse {
	return []*llms.ContentResponse{contentResponse(finalAnswer)}
}

func roleDefaultToolResponses(toolName, arguments, finalAnswer string) []*llms.ContentResponse {
	return []*llms.ContentResponse{
		toolCallResponse("call_1", toolName, arguments),
		contentResponse(finalAnswer),
	}
}

func roleToolResponses(toolName, arguments, finalAnswer string) []*llms.ContentResponse {
	return roleDefaultToolResponses(toolName, arguments, finalAnswer)
}

func roleReviewedToolResponses(toolName, arguments, finalAnswer string) []*llms.ContentResponse {
	responses := roleToolResponses(toolName, arguments, finalAnswer)
	return append(responses, verifierFinishResponse(finalAnswer))
}

func firstRunEventOfType(events []RunEvent, eventType string) (RunEvent, bool) {
	for _, event := range events {
		if event.Type == eventType {
			return event, true
		}
	}
	return RunEvent{}, false
}

func TestRuntimeCallbackPropagatesToolErrorToEventsAndMessages(t *testing.T) {
	toolErr := NewToolErrorWithDetails(CodePermissionDenied, "contacts permission denied", map[string]any{"scope": "contacts"})
	var gotRunEvent RunEvent
	var gotSessionEvent SessionEvent
	handler := &runtimeCallbackHandler{
		episodeID: "ep-1",
		runtimeID: "runtime-1",
		requestID: "req-1",
		runID:     "run-1",
		eventHandler: func(event RunEvent) {
			gotRunEvent = event
		},
		sessionEventAppender: func(ctx context.Context, event SessionEvent) error {
			gotSessionEvent = event
			return nil
		},
	}
	call := ToolCall{
		Spec: ToolSpec{Name: "bridge_contacts"},
		Action: schema.AgentAction{
			Tool:      "bridge_contacts",
			ToolID:    "call-1",
			ToolInput: `{"action":"query"}`,
		},
		Input: `{"action":"query"}`,
	}

	handler.HandleToolCallResult(context.Background(), call, ToolResult{Output: toolErr.Message, Error: toolErr})

	if gotRunEvent.ToolError == nil || gotRunEvent.ToolError.Code != CodePermissionDenied {
		t.Fatalf("RunEvent.ToolError = %+v, want permission_denied", gotRunEvent.ToolError)
	}
	if gotRunEvent.ToolError.Details["scope"] != "contacts" {
		t.Fatalf("RunEvent.ToolError.Details = %+v, want scope=contacts", gotRunEvent.ToolError.Details)
	}
	if gotSessionEvent.ToolError == nil || gotSessionEvent.ToolError.Code != CodePermissionDenied {
		t.Fatalf("SessionEvent.ToolError = %+v, want permission_denied", gotSessionEvent.ToolError)
	}
	if gotSessionEvent.ToolError.Details["scope"] != "contacts" {
		t.Fatalf("SessionEvent.ToolError.Details = %+v, want scope=contacts", gotSessionEvent.ToolError.Details)
	}
	message := messageFromRunEvent(gotRunEvent, "", "req-1")
	if message.ToolError == nil || message.ToolError.Code != CodePermissionDenied {
		t.Fatalf("Message.ToolError = %+v, want permission_denied", message.ToolError)
	}
	if message.ToolError.Details["scope"] != "contacts" {
		t.Fatalf("Message.ToolError.Details = %+v, want scope=contacts", message.ToolError.Details)
	}
	if gotRunEvent.Content != toolErr.Message || message.Content != toolErr.Message {
		t.Fatalf("error message content mismatch: run=%q message=%q want=%q", gotRunEvent.Content, message.Content, toolErr.Message)
	}
}

func TestRuntimeCallbackPersistsSessionEventWithCanceledRunContext(t *testing.T) {
	var appenderCtxErr error
	handler := &runtimeCallbackHandler{
		episodeID: "ep-1",
		runtimeID: "runtime-1",
		requestID: "req-1",
		runID:     "run-1",
		sessionEventAppender: func(ctx context.Context, event SessionEvent) error {
			appenderCtxErr = ctx.Err()
			return appenderCtxErr
		},
	}

	handler.emitRunEvent(RunEvent{Type: "tool_result", Content: "ok"})

	if appenderCtxErr != nil {
		t.Fatalf("sessionEventAppender ctx.Err() = %v, want nil", appenderCtxErr)
	}
}

func runEventsOfType(events []RunEvent, eventType string) []RunEvent {
	var matching []RunEvent
	for _, event := range events {
		if event.Type == eventType {
			matching = append(matching, event)
		}
	}
	return matching
}

func taskEpisodeEventsOfType(events []TaskEpisodeEvent, eventType string) []TaskEpisodeEvent {
	var matching []TaskEpisodeEvent
	for _, event := range events {
		if event.Type == eventType {
			matching = append(matching, event)
		}
	}
	return matching
}

type stubTool struct {
	name        string
	description string
	output      string
	err         error
	visual      bool
	inputs      []string
	callFn      func(context.Context, string) (string, error)
}

func (t *stubTool) Name() string { return t.name }

func (t *stubTool) Description() string { return t.description }

func (t *stubTool) ReturnsVisualObservation() bool { return t.visual }

func (t *stubTool) Call(ctx context.Context, input string) (string, error) {
	t.inputs = append(t.inputs, input)
	if t.callFn != nil {
		return t.callFn(ctx, input)
	}
	if t.err != nil {
		return "", t.err
	}
	return t.output, nil
}

type blockingTool struct {
	name          string
	description   string
	output        string
	inputs        []string
	started       chan struct{}
	release       chan struct{}
	canceled      chan struct{}
	interruptSeen chan struct{}
	ignoreCancel  bool
}

func (t *blockingTool) Name() string { return t.name }

func (t *blockingTool) Description() string { return t.description }

func (t *blockingTool) Call(ctx context.Context, input string) (string, error) {
	t.inputs = append(t.inputs, input)
	if t.started != nil {
		close(t.started)
	}
	if t.ignoreCancel {
		if t.interruptSeen != nil {
			go func() {
				<-ctx.Done()
				close(t.interruptSeen)
			}()
		}
		if t.release != nil {
			<-t.release
		}
		return t.output, nil
	}
	select {
	case <-ctx.Done():
		if t.canceled != nil {
			close(t.canceled)
		}
		return "", ctx.Err()
	case <-t.release:
		return t.output, nil
	}
}

func TestBuildLLMStructuredSummarizeFnParsesStrictJSON(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{{Choices: []*llms.ContentChoice{{Content: `{
		"summary":"讨论 MiniCPM 局域网 VLM",
		"user_goals":["测试局域网 VLM"],
		"confirmed_facts":["主模型负责语音链路"],
		"decisions":["VLM 使用 model_vision"],
		"proposals":["screen_memory_summarizer 优先读取 model_vision"],
		"open_tasks":["实现配置解析"],
		"risks_or_pitfalls":["不要替换主模型"],
		"memory_candidates":["语音模型和 VLM 分离配置"]
	}`}}}}}
	fn := buildLLMStructuredSummarizeFn(&testModelResolver{model: model}, nil)
	got := fn(context.Background(), []SessionEvent{{Role: "user", Content: "测试 MiniCPM"}})
	if got.Summary != "讨论 MiniCPM 局域网 VLM" {
		t.Fatalf("summary = %q", got.Summary)
	}
	if got.Decisions[0] != "VLM 使用 model_vision" || got.OpenTasks[0] != "实现配置解析" {
		t.Fatalf("unexpected structured summary: %#v", got)
	}
}

func TestBuildLLMStructuredSummarizeFnFallsBackOnInvalidJSON(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{{Choices: []*llms.ContentChoice{{Content: `not json`}}}}}
	fn := buildLLMStructuredSummarizeFn(&testModelResolver{model: model}, nil)
	got := fn(context.Background(), []SessionEvent{{Role: "user", Content: "hello"}})
	if !got.Empty() {
		t.Fatalf("expected empty structured summary on invalid JSON, got %#v", got)
	}
}

func TestRuntimeRunOpenRouterUsesToolsWithoutStreaming(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}"}`, "The current audio volume is 42."),
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use tools when external state is requested.",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "当前音量是多少？"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "The current audio volume is 42." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "{}" {
		t.Fatalf("unexpected tool inputs: %#v", tool.inputs)
	}
	if len(model.sawStreaming) != 2 || model.sawStreaming[0] || model.sawStreaming[1] {
		t.Fatalf("expected non-streaming default-mode planner calls, got %#v", model.sawStreaming)
	}
}

func TestRuntimeRunFakeProviderUsesFunctionAgentToolCalls(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}"}`, "The current audio volume is 42."),
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when external state is requested.",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "当前音量是多少？"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "The current audio volume is 42." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "{}" {
		t.Fatalf("unexpected tool inputs: %#v", tool.inputs)
	}
}

func TestRuntimeRunRestoresPlannerToolCallsIntoNextRunPrompt(t *testing.T) {
	model := &scriptedModel{
		responses: append(
			roleToolResponses("echo", `{"__arg1":"{}"}`, "first run done"),
			contentResponse("second run done"),
		),
	}
	tool := &stubTool{
		name:        "echo",
		description: "Echo test tool.",
		output:      "echo result",
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when needed.",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(t.TempDir()),
		&ToolSet{tools: map[string]langtools.Tool{
			"echo": tool,
		}},
		NewSkillIndex(),
	)
	t.Cleanup(func() { _ = runtime.Close() })

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "call echo"}); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if _, err := runtime.Run(context.Background(), RunRequest{Input: "continue"}); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if len(model.messages) < 3 {
		t.Fatalf("model calls = %d, want second run planner prompt", len(model.messages))
	}

	secondRunPrompt := model.messages[2]
	var foundToolCall, foundToolResponse bool
	for _, msg := range secondRunPrompt {
		for _, part := range msg.Parts {
			switch typed := part.(type) {
			case llms.ToolCall:
				if msg.Role == llms.ChatMessageTypeAI &&
					typed.ID == "call_1" &&
					typed.FunctionCall != nil &&
					typed.FunctionCall.Name == "echo" {
					foundToolCall = true
				}
			case llms.ToolCallResponse:
				if msg.Role == llms.ChatMessageTypeTool &&
					typed.ToolCallID == "call_1" &&
					strings.Contains(typed.Content, "echo result") {
					foundToolResponse = true
				}
			}
		}
	}
	if !foundToolCall || !foundToolResponse {
		t.Fatalf("second run planner prompt missing persisted tool scratchpad: found call=%v response=%v messages=%#v",
			foundToolCall, foundToolResponse, secondRunPrompt)
	}
}

func TestRuntimeRunExecutesOnlyFirstToolCallAndKeepsModelToolCallMessage(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			multiToolCallResponse(
				llms.ToolCall{
					ID:   "call_1",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name:      "slow_a",
						Arguments: `{"__arg1":"{}"}`,
					},
				},
				llms.ToolCall{
					ID:   "call_2",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name:      "slow_b",
						Arguments: `{"__arg1":"{}"}`,
					},
				},
			),
			contentResponse("done"),
		},
	}
	toolA := &stubTool{name: "slow_a", description: "First tool.", output: `{"ok":true}`}
	toolB := &stubTool{name: "slow_b", description: "Second tool.", output: `{"ok":true}`}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools.",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"slow_a": toolA,
			"slow_b": toolB,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "run both"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(toolA.inputs) != 1 || toolA.inputs[0] != "{}" {
		t.Fatalf("first tool inputs = %#v, want one empty JSON call", toolA.inputs)
	}
	if len(toolB.inputs) != 0 {
		t.Fatalf("second tool inputs = %#v, want no calls", toolB.inputs)
	}
	if len(model.messages) < 2 {
		t.Fatalf("model calls = %d, want at least 2", len(model.messages))
	}
	var toolCallNames []string
	for _, msg := range model.messages[1] {
		for _, part := range msg.Parts {
			toolCall, ok := part.(llms.ToolCall)
			if !ok || toolCall.FunctionCall == nil {
				continue
			}
			toolCallNames = append(toolCallNames, toolCall.FunctionCall.Name)
		}
	}
	if !slices.Equal(toolCallNames, []string{"slow_a"}) {
		t.Fatalf("scratchpad tool calls = %#v, want only first model tool call", toolCallNames)
	}
}

func TestRuntimeRunFeedsToolErrorsBackToModel(t *testing.T) {
	model := &scriptedModel{
		responses: roleReviewedToolResponses("screenshot", `{"__arg1":"{}"}`, "屏幕暂时获取失败，frame service 正在恢复。"),
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools.",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": &stubTool{
				name:        "screenshot",
				description: "Capture a screenshot from the connected display.",
				visual:      true,
				err:         errors.New("frame service: SERVICE_RECOVERING"),
			},
		}},
		NewSkillIndex(),
	)

	var events []RunEvent
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "看看屏幕",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "屏幕暂时获取失败，frame service 正在恢复。" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.messages) < 2 {
		t.Fatalf("expected follow-up planner call with tool observation, got %d calls", len(model.messages))
	}
	var toolObservation string
	for _, msg := range model.messages[1] {
		if msg.Role != llms.ChatMessageTypeTool {
			continue
		}
		if len(msg.Parts) == 1 {
			if part, ok := msg.Parts[0].(llms.ToolCallResponse); ok {
				toolObservation = part.Content
			}
		}
	}
	if !strings.Contains(toolObservation, "frame service: SERVICE_RECOVERING") || strings.Contains(toolObservation, "error:") {
		t.Fatalf("unexpected tool observation: %q", toolObservation)
	}
	toolResult, ok := firstRunEventOfType(events, "tool_result")
	if !ok || !toolResult.IsError {
		t.Fatalf("expected error tool_result event, got %#v", events)
	}
	if toolResult.ToolError == nil || toolResult.ToolError.Code != CodeToolExecutionFailed || toolResult.ToolError.Message != toolObservation {
		t.Fatalf("tool_result ToolError = %+v, want execution failure matching observation %q", toolResult.ToolError, toolObservation)
	}
}
func TestRuntimeRunStripsLegacyToolInputWithoutDerivingContent(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}","description":"我先读取当前音量。","speech":"读取音量。"}`, "The current audio volume is 42."),
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when external state is requested.",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)

	var events []RunEvent
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "当前音量是多少？",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "The current audio volume is 42." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "{}" {
		t.Fatalf("unexpected tool inputs: %#v", tool.inputs)
	}
	toolCall, ok := firstRunEventOfType(events, runEventToolCall)
	if !ok {
		t.Fatalf("expected tool_call event, got %#v", events)
	}
	if toolCall.Content != "" {
		t.Fatalf("tool_call content = %q, want empty without assistant content", toolCall.Content)
	}
	if toolCall.ToolInput != "{}" {
		t.Fatalf("tool_call event input = %q, want stripped input", toolCall.ToolInput)
	}
}

func TestRuntimeRunEmitsToolContentForToolCallSpeech(t *testing.T) {
	speech := "读取音量。"
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponseWithContent("call_1", "audio_volume", `{"__arg1":"{}"}`, speech),
			contentResponse("The current audio volume is 42."),
		},
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	toolSpeechEnabled := true
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:               ModelConfig{Provider: "fake"},
			Instruction:         "Use tools when external state is requested.",
			VoiceToolCallSpeech: &toolSpeechEnabled,
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)

	var events []RunEvent
	if _, err := runtime.Run(context.Background(), RunRequest{
		Input: "当前音量是多少？",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	toolCall, ok := firstRunEventOfType(events, runEventToolCall)
	if !ok {
		t.Fatalf("expected tool_call event, got %#v", events)
	}
	if toolCall.Content != speech {
		t.Fatalf("tool_call content = %q, want assistant content", toolCall.Content)
	}
}

func TestRuntimeLogsPreserveThinkStartTagInToolCallContent(t *testing.T) {
	thinkingContent := "<think>\n需要查当前时间。\n</think>"
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponseWithContent("call_1", "shell", `{"command":"date"}`, thinkingContent),
			contentResponse("已完成。"),
		},
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when external state is requested.",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"shell": &stubTool{name: "shell", description: "Run controller commands.", output: "now"},
		}},
		NewSkillIndex(),
	)
	var logs bytes.Buffer
	runtime.logger = &Logger{logger: log.New(&logs, "", 0)}

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "现在几点了？"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	logText := logs.String()
	if !strings.Contains(logText, "Role output: role=agent") {
		t.Fatalf("missing agent role output log:\n%s", logText)
	}
	if !strings.Contains(logText, "Tool call: name=shell") {
		t.Fatalf("missing shell tool call log:\n%s", logText)
	}
	if strings.Count(logText, "<think>") < 2 {
		t.Fatalf("logs lost think start tag:\n%s", logText)
	}
	if !strings.Contains(logText, "</think>") {
		t.Fatalf("log lost think end tag:\n%s", logText)
	}
}

func TestRuntimeRunDoesNotDeriveToolContentFromDescriptionArgument(t *testing.T) {
	description := "我先读取当前音量并检查当前播放设备、音量状态、静音状态、输出通道以及系统返回结果是否一致。然后继续回答。"
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", fmt.Sprintf(`{"__arg1":"{}","description":%q}`, description), "The current audio volume is 42."),
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	toolSpeechEnabled := true
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:               ModelConfig{Provider: "fake"},
			Instruction:         "Use tools when external state is requested.",
			VoiceToolCallSpeech: &toolSpeechEnabled,
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)

	var events []RunEvent
	if _, err := runtime.Run(context.Background(), RunRequest{
		Input: "当前音量是多少？",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	toolCall, ok := firstRunEventOfType(events, runEventToolCall)
	if !ok {
		t.Fatalf("expected tool_call event, got %#v", events)
	}
	if toolCall.Content != "" {
		t.Fatalf("tool_call content = %q, want empty without assistant content", toolCall.Content)
	}
}
func TestRuntimeDirectAnswerDoesNotGenerateTodo(t *testing.T) {
	model := &scriptedModel{responses: roleDirectResponses("done")}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var events []RunEvent
	if _, err := runtime.Run(context.Background(), RunRequest{
		Input: "hello",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if todos := runEventsOfType(events, "todo_update"); len(todos) != 0 {
		t.Fatalf("direct answer emitted todo_update events: %#v", todos)
	}
	if closed := runEventsOfType(events, "todo_closed"); len(closed) != 0 {
		t.Fatalf("direct answer emitted todo_closed events: %#v", closed)
	}
}

func TestRuntimeSimpleLoopDoesNotGenerateImplicitTodo(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponse("call_1", "screenshot", `{"__arg1":"{}"}`),
			toolCallResponse("call_2", "shell", `{"command":"date"}`),
			contentResponse("done"),
			verifierFinishResponse("done"),
		},
	}
	screenshot := &stubTool{name: "screenshot", description: "Capture screen.", output: "screen"}
	shell := &stubTool{name: "shell", description: "Run controller commands.", output: "now"}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": screenshot,
			"shell":      shell,
		}},
		NewSkillIndex(),
	)

	var events []RunEvent
	if _, err := runtime.Run(context.Background(), RunRequest{
		Input: "look",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	todos := runEventsOfType(events, "todo_update")
	if len(todos) != 0 {
		t.Fatalf("simple loop emitted implicit todo_update events: %#v", todos)
	}
	if closed := runEventsOfType(events, "todo_closed"); len(closed) != 0 {
		t.Fatalf("simple loop emitted implicit todo_closed events: %#v", closed)
	}
	if len(screenshot.inputs) != 1 || len(shell.inputs) != 1 {
		t.Fatalf("expected simple tools to execute without todo, screenshot=%#v shell=%#v", screenshot.inputs, shell.inputs)
	}
}

func TestRuntimeForceSimpleLoopDoesNotGenerateTodo(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponse("call_1", "shell", `{"command":"date"}`),
			contentResponse("done"),
		},
	}
	shell := &stubTool{name: "shell", description: "Run controller commands.", output: "now"}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools.", ForceSimpleLoop: true}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"shell": shell}},
		NewSkillIndex(),
	)

	var events []RunEvent
	if _, err := runtime.Run(context.Background(), RunRequest{
		Input: "search",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	todos := runEventsOfType(events, "todo_update")
	if len(todos) != 0 {
		t.Fatalf("single-agent loop emitted todo_update events: %#v", todos)
	}
	if closed := runEventsOfType(events, "todo_closed"); len(closed) != 0 {
		t.Fatalf("single-agent loop emitted todo_closed events: %#v", closed)
	}
	if len(shell.inputs) != 1 {
		t.Fatalf("expected single-agent tool to execute without todo, inputs=%#v", shell.inputs)
	}
}

func TestRuntimeForceSimpleLoopRejectsLegacySetTodoTool(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponse("todo_1", "set_todo", `{"__arg1":"{\"items\":[\"inspect state\"]}"}`),
			contentResponse("done"),
		},
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools.", ForceSimpleLoop: true}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	var events []RunEvent
	if _, err := runtime.Run(context.Background(), RunRequest{
		Input: "complex single-agent task",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	toolCall, ok := firstRunEventOfType(events, runEventToolCall)
	if !ok || toolCall.ToolName != "set_todo" || toolCall.IsError {
		t.Fatalf("legacy set_todo should be recorded as a normal tool_call event, got %#v", events)
	}
	toolResult, ok := firstRunEventOfType(events, "tool_result")
	if !ok || !toolResult.IsError || toolResult.ToolName != "set_todo" {
		t.Fatalf("legacy set_todo should fail in tool_result, got %#v", events)
	}
	if toolResult.ToolError == nil || toolResult.ToolError.Code != CodeToolNotFound {
		t.Fatalf("legacy set_todo ToolError = %#v, want tool_not_found", toolResult.ToolError)
	}
	if todos := runEventsOfType(events, "todo_update"); len(todos) != 0 {
		t.Fatalf("legacy set_todo emitted todo_update events: %#v", todos)
	}
	if closed := runEventsOfType(events, "todo_closed"); len(closed) != 0 {
		t.Fatalf("legacy set_todo emitted todo_closed events: %#v", closed)
	}
}

func TestRuntimeSimpleLoopDoesNotInjectLegacyTodoReminderAfterSeveralToolCalls(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponse("call_1", "web_search", `{"__arg1":"one"}`),
			toolCallResponse("call_2", "web_search", `{"__arg1":"two"}`),
			toolCallResponse("call_3", "web_search", `{"__arg1":"three"}`),
			contentResponse("done"),
		},
	}
	webSearch := &stubTool{name: "web_search", description: "Search web.", output: "result"}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools.", ForceSimpleLoop: true}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"web_search": webSearch}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "complex single-agent task"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) < 4 {
		t.Fatalf("expected fourth model call after three tools, got %d", len(model.messages))
	}
	for i, messages := range model.messages {
		prompt := messageText(messages)
		if strings.Contains(prompt, "Todo reminder") || strings.Contains(prompt, "call set_todo") {
			t.Fatalf("model call %d leaked legacy todo reminder runtime state:\n%s", i, prompt)
		}
	}
}

func TestRuntimeSimpleLoopIgnoresLegacyTodoReminderThreshold(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponse("call_1", "web_search", `{"__arg1":"one"}`),
			toolCallResponse("call_2", "web_search", `{"__arg1":"two"}`),
			contentResponse("done"),
		},
	}
	webSearch := &stubTool{name: "web_search", description: "Search web.", output: "result"}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools.", ForceSimpleLoop: true, TodoReminderToolCalls: 2}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"web_search": webSearch}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "complex single-agent task"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) < 3 {
		t.Fatalf("expected third model call after two tools, got %d", len(model.messages))
	}
	for i, messages := range model.messages {
		prompt := messageText(messages)
		if strings.Contains(prompt, "Todo reminder") || strings.Contains(prompt, "call set_todo") {
			t.Fatalf("model call %d leaked legacy todo reminder runtime state:\n%s", i, prompt)
		}
	}
}

func TestRuntimeCallbackRemovesPendingActionWithNormalizedToolInput(t *testing.T) {
	handler := &runtimeCallbackHandler{}
	handler.pushPendingAction(schema.AgentAction{
		Tool:      "audio_volume",
		ToolInput: "{}\nObservation:",
	})

	handler.removePendingAction("AUDIO_VOLUME", "{}")

	if action, ok := handler.popPendingAction(); ok {
		t.Fatalf("pending action was not removed: %#v", action)
	}
}

func TestRuntimeRunOpenRouterStreamsWhenWriterIsProvided(t *testing.T) {
	model := &scriptedModel{
		responses: roleDirectResponses("completed"),
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Answer directly.",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var stream bytes.Buffer
	result, err := runtime.Run(context.Background(), RunRequest{
		Input:        "hello",
		StreamWriter: &stream,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "completed" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.sawStreaming) != 1 || !model.sawStreaming[0] {
		t.Fatalf("expected planner call to use provider streaming, got %#v", model.sawStreaming)
	}
	if stream.String() != "chunk:completed" {
		t.Fatalf("stream = %q, want streamed model chunk", stream.String())
	}
}

func TestRuntimeRunDoesNotCallFinalSteerProviderForDirectFinalAnswer(t *testing.T) {
	model := &scriptedModel{
		responses:    roleDirectResponses("final answer"),
		streamChunks: [][]string{{}},
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:           ModelConfig{Provider: "fake"},
			Instruction:     "Answer directly.",
			ForceSimpleLoop: true,
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var finalSteerCalls int32
	var stream bytes.Buffer
	result, err := runtime.Run(context.Background(), RunRequest{
		Input:        "hello",
		StreamWriter: &stream,
		FinalSteerProvider: func(ctx context.Context) (RunSteerMessage, bool) {
			atomic.AddInt32(&finalSteerCalls, 1)
			return RunSteerMessage{}, false
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "final answer" {
		t.Fatalf("Output = %q, want final answer", result.Output)
	}
	if finalSteerCalls != 0 {
		t.Fatalf("FinalSteerProvider calls = %d, want 0 for context-manager loop", finalSteerCalls)
	}
	if stream.String() != "" {
		t.Fatalf("stream = %q, want empty", stream.String())
	}
}
func TestRuntimeRunStreamsToolCapableCallsWhenWriterIsProvided(t *testing.T) {
	toolSpeech := "我先读取当前音量。"
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponseWithContent("call_1", "audio_volume", `{"__arg1":"{}"}`, toolSpeech),
			contentResponse("The current audio volume is 42."),
		},
		streamChunks: [][]string{
			{toolSpeech},
			{"The current audio volume is 42."},
		},
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:           ModelConfig{Provider: "openrouter"},
			Instruction:     "Use tools when external state is requested.",
			ForceSimpleLoop: true,
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)

	var stream bytes.Buffer
	result, err := runtime.Run(context.Background(), RunRequest{
		Input:        "当前音量是多少？",
		StreamWriter: &stream,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "The current audio volume is 42." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "{}" {
		t.Fatalf("unexpected tool inputs: %#v", tool.inputs)
	}
	if got, want := model.sawStreaming, []bool{true, true}; !slices.Equal(got, want) {
		t.Fatalf("expected tool-capable model calls to use provider streaming, got %#v", got)
	}
	if stream.String() != "我先读取当前音量。The current audio volume is 42." {
		t.Fatalf("stream = %q, want streamed model chunks", stream.String())
	}
}
func TestRuntimeRunKeyboardToolFeedsPostActionScreenshotImage(t *testing.T) {
	jpegBytes := []byte("keyboard-post-action-jpeg")
	model := &scriptedModel{
		responses: roleReviewedToolResponses("keyboard_tap", `{"keys":["enter"]}`, "The keyboard action updated the UI."),
	}
	tool := &stubTool{
		name:        "keyboard_tap",
		description: "Press and release keyboard keys.",
		visual:      true,
		output: `{"action_output":"ok","screen_changed":false,"screen_stable":true,"stable_wait_ms":250,"width":800,"height":600,"format":"jpeg","size":25,"data":"` +
			base64.StdEncoding.EncodeToString(jpegBytes) + `"}`,
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use input tools when needed.",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"keyboard_tap": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "press enter"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "The keyboard action updated the UI." {
		t.Fatalf("unexpected output: %q", result.Output)
	}

	var foundToolResponse, foundImage bool
	for _, msg := range model.messages[1] {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.ToolCallResponse:
				if p.ToolCallID == "call_1" {
					foundToolResponse = true
					if !strings.Contains(p.Content, "returned a screenshot observation") {
						t.Fatalf("keyboard tool response = %q, want screenshot observation summary", p.Content)
					}
					if !strings.Contains(p.Content, "No visible screen change was observed") {
						t.Fatalf("keyboard tool response = %q, want screen_changed warning", p.Content)
					}
					if !strings.Contains(p.Content, "Do not assume the action succeeded") {
						t.Fatalf("keyboard tool response = %q, want no-success warning", p.Content)
					}
					if strings.Contains(p.Content, base64.StdEncoding.EncodeToString(jpegBytes)) {
						t.Fatalf("keyboard tool response should not inline screenshot payload: %q", p.Content)
					}
				}
			case llms.BinaryContent:
				if p.MIMEType == "image/jpeg" && string(p.Data) == string(jpegBytes) {
					foundImage = true
				}
			}
		}
	}
	if !foundToolResponse {
		t.Fatalf("expected keyboard tool response in second model call")
	}
	if !foundImage {
		t.Fatalf("expected keyboard post-action screenshot image in second model call")
	}
}

func TestRuntimeCallbackHandlerCapturesUsageMetrics(t *testing.T) {
	metrics := &RunMetrics{}
	handler := &runtimeCallbackHandler{metrics: metrics}

	handler.HandleLLMGenerateContentEnd(context.Background(), &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			GenerationInfo: map[string]any{
				"prompt_tokens":     12,
				"completion_tokens": 34,
				"total_tokens":      46,
			},
		}},
	})

	if metrics.PromptTokens != 12 || metrics.CompletionTokens != 34 || metrics.TotalTokens != 46 {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
}

func TestRuntimeRunCapturesUsageMetricsFromDirectModelCall(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponseWithInfo("completed", map[string]any{
				"prompt_tokens":     600,
				"completion_tokens": 40,
				"total_tokens":      640,
			}),
		},
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Metrics == nil || result.Metrics.PromptTokens != 600 || result.Metrics.CompletionTokens != 40 || result.Metrics.TotalTokens != 640 {
		t.Fatalf("unexpected metrics: %#v", result.Metrics)
	}
}

func TestRuntimeRunResetsPromptTokensWhenUsageUnavailable(t *testing.T) {
	manager := NewMemoryManager("")
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponseWithInfo("with usage", map[string]any{
				"prompt_tokens": 600,
			}),
			contentResponse("without usage"),
		},
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."}),
		&testModelResolver{model: model},
		manager,
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "first"}); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if got := manager.LastPromptTokens(); got != 600 {
		t.Fatalf("expected first run prompt tokens 600, got %d", got)
	}

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "second"}); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if got := manager.LastPromptTokens(); got != 0 {
		t.Fatalf("expected missing usage to reset prompt tokens, got %d", got)
	}
}

func TestRuntimePersistsMemoryUnderConfigDir(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())

	firstRuntime, err := NewRuntime(Config{
		ConfigDir:     configDir,
		Model:         ModelConfig{Provider: "fake"},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer firstRuntime.Close()

	firstRuntime.models = &testModelResolver{
		model: &scriptedModel{responses: roleDirectResponses("first")},
	}

	firstResult, err := firstRuntime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if len(firstResult.Memory) != 0 {
		t.Fatalf("first run memory snapshot = %#v, want empty pre-run snapshot", firstResult.Memory)
	}

	memoryDir := filepath.Join(configDir, "memory")
	eventsPath := filepath.Join(memoryDir, "session", "events.jsonl")
	if _, err := os.Stat(eventsPath); err != nil {
		t.Fatalf("expected persisted session events at %s: %v", eventsPath, err)
	}
	assertNoTopLevelJSONFiles(t, memoryDir)

	secondRuntime, err := NewRuntime(Config{
		ConfigDir:     configDir,
		Model:         ModelConfig{Provider: "fake"},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() second error = %v", err)
	}
	defer secondRuntime.Close()

	secondRuntime.models = &testModelResolver{
		model: &scriptedModel{responses: roleDirectResponses("second")},
	}

	secondResult, err := secondRuntime.Run(context.Background(), RunRequest{Input: "again"})
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if len(secondResult.Memory) < 2 {
		t.Fatalf("expected restored memory entries after reload, got %d: %#v", len(secondResult.Memory), secondResult.Memory)
	}
	if secondResult.Memory[0].Role != "human" || secondResult.Memory[0].Content != "hello" {
		t.Fatalf("expected first persisted message to be restored, got %#v", secondResult.Memory[0])
	}
	if !messageRecordExists(secondResult.Memory, "ai", "first") {
		t.Fatalf("expected first assistant message to be restored, got %#v", secondResult.Memory)
	}
}

func TestNewRuntimeLoadsBundledSkillsSeededOnFirstStartup(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	bundledDir := t.TempDir()
	writeSKILL(t, bundledDir, "alpha", testSkillA)

	runtime, err := NewRuntime(Config{
		ConfigDir:        configDir,
		BundledSkillsDir: bundledDir,
		Model:            ModelConfig{Provider: "fake"},
		Instruction:      "Answer directly.",
		SkillsDirs:       []string{},
		MaxIterations:    1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()

	if _, ok := runtime.skills.GetIndex().Get("alpha"); !ok {
		t.Fatalf("expected runtime to load skill copied during startup sync")
	}
}

func TestRuntimeRunCompactsRealChatExchangesBeyondWindow(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	memDir := filepath.Join(configDir, "memory")
	os.MkdirAll(memDir, 0o755)
	os.WriteFile(filepath.Join(memDir, "extraction.yaml"), []byte("hot_window_events: 20\ncount_compress_after_events: 24\n"), 0o644)

	response := "ok\n<tts>ok</tts>"
	responses := make([]string, 90)
	for i := range responses {
		responses[i] = response
	}
	runtime, err := NewRuntime(Config{
		ConfigDir:     configDir,
		Model:         ModelConfig{Provider: "fake", Responses: responses},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()

	inputs := []string{
		"我是硬件产品经理，平时用中文沟通，关注开发板 agent 端到端行为。",
		"记一下，以后处理蓝海报销App超过100元的提交或付款动作，必须先给风险摘要并等我确认。",
	}
	for i := 0; i < 19; i++ {
		inputs = append(inputs, "填充对话轮次")
	}
	for _, input := range inputs {
		if _, err := runtime.Run(context.Background(), RunRequest{Input: input}); err != nil {
			t.Fatalf("Run(%q) error = %v", input, err)
		}
	}

	waitForSessionCompaction(t, configDir, runtime.memories)
}

func TestRuntimeRunRotatesSessionOnNewBoundary(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	oldSummary := "OLD SESSION SUMMARY MUST NOT ENTER NEW PROMPT"
	now := time.Now().UTC().Add(-6 * time.Minute)
	for i := 0; i < DefaultBoundaryConfig().SmallSessionEventThreshold+1; i++ {
		if _, err := session.AppendEvent(context.Background(), SessionEvent{
			EventID: fmt.Sprintf("evt_old_%d", i),
			Ts:      now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("查天气旧任务 %d", i),
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(storageDir, "session", "summary.md"), []byte(oldSummary), 0o644); err != nil {
		t.Fatalf("WriteFile old summary.md: %v", err)
	}

	releaseMaintenance := make(chan struct{})
	manager := NewMemoryManager(storageDir,
		WithSummarizeFn(func(ctx context.Context, events []SessionEvent) string {
			select {
			case <-ctx.Done():
				return ""
			case <-releaseMaintenance:
				return "old task summary"
			}
		}),
	)
	defer func() {
		close(releaseMaintenance)
		waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.WaitMaintenance(waitCtx); err != nil {
			t.Fatalf("WaitMaintenance() error = %v", err)
		}
	}()
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = NewFilesystemMemoryPlane(storageDir, manager.extraction, nil)
	t.Cleanup(func() { _ = runtime.Close() })

	result, err := runtime.Run(context.Background(), RunRequest{Input: "打开微信"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Memory) != 0 {
		t.Fatalf("new task memory snapshot = %#v, want empty pre-run snapshot after rotation", result.Memory)
	}

	active := readSessionEvents(t, session.eventsPath())
	activeChat := sessionEventsOfTypes(active, "user_input", "assistant_output")
	if !sessionEventExists(activeChat, "user_input", "user", "打开微信") ||
		!sessionEventExists(activeChat, "assistant_output", "assistant", "ok") {
		t.Fatalf("expected active events to contain only current exchange, got %#v", active)
	}
	archiveDirs, err := filepath.Glob(filepath.Join(storageDir, "session_archive", "*"))
	if err != nil {
		t.Fatalf("Glob archived sessions: %v", err)
	}
	if len(archiveDirs) != 1 {
		t.Fatalf("expected one archived rotated session, got %v", archiveDirs)
	}
	archived := readSessionEvents(t, filepath.Join(archiveDirs[0], "events.jsonl"))
	if len(archived) != DefaultBoundaryConfig().SmallSessionEventThreshold+1 || archived[0].EventID != "evt_old_0" {
		t.Fatalf("unexpected archived events: %#v", archived)
	}
	if _, err := os.Stat(filepath.Join(storageDir, "session", "summary.md")); !os.IsNotExist(err) {
		t.Fatalf("active summary.md should be absent after rotation, stat err = %v", err)
	}
	archivedSummary, err := os.ReadFile(filepath.Join(archiveDirs[0], "summary.md"))
	if err != nil {
		t.Fatalf("ReadFile archived summary.md: %v", err)
	}
	if string(archivedSummary) != oldSummary {
		t.Fatalf("old summary not preserved in archive: %q", archivedSummary)
	}
	var promptText strings.Builder
	for _, call := range model.messages {
		for _, message := range call {
			for _, part := range message.Parts {
				if text, ok := part.(llms.TextContent); ok {
					promptText.WriteString(text.Text)
				}
			}
		}
	}
	if strings.Contains(promptText.String(), oldSummary) {
		t.Fatalf("new-session prompt leaked archived summary:\n%s", promptText.String())
	}

	episode, err := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes")).Get(context.Background(), result.EpisodeID)
	if err != nil {
		t.Fatalf("Get episode: %v", err)
	}
	if got := episode.Extra["session_boundary_decision"]; got != BoundaryNew {
		t.Fatalf("session_boundary_decision = %#v, want %q", got, BoundaryNew)
	}
	if got := episode.Extra["session_rotated"]; got != true {
		t.Fatalf("session_rotated = %#v, want true", got)
	}
	if got := numericExtraValue(episode.Extra["pending_chunks_recalled"]); got != 0 {
		t.Fatalf("pending_chunks_recalled = %#v, want 0 without recall tool call", got)
	}
}

func TestRuntimeRunShortGapKeepsActiveSessionWithoutForcedContinuation(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC().Add(-45 * time.Second)
	for i := 0; i < DefaultBoundaryConfig().SmallSessionEventThreshold+1; i++ {
		if _, err := session.AppendEvent(context.Background(), SessionEvent{
			EventID: fmt.Sprintf("evt_old_%d", i),
			Ts:      now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("查天气旧任务 %d", i),
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	manager := NewMemoryManager(storageDir)
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = NewFilesystemMemoryPlane(storageDir, manager.extraction, nil)
	t.Cleanup(func() { _ = runtime.Close() })

	result, err := runtime.Run(context.Background(), RunRequest{Input: "打开微信"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Memory) != DefaultBoundaryConfig().SmallSessionEventThreshold+1 {
		t.Fatalf("short gap should keep previous context, got %#v", result.Memory)
	}

	archiveDirs, err := filepath.Glob(filepath.Join(storageDir, "session_archive", "*"))
	if err != nil {
		t.Fatalf("Glob archived sessions: %v", err)
	}
	if len(archiveDirs) != 0 {
		t.Fatalf("short gap rotated session: %v", archiveDirs)
	}
	for _, event := range readSessionEventObjects(t, session.eventsPath()) {
		if _, ok := event["relation"]; ok {
			t.Fatalf("session event should not persist relation: %#v", event)
		}
	}

	episode, err := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes")).Get(context.Background(), result.EpisodeID)
	if err != nil {
		t.Fatalf("Get episode: %v", err)
	}
	if got := episode.Extra["session_boundary_decision"]; got != BoundaryContinue {
		t.Fatalf("session_boundary_decision = %#v, want %q", got, BoundaryContinue)
	}
	if got := episode.Extra["session_boundary_reason"]; got != BoundaryReasonTimeGapShort {
		t.Fatalf("session_boundary_reason = %#v, want %q", got, BoundaryReasonTimeGapShort)
	}
	if _, ok := episode.Extra["session_continuation_reason"]; ok {
		t.Fatalf("session_continuation_reason should not be recorded: %#v", episode.Extra)
	}
}

func TestRuntimeRunRepairsTruncatedSessionTailBeforeBoundaryRotation(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC().Add(-6 * time.Minute)
	for i := 0; i < DefaultBoundaryConfig().SmallSessionEventThreshold+1; i++ {
		if _, err := session.AppendEvent(context.Background(), SessionEvent{
			EventID: fmt.Sprintf("evt_old_%d", i),
			Ts:      now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("查天气旧任务 %d", i),
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	file, err := os.OpenFile(session.eventsPath(), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("OpenFile events.jsonl: %v", err)
	}
	if _, err := file.WriteString(`{"event_id":"partial_crash_tail","type":"assistant_output","role":"assistant","content":"cut`); err != nil {
		file.Close()
		t.Fatalf("write truncated event tail: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close events.jsonl: %v", err)
	}

	manager := NewMemoryManager(storageDir)
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: &scriptedModel{responses: roleDirectResponses("ok")}},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = NewFilesystemMemoryPlane(storageDir, manager.extraction, nil)
	t.Cleanup(func() { _ = runtime.Close() })

	result, err := runtime.Run(context.Background(), RunRequest{Input: "打开微信"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Memory) != 0 {
		t.Fatalf("new task memory snapshot = %#v, want empty pre-run snapshot after rotation", result.Memory)
	}

	archiveDirs, err := filepath.Glob(filepath.Join(storageDir, "session_archive", "*"))
	if err != nil {
		t.Fatalf("Glob archived sessions: %v", err)
	}
	if len(archiveDirs) != 1 {
		t.Fatalf("expected one archived rotated session, got %v", archiveDirs)
	}
	archivedPath := filepath.Join(archiveDirs[0], "events.jsonl")
	archivedRaw, err := os.ReadFile(archivedPath)
	if err != nil {
		t.Fatalf("ReadFile archived events: %v", err)
	}
	if strings.Contains(string(archivedRaw), "partial_crash_tail") {
		t.Fatalf("runtime boundary rotation archived unrepaired truncated tail: %q", archivedRaw)
	}
	archived := readSessionEvents(t, archivedPath)
	if len(archived) != DefaultBoundaryConfig().SmallSessionEventThreshold+1 {
		t.Fatalf("unexpected archived events after repair: %#v", archived)
	}
}

func TestRuntimeRunKeepsSmallSessionOnUnrelatedInput(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC().Add(-6 * time.Minute)
	for i := 0; i < DefaultBoundaryConfig().SmallSessionEventThreshold; i++ {
		if _, err := session.AppendEvent(context.Background(), SessionEvent{
			EventID: fmt.Sprintf("evt_small_%d", i),
			Ts:      now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("查天气小会话 %d", i),
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	manager := NewMemoryManager(storageDir)
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = NewFilesystemMemoryPlane(storageDir, manager.extraction, nil)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "打开微信"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Memory) != DefaultBoundaryConfig().SmallSessionEventThreshold {
		t.Fatalf("small session should keep previous context, got %#v", result.Memory)
	}

	archiveDirs, err := filepath.Glob(filepath.Join(storageDir, "session_archive", "*"))
	if err != nil {
		t.Fatalf("Glob archived sessions: %v", err)
	}
	if len(archiveDirs) != 0 {
		t.Fatalf("small session with unrelated input rotated session: %v", archiveDirs)
	}

	episode, err := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes")).Get(context.Background(), result.EpisodeID)
	if err != nil {
		t.Fatalf("Get episode: %v", err)
	}
	if got := episode.Extra["session_boundary_decision"]; got != BoundaryContinue {
		t.Fatalf("session_boundary_decision = %#v, want %q", got, BoundaryContinue)
	}
	if got := episode.Extra["session_boundary_reason"]; got != BoundaryReasonSmallSession {
		t.Fatalf("session_boundary_reason = %#v, want %q", got, BoundaryReasonSmallSession)
	}
}

func TestRuntimeRunRotatesNeutralFollowUpAfterFinishedEpisode(t *testing.T) {
	// A finished (non-running) episode must not, on its own, keep a stale
	// session alive across a mid-range gap. The session here is large enough to
	// defeat the small-session bias, and the input is neutral with no
	// continuation marker, so the only thing that could force "continue" is a
	// recently-finished episode — which is exactly the signal we removed.
	configDir := ensureTestConfigDir(t, t.TempDir())
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC().Add(-8 * time.Minute)
	const prevEventCount = 18 // > SmallSessionEventThreshold (16)
	for i := 0; i < prevEventCount; i++ {
		role := "user"
		eventType := "user_input"
		content := "查一下今天天气"
		if i%2 == 1 {
			role = "assistant"
			eventType = "assistant_output"
			content = "今天多云"
		}
		if _, err := session.AppendEvent(context.Background(), SessionEvent{
			EventID: fmt.Sprintf("evt_prev_%d", i),
			Ts:      now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Type:    eventType,
			Role:    role,
			Content: content,
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	episodeStore := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes"))
	if _, err := episodeStore.AddEpisode(context.Background(), TaskEpisode{
		ID:        "ep_weather_done",
		Status:    "active",
		StartedAt: now.Add(-30 * time.Second).Format(time.RFC3339Nano),
		EndedAt:   now.Add(prevEventCount * time.Second).Format(time.RFC3339Nano),
		UserGoal:  "查一下今天天气",
		Outcome:   TaskEpisodeOutcome{Success: true, FinalAnswer: "今天多云"},
	}); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}

	manager := NewMemoryManager(storageDir)
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = NewFilesystemMemoryPlane(storageDir, manager.extraction, nil)
	t.Cleanup(func() { _ = runtime.Close() })

	result, err := runtime.Run(context.Background(), RunRequest{Input: "你有什么爱好？"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	archiveDirs, err := filepath.Glob(filepath.Join(storageDir, "session_archive", "*"))
	if err != nil {
		t.Fatalf("Glob archived sessions: %v", err)
	}
	if len(archiveDirs) != 1 {
		t.Fatalf("neutral follow-up after a finished episode should rotate the session, got %d archives: %v", len(archiveDirs), archiveDirs)
	}

	episode, err := episodeStore.Get(context.Background(), result.EpisodeID)
	if err != nil {
		t.Fatalf("Get episode: %v", err)
	}
	if got := episode.Extra["session_boundary_decision"]; got != BoundaryNew {
		t.Fatalf("session_boundary_decision = %#v, want %q", got, BoundaryNew)
	}
}

func TestRecentEpisodeContextDetectsRunningEpisode(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	store := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes"))
	now := time.Now().UTC()

	if _, err := store.StartEpisode(context.Background(), TaskEpisode{
		ID:        "ep_running",
		StartedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
		UserGoal:  "继续处理天气",
	}); err != nil {
		t.Fatalf("StartEpisode() error = %v", err)
	}
	// A finished episode alongside the running one must not disturb detection.
	if _, err := store.AddEpisode(context.Background(), TaskEpisode{
		ID:        "ep_active_recent",
		Status:    "active",
		StartedAt: now.Add(-3 * time.Minute).Format(time.RFC3339Nano),
		EndedAt:   now.Add(-time.Minute).Format(time.RFC3339Nano),
		UserGoal:  "查天气",
		Outcome:   TaskEpisodeOutcome{Success: true},
	}); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}

	plane := NewFilesystemMemoryPlane(storageDir, DefaultMemoryExtractionConfig(), nil)
	ctx := recentEpisodeContext(plane)
	if !ctx.HasRunning {
		t.Fatalf("expected running episode context")
	}
}

func TestRecentEpisodeContextFinishedEpisodeIsNotASignal(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	store := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes"))
	now := time.Now().UTC()

	if _, err := store.AddEpisode(context.Background(), TaskEpisode{
		ID:        "ep_active_recent",
		Status:    "active",
		StartedAt: now.Add(-3 * time.Minute).Format(time.RFC3339Nano),
		EndedAt:   now.Add(-time.Minute).Format(time.RFC3339Nano),
		UserGoal:  "查天气",
		Outcome:   TaskEpisodeOutcome{Success: true},
	}); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}

	plane := NewFilesystemMemoryPlane(storageDir, DefaultMemoryExtractionConfig(), nil)
	ctx := recentEpisodeContext(plane)
	if ctx.HasRunning {
		t.Fatalf("a finished episode must not produce a running-episode signal")
	}
}

func TestRuntimeRunCanceledWhileQueuedDoesNotRotateSessionOrStartEpisode(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC().Add(-2 * time.Minute)
	for i := 0; i < 2; i++ {
		if _, err := session.AppendEvent(context.Background(), SessionEvent{
			EventID: fmt.Sprintf("evt_old_%d", i),
			Ts:      now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("查天气旧任务 %d", i),
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	model := &queuedCancelModel{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	manager := NewMemoryManager(storageDir)
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = NewFilesystemMemoryPlane(storageDir, manager.extraction, nil)

	firstDone := make(chan error, 1)
	go func() {
		_, err := runtime.Run(context.Background(), RunRequest{Input: "继续查天气"})
		firstDone <- err
	}()
	select {
	case <-model.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first run did not reach model call")
	}

	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := runtime.Run(queuedCtx, RunRequest{Input: "打开微信"})
		secondDone <- err
	}()
	cancelQueued()
	close(model.releaseFirst)

	if err := <-firstDone; err == nil || !strings.Contains(err.Error(), "first run stopped") {
		t.Fatalf("first Run() error = %v, want first run stopped", err)
	}
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued Run() did not return after cancellation")
	}

	archiveDirs, err := filepath.Glob(filepath.Join(storageDir, "session_archive", "*"))
	if err != nil {
		t.Fatalf("Glob archived sessions: %v", err)
	}
	if len(archiveDirs) != 0 {
		t.Fatalf("canceled queued run rotated session: %v", archiveDirs)
	}
	active := readSessionEvents(t, session.eventsPath())
	if len(active) < 2 || active[0].EventID != "evt_old_0" {
		t.Fatalf("active session lost original events: %#v", active)
	}
	if sessionEventsContain(active, func(event SessionEvent) bool {
		return event.Type == "user_input" && event.Content == "打开微信"
	}) {
		t.Fatalf("canceled queued run wrote its input to active session events: %#v", active)
	}
	if !sessionEventsContain(active, func(event SessionEvent) bool {
		return event.Type == "user_input" && event.Content == "继续查天气"
	}) {
		t.Fatalf("started first run should persist its root input: %#v", active)
	}

	index, err := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes")).loadIndex()
	if err != nil {
		t.Fatalf("load episode index: %v", err)
	}
	if len(index.Episodes) != 1 {
		t.Fatalf("episode index contains %d entries, want only the first run: %#v", len(index.Episodes), index.Episodes)
	}
	if index.Episodes[0].UserGoal != "继续查天气" {
		t.Fatalf("unexpected episode goal: %#v", index.Episodes[0])
	}
}

type queuedCancelModel struct {
	firstStarted     chan struct{}
	releaseFirst     chan struct{}
	firstStartedOnce atomic.Bool
	callCount        atomic.Int64
}

func (m *queuedCancelModel) GenerateContent(ctx context.Context, _ []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	if m.callCount.Add(1) == 1 {
		if m.firstStartedOnce.CompareAndSwap(false, true) {
			close(m.firstStarted)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-m.releaseFirst:
			return nil, errors.New("first run stopped")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("queued run reached model")
}

func (m *queuedCancelModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	panic("unexpected Call invocation")
}

func numericExtraValue(v interface{}) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return -1
	}
}

func TestSessionRecallTelemetryCountsPendingResults(t *testing.T) {
	counter := &atomic.Int64{}
	tool := &sessionRecallTelemetryTool{
		inner: &staticTool{
			name:   "recall_session_chunks",
			output: `{"results":[{"chunk_id":"chunk_001"},{"chunk_id":"pending-123","source":"pending"}]}`,
		},
		counter: counter,
	}
	if _, err := tool.Call(context.Background(), `{}`); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got := counter.Load(); got != 1 {
		t.Fatalf("pending recall count = %d, want 1", got)
	}
}

func TestSessionRecallTelemetryIgnoresActiveChunksWithPendingPrefix(t *testing.T) {
	counter := &atomic.Int64{}
	tool := &sessionRecallTelemetryTool{
		inner: &staticTool{
			name: "recall_session_chunks",
			output: `{"results":[` +
				`{"chunk_id":"pending-archived","source":"active"},` +
				`{"chunk_id":"pending-live","source":"pending"}` +
				`]}`,
		},
		counter: counter,
	}
	if _, err := tool.Call(context.Background(), `{}`); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got := counter.Load(); got != 1 {
		t.Fatalf("pending recall count = %d, want 1", got)
	}
}

func TestSessionRecallTelemetryIgnoresCompressedChunkWithPendingPrefix(t *testing.T) {
	ctx := context.Background()
	session := NewSessionMemoryStore(filepath.Join(t.TempDir(), "session"))
	if _, err := session.AppendEvent(ctx, SessionEvent{
		EventID: "evt_pending_consumed",
		Type:    "user_input",
		Role:    "user",
		Content: "already compressed from pending file",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if _, err := session.Compress(ctx, CompressOption{
		ChunkID: "pending-consumed",
		Summary: "already compressed pending file",
	}); err != nil {
		t.Fatalf("Compress() error = %v", err)
	}

	counter := &atomic.Int64{}
	tool := &sessionRecallTelemetryTool{
		inner:   NewRecallSessionChunksTool(session, nil),
		counter: counter,
	}
	output, err := tool.Call(ctx, `{"chunk_ids":["pending-consumed"]}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	var decoded struct {
		Results []struct {
			ChunkID string `json:"chunk_id"`
			Source  string `json:"source"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("decode output %q: %v", output, err)
	}
	if len(decoded.Results) != 1 {
		t.Fatalf("expected 1 result, got %#v", decoded.Results)
	}
	if decoded.Results[0].ChunkID != "pending-consumed" || decoded.Results[0].Source != chunkRecallSourceActive {
		t.Fatalf("unexpected recall source: %#v", decoded.Results[0])
	}
	if got := counter.Load(); got != 0 {
		t.Fatalf("pending recall count = %d, want 0 for active compressed chunk", got)
	}
}

func waitForSessionCompaction(t *testing.T, configDir string, manager *MemoryManager) {
	t.Helper()
	if manager != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.WaitMaintenance(ctx); err != nil {
			t.Fatalf("wait memory maintenance: %v", err)
		}
	}
	session := NewSessionMemoryStore(filepath.Join(configDir, "memory", "session"))
	deadline := time.Now().Add(3 * time.Second)
	var lastEventCount int
	var lastChunkCount int
	var lastErr error

	for time.Now().Before(deadline) {
		events, err := session.readEvents(session.eventsPath())
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		chunks, err := session.RecallChunks(context.Background(), ChunkRecallQuery{Entities: []string{"蓝海报销App"}, Limit: 1})
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		lastEventCount = len(events)
		lastChunkCount = len(chunks)
		if lastEventCount <= 26 && lastChunkCount == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	if lastErr != nil {
		t.Fatalf("waiting for session compaction: %v", lastErr)
	}
	t.Fatalf("expected compacted chunk and hot window events <= 26 including persisted role and assistant outputs, got chunks=%d events=%d", lastChunkCount, lastEventCount)
}

func TestRuntimeRegistersMemoryRecallToolsWhenConfigDirSet(t *testing.T) {
	runtime, err := NewRuntime(Config{
		ConfigDir:     t.TempDir(),
		Model:         ModelConfig{Provider: "fake"},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()

	if _, ok := runtime.tools.Get("recall_session_chunks"); !ok {
		t.Fatalf("expected runtime to register recall_session_chunks")
	}
	if _, ok := runtime.tools.Get("recall_memory"); !ok {
		t.Fatalf("expected runtime to register recall_memory")
	}
}

func TestRuntimeRunOmitsMemoryFilesFromSystemPrompt(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	summary := "SESSION SUMMARY SENTINEL"
	profile := "PROFILE SENTINEL"

	sessionDir := filepath.Join(configDir, "memory", "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("MkdirAll session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.md"), []byte(summary), 0o644); err != nil {
		t.Fatalf("WriteFile summary.md: %v", err)
	}

	longTermDir := filepath.Join(configDir, "memory", "long_term")
	if err := os.MkdirAll(longTermDir, 0o755); err != nil {
		t.Fatalf("MkdirAll long_term: %v", err)
	}
	if err := os.WriteFile(filepath.Join(longTermDir, "profile.md"), []byte(profile), 0o644); err != nil {
		t.Fatalf("WriteFile profile.md: %v", err)
	}

	model := &scriptedModel{
		responses: roleDirectResponses("ok"),
	}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) != 1 || len(model.messages[0]) == 0 {
		t.Fatalf("expected one default-mode planner call with messages, got %#v", model.messages)
	}

	systemMessage := model.messages[0][0]
	if systemMessage.Role != llms.ChatMessageTypeSystem {
		t.Fatalf("expected first message to be system, got %q", systemMessage.Role)
	}
	var systemText strings.Builder
	for _, part := range systemMessage.Parts {
		text, ok := part.(llms.TextContent)
		if ok {
			systemText.WriteString(text.Text)
		}
	}
	for _, unexpected := range []string{summary, profile} {
		if strings.Contains(systemText.String(), unexpected) {
			t.Fatalf("system message should not include memory file %q:\n%s", unexpected, systemText.String())
		}
	}
}

func TestRuntimeMemoryContextIgnoresArchivedSessionSummary(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	memoryDir := filepath.Join(configDir, "memory")
	archiveSummary := "ARCHIVED SESSION SUMMARY SENTINEL"
	profile := "PROFILE STILL ACTIVE"

	archiveDir := filepath.Join(memoryDir, "session_archive", "closed-session")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("MkdirAll archive: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archiveDir, "summary.md"), []byte(archiveSummary), 0o644); err != nil {
		t.Fatalf("WriteFile archive summary.md: %v", err)
	}
	longTermDir := filepath.Join(memoryDir, "long_term")
	if err := os.MkdirAll(longTermDir, 0o755); err != nil {
		t.Fatalf("MkdirAll long_term: %v", err)
	}
	if err := os.WriteFile(filepath.Join(longTermDir, "profile.md"), []byte(profile), 0o644); err != nil {
		t.Fatalf("WriteFile profile.md: %v", err)
	}

	runtime := &Runtime{config: Config{ConfigDir: configDir}}
	promptContext := runtime.memoryContextForPrompt()
	if strings.Contains(promptContext, archiveSummary) {
		t.Fatalf("memoryContextForPrompt leaked archived summary:\n%s", promptContext)
	}
	if !strings.Contains(promptContext, profile) {
		t.Fatalf("memoryContextForPrompt missing active profile:\n%s", promptContext)
	}

	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	retrieved, err := plane.Retrieve(context.Background(), MemoryRetrieveRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if strings.Contains(retrieved.Common.SessionSummary, archiveSummary) {
		t.Fatalf("Retrieve() leaked archived summary: %q", retrieved.Common.SessionSummary)
	}
	if retrieved.Common.Profile != profile {
		t.Fatalf("Retrieve() profile = %q, want %q", retrieved.Common.Profile, profile)
	}
}

func TestRuntimeRunPlacesSystemPromptBeforeCurrentUserMessage(t *testing.T) {
	model := &scriptedModel{
		responses: roleDirectResponses("processed"),
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use context when answering.",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	const userText = "REAL_USER_REQUEST_MARKER"
	if _, err := runtime.Run(context.Background(), RunRequest{
		Input: userText,
		Attachments: []InputAttachment{
			{
				Kind:     AttachmentKindImage,
				Name:     "photo.png",
				MIMEType: "image/png",
				Data:     []byte{0x89, 0x50, 0x4e, 0x47},
			},
		},
		DeviceEnvironment: &PhoneEnvironment{
			Platform:      "ios",
			SystemName:    "iOS",
			SystemVersion: "18.0",
		},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) != 1 {
		t.Fatalf("expected one model call, got %d", len(model.messages))
	}

	messages := model.messages[0]
	if len(messages) < 2 {
		t.Fatalf("expected system and current user messages, got %#v", messages)
	}
	if messages[0].Role != llms.ChatMessageTypeSystem {
		t.Fatalf("first message role = %q, want system", messages[0].Role)
	}
	userMessage := messages[len(messages)-1]
	if userMessage.Role != llms.ChatMessageTypeHuman {
		t.Fatalf("current user message role = %q, want human", userMessage.Role)
	}
	userPrompt := messageText([]llms.MessageContent{userMessage})
	if !strings.Contains(userPrompt, userText) || !strings.Contains(userPrompt, "Attached content") || !strings.Contains(userPrompt, "photo.png") {
		t.Fatalf("current user message missing attachment-aware prompt: %q", userPrompt)
	}
	var userHasImageURL, userHasImageBinary bool
	for _, part := range userMessage.Parts {
		switch typed := part.(type) {
		case llms.ImageURLContent:
			userHasImageURL = true
		case llms.BinaryContent:
			if typed.MIMEType == "image/png" && string(typed.Data) == string([]byte{0x89, 0x50, 0x4e, 0x47}) {
				userHasImageBinary = true
			}
		}
	}
	if userHasImageURL {
		t.Fatalf("current user message unexpectedly retained separate image URL part: %#v", userMessage.Parts)
	}
	if !userHasImageBinary {
		t.Fatalf("current user message missing image binary part: %#v", userMessage.Parts)
	}
}

func TestRuntimeRunIncludesUserAttachments(t *testing.T) {
	model := &scriptedModel{
		responses: roleDirectResponses("processed"),
	}

	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use the provided media when answering.",
		}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "Describe the uploaded media.",
		Attachments: []InputAttachment{
			{
				Kind:     AttachmentKindImage,
				Name:     "photo.png",
				MIMEType: "image/png",
				Data:     []byte{0x89, 0x50, 0x4e, 0x47},
			},
			{
				Kind:     AttachmentKindAudio,
				Name:     "note.wav",
				MIMEType: "audio/wav",
				Data:     []byte{0x52, 0x49, 0x46, 0x46},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "processed" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.messages) != 1 {
		t.Fatalf("expected 1 default-mode planner call, got %d", len(model.messages))
	}

	lastCall := model.messages[0]
	if len(lastCall) < 2 {
		t.Fatalf("expected messages in model call")
	}
	userMessage := lastCall[len(lastCall)-1]
	if userMessage.Role != llms.ChatMessageTypeHuman {
		t.Fatalf("expected raw user message to be human, got %q", userMessage.Role)
	}

	var textContent string
	var imageURL string
	var imageBinary, audioBinary bool
	for _, part := range userMessage.Parts {
		switch p := part.(type) {
		case llms.TextContent:
			textContent = p.Text
		case llms.ImageURLContent:
			imageURL = p.URL
		case llms.BinaryContent:
			if p.MIMEType == "image/png" && string(p.Data) == string([]byte{0x89, 0x50, 0x4e, 0x47}) {
				imageBinary = true
			}
			if p.MIMEType == "audio/wav" && string(p.Data) == string([]byte{0x52, 0x49, 0x46, 0x46}) {
				audioBinary = true
			}
		}
	}

	for _, expected := range []string{"Describe the uploaded media.", "Attached content", "photo.png", "note.wav"} {
		if !strings.Contains(textContent, expected) {
			t.Fatalf("agent user message text missing %q: %q", expected, textContent)
		}
	}
	if strings.Contains(textContent, "data:image/png;base64,") {
		t.Fatalf("image attachment should not be kept inline in text: %q", textContent)
	}
	if imageURL != "" {
		t.Fatalf("image attachment should be folded into text by context manager bridge, got %q", imageURL)
	}
	if !imageBinary || !audioBinary {
		t.Fatalf("missing binary attachment parts: image=%v audio=%v parts=%#v", imageBinary, audioBinary, userMessage.Parts)
	}
}

func TestRuntimeClearMemoryRemovesPersistedSession(t *testing.T) {
	configDir := ensureTestConfigDir(t, t.TempDir())
	runtime, err := NewRuntime(Config{
		ConfigDir:     configDir,
		Model:         ModelConfig{Provider: "fake"},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()
	if err := os.MkdirAll(agentpath.ContextManagerSessionFolder(configDir), 0o755); err != nil {
		t.Fatalf("MkdirAll sessions dir: %v", err)
	}

	runtime.models = &testModelResolver{
		model: &scriptedModel{responses: roleDirectResponses("first")},
	}

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	memoryDir := filepath.Join(configDir, "memory")
	eventsPath := filepath.Join(memoryDir, "session", "events.jsonl")
	if _, err := os.Stat(eventsPath); err != nil {
		t.Fatalf("expected persisted session events at %s: %v", eventsPath, err)
	}
	assertNoTopLevelJSONFiles(t, memoryDir)
	legacyPath := legacyMemorySnapshotPath(memoryDir, "default")
	if err := os.WriteFile(legacyPath, []byte("[]\n"), 0o644); err != nil {
		t.Fatalf("WriteFile legacy snapshot: %v", err)
	}

	if err := runtime.ClearMemory(context.Background()); err != nil {
		t.Fatalf("ClearMemory() error = %v", err)
	}

	if _, err := os.Stat(eventsPath); !os.IsNotExist(err) {
		t.Fatalf("expected session events to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("expected legacy snapshot to be removed, stat err = %v", err)
	}
}

func TestRuntimePreemptCancelsActiveRun(t *testing.T) {
	t.Parallel()

	// Model that blocks until context is canceled.
	model := &blockingFirstCallModel{
		firstCallStarted: make(chan struct{}),
		releaseFirstCall: make(chan struct{}),
		responses:        []*llms.ContentResponse{contentResponse("first"), contentResponse("second")},
	}

	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "test"}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	// Start first run in background.
	firstDone := make(chan struct{})
	var firstErr error
	go func() {
		defer close(firstDone)
		_, firstErr = runtime.Run(context.Background(), RunRequest{Input: "first task"})
	}()

	// Wait for first run to actually start the model call.
	select {
	case <-model.firstCallStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first run did not start model call")
	}

	// Start second run — should preempt the first.
	secondResult, secondErr := runtime.Run(context.Background(), RunRequest{Input: "second task"})

	// First run should have been canceled.
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first run did not finish after preemption")
	}
	if firstErr == nil {
		t.Fatal("expected first run to return error after preemption")
	}

	// Second run should succeed.
	if secondErr != nil {
		t.Fatalf("second run error = %v", secondErr)
	}
	if secondResult.Output != "second" {
		t.Fatalf("second run output = %q, want 'second'", secondResult.Output)
	}

	// WasPreempted should report true.
	if !runtime.WasPreempted(5 * time.Second) {
		t.Fatal("WasPreempted should return true after preemption")
	}
}

func TestRuntimePreemptHooksAreCalled(t *testing.T) {
	t.Parallel()

	model := &blockingFirstCallModel{
		firstCallStarted: make(chan struct{}),
		releaseFirstCall: make(chan struct{}),
		responses:        []*llms.ContentResponse{contentResponse("first"), contentResponse("second")},
	}

	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "test"}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var hookCalled atomic.Int32
	runtime.RegisterPreemptHook(func() {
		hookCalled.Add(1)
	})

	// Start first run.
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		runtime.Run(context.Background(), RunRequest{Input: "first"})
	}()

	select {
	case <-model.firstCallStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first run did not start")
	}

	// Second run triggers preemption.
	runtime.Run(context.Background(), RunRequest{Input: "second"})

	if hookCalled.Load() == 0 {
		t.Fatal("preempt hook was not called")
	}

	// Wait for first run to complete.
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first run did not complete")
	}
}

func TestRuntimePreemptHookPanicDoesNotStopOtherHooks(t *testing.T) {
	t.Parallel()

	model := &blockingFirstCallModel{
		firstCallStarted: make(chan struct{}),
		releaseFirstCall: make(chan struct{}),
		responses:        []*llms.ContentResponse{contentResponse("first"), contentResponse("second")},
	}

	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "test"}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var hook1Called atomic.Int32
	var hook2Called atomic.Int32
	var hook3Called atomic.Int32

	// Register hooks: first and third work normally, second panics.
	runtime.RegisterPreemptHook(func() {
		hook1Called.Add(1)
	})
	runtime.RegisterPreemptHook(func() {
		hook2Called.Add(1)
		panic("intentional test panic")
	})
	runtime.RegisterPreemptHook(func() {
		hook3Called.Add(1)
	})

	// Start first run.
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		runtime.Run(context.Background(), RunRequest{Input: "first"})
	}()

	select {
	case <-model.firstCallStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first run did not start")
	}

	// Second run triggers preemption with panicking hook.
	runtime.Run(context.Background(), RunRequest{Input: "second"})

	// All hooks should be called despite the panic in hook 2.
	if hook1Called.Load() == 0 {
		t.Error("hook 1 was not called")
	}
	if hook2Called.Load() == 0 {
		t.Error("hook 2 was not called (should have been called before panic)")
	}
	if hook3Called.Load() == 0 {
		t.Error("hook 3 was not called (panic in hook 2 should not stop subsequent hooks)")
	}

	// Wait for first run to complete.
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first run did not complete")
	}
}
