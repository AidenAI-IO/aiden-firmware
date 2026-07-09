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

func TestAgentLoopStopsOnRepeatedNoProgressToolCalls(t *testing.T) {
	t.Parallel()

	screen := `{"width":100,"height":100,"format":"jpeg","data":"same-screen"}`
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call-1", "touch_gesture", `{"gesture":"swipe_up"}`),
		toolCallResponse("call-2", "touch_gesture", `{"gesture":"swipe_up"}`),
		toolCallResponse("call-3", "touch_gesture", `{"gesture":"swipe_up"}`),
		toolCallResponse("call-4", "touch_gesture", `{"gesture":"swipe_up"}`),
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
	loop.TerminationPolicy = NewTerminationPolicy(DefaultTerminationPolicyConfig())

	output, err := loop.Run(context.Background(), "swipe until done")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(output, "没有明显进展") {
		t.Fatalf("output = %q, want loop-guard stop message", output)
	}
	if model.callCount > 3 {
		t.Fatalf("model call count = %d, want loop guard to stop before exhausting scripted responses", model.callCount)
	}

	dump := manager.MessageListDump()
	foundNotice := false
	for _, message := range dump.Messages {
		if message.Role == context_manager.MessageRoleNotice && strings.Contains(message.Content, "Loop guard") {
			foundNotice = true
			break
		}
	}
	if !foundNotice {
		t.Fatalf("expected loop guard notice in context manager, got %#v", dump.Messages)
	}
}

func TestAgentLoopBudgetExhaustionReturnsGracefulStop(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call-1", "touch_gesture", `{"gesture":"swipe_up"}`),
		toolCallResponse("call-2", "touch_gesture", `{"gesture":"swipe_up"}`),
	}}
	screen := `{"width":100,"height":100,"format":"jpeg","data":"screen-1"}`
	manager, err := freshNewContextManager("system", "keep going", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}

	loop := NewAgentLoop(
		model,
		RoleProfile{Tools: []langtools.Tool{&loopGuardEchoTool{output: screen}}},
		nil,
		1,
		nil,
		nil,
		ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	loop.TerminationPolicy = NewTerminationPolicy(TerminationPolicyConfig{
		RepeatActionLimit: 99,
		SameResultLimit:   99,
	})

	output, err := loop.Run(context.Background(), "keep going")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(output, "budget_exceeded") {
		t.Fatalf("output = %q, want budget exhaustion message", output)
	}
}

func TestAgentLoopReturnsContextErrorBeforeGracefulStop(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{responses: roleDirectResponses("should not be called")}
	manager, err := freshNewContextManager("system", "stop", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	loop := NewAgentLoop(
		model,
		RoleProfile{},
		nil,
		10,
		nil,
		nil,
		ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	output, err := loop.Run(ctx, "stop")
	if err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if output != "" {
		t.Fatalf("output = %q, want empty output", output)
	}
	if model.callCount != 0 {
		t.Fatalf("model call count = %d, want 0", model.callCount)
	}
}
