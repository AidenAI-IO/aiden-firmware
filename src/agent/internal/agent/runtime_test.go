package agent

import (
	"context"
	"testing"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	fakellm "github.com/tmc/langchaingo/llms/fake"
)

type testModelResolver struct {
	model llms.Model
	calls int
}

func (r *testModelResolver) Get() (llms.Model, error) {
	r.calls++
	return r.model, nil
}

func (r *testModelResolver) CallOptions() []chains.ChainCallOption {
	return nil
}

func TestRuntimeRun(t *testing.T) {
	cfg := Config{
		Model:       ModelConfig{Provider: "fake"},
		Instruction: "Answer directly.",
	}

	resolver := &testModelResolver{
		model: fakellm.NewFakeLLM([]string{
			"Thought: I now know the final answer\nFinal Answer: completed",
		}),
	}

	runtime := NewRuntimeWithDeps(cfg, resolver, NewMemoryManager(), NewBuiltinToolSet(HIDConfig{}), NewSkillIndex())
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
