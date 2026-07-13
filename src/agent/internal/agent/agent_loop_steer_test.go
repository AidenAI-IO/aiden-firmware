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
		// Each completed tool iteration checks before the model, before the tool,
		// and after the tool. Return steer only from the termination boundary.
		if checkCount != 10 {
			return RunSteerMessage{}, false
		}
		return RunSteerMessage{
			Content:   "Stop the current task.",
			Timestamp: time.Now(),
		}, true
	}

	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call-1", "touch_gesture", `{"type":"swipe_up"}`),
		toolCallResponse("call-2", "touch_gesture", `{"type":"swipe_up"}`),
		toolCallResponse("call-3", "touch_gesture", `{"type":"swipe_up"}`),
		contentResponse("Stopped as requested."),
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

	// The termination boundary should inject the steer and let the model decide.
	if output != "Stopped as requested." {
		t.Fatalf("output = %q, want steer-adjusted response", output)
	}
	if strings.Contains(output, "not making measurable progress") {
		t.Fatalf("output = %q, should continue from steer instead of returning loop guard message", output)
	}
	if len(model.messages) < 4 || !runtimeModelCallContains(model.messages[3], "Stop the current task.") {
		t.Fatalf("follow-up model call missing steer: %#v", model.messages)
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
