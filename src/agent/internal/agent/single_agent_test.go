package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestSingleAgentProfileDoesNotBuildDelegatedRoles(t *testing.T) {
	profile := buildAgentProfile(
		AgentConfig{Instruction: "base", AdditionalPrompt: "extra"},
		ResolvedSkills{Names: []string{"ui"}, Instructions: []string{"[ui] inspect first"}},
		[]langtools.Tool{&stubTool{name: "screenshot", description: "Capture screen."}},
	)

	for _, want := range []string{"base", "extra", "[ui] inspect first", "You are the Aiden agent."} {
		if !strings.Contains(profile.SystemPrompt(), want) {
			t.Fatalf("agent prompt missing %q:\n%s", want, profile.SystemPrompt())
		}
	}
	for _, unexpected := range []string{"enter_plan_mode", "commit_plan", "cancel_plan", "planner role", "executor role", "verifier role"} {
		if strings.Contains(profile.SystemPrompt(), unexpected) {
			t.Fatalf("agent prompt should not contain old delegated role wording %q:\n%s", unexpected, profile.SystemPrompt())
		}
	}
}

func TestSingleAgentRuntimeUsesAgentRoleAndDirectTools(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{\"volume\":3}"}`, "Volume set to 3."),
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get or set audio playback volume.",
		output:      `{"volume":3}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use direct tools."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{"audio_volume": tool}},
		NewSkillIndex(),
	)

	var events []RunEvent
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "set volume to 3",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "Volume set to 3." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.tools) != 2 {
		t.Fatalf("expected tool call turn and final turn, got %d model calls", len(model.tools))
	}
	if !singleAgentLLMToolsContain(model.tools[0], "audio_volume") {
		t.Fatalf("agent did not receive audio_volume tool: %#v", model.tools[0])
	}
	for _, name := range []string{"enter_plan_mode", "commit_plan", "cancel_plan", "finish_step", "abort_step"} {
		if singleAgentLLMToolsContain(model.tools[0], name) {
			t.Fatalf("single-agent turn exposed delegated meta tool %q: %#v", name, model.tools[0])
		}
	}
	if prompt := messageText(model.messages[0]); strings.Contains(prompt, "Planner runtime context") || strings.Contains(prompt, "force_simple_loop") {
		t.Fatalf("agent prompt leaked old runtime context:\n%s", prompt)
	}
	for _, event := range events {
		if event.Type == "role_output" && event.Role != "agent" {
			t.Fatalf("role_output role = %q, want %q", event.Role, "agent")
		}
	}
}

func TestSingleAgentDoesNotRunDefaultFinalVerifierReview(t *testing.T) {
	model := &scriptedModel{responses: roleToolResponses("screenshot", `{"__arg1":"{}"}`, "screen checked")}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": &stubTool{
				name:        "screenshot",
				description: "Capture screen.",
				visual:      true,
				output:      `{"format":"jpeg","width":1,"height":1,"size":1}`,
			},
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "check screen"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "screen checked" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if model.callCount != 2 {
		t.Fatalf("model calls = %d, want tool turn plus final answer only", model.callCount)
	}
}

func singleAgentLLMToolsContain(tools []llms.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == name {
			return true
		}
	}
	return false
}
