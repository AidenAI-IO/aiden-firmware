package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

type testModelResolver struct {
	model llms.Model
	calls int
	spec  ModelSpec
}

func (r *testModelResolver) Get() (llms.Model, error) {
	r.calls++
	return r.model, nil
}

func (r *testModelResolver) CallOptions() []chains.ChainCallOption {
	return nil
}

func (r *testModelResolver) Spec() ModelSpec {
	return r.spec
}

func TestRuntimeRun(t *testing.T) {
	cfg := Config{
		Model:       ModelConfig{Provider: "fake"},
		Instruction: "Answer directly.",
	}

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
	if len(result.Memory) != 2 {
		t.Fatalf("expected 2 memory entries, got %d", len(result.Memory))
	}
}

func TestRuntimeRunWaitForWakeupTerminatesRoleLoop(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("wait_1", "wait_for_wakeup", `{"reason":"user asked"}`),
		toolCallResponse("wait_2", "wait_for_wakeup", `{"reason":"still awake"}`),
	}}
	controller := NewWaitForWakeupController()
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools.", MaxIterations: 2},
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
	if result.Output != "I will wait for the next wakeup." {
		t.Fatalf("Output = %q, want wait-for-wakeup final answer", result.Output)
	}
	if model.callCount != 1 {
		t.Fatalf("model call count = %d, want role loop to stop after wait_for_wakeup", model.callCount)
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
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
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
	want := "Current date: 2026-06-15 (2026年06月15日 星期一)"
	if !strings.Contains(systemPrompt, want) {
		t.Fatalf("planner system prompt missing current date %q:\n%s", want, systemPrompt)
	}
}

func TestRuntimeRunRestoresHotWindowHistoryAsChatMessages(t *testing.T) {
	ctx := context.Background()
	storageDir := filepath.Join(t.TempDir(), "memory")
	manager := NewMemoryManager(storageDir)
	if err := manager.AppendExchange(ctx, "default", "上一轮用户问题", "上一轮回答"); err != nil {
		t.Fatalf("AppendExchange() error = %v", err)
	}

	model := &scriptedModel{responses: roleDirectResponses("completed")}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(ctx, RunRequest{Input: "继续上一轮"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) == 0 || len(model.messages[0]) < 4 {
		t.Fatalf("expected planner system, restored history, and state prompt messages, got %#v", model.messages)
	}
	messages := model.messages[0]
	if messages[1].Role != llms.ChatMessageTypeHuman || messageText(messages[1:2]) != "上一轮用户问题\n" {
		t.Fatalf("restored user history message = role %q text %q", messages[1].Role, messageText(messages[1:2]))
	}
	if messages[2].Role != llms.ChatMessageTypeAI || messageText(messages[2:3]) != "上一轮回答\n" {
		t.Fatalf("restored assistant history message = role %q text %q", messages[2].Role, messageText(messages[2:3]))
	}
	statePrompt := messageText(messages[3:])
	if strings.Contains(statePrompt, "Conversation history:") ||
		strings.Contains(statePrompt, "Human: 上一轮用户问题") ||
		strings.Contains(statePrompt, "AI: 上一轮回答") ||
		strings.Contains(statePrompt, "上一轮回答") {
		t.Fatalf("state prompt should not duplicate restored chat history:\n%s", statePrompt)
	}
}

func TestRuntimeRunAllowsNilMemoryManager(t *testing.T) {
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1},
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

func TestRuntimeRunUsesSessionManager(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponseWithInfo("Old answer.", map[string]any{"prompt_tokens": 321}),
			contentResponse("Committed answer."),
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
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.sessionManager = manager

	var steerCalls int32
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "original request",
		SteerProvider: func(ctx context.Context) (RunSteerMessage, bool) {
			if atomic.AddInt32(&steerCalls, 1) != 1 {
				return RunSteerMessage{}, false
			}
			return RunSteerMessage{ID: "steer-1", Content: "change direction"}, true
		},
	})
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
	if manager.commitReq.Input != "original request" || manager.commitReq.Output != "Committed answer." {
		t.Fatalf("unexpected commit request: %#v", manager.commitReq)
	}
	if len(manager.commitReq.Steers) != 1 || manager.commitReq.Steers[0].Content != "change direction" {
		t.Fatalf("unexpected commit steers: %#v", manager.commitReq.Steers)
	}
	if manager.commitReq.Metrics == nil || manager.commitReq.Metrics.LastPromptTokens != 321 {
		t.Fatalf("commit metrics missing prompt tokens: %#v", manager.commitReq.Metrics)
	}
	assertMemoryRecords(t, result.Memory, manager.result.Memory)
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

func TestRuntimeRunAttachesPendingSteerToNextToolCall(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("echo", `{"__arg1":"original action"}`, "Changed course."),
	}
	tool := &stubTool{
		name:        "echo",
		description: "Echo.",
		output:      "tool output",
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."},
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
	if result.Output != "Changed course." {
		t.Fatalf("output = %q, want Changed course.", result.Output)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "original action" {
		t.Fatalf("tool should run before steer is attached, got inputs %#v", tool.inputs)
	}

	steerEvent, ok := firstRunEventOfType(events, "steer")
	if !ok || steerEvent.Content != "Use the updated instruction instead." {
		t.Fatalf("missing steer event: %#v", events)
	}
	toolResult, ok := firstRunEventOfType(events, "tool_result")
	if !ok {
		t.Fatalf("missing tool_result event: %#v", events)
	}
	if toolResult.Content != "tool output" || toolResult.IsError {
		t.Fatalf("unexpected steer tool result: %#v", toolResult)
	}
	if len(model.messages) < 2 {
		t.Fatalf("expected follow-up model call with steer message, got %#v", model.messages)
	}
	role, text, ok := runtimeLastMessageText(model.messages[1])
	if !ok || role != llms.ChatMessageTypeHuman || text != "Use the updated instruction instead." {
		t.Fatalf("second model call missing steer message: %#v", model.messages)
	}
	if runtimeModelCallContains(model.messages[1], "User steering update received while the agent was already working") {
		t.Fatalf("steer should be appended as a human message, not rewritten as prompt text: %#v", model.messages[1])
	}
	assertMemoryRecords(t, result.Memory, []MessageRecord{
		{Role: "human", Content: "do the original action"},
		{Role: "human", Content: "Use the updated instruction instead."},
		{Role: "ai", Content: "Changed course."},
	})
}

func TestRuntimeRunAttachesPendingSteerBeforeFinalAnswer(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponse("Old answer."),
			contentResponse("Changed course."),
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
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
	if result.Output != "Changed course." {
		t.Fatalf("output = %q, want Changed course.", result.Output)
	}
	if _, ok := firstRunEventOfType(events, "steer"); !ok {
		t.Fatalf("missing steer event: %#v", events)
	}
	if len(model.messages) < 2 {
		t.Fatalf("expected follow-up model call with final-boundary steer message, got %#v", model.messages)
	}
	role, text, ok := runtimeLastMessageText(model.messages[1])
	if !ok || role != llms.ChatMessageTypeHuman || text != "Actually change direction before answering." {
		t.Fatalf("second model call missing final-boundary steer message: %#v", model.messages)
	}
	assertMemoryRecords(t, result.Memory, []MessageRecord{
		{Role: "human", Content: "answer the old request"},
		{Role: "human", Content: "Actually change direction before answering."},
		{Role: "ai", Content: "Changed course."},
	})
}

func TestRuntimeRunPersistsSteerAsConversationHumanMessage(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponse("Old answer."),
			contentResponse("Changed course."),
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
		&testModelResolver{model: model},
		NewMemoryManager(storageDir),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

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
	assertMemoryRecords(t, result.Memory, []MessageRecord{
		{Role: "human", Content: "original persisted request"},
		{Role: "human", Content: "persist this steering message"},
		{Role: "ai", Content: "Changed course."},
	})

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	if !sessionEventsContain(events, func(event SessionEvent) bool {
		return event.Type == "role_output" && event.Role == "planner" && event.Content == "Old answer."
	}) {
		t.Fatalf("expected planner role_output to be persisted in session events: %#v", events)
	}
	chatEvents := sessionEventsOfTypes(events, "user_input", "steer", "assistant_output")
	if len(chatEvents) != 3 {
		t.Fatalf("expected 3 chat-like session events, got %d: %#v", len(chatEvents), events)
	}
	for i, want := range []SessionEvent{
		{Role: "user", Content: "original persisted request"},
		{Role: "user", Content: "persist this steering message"},
		{Role: "assistant", Content: "Changed course."},
	} {
		if chatEvents[i].Role != want.Role || chatEvents[i].Content != want.Content {
			t.Fatalf("chat-like session event %d = %#v, want role=%q content=%q; all events: %#v", i, chatEvents[i], want.Role, want.Content, events)
		}
	}
}

func TestRuntimeRunPersistsSteerEventsWhenSnapshotWindowIsFull(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	memoryManager := NewMemoryManager(storageDir)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := memoryManager.AppendExchange(ctx, "default", fmt.Sprintf("prior user %02d", i), fmt.Sprintf("prior assistant %02d", i)); err != nil {
			t.Fatalf("AppendExchange(%d) error = %v", i, err)
		}
	}

	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponse("Old answer."),
			contentResponse("Changed course."),
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
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
	if result.Output != "Changed course." {
		t.Fatalf("output = %q, want Changed course.", result.Output)
	}

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	chatEvents := sessionEventsOfTypes(events, "user_input", "steer", "assistant_output")
	if len(chatEvents) != 23 {
		t.Fatalf("expected 23 chat-like session events, got %d: %#v", len(chatEvents), events)
	}
	last := chatEvents[len(chatEvents)-3:]
	for i, want := range []SessionEvent{
		{Role: "user", Content: "windowed request"},
		{Role: "user", Content: "persist even when the hot window is full"},
		{Role: "assistant", Content: "Changed course."},
	} {
		if last[i].Role != want.Role || last[i].Content != want.Content {
			t.Fatalf("last session event %d = %#v, want role=%q content=%q; all events: %#v", i, last[i], want.Role, want.Content, events)
		}
	}
}

