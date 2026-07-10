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

func TestAgentLoopDeliversSoftNoticeAsTransient(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call-1", "touch_gesture", `{"gesture":"swipe_up"}`),
		toolCallResponse("call-2", "touch_gesture", `{"gesture":"swipe_up"}`),
		contentResponse("I will change strategy."),
	}}
	manager, err := freshNewContextManager("system", "swipe until done", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	loop := NewAgentLoop(
		model,
		RoleProfile{Tools: []langtools.Tool{&loopGuardEchoTool{output: "same"}}},
		nil,
		10,
		nil,
		nil,
		ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	loop.TerminationPolicy = NewTerminationPolicy(TerminationPolicyConfig{
		RepeatActionLimit:       100,
		SameResultLimit:         100,
		ScreenUnchangedLimit:    100,
		SoftNoticeStallScore:    1,
		RestrictToolsStallScore: 100,
		TerminateStallScore:     100,
	})

	output, err := loop.Run(context.Background(), "swipe until done")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if output != "I will change strategy." {
		t.Fatalf("output = %q", output)
	}
	if got := countLoopGuardNotices(model.messages[1]); got != 1 {
		t.Fatalf("soft notice count in deciding prompt = %d, want 1", got)
	}
	for _, message := range manager.MessageListDump().Messages {
		if message.Role == context_manager.MessageRoleNotice {
			t.Fatalf("soft notice should not persist: %#v", manager.MessageListDump().Messages)
		}
	}
}

func TestAgentLoopEscalatesFromTransientNoticeToRestrictionAndTermination(t *testing.T) {
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
	if !strings.Contains(output, "not making measurable progress") {
		t.Fatalf("output = %q, want loop-guard stop message", output)
	}
	if model.callCount != 3 {
		t.Fatalf("model call count = %d, want termination before fourth response", model.callCount)
	}
	if got := countLoopGuardNotices(model.messages[2]); got != 1 {
		t.Fatalf("restriction notice count in deciding prompt = %d, want 1", got)
	}
	for _, message := range manager.MessageListDump().Messages {
		if message.Role == context_manager.MessageRoleNotice {
			t.Fatalf("loop guard notice should not persist in context manager: %#v", manager.MessageListDump().Messages)
		}
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

func countLoopGuardNotices(messages []llms.MessageContent) int {
	var notices int
	for _, message := range messages {
		for _, part := range message.Parts {
			text, ok := part.(llms.TextContent)
			if ok && strings.Contains(text.Text, "Loop guard:") {
				notices++
			}
		}
	}
	return notices
}
