package agent

import (
	"context"
	"strings"
	"testing"

	"aiden-agent/internal/agent/context_manager"

	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"
)

type loopGuardEchoTool struct {
	output string
}

func (t *loopGuardEchoTool) Name() string        { return "touch_gesture" }
func (t *loopGuardEchoTool) Description() string { return "echo gesture" }
func (t *loopGuardEchoTool) Call(context.Context, string) (string, error) {
	return t.output, nil
}

func TestAgentLoopInjectsLoopGuardNoticeThroughContextHook(t *testing.T) {
	t.Parallel()

	screen := `{"width":100,"height":100,"format":"jpeg","data":"same-screen"}`
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call-1", "touch_gesture", `{"gesture":"swipe_up"}`),
		toolCallResponse("call-2", "touch_gesture", `{"gesture":"swipe_up"}`),
		contentResponse("I should stop retrying and explain the blocker."),
	}}
	manager, err := freshNewContextManager("system", "swipe until done", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	loop := NewAgentLoop(
		model,
		RoleProfile{Tools: []langtools.Tool{&loopGuardEchoTool{output: screen}}},
		nil,
		10,
		nil,
		nil,
		ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	loop.LoopGuardNotice = LoopGuardNoticeConfig{
		ToolResultNoticeThreshold:      2,
		RepeatToolNoticeThreshold:      2,
		SameObservationNoticeThreshold: 2,
	}

	output, err := loop.Run(context.Background(), "swipe until done")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(output, "stop retrying") {
		t.Fatalf("output = %q, want scripted self-stop response", output)
	}
	if model.callCount != 3 {
		t.Fatalf("model call count = %d, want model to receive notice then decide", model.callCount)
	}

	dump := manager.MessageListDump()
	foundNotice := false
	for _, message := range dump.Messages {
		if message.Role == context_manager.MessageRoleNotice && strings.Contains(message.Content, "Loop guard notice") {
			foundNotice = true
			break
		}
	}
	if !foundNotice {
		t.Fatalf("expected loop guard notice in context manager, got %#v", dump.Messages)
	}
}

func TestAgentLoopLoopGuardHookIsScopedToRun(t *testing.T) {
	t.Parallel()

	manager, err := freshNewContextManager("system", "first", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	newLoop := func(model *scriptedModel) *AgentLoop {
		loop := NewAgentLoop(
			model,
			RoleProfile{Tools: []langtools.Tool{&loopGuardEchoTool{output: "ok"}}},
			nil,
			3,
			nil,
			nil,
			ScreenshotPruningConfig{}.WithDefaults(),
			manager,
		)
		loop.LoopGuardNotice = LoopGuardNoticeConfig{ToolResultNoticeThreshold: 1, NoticeEvery: 1}
		return loop
	}

	firstModel := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call-1", "touch_gesture", `{"gesture":"swipe_up"}`),
		contentResponse("first done"),
	}}
	if _, err := newLoop(firstModel).Run(context.Background(), "first"); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}

	secondModel := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call-2", "touch_gesture", `{"gesture":"swipe_up"}`),
		contentResponse("second done"),
	}}
	if _, err := newLoop(secondModel).Run(context.Background(), "second"); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}

	var notices int
	for _, message := range manager.MessageListDump().Messages {
		if message.Role == context_manager.MessageRoleNotice && strings.Contains(message.Content, "Loop guard notice") {
			notices++
		}
	}
	if notices != 2 {
		t.Fatalf("loop guard notices = %d, want one per run without accumulated hooks", notices)
	}
}
