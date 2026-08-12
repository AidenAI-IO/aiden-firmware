package agent

import (
	"aiden-agent/internal/agent/executor"
	"context"
	"net/http"
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
		executor.ScreenshotPruningConfig{}.WithDefaults(),
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
		executor.ScreenshotPruningConfig{}.WithDefaults(),
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

func TestAgentLoopBudgetBoundarySteerStartsFreshIterationBudget(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call-1", "touch_gesture", `{"type":"swipe_up"}`),
		contentResponse("Changed direction after the budget boundary."),
	}}
	manager, err := freshNewContextManager("system", "task", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}

	var polls int
	loop := NewAgentLoop(
		model,
		RoleProfile{Tools: []langtools.Tool{&loopGuardEchoTool{output: "ok"}}},
		nil,
		1,
		nil,
		nil,
		executor.ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	loop.SteerProvider = func(context.Context) (RunSteerMessage, bool) {
		polls++
		// One tool iteration polls before the model, before the tool, and after
		// the tool. The fourth poll is the max-iterations termination boundary.
		if polls != 4 {
			return RunSteerMessage{}, false
		}
		return RunSteerMessage{Content: "Change direction now."}, true
	}

	output, err := loop.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if output != "Changed direction after the budget boundary." {
		t.Fatalf("output = %q, want steer-adjusted response", output)
	}
	if model.callCount != 2 {
		t.Fatalf("model call count = %d, want 2", model.callCount)
	}
	if len(model.messages) < 2 || !runtimeModelCallContains(model.messages[1], "Change direction now.") {
		t.Fatalf("follow-up model call missing boundary steer: %#v", model.messages)
	}
}

func TestAgentLoopRetriesAfterCanceledSteerInterruptWithoutReplacementText(t *testing.T) {
	t.Parallel()

	model := &blockingFirstCallModel{
		firstCallStarted: make(chan struct{}),
		releaseFirstCall: make(chan struct{}),
		responses: []*llms.ContentResponse{
			contentResponse("unused"),
			contentResponse("Original task resumed."),
		},
	}
	manager, err := freshNewContextManager("system", "task", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	closedInterrupt := make(chan struct{})
	close(closedInterrupt)
	activeInterrupt := make(chan struct{})
	var interruptCalls int

	loop := NewAgentLoop(
		model,
		RoleProfile{},
		nil,
		1,
		nil,
		nil,
		executor.ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	loop.SteerInterrupt = func() <-chan struct{} {
		interruptCalls++
		if interruptCalls == 1 {
			return closedInterrupt
		}
		return activeInterrupt
	}

	output, err := loop.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if output != "Original task resumed." {
		t.Fatalf("output = %q, want original task to resume", output)
	}
	if model.callCount != 2 {
		t.Fatalf("model call count = %d, want 2", model.callCount)
	}
}

func TestAgentLoopRetriesAfterCanceledSteerInterruptDuringToolWithoutReplacementText(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call-1", "slow", `{"__arg1":"original action"}`),
		contentResponse("Original task resumed after canceled steer."),
	}}
	toolStarted := make(chan struct{})
	toolCanceled := make(chan struct{})
	tool := &blockingTool{
		name:        "slow",
		description: "Slow tool.",
		started:     toolStarted,
		canceled:    toolCanceled,
	}
	manager, err := freshNewContextManager("system", "task", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	interrupt := make(chan struct{})
	rearmedInterrupt := make(chan struct{})
	providerPolls := 0
	currentInterrupt := (<-chan struct{})(interrupt)

	loop := NewAgentLoop(
		model,
		RoleProfile{Tools: []langtools.Tool{tool}},
		nil,
		1,
		nil,
		nil,
		executor.ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	loop.SteerProvider = func(context.Context) (RunSteerMessage, bool) {
		providerPolls++
		if providerPolls == 3 {
			// The queued steer was canceled after its signal interrupted the tool.
			currentInterrupt = rearmedInterrupt
		}
		return RunSteerMessage{}, false
	}
	loop.SteerInterrupt = func() <-chan struct{} {
		return currentInterrupt
	}

	resultCh := make(chan struct {
		output string
		err    error
	}, 1)
	go func() {
		output, err := loop.Run(context.Background(), "task")
		resultCh <- struct {
			output string
			err    error
		}{output: output, err: err}
	}()

	select {
	case <-toolStarted:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	close(interrupt)
	select {
	case <-toolCanceled:
	case <-time.After(time.Second):
		t.Fatal("tool was not canceled by steer interrupt")
	}

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("Run() error = %v", result.err)
		}
		if result.output != "Original task resumed after canceled steer." {
			t.Fatalf("output = %q, want original task to resume", result.output)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not resume after canceled tool steer")
	}
	if model.callCount != 2 {
		t.Fatalf("model call count = %d, want 2", model.callCount)
	}
}

func TestAgentLoopDoesNotRestartBudgetForToolCancellationWithoutSteer(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{responses: []*llms.ContentResponse{
		toolCallResponse("call-1", "slow", `{"__arg1":"original action"}`),
		contentResponse("unexpected second model turn"),
	}}
	tool := &stubTool{name: "slow", description: "Slow tool."}
	bridge := NewEnvironmentBridgeClient("http://bridge.local")
	bridge.httpClient = &http.Client{Transport: bridgeCancelRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.Canceled
	})}
	manager, err := freshNewContextManager("system", "task", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	interrupt := make(chan struct{})

	loop := NewAgentLoop(
		model,
		RoleProfile{Tools: []langtools.Tool{tool}},
		nil,
		1,
		nil,
		nil,
		executor.ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	loop.EnvironmentBridge = bridge
	loop.EnvironmentBridgeTools = []string{"slow"}
	loop.SteerInterrupt = func() <-chan struct{} {
		return interrupt
	}

	output, err := loop.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if model.callCount != 1 {
		t.Fatalf("model call count = %d, want 1 without a steer restart", model.callCount)
	}
	if output == "unexpected second model turn" {
		t.Fatal("tool-local cancellation incorrectly restarted the iteration budget")
	}
}

func TestAgentLoopPendingSteerBeforeFirstModelGetsFreshIterationBudget(t *testing.T) {
	t.Parallel()

	model := &scriptedModel{responses: roleDirectResponses("Changed direction immediately.")}
	manager, err := freshNewContextManager("system", "task", nil, t.TempDir())
	if err != nil {
		t.Fatalf("freshNewContextManager() error = %v", err)
	}
	var delivered bool
	loop := NewAgentLoop(
		model,
		RoleProfile{},
		nil,
		1,
		nil,
		nil,
		executor.ScreenshotPruningConfig{}.WithDefaults(),
		manager,
	)
	loop.SteerProvider = func(context.Context) (RunSteerMessage, bool) {
		if delivered {
			return RunSteerMessage{}, false
		}
		delivered = true
		return RunSteerMessage{Content: "Use the new instruction."}, true
	}

	output, err := loop.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if output != "Changed direction immediately." {
		t.Fatalf("output = %q, want steer-adjusted response", output)
	}
	if model.callCount != 1 {
		t.Fatalf("model call count = %d, want 1", model.callCount)
	}
}
