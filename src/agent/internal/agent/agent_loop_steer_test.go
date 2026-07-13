package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestAgentLoopSteerInterruptOnTermination(t *testing.T) {
	t.Parallel()

	// Simulate: agent hits termination policy (loop detected), checks for pending steer
	screen := `{"width":100,"height":100,"format":"jpeg","data":"same-screen"}`
	var checkCount int
	steerProvider := func(ctx context.Context) (RunSteerMessage, bool) {
		checkCount++
		// Return steer only on the last check (at termination)
		// Tool execution will check 3 times, then termination will check once
		if checkCount < 4 {
			return RunSteerMessage{}, false
		}
		return RunSteerMessage{
			Content:   "停止当前任务",
			Timestamp: time.Now(),
		}, true
	}

	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call-1", "touch_gesture", `{"type":"swipe_up"}`),
		toolCallResponse("call-2", "touch_gesture", `{"type":"swipe_up"}`),
		toolCallResponse("call-3", "touch_gesture", `{"type":"swipe_up"}`),
	}}

	manager, err := freshNewContextManager("system", "task", nil, t.TempDir())
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
	loop.SteerProvider = steerProvider
	loop.TerminationPolicy = NewTerminationPolicy(DefaultTerminationPolicyConfig())

	output, err := loop.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Should stop due to loop detection and return steer message instead of loop guard message
	if !strings.Contains(output, "停止当前任务") {
		t.Fatalf("output = %q, want steer content", output)
	}
	if strings.Contains(output, "not making measurable progress") {
		t.Fatalf("output = %q, should use steer message not loop guard message", output)
	}
}

func TestAgentLoopNoSteerProviderDoesNotBlock(t *testing.T) {
	t.Parallel()

	// Ensure loop works normally without SteerProvider
	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call-1", "touch_gesture", `{"type":"swipe_up"}`),
		contentResponse("Done"),
	}}

	manager, err := freshNewContextManager("system", "task", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}

	loop := NewAgentLoop(
		model,
		RoleProfile{Tools: []langtools.Tool{&loopGuardEchoTool{output: "ok"}}},
		nil,
		10,
		nil,
		nil,
		ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	// No SteerProvider set

	output, err := loop.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if output != "Done" {
		t.Fatalf("output = %q, want 'Done'", output)
	}
}