func TestRuntimeRunPersistsRootInputBeforeModelFailure(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
		&testModelResolver{model: failingGenerateModel{err: errors.New("model unavailable")}},
		NewMemoryManager(storageDir),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

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
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
		&testModelResolver{model: failingGenerateModel{err: errors.New("model unavailable")}},
		NewMemoryManager(storageDir),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

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
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
		&testModelResolver{model: failingGenerateModel{err: errors.New("model unavailable")}},
		NewMemoryManager(storageDir),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

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

func TestRuntimeRunResumeCorrectionUsesRootRequestAndCommittedPlan(t *testing.T) {
	ctx := context.Background()
	storageDir := filepath.Join(t.TempDir(), "memory")
	rootRequest := "打开微信，进入 den 群，发送100块钱红包"
	correction := "群名你听错了，是 Aden AI agent"
	committedPlanPayload, _ := json.Marshal(map[string]any{
		"objective":           "在微信群发送100块钱红包",
		"completion_criteria": []string{"目标群是 Aden AI agent", "发送金额是100块钱红包"},
		"plan": []string{
			"打开微信",
			"进入 den 群",
			"发送100块钱红包",
		},
		"reason": "phone control task needs delegated execution",
	})
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponse(`{"mode":"plan","reason":"requires delegated phone control"}`),
			{
				Choices: []*llms.ContentChoice{{
					Content: "错误推断：发介绍",
					ToolCalls: []llms.ToolCall{{
						ID:   "call_commit",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      toolCommitPlan,
							Arguments: string(committedPlanPayload),
						},
					}},
				}},
			},
			contentResponse("收到，我会按更正后的群名继续。"),
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, MaxIterations: 2},
		&testModelResolver{model: model},
		NewMemoryManager(storageDir),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	_, err := runtime.Run(ctx, RunRequest{
		Input:     rootRequest,
		EpisodeID: "ep_red_packet_resume",
		RequestID: "req-red-packet-1",
	})
	if err == nil {
		t.Fatalf("first Run() error = nil, want forced interruption from max iterations")
	}

	events := readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	if !sessionEventsContain(events, func(event SessionEvent) bool {
		return event.Type == "planner_decision" &&
			event.Objective == "在微信群发送100块钱红包" &&
			slices.Contains(event.Plan, "发送100块钱红包")
	}) {
		t.Fatalf("session events missing structured committed plan: %#v", events)
	}

	if _, err := runtime.Run(ctx, RunRequest{
		Input:     correction,
		EpisodeID: "ep_red_packet_resume",
		RequestID: "req-red-packet-2",
	}); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if len(model.messages) < 3 {
		t.Fatalf("expected second run planner prompt, got %#v", model.messages)
	}
	resumePrompt := messageText(model.messages[2])
	for _, want := range []string{
		"Original user request / root request",
		rootRequest,
		"Latest user message",
		correction,
		"Interpretation:",
		"correction",
		"Latest committed plan",
		"在微信群发送100块钱红包",
		"发送100块钱红包",
	} {
		if !strings.Contains(resumePrompt, want) {
			t.Fatalf("resume planner prompt missing %q:\n%s", want, resumePrompt)
		}
	}
	if strings.Contains(resumePrompt, "发介绍") {
		t.Fatalf("resume planner prompt should not promote unverified role_output into context:\n%s", resumePrompt)
	}
	events = readSessionEvents(t, filepath.Join(storageDir, "session", "events.jsonl"))
	chatEvents := sessionEventsOfTypes(events, "user_input", "assistant_output")
	correctionCount := 0
	answerCount := 0
	for _, event := range chatEvents {
		if event.Type == "user_input" && event.Content == correction {
			correctionCount++
		}
		if event.Type == "assistant_output" && event.Content == "收到，我会按更正后的群名继续。" {
			answerCount++
		}
	}
	if correctionCount != 1 || answerCount != 1 {
		t.Fatalf("chat-like session events duplicated correction/answer: correction=%d answer=%d events=%#v", correctionCount, answerCount, chatEvents)
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
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1},
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
	configDir := t.TempDir()
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
	configDir := t.TempDir()
	tools := &ToolSet{tools: map[string]langtools.Tool{}}
	tools.RegisterSkillTools(filepath.Join(configDir, "skills"), filepath.Join(configDir, "skill-state", ".bundled_manifest.json"))
	runtime := NewRuntimeWithDeps(Config{}, nil, nil, tools, NewSkillIndex())

	for _, name := range []string{"skill_list", "skill_read", "skill_mark_used"} {
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
	tools.RegisterMemoryTools(t.TempDir(), nil, 3, nil)
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

func TestResolveToolsKeepsSkillMetaToolsWhenRestricted(t *testing.T) {
	tools := &ToolSet{tools: map[string]langtools.Tool{
		"screenshot":      &stubTool{name: "screenshot", description: "Take screenshot."},
		"skill_list":      NewSkillListTool(t.TempDir()),
		"skill_read":      NewSkillReadTool(t.TempDir()),
		"skill_mark_used": NewSkillMarkUsedTool(t.TempDir(), ""),
		"skill_manage":    NewSkillManageTool(t.TempDir(), ""),
		"recall_memory":   &stubTool{name: "recall_memory", description: "Recall memory."},
	}}
	runtime := NewRuntimeWithDeps(Config{}, nil, nil, tools, NewSkillIndex())
	resolved := ResolvedSkills{
		AllowedTools:       map[string]struct{}{"screenshot": {}},
		HasToolRestriction: true,
	}
	resolvedTools := runtime.resolveTools(resolved)
	names := map[string]bool{}
	for _, tool := range resolvedTools {
		names[tool.Name()] = true
	}
	for _, name := range []string{"screenshot", "skill_list", "skill_read", "skill_manage", "skill_mark_used"} {
		if !names[name] {
			t.Fatalf("expected %s to be available under tool restrictions; got %#v", name, names)
		}
	}
	for _, name := range []string{"recall_memory"} {
		if names[name] {
			t.Fatalf("did not expect %s without explicit allowed_tools entry; got %#v", name, names)
		}
	}
}

func TestRuntimeRunReloadsSkillsWhenMarkedDirty(t *testing.T) {
	configDir := t.TempDir()
	skillsDir := filepath.Join(configDir, "skills")
	v1 := "---\nname: alpha\ndescription: Alpha\n---\n\nUse alpha v1.\n"
	v2 := "---\nname: alpha\ndescription: Alpha\n---\n\nUse alpha v2.\n"
	writeSKILL(t, skillsDir, "alpha", v1)
	index, err := LoadSkillsFromDirs([]string{skillsDir})
	if err != nil {
		t.Fatal(err)
	}

	model := &scriptedModel{
		responses: append(roleDirectResponses("first"), roleDirectResponses("second")...),
	}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			SkillsDirs:    []string{skillsDir},
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		index,
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello", Skills: []string{"alpha"}}); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if !runtimeModelCallContains(model.messages[0], "Use alpha v1.") {
		t.Fatalf("first run missing v1 skill instructions")
	}

	writeSKILL(t, skillsDir, "alpha", v2)
	runtime.MarkSkillsDirty()

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello again", Skills: []string{"alpha"}}); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	// Default mode issues one planner call per run.
	secondRunPlannerPrompt := model.messages[1]
	if !runtimeModelCallContains(secondRunPlannerPrompt, "Use alpha v2.") {
		t.Fatalf("second run missing reloaded v2 skill instructions")
	}
	if runtimeModelCallContains(secondRunPlannerPrompt, "Use alpha v1.") {
		t.Fatalf("second run still contains stale v1 skill instructions")
	}
}

