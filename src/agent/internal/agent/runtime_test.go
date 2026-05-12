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

func TestRuntimeRunWithToolAndMemory(t *testing.T) {
	skillIndex := NewSkillIndex()
	skillIndex.skills["hid_skill"] = &SkillDefinition{
		Name:         "hid_skill",
		Description:  "HID skill for testing",
		Instructions: "Use keyboard_tap tool when helpful.",
		AllowedTools: []string{"keyboard_tap"},
	}

	cfg := Config{
		DefaultAgent: "main",
		Model:        ModelConfig{Provider: "fake"},
		Agents: map[string]AgentConfig{
			"main": {
				Instruction:   "Use the tool when helpful.",
				DefaultSkills: []string{"hid_skill"},
				Memory:        MemoryConfig{Type: "buffer"},
			},
		},
	}

	resolver := &testModelResolver{
		model: fakellm.NewFakeLLM([]string{
			"Thought: I now know the final answer\nFinal Answer: completed",
		}),
	}

	runtime := NewRuntimeWithDeps(cfg, resolver, NewMemoryManager(), NewBuiltinToolSet(HIDConfig{}), skillIndex)
	result, err := runtime.Run(context.Background(), RunRequest{Input: "do something"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "completed" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(result.Memory) != 2 {
		t.Fatalf("expected 2 memory entries, got %d", len(result.Memory))
	}
	if result.Memory[0].Content != "do something" {
		t.Fatalf("unexpected user memory content: %q", result.Memory[0].Content)
	}
	if result.Memory[1].Content != "completed" {
		t.Fatalf("unexpected assistant memory content: %q", result.Memory[1].Content)
	}
}

func TestRuntimeRunWithSkillAndChildAgent(t *testing.T) {
	skillIndex := NewSkillIndex()
	skillIndex.skills["planner"] = &SkillDefinition{
		Name:            "planner",
		Description:     "Task decomposition and delegation.",
		Instructions:    "Delegate research work.",
		PreferredModel:  "planner",
		AllowedTools:    []string{DelegateToolName("researcher")},
		AllowedChildren: []string{"researcher"},
	}

	cfg := Config{
		DefaultAgent: "coordinator",
		Model:        ModelConfig{Provider: "fake"},
		Agents: map[string]AgentConfig{
			"coordinator": {
				Instruction:   "Delegate focused work to the child agent.",
				DefaultSkills: []string{"planner"},
				Children:      []string{"researcher"},
				Memory:        MemoryConfig{Type: "buffer"},
			},
			"researcher": {
				Description: "Focused child agent.",
				Instruction: "Finish the delegated task directly.",
				Memory:      MemoryConfig{Type: "buffer"},
			},
		},
	}

	resolver := &testModelResolver{
		model: fakellm.NewFakeLLM([]string{
			"Thought: I should delegate\nAction: delegate_researcher\nAction Input: summarize the subtask",
			"Thought: I now know the final answer\nFinal Answer: child result",
			"Thought: I now know the final answer\nFinal Answer: synthesized from child",
		}),
	}

	runtime := NewRuntimeWithDeps(cfg, resolver, NewMemoryManager(), NewBuiltinToolSet(HIDConfig{}), skillIndex)
	result, err := runtime.Run(context.Background(), RunRequest{Input: "handle the task"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "synthesized from child" {
		t.Fatalf("unexpected output: %q", result.Output)
	}

	childMemory, err := runtime.memories.Snapshot(context.Background(), "researcher")
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(childMemory) != 2 {
		t.Fatalf("expected child memory to contain one exchange, got %d", len(childMemory))
	}
}