func TestRuntimeRunSnapshotUnaffectedByConcurrentReload(t *testing.T) {
	configDir := t.TempDir()
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
	responses    []*llms.ContentResponse
	streamChunks [][]string
	callCount    int
	sawStreaming []bool
	messages     [][]llms.MessageContent
	tools        [][]llms.Tool
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
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
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

func enterPlanModeToolCall() *llms.ContentResponse {
	return toolCallResponse("enter_1", toolEnterPlanMode, `{"__arg1":"{}","description":"enter plan mode"}`)
}

func finishStepToolCall(summary string) *llms.ContentResponse {
	payload, _ := json.Marshal(map[string]string{"summary": summary})
	return toolCallResponse("finish_1", toolFinishStep, fmt.Sprintf(`{"__arg1":%q,"description":"finish step"}`, string(payload)))
}

func abortStepToolCall(reason string) *llms.ContentResponse {
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	return toolCallResponse("abort_1", toolAbortStep, fmt.Sprintf(`{"__arg1":%q,"description":"abort step"}`, string(payload)))
}

func commitPlanToolCall(plan ...string) *llms.ContentResponse {
	if len(plan) == 0 {
		plan = []string{"step one"}
	}
	payload, _ := json.Marshal(map[string]any{
		"objective":           "test objective",
		"completion_criteria": []string{"test request is satisfied"},
		"plan":                plan,
		"reason":              "test plan ready",
	})
	return toolCallResponse("commit_1", toolCommitPlan, fmt.Sprintf(`{"__arg1":%q,"description":"commit plan"}`, string(payload)))
}

func setTodoToolCall(id string, items []string, currentIndex int, completed, blocked []int) *llms.ContentResponse {
	payload, _ := json.Marshal(map[string]any{
		"objective":         "test objective",
		"items":             items,
		"current_index":     currentIndex,
		"completed_indices": completed,
		"blocked_indices":   blocked,
		"reason":            "test todo update",
	})
	return toolCallResponse(id, toolSetTodo, fmt.Sprintf(`{"__arg1":%q,"description":"set todo"}`, string(payload)))
}

func roleCommittedExecutionResponses(planSteps []string, pairs ...*llms.ContentResponse) []*llms.ContentResponse {
	responses := []*llms.ContentResponse{
		enterPlanModeToolCall(),
		commitPlanToolCall(planSteps...),
	}
	return append(responses, pairs...)
}

func firstRunEventOfType(events []RunEvent, eventType string) (RunEvent, bool) {
	for _, event := range events {
		if event.Type == eventType {
			return event, true
		}
	}
	return RunEvent{}, false
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

func assertTodoItemStatus(t *testing.T, event RunEvent, itemIndex int, status TodoStatus) {
	t.Helper()
	if event.Todo == nil {
		t.Fatalf("event has nil todo: %#v", event)
	}
	if itemIndex < 0 || itemIndex >= len(event.Todo.Items) {
		t.Fatalf("todo item index %d out of range: %#v", itemIndex, event.Todo.Items)
	}
	if got := event.Todo.Items[itemIndex].Status; got != status {
		t.Fatalf("todo item %d status = %q, want %q in event %#v", itemIndex, got, status, event)
	}
}

type stubTool struct {
	name        string
	description string
	output      string
	err         error
	visual      bool
	inputs      []string
}

func (t *stubTool) Name() string { return t.name }

func (t *stubTool) Description() string { return t.description }

func (t *stubTool) ReturnsVisualObservation() bool { return t.visual }

func (t *stubTool) Call(_ context.Context, input string) (string, error) {
	t.inputs = append(t.inputs, input)
	if t.err != nil {
		return "", t.err
	}
	return t.output, nil
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
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use tools when external state is requested.",
		},
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
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when external state is requested.",
		},
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

func TestRuntimeRunExecutesOnlyFirstToolCallPerIteration(t *testing.T) {
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
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools.",
		},
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
	if len(toolCallNames) != 1 || toolCallNames[0] != "slow_a" {
		t.Fatalf("scratchpad tool calls = %#v, want only slow_a", toolCallNames)
	}
}

func TestRuntimeRunFeedsToolErrorsBackToModel(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("screenshot", `{"__arg1":"{}"}`, "屏幕暂时获取失败，frame service 正在恢复。"),
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools.",
		},
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
	if !strings.Contains(toolObservation, "error: screenshot failed: frame service: SERVICE_RECOVERING") {
		t.Fatalf("unexpected tool observation: %q", toolObservation)
	}
	toolResult, ok := firstRunEventOfType(events, "tool_result")
	if !ok || !toolResult.IsError {
		t.Fatalf("expected error tool_result event, got %#v", events)
	}
}

func TestRuntimeAllowsNearRepeatedMouseClick(t *testing.T) {
	model := &scriptedModel{
		responses: roleCommittedExecutionResponses(
			[]string{"click first point", "click nearby point"},
			toolCallResponse("call_1", "mouse_click", `{"x":"500","y":"80","coord_space":"normalized"}`),
			finishStepToolCall("clicked first point"),
			verifierStepContinueResponse("need a second click"),
			toolCallResponse("call_2", "mouse_click", `{"x":500,"y":120,"coord_space":"normalized"}`),
			finishStepToolCall("clicked nearby point"),
			verifierFinishResponse("我会换一个方式继续。"),
		),
	}
	tool := &stubTool{
		name:        "mouse_click",
		description: "Move mouse to a position and click.",
		output:      "ok",
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"mouse_click": tool,
		}},
		NewSkillIndex(),
	)

	var events []RunEvent
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "tap the field",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "我会换一个方式继续。" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(tool.inputs) != 2 {
		t.Fatalf("expected repeated click attempts to reach the tool, got inputs %#v", tool.inputs)
	}
	if !strings.Contains(tool.inputs[0], `"x":"500"`) {
		t.Fatalf("first click should preserve model input, got %q", tool.inputs[0])
	}
	if !strings.Contains(tool.inputs[1], `"x":500`) {
		t.Fatalf("second click should reach the tool, got %q", tool.inputs[1])
	}

	var resultCount int
	for _, event := range events {
		if event.Type == "tool_result" && event.ToolName == "mouse_click" {
			resultCount++
			if event.IsError {
				t.Fatalf("repeated click should not be marked as an error: %#v", event)
			}
		}
	}
	if resultCount != 2 {
		t.Fatalf("expected two mouse_click result events, got %#v", events)
	}
}

func TestRuntimeAllowsRepeatedKeyboardText(t *testing.T) {
	model := &scriptedModel{
		responses: roleCommittedExecutionResponses(
			[]string{"type first time", "type second time"},
			toolCallResponse("call_1", "keyboard_text", `{"text":"yuanshen"}`),
			finishStepToolCall("typed first time"),
			verifierStepContinueResponse("need a repeated input"),
			toolCallResponse("call_2", "keyboard_text", `{"text":"yuanshen"}`),
			finishStepToolCall("typed second time"),
			verifierFinishResponse("我不会重复输入。"),
		),
	}
	tool := &stubTool{
		name:        "keyboard_text",
		description: "Type text.",
		output:      "ok",
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"keyboard_text": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "type twice"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "我不会重复输入。" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(tool.inputs) != 2 {
		t.Fatalf("expected repeated keyboard_text attempts to reach the tool, got inputs %#v", tool.inputs)
	}
}

func TestRuntimeRunEmitsToolDescriptionEventAndStripsToolInput(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}","description":"我先读取当前音量。","speech":"读取音量。"}`, "The current audio volume is 42."),
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when external state is requested.",
		},
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
	if toolCall.Description != "我先读取当前音量。" || toolCall.Content != toolCall.Description {
		t.Fatalf("unexpected tool description event: %#v", toolCall)
	}
	if toolCall.Speech != "" {
		t.Fatalf("tool_call speech = %q, want empty when voice_tool_call_speech is disabled", toolCall.Speech)
	}
	if toolCall.ToolInput != "{}" {
		t.Fatalf("tool_call event input = %q, want stripped input", toolCall.ToolInput)
	}
}

func TestRuntimeRunEmitsToolSpeechOnlyWhenToolCallSpeechEnabled(t *testing.T) {
	description := "我先读取当前音量并检查当前播放设备、音量状态、静音状态、输出通道以及系统返回结果是否一致。然后继续回答。"
	speech := "读取音量。"
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", fmt.Sprintf(`{"__arg1":"{}","description":%q,"speech":%q}`, description, speech), "The current audio volume is 42."),
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	toolSpeechEnabled := true
	runtime := NewRuntimeWithDeps(
		Config{
			Model:               ModelConfig{Provider: "fake"},
			Instruction:         "Use tools when external state is requested.",
			VoiceToolCallSpeech: &toolSpeechEnabled,
		},
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
	if toolCall.Description != description {
		t.Fatalf("tool_call description changed: %q", toolCall.Description)
	}
	if toolCall.Content != description {
		t.Fatalf("tool_call content = %q, want full description", toolCall.Content)
	}
	if toolCall.Speech == "" {
		t.Fatal("tool_call speech is empty when voice_tool_call_speech is enabled")
	}
	if toolCall.Speech != speech {
		t.Fatalf("tool_call speech = %q, want LLM speech %q", toolCall.Speech, speech)
	}
}

func TestRuntimeRunDoesNotDeriveToolSpeechWhenMissing(t *testing.T) {
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
		Config{
			Model:               ModelConfig{Provider: "fake"},
			Instruction:         "Use tools when external state is requested.",
			VoiceToolCallSpeech: &toolSpeechEnabled,
		},
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
	if toolCall.Description != description {
		t.Fatalf("tool_call description changed: %q", toolCall.Description)
	}
	if toolCall.Speech != "" {
		t.Fatalf("tool_call speech = %q, want empty when LLM omitted speech", toolCall.Speech)
	}
}

func TestRuntimePlannedTodoLifecycleEvents(t *testing.T) {
	model := &scriptedModel{
		responses: roleCommittedExecutionResponses(
			[]string{"inspect screen", "write answer"},
			finishStepToolCall("inspected"),
			verifierStepContinueResponse("step ok"),
			finishStepToolCall("answered"),
			verifierFinishResponse("done"),
		),
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use plan mode."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var events []RunEvent
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "do a planned task",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("Output = %q, want done", result.Output)
	}

	todos := runEventsOfType(events, runEventTodoUpdate)
	if len(todos) != 4 {
		t.Fatalf("todo_update events = %d, want 4: %#v", len(todos), todos)
	}
	if todos[0].Todo == nil || todos[0].Todo.Mode != TodoModePlanned || todos[0].Todo.Revision != 1 || len(todos[0].Todo.Items) != 2 {
		t.Fatalf("unexpected committed todo event: %#v", todos[0])
	}
	if todos[0].Todo.Items[0].Status != TodoPending || todos[0].Todo.Items[1].Status != TodoPending {
		t.Fatalf("commit event should keep all items pending: %#v", todos[0].Todo.Items)
	}
	assertTodoItemStatus(t, todos[1], 0, TodoInProgress)
	if todos[1].Content != "inspect screen" {
		t.Fatalf("step 1 start content = %q", todos[1].Content)
	}
	if !todos[1].SpeechEligible {
		t.Fatalf("step 1 start should be speech eligible: %#v", todos[1])
	}
	assertTodoItemStatus(t, todos[2], 0, TodoDone)
	assertTodoItemStatus(t, todos[2], 1, TodoInProgress)
	if todos[2].Content != "write answer" || !todos[2].SpeechEligible {
		t.Fatalf("step transition should speak step 2 start: %#v", todos[2])
	}
	assertTodoItemStatus(t, todos[3], 1, TodoDone)
	if todos[3].SpeechEligible {
		t.Fatalf("final done event should not be speech eligible: %#v", todos[3])
	}
	closed := runEventsOfType(events, runEventTodoClosed)
	if len(closed) != 1 {
		t.Fatalf("todo_closed events = %d, want 1: %#v", len(closed), closed)
	}
	if closed[0].Todo == nil || closed[0].Todo.Revision != todos[3].Todo.Revision {
		t.Fatalf("todo_closed should carry final todo snapshot: closed=%#v final_todo=%#v", closed[0], todos[3].Todo)
	}
	if closed[0].SpeechEligible {
		t.Fatalf("todo_closed must not be speech eligible: %#v", closed[0])
	}
}

func TestRuntimeTodoUpdatesRecordedInEpisode(t *testing.T) {
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	model := &scriptedModel{
		responses: roleCommittedExecutionResponses(
			[]string{"inspect state"},
			finishStepToolCall("inspected"),
			verifierFinishResponse("done"),
		),
	}
	runtime := NewRuntimeWithDeps(
		Config{ConfigDir: configDir, Model: ModelConfig{Provider: "fake"}, Instruction: "Use plan mode."},
		&testModelResolver{model: model},
		NewMemoryManager(memoryDir),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var events []RunEvent
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "do a planned task",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.EpisodeID == "" {
		t.Fatal("EpisodeID is empty")
	}
	traceTodos := runEventsOfType(events, runEventTodoUpdate)
	if len(traceTodos) == 0 {
		t.Fatalf("runtime emitted no todo_update events: %#v", events)
	}

	eventPaths, err := filepath.Glob(filepath.Join(memoryDir, "episodes", "*", result.EpisodeID, "events.jsonl"))
	if err != nil || len(eventPaths) != 1 {
		t.Fatalf("episode events glob paths=%#v err=%v", eventPaths, err)
	}
	episodeEvents, err := readEpisodeEvents(eventPaths[0])
	if err != nil {
		t.Fatalf("read episode events: %v", err)
	}
	episodeTodos := taskEpisodeEventsOfType(episodeEvents, runEventTodoUpdate)
	if len(episodeTodos) != len(traceTodos) {
		t.Fatalf("episode todo_update count = %d, want %d\ntrace=%#v\nepisode=%#v", len(episodeTodos), len(traceTodos), traceTodos, episodeTodos)
	}
	for i := range traceTodos {
		if episodeTodos[i].Content != traceTodos[i].Content {
			t.Fatalf("todo %d content = %q, want %q", i, episodeTodos[i].Content, traceTodos[i].Content)
		}
		if episodeTodos[i].SpeechEligible != traceTodos[i].SpeechEligible {
			t.Fatalf("todo %d speechEligible = %v, want %v", i, episodeTodos[i].SpeechEligible, traceTodos[i].SpeechEligible)
		}
		if episodeTodos[i].Todo == nil || traceTodos[i].Todo == nil {
			t.Fatalf("todo %d missing snapshot: trace=%#v episode=%#v", i, traceTodos[i].Todo, episodeTodos[i].Todo)
		}
		if !reflect.DeepEqual(*episodeTodos[i].Todo, *traceTodos[i].Todo) {
			t.Fatalf("todo %d snapshot mismatch:\ntrace=%#v\nepisode=%#v", i, *traceTodos[i].Todo, *episodeTodos[i].Todo)
		}
	}

	traceClosed := runEventsOfType(events, runEventTodoClosed)
	episodeClosed := taskEpisodeEventsOfType(episodeEvents, runEventTodoClosed)
	if len(traceClosed) != 1 || len(episodeClosed) != 1 {
		t.Fatalf("todo_closed counts trace=%d episode=%d\ntrace=%#v\nepisode=%#v", len(traceClosed), len(episodeClosed), traceClosed, episodeClosed)
	}
	if traceClosed[0].Content != "final_answer" || episodeClosed[0].Reason != "final_answer" {
		t.Fatalf("todo_closed reason mismatch: trace=%#v episode=%#v", traceClosed[0], episodeClosed[0])
	}
	if traceClosed[0].Todo == nil || episodeClosed[0].Todo == nil {
		t.Fatalf("todo_closed missing snapshot: trace=%#v episode=%#v", traceClosed[0].Todo, episodeClosed[0].Todo)
	}
	if !reflect.DeepEqual(*traceClosed[0].Todo, *episodeClosed[0].Todo) {
		t.Fatalf("todo_closed snapshot mismatch:\ntrace=%#v\nepisode=%#v", *traceClosed[0].Todo, *episodeClosed[0].Todo)
	}
}

func TestRuntimeTodoBlocksAndRevisionsOnReplan(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			enterPlanModeToolCall(),
			commitPlanToolCall("blocked step"),
			finishStepToolCall("blocked attempt"),
			verifierContinueResponse("need a new plan"),
			commitPlanToolCall("replacement step"),
			finishStepToolCall("replacement done"),
			verifierFinishResponse("done"),
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use plan mode."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var events []RunEvent
	if _, err := runtime.Run(context.Background(), RunRequest{
		Input: "do a planned task",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	todos := runEventsOfType(events, runEventTodoUpdate)
	if len(todos) < 5 {
		t.Fatalf("todo_update events = %d, want at least 5: %#v", len(todos), todos)
	}
	assertTodoItemStatus(t, todos[2], 0, TodoBlocked)
	if todos[2].Todo.Revision != 1 {
		t.Fatalf("blocked revision = %d, want 1", todos[2].Todo.Revision)
	}
	if todos[3].Todo == nil || todos[3].Todo.Revision != 2 || len(todos[3].Todo.Items) != 1 {
		t.Fatalf("replan commit should replace items with revision 2: %#v", todos[3].Todo)
	}
	if todos[3].Todo.Items[0].Text != "replacement step" {
		t.Fatalf("replan commit item text = %q, want replacement step", todos[3].Todo.Items[0].Text)
	}
	assertTodoItemStatus(t, todos[4], 0, TodoInProgress)
	if todos[4].Todo.Revision != 2 {
		t.Fatalf("replacement step revision = %d, want 2", todos[4].Todo.Revision)
	}
}

func TestRuntimeDirectAnswerDoesNotGenerateTodo(t *testing.T) {
	model := &scriptedModel{responses: roleDirectResponses("done")}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
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
	if todos := runEventsOfType(events, runEventTodoUpdate); len(todos) != 0 {
		t.Fatalf("direct answer emitted todo_update events: %#v", todos)
	}
	if closed := runEventsOfType(events, runEventTodoClosed); len(closed) != 0 {
		t.Fatalf("direct answer emitted todo_closed events: %#v", closed)
	}
}

func TestRuntimeSimpleLoopDoesNotGenerateImplicitTodo(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponse("call_1", "screenshot", `{"__arg1":"{}"}`),
			toolCallResponse("call_2", "web_search", `{"__arg1":"Aiden"}`),
			contentResponse("done"),
		},
	}
	screenshot := &stubTool{name: "screenshot", description: "Capture screen.", output: "screen"}
	webSearch := &stubTool{name: "web_search", description: "Search web.", output: "result"}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": screenshot,
			"web_search": webSearch,
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
	todos := runEventsOfType(events, runEventTodoUpdate)
	if len(todos) != 0 {
		t.Fatalf("simple loop emitted implicit todo_update events: %#v", todos)
	}
	if closed := runEventsOfType(events, runEventTodoClosed); len(closed) != 0 {
		t.Fatalf("simple loop emitted implicit todo_closed events: %#v", closed)
	}
	if len(screenshot.inputs) != 1 || len(webSearch.inputs) != 1 {
		t.Fatalf("expected simple tools to execute without todo, screenshot=%#v web=%#v", screenshot.inputs, webSearch.inputs)
	}
}

func TestRuntimeForceSimpleLoopDoesNotGenerateTodo(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponse("call_1", "web_search", `{"__arg1":"Aiden"}`),
			contentResponse("done"),
		},
	}
	webSearch := &stubTool{name: "web_search", description: "Search web.", output: "result"}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools.", ForceSimpleLoop: true},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"web_search": webSearch}},
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
	todos := runEventsOfType(events, runEventTodoUpdate)
	if len(todos) != 0 {
		t.Fatalf("force_simple_loop emitted todo_update events: %#v", todos)
	}
	if closed := runEventsOfType(events, runEventTodoClosed); len(closed) != 0 {
		t.Fatalf("force_simple_loop emitted todo_closed events: %#v", closed)
	}
	if len(webSearch.inputs) != 1 {
		t.Fatalf("expected force_simple_loop tool to execute without todo, inputs=%#v", webSearch.inputs)
	}
}

func TestRuntimeForceSimpleLoopExplicitTodoLifecycle(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			setTodoToolCall("todo_1", []string{"inspect state", "write answer"}, 1, nil, nil),
			toolCallResponse("call_1", "web_search", `{"__arg1":"Aiden"}`),
			setTodoToolCall("todo_2", []string{"inspect state", "write answer"}, 2, []int{1}, nil),
			contentResponse("done"),
		},
	}
	webSearch := &stubTool{name: "web_search", description: "Search web.", output: "result"}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools.", ForceSimpleLoop: true},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"web_search": webSearch}},
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
	todos := runEventsOfType(events, runEventTodoUpdate)
	if len(todos) != 2 {
		t.Fatalf("todo_update events = %d, want 2: %#v", len(todos), todos)
	}
	first := todos[0].Todo
	if first == nil || first.Mode != TodoModeSimple || first.Revision != 1 || len(first.Items) != 2 {
		t.Fatalf("unexpected first todo: %#v", first)
	}
	if !todos[0].SpeechEligible || first.Items[0].Status != TodoInProgress || first.Items[0].Source != TodoSourceExplicitSimple {
		t.Fatalf("first todo should start item 1 with speech eligibility: event=%#v todo=%#v", todos[0], first)
	}
	second := todos[1].Todo
	if second == nil || second.Revision != 2 {
		t.Fatalf("unexpected second todo: %#v", second)
	}
	if !todos[1].SpeechEligible || second.Items[0].Status != TodoDone || second.Items[1].Status != TodoInProgress {
		t.Fatalf("second todo should advance to item 2 with speech eligibility: event=%#v todo=%#v", todos[1], second)
	}
	closed := runEventsOfType(events, runEventTodoClosed)
	if len(closed) != 1 {
		t.Fatalf("todo_closed events = %d, want 1: %#v", len(closed), closed)
	}
	if closed[0].Todo == nil || closed[0].Todo.Items[1].Status != TodoInProgress {
		t.Fatalf("todo_closed should preserve last simple todo snapshot without forcing done: %#v", closed[0])
	}
}

func TestRuntimeSimpleLoopTodoReminderAfterSeveralToolCalls(t *testing.T) {
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
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools.", ForceSimpleLoop: true},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"web_search": webSearch}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "complex single-agent task"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) < 4 {
		t.Fatalf("expected fourth model call after reminder, got %d", len(model.messages))
	}
	prompt := messageText(model.messages[3])
	if !strings.Contains(prompt, "Todo reminder") || !strings.Contains(prompt, "call set_todo") {
		t.Fatalf("fourth prompt missing todo reminder:\n%s", prompt)
	}
}

func TestRuntimeSimpleLoopTodoReminderUsesConfiguredToolCallThreshold(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			toolCallResponse("call_1", "web_search", `{"__arg1":"one"}`),
			toolCallResponse("call_2", "web_search", `{"__arg1":"two"}`),
			contentResponse("done"),
		},
	}
	webSearch := &stubTool{name: "web_search", description: "Search web.", output: "result"}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools.", ForceSimpleLoop: true, TodoReminderToolCalls: 2},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"web_search": webSearch}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "complex single-agent task"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) < 3 {
		t.Fatalf("expected third model call after configured reminder, got %d", len(model.messages))
	}
	prompt := messageText(model.messages[2])
	if !strings.Contains(prompt, "Todo reminder") || !strings.Contains(prompt, "call set_todo") {
		t.Fatalf("third prompt missing configured todo reminder:\n%s", prompt)
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

func TestRuntimeRunOpenRouterStreamsOnlyWhenRequested(t *testing.T) {
	model := &scriptedModel{
		responses: roleDirectResponses("completed"),
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Answer directly.",
		},
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
	if len(model.sawStreaming) != 1 || model.sawStreaming[0] {
		t.Fatalf("expected default-mode planner call to avoid provider streaming, got %#v", model.sawStreaming)
	}
	if stream.String() != "completed" {
		t.Fatalf("unexpected stream output: %q", stream.String())
	}
}

func TestRuntimeRunDirectRouteUsesProviderFinalStreaming(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponse(`{"mode":"direct_answer","speech":"Short answer.","text":"Complete answer.","final_answer":"Complete answer.","reason":"direct"}`),
		},
		streamChunks: [][]string{
			{
				`{"mode":"direct_answer","speech":"Short`,
				` answer.","text":"Complete answer.","final_answer":"Complete answer.","reason":"direct"}`,
			},
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Answer directly.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var stream strings.Builder
	result, err := runtime.Run(context.Background(), RunRequest{
		Input:             "hello",
		StreamWriter:      NewSpeechStreamWriter(&stream),
		StreamFinalChunks: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := model.sawStreaming, []bool{true}; !slices.Equal(got, want) {
		t.Fatalf("expected direct route call to use provider streaming, got %#v", got)
	}
	if stream.String() != "Short answer." {
		t.Fatalf("stream = %q, want speech from provider chunks", stream.String())
	}
	if result.Output != "Complete answer." {
		t.Fatalf("Output = %q", result.Output)
	}
	if result.SpeechText != "Short answer." {
		t.Fatalf("SpeechText = %q", result.SpeechText)
	}
}

func TestRuntimeRunDefaultModeFinalAnswerUsesProviderFinalStreaming(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponse(`{"mode":"simple","reason":"ordinary one-pass answer"}`),
			contentResponse(`{"speech":"Short answer.","text":"Complete answer."}`),
		},
		streamChunks: [][]string{
			{
				`{"mode":"simple","reason":"ordinary one-pass answer"}`,
			},
			{
				`{"speech":"Short`,
				` answer.","text":"Complete answer."}`,
			},
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Answer in default mode.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var stream strings.Builder
	result, err := runtime.Run(context.Background(), RunRequest{
		Input:             "hello",
		StreamWriter:      NewSpeechStreamWriter(&stream),
		StreamFinalChunks: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := model.sawStreaming, []bool{true, true}; !slices.Equal(got, want) {
		t.Fatalf("expected route and default final calls to use provider streaming, got %#v", got)
	}
	if stream.String() != "Short answer." {
		t.Fatalf("stream = %q, want only default final speech", stream.String())
	}
	if result.Output != "Complete answer." {
		t.Fatalf("Output = %q", result.Output)
	}
	if result.SpeechText != "Short answer." {
		t.Fatalf("SpeechText = %q", result.SpeechText)
	}
}

func TestRuntimeRunFinalStreamingDoesNotStreamIntermediateToolCalls(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}"}`, "The current audio volume is 42."),
		streamChunks: [][]string{
			{},
			{"The current audio volume is 42."},
		},
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:           ModelConfig{Provider: "openrouter"},
			Instruction:     "Use tools when external state is requested.",
			ForceSimpleLoop: true,
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)

	var stream bytes.Buffer
	result, err := runtime.Run(context.Background(), RunRequest{
		Input:             "当前音量是多少？",
		StreamWriter:      &stream,
		StreamFinalChunks: true,
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
		t.Fatalf("expected default-mode model calls to use provider streaming, got %#v", got)
	}
	if stream.String() != "The current audio volume is 42." {
		t.Fatalf("unexpected stream output: %q", stream.String())
	}
}

func TestRuntimeRunResetsSpeechStreamWriterBetweenFinalStreamingAttempts(t *testing.T) {
	firstVerifier := verifierContinueJSON("need more evidence")
	finalVerifier := structuredVerifierFinishJSON("最终口播。", "最终完整回答。")
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			enterPlanModeToolCall(),
			commitPlanToolCall("inspect first"),
			finishStepToolCall("first candidate"),
			contentResponse(firstVerifier),
			commitPlanToolCall("answer from evidence"),
			finishStepToolCall("final candidate"),
			structuredVerifierFinishResponse("最终口播。", "最终完整回答。"),
		},
		streamChunks: [][]string{
			{},
			{},
			{},
			{firstVerifier},
			{},
			{},
			{finalVerifier[:len(finalVerifier)/2], finalVerifier[len(finalVerifier)/2:]},
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use plan mode."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var stream strings.Builder
	result, err := runtime.Run(context.Background(), RunRequest{
		Input:             "do a planned task",
		StreamWriter:      NewSpeechStreamWriter(&stream),
		StreamFinalChunks: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.sawStreaming) != len(model.responses) || !model.sawStreaming[3] || !model.sawStreaming[6] {
		t.Fatalf("expected both final-step verifier calls to use streaming, got %#v", model.sawStreaming)
	}
	if result.Output != "最终完整回答。" {
		t.Fatalf("Output = %q", result.Output)
	}
	if result.SpeechText != "最终口播。" {
		t.Fatalf("SpeechText = %q", result.SpeechText)
	}
	if stream.String() != "最终口播。" {
		t.Fatalf("stream = %q, want final speech only", stream.String())
	}
}

func TestRuntimeRunFallsBackWhenProviderStreamEmitsNoSpeech(t *testing.T) {
	finalVerifier := verifierFinishJSON("Fallback final answer.")
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			enterPlanModeToolCall(),
			commitPlanToolCall("answer"),
			finishStepToolCall("candidate"),
			contentResponse(finalVerifier),
		},
		streamChunks: [][]string{
			{},
			{},
			{},
			{finalVerifier},
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use plan mode."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var stream strings.Builder
	result, err := runtime.Run(context.Background(), RunRequest{
		Input:             "do a planned task",
		StreamWriter:      NewJSONFieldOrPlainStreamWriter(&stream, "text"),
		StreamFinalChunks: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.sawStreaming) != len(model.responses) || !model.sawStreaming[3] {
		t.Fatalf("expected final verifier call to use provider streaming, got %#v", model.sawStreaming)
	}
	if result.Output != "Fallback final answer." {
		t.Fatalf("Output = %q", result.Output)
	}
	if stream.String() != "Fallback final answer." {
		t.Fatalf("stream = %q, want fallback final answer", stream.String())
	}
}

func TestRuntimeRunFallsBackWhenProviderStreamWriterErrorsAfterPartialSpeech(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			contentResponse(`{"mode":"direct_answer","speech":"Recovered speech.","text":"Complete answer.","final_answer":"Complete answer.","reason":"direct"}`),
		},
		streamChunks: [][]string{
			{
				`{"mode":"direct_answer","speech":"Broken`,
				`\q","text":"ignored"}`,
			},
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var stream strings.Builder
	result, err := runtime.Run(context.Background(), RunRequest{
		Input:             "hello",
		StreamWriter:      NewSpeechStreamWriter(&stream),
		StreamFinalChunks: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := model.sawStreaming, []bool{true}; !slices.Equal(got, want) {
		t.Fatalf("expected direct route call to use provider streaming, got %#v", got)
	}
	if result.Output != "Complete answer." {
		t.Fatalf("Output = %q", result.Output)
	}
	if result.SpeechText != "Recovered speech." {
		t.Fatalf("SpeechText = %q", result.SpeechText)
	}
	if stream.String() != "BrokenRecovered speech." {
		t.Fatalf("stream = %q, want partial speech followed by fallback speech", stream.String())
	}
}

func TestRuntimeRunScreenshotAddsBinaryImageObservation(t *testing.T) {
	jpegBytes := []byte("fake-jpeg-binary")
	model := &scriptedModel{
		responses: roleToolResponses("screenshot", `{"__arg1":"{}"}`, "The screenshot shows a UI."),
	}
	tool := &stubTool{
		name:        "screenshot",
		description: "Capture a screenshot from the connected display.",
		visual:      true,
		output: `{"width":800,"height":600,"format":"jpeg","size":16,"data":"` +
			base64.StdEncoding.EncodeToString(jpegBytes) + `"}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use tools when visual state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "屏幕上有什么？"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "The screenshot shows a UI." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.messages) != 2 {
		t.Fatalf("expected 2 default-mode planner calls, got %d", len(model.messages))
	}

	secondCall := model.messages[1]
	var foundToolResponse bool
	var foundImageURL bool

	for _, msg := range secondCall {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.ToolCallResponse:
				if p.ToolCallID == "call_1" {
					foundToolResponse = true
					if p.Content == tool.output {
						t.Fatalf("expected screenshot tool response to be summarized, got raw payload")
					}
				}
			case llms.ImageURLContent:
				foundImageURL = true
				expectedPrefix := "data:image/jpeg;base64,"
				if !strings.HasPrefix(p.URL, expectedPrefix) {
					t.Fatalf("unexpected image URL prefix: %q", p.URL)
				}
				if p.URL != expectedPrefix+base64.StdEncoding.EncodeToString(jpegBytes) {
					t.Fatalf("unexpected image URL payload: %q", p.URL)
				}
			}
		}
	}

	if !foundToolResponse {
		t.Fatalf("expected screenshot tool response in second model call")
	}
	if !foundImageURL {
		t.Fatalf("expected screenshot image URL in second model call")
	}
}

func TestRuntimeRunScreenshotImageSurvivesCallbackToolWrapping(t *testing.T) {
	jpegBytes := []byte("fake-jpeg-binary")
	model := &scriptedModel{
		responses: roleToolResponses("screenshot", `{"__arg1":"{}"}`, "The screenshot shows a UI."),
	}
	tool := &stubTool{
		name:        "screenshot",
		description: "Capture a screenshot from the connected display.",
		visual:      true,
		output: `{"width":800,"height":600,"format":"jpeg","size":16,"data":"` +
			base64.StdEncoding.EncodeToString(jpegBytes) + `"}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use tools when visual state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": tool,
		}},
		NewSkillIndex(),
	)

	var streamBuf bytes.Buffer
	if _, err := runtime.Run(context.Background(), RunRequest{
		Input:        "屏幕上有什么？",
		StreamWriter: &streamBuf,
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(model.messages) != 2 {
		t.Fatalf("expected 2 default-mode planner calls, got %d", len(model.messages))
	}

	var foundToolResponse, foundImageURL bool
	for _, msg := range model.messages[1] {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.ToolCallResponse:
				if p.ToolCallID == "call_1" {
					foundToolResponse = true
					if p.Content == tool.output {
						t.Fatalf("expected screenshot tool response to be summarized when wrapped by callbackTool, got raw payload")
					}
				}
			case llms.ImageURLContent:
				foundImageURL = true
				expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes)
				if p.URL != expected {
					t.Fatalf("unexpected image URL: %q", p.URL)
				}
			}
		}
	}
	if !foundToolResponse {
		t.Fatalf("expected screenshot tool response when wrapped by callbackTool")
	}
	if !foundImageURL {
		t.Fatalf("expected screenshot image URL when wrapped by callbackTool")
	}
}

func TestRuntimeRunKeyboardToolAddsPostActionImageObservation(t *testing.T) {
	jpegBytes := []byte("keyboard-post-action-jpeg")
	model := &scriptedModel{
		responses: roleToolResponses("keyboard_tap", `{"keys":["enter"]}`, "The keyboard action updated the UI."),
	}
	tool := &stubTool{
		name:        "keyboard_tap",
		description: "Press and release keyboard keys.",
		visual:      true,
		output: `{"action_output":"ok","width":800,"height":600,"format":"jpeg","size":25,"data":"` +
			base64.StdEncoding.EncodeToString(jpegBytes) + `"}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use input tools when needed.",
		},
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

	var foundToolResponse, foundImageURL bool
	for _, msg := range model.messages[1] {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.ToolCallResponse:
				if p.ToolCallID == "call_1" {
					foundToolResponse = true
					if p.Content == tool.output {
						t.Fatalf("expected keyboard tool response to be summarized, got raw screenshot payload")
					}
					if !strings.Contains(p.Content, `keyboard_tap completed with output "ok"`) {
						t.Fatalf("unexpected keyboard tool response summary: %q", p.Content)
					}
				}
			case llms.ImageURLContent:
				foundImageURL = true
				expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes)
				if p.URL != expected {
					t.Fatalf("unexpected image URL: %q", p.URL)
				}
			}
		}
	}
	if !foundToolResponse {
		t.Fatalf("expected keyboard tool response in second model call")
	}
	if !foundImageURL {
		t.Fatalf("expected keyboard post-action screenshot image URL in second model call")
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
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
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
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
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
	configDir := t.TempDir()

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
	if len(firstResult.Memory) != 2 {
		t.Fatalf("expected 2 memory entries after first run, got %d", len(firstResult.Memory))
	}

	memoryPath := filepath.Join(configDir, "memory", "default.json")
	if _, err := os.Stat(memoryPath); err != nil {
		t.Fatalf("expected persisted memory file at %s: %v", memoryPath, err)
	}

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
	if len(secondResult.Memory) != 4 {
		t.Fatalf("expected 4 memory entries after reload, got %d", len(secondResult.Memory))
	}
	if secondResult.Memory[0].Role != "human" || secondResult.Memory[0].Content != "hello" {
		t.Fatalf("expected first persisted message to be restored, got %#v", secondResult.Memory[0])
	}
}

func TestNewRuntimeLoadsBundledSkillsSeededOnFirstStartup(t *testing.T) {
	configDir := t.TempDir()
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
	configDir := t.TempDir()
	memDir := filepath.Join(configDir, "memory")
	os.MkdirAll(memDir, 0o755)
	os.WriteFile(filepath.Join(memDir, "extraction.yaml"), []byte("hot_window_events: 20\n"), 0o644)

	response := `{"objective":"test objective","completion_criteria":["test request is satisfied"],"plan":["answer directly"],"next_step":"answer directly","can_finish":true,"final_answer":"ok","reason":"test verified"}`
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

	waitForSessionCompaction(t, configDir)
}

func TestRuntimeRunSchedulesMemoryMaintenanceAsync(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	cfg := DefaultMemoryExtractionConfig()
	cfg.HotWindowEvents = 4

	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC()
	for i := 0; i < 9; i++ {
		if _, err := session.AppendEvent(context.Background(), SessionEvent{
			EventID: fmt.Sprintf("evt_%d", i),
			Ts:      now.Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: "历史消息",
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var released atomic.Bool
	releaseMaintenance := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer releaseMaintenance()
	var startedOnce atomic.Bool
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg), WithSummarizeFn(func(ctx context.Context, events []SessionEvent) string {
		if startedOnce.CompareAndSwap(false, true) {
			close(started)
		}
		select {
		case <-ctx.Done():
			return ""
		case <-release:
			return "async summary"
		}
	}))

	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1},
		&testModelResolver{model: &scriptedModel{responses: roleDirectResponses("ok")}},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	defer runtime.Close()

	done := make(chan error, 1)
	go func() {
		_, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run() blocked on memory maintenance")
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("async memory maintenance did not start")
	}
	releaseMaintenance()
}

func TestRuntimeRunRotatesSessionOnNewBoundary(t *testing.T) {
	configDir := t.TempDir()
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	oldSummary := "OLD SESSION SUMMARY MUST NOT ENTER NEW PROMPT"
	now := time.Now().UTC().Add(-4 * time.Minute)
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
	manager := NewMemoryManager(storageDir, WithSummarizeFn(func(ctx context.Context, events []SessionEvent) string {
		select {
		case <-ctx.Done():
			return ""
		case <-releaseMaintenance:
			return "old task summary"
		}
	}))
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

	result, err := runtime.Run(context.Background(), RunRequest{Input: "打开微信"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Memory) != 2 || result.Memory[0].Content != "打开微信" {
		t.Fatalf("expected clean memory snapshot for new task, got %#v", result.Memory)
	}

	active := readSessionEvents(t, session.eventsPath())
	activeChat := sessionEventsOfTypes(active, "user_input", "assistant_output")
	if len(activeChat) != 2 || activeChat[0].Content != "打开微信" {
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

func TestRuntimeRunRepairsTruncatedSessionTailBeforeBoundaryRotation(t *testing.T) {
	configDir := t.TempDir()
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC().Add(-4 * time.Minute)
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

	result, err := runtime.Run(context.Background(), RunRequest{Input: "打开微信"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Memory) != 2 || result.Memory[0].Content != "打开微信" {
		t.Fatalf("expected clean memory snapshot for new task, got %#v", result.Memory)
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
	configDir := t.TempDir()
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC().Add(-4 * time.Minute)
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
	if len(result.Memory) != DefaultBoundaryConfig().SmallSessionEventThreshold+2 {
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

func TestRuntimeRunKeepsNeutralFollowUpWithActiveEpisode(t *testing.T) {
	configDir := t.TempDir()
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC().Add(-4 * time.Minute)
	for i, content := range []string{"查一下今天天气", "今天多云"} {
		role := "user"
		eventType := "user_input"
		if i == 1 {
			role = "assistant"
			eventType = "assistant_output"
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
		EndedAt:   now.Add(time.Second).Format(time.RFC3339Nano),
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

	result, err := runtime.Run(context.Background(), RunRequest{Input: "好的"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Memory) != 4 || result.Memory[0].Content != "查一下今天天气" {
		t.Fatalf("neutral follow-up should keep previous session context, got %#v", result.Memory)
	}

	archiveDirs, err := filepath.Glob(filepath.Join(storageDir, "session_archive", "*"))
	if err != nil {
		t.Fatalf("Glob archived sessions: %v", err)
	}
	if len(archiveDirs) != 0 {
		t.Fatalf("neutral follow-up with active episode rotated session: %v", archiveDirs)
	}

	episode, err := episodeStore.Get(context.Background(), result.EpisodeID)
	if err != nil {
		t.Fatalf("Get episode: %v", err)
	}
	if got := episode.Extra["session_boundary_decision"]; got != BoundaryContinue {
		t.Fatalf("session_boundary_decision = %#v, want %q", got, BoundaryContinue)
	}
	if got := episode.Extra["session_boundary_reason"]; got != BoundaryReasonActiveEpisode {
		t.Fatalf("session_boundary_reason = %#v, want %q", got, BoundaryReasonActiveEpisode)
	}
}

func TestRecentEpisodeContextIncludesRunningAndRecentActiveEpisodes(t *testing.T) {
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
	ctx := recentEpisodeContext(plane, now, 5*time.Minute)
	if !ctx.HasRunning {
		t.Fatalf("expected running episode context")
	}
	if !ctx.HasActive {
		t.Fatalf("expected recent active episode context")
	}
}

func TestRecentEpisodeContextIgnoresOldActiveEpisode(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	store := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes"))
	now := time.Now().UTC()

	if _, err := store.AddEpisode(context.Background(), TaskEpisode{
		ID:        "ep_active_old",
		Status:    "active",
		StartedAt: now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
		EndedAt:   now.Add(-6 * time.Minute).Format(time.RFC3339Nano),
		UserGoal:  "查天气",
		Outcome:   TaskEpisodeOutcome{Success: true},
	}); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}

	plane := NewFilesystemMemoryPlane(storageDir, DefaultMemoryExtractionConfig(), nil)
	ctx := recentEpisodeContext(plane, now, 5*time.Minute)
	if ctx.HasActive {
		t.Fatalf("old active episode should not bias session boundary")
	}
}

func TestRuntimeRunCanceledWhileQueuedDoesNotRotateSessionOrStartEpisode(t *testing.T) {
	configDir := t.TempDir()
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
		inner:   NewRecallSessionChunksTool(session),
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

func waitForSessionCompaction(t *testing.T, configDir string) {
	t.Helper()
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
		if lastEventCount <= 22 && lastChunkCount == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	if lastErr != nil {
		t.Fatalf("waiting for session compaction: %v", lastErr)
	}
	t.Fatalf("expected compacted chunk and hot window events <= 22 including pinned root and realtime role_output, got chunks=%d events=%d", lastChunkCount, lastEventCount)
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

func TestRuntimeRunInjectsMemoryFilesIntoSystemPrompt(t *testing.T) {
	configDir := t.TempDir()
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
	if !strings.Contains(systemText.String(), summary) {
		t.Fatalf("system message missing summary:\n%s", systemText.String())
	}
	if !strings.Contains(systemText.String(), profile) {
		t.Fatalf("system message missing profile:\n%s", systemText.String())
	}
}

func TestRuntimeMemoryContextIgnoresArchivedSessionSummary(t *testing.T) {
	configDir := t.TempDir()
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

func TestRuntimeRunIncludesRuntimeContextInSystemMessage(t *testing.T) {
	model := &scriptedModel{
		responses: roleDirectResponses("ok"),
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	runtimeContext := "Phone bridge status:\n- connected: true"
	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello", RuntimeContext: runtimeContext}); err != nil {
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
	if !strings.Contains(systemText.String(), "## Runtime context\n"+runtimeContext) {
		t.Fatalf("system message missing runtime context:\n%s", systemText.String())
	}
}

func TestRuntimeRunIncludesUserAttachments(t *testing.T) {
	model := &scriptedModel{
		responses: roleDirectResponses("processed"),
	}

	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use the provided media when answering.",
		},
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
	if len(lastCall) == 0 {
		t.Fatalf("expected messages in model call")
	}
	userMessage := lastCall[len(lastCall)-1]
	if userMessage.Role != llms.ChatMessageTypeHuman {
		t.Fatalf("expected final message to be human, got %q", userMessage.Role)
	}

	var textContent string
	var imageURL string
	var binaryMIMEs []string
	for _, part := range userMessage.Parts {
		switch p := part.(type) {
		case llms.TextContent:
			textContent = p.Text
		case llms.ImageURLContent:
			imageURL = p.URL
		case llms.BinaryContent:
			binaryMIMEs = append(binaryMIMEs, p.MIMEType)
		}
	}

	if !strings.Contains(textContent, "photo.png") || !strings.Contains(textContent, "note.wav") {
		t.Fatalf("expected attachment names in prompt text, got %q", textContent)
	}
	if imageURL == "" || !strings.HasPrefix(imageURL, "data:image/png;base64,") {
		t.Fatalf("expected image attachment as data URL, got %q", imageURL)
	}
	if len(binaryMIMEs) != 1 || binaryMIMEs[0] != "audio/wav" {
		t.Fatalf("unexpected binary attachment MIME types: %#v", binaryMIMEs)
	}
}

func TestRuntimeClearMemoryRemovesPersistedFile(t *testing.T) {
	configDir := t.TempDir()
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

	runtime.models = &testModelResolver{
		model: &scriptedModel{responses: roleDirectResponses("first")},
	}

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	memoryPath := filepath.Join(configDir, "memory", "default.json")
	if _, err := os.Stat(memoryPath); err != nil {
		t.Fatalf("expected persisted memory file at %s: %v", memoryPath, err)
	}

	if err := runtime.ClearMemory(context.Background()); err != nil {
		t.Fatalf("ClearMemory() error = %v", err)
	}

	if _, err := os.Stat(memoryPath); !os.IsNotExist(err) {
		t.Fatalf("expected memory file to be removed, stat err = %v", err)
	}
}
