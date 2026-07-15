package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestSingleAgentProfileDoesNotBuildDelegatedRoles(t *testing.T) {
	index := NewSkillIndex()
	index.skills["ui"] = &SkillDefinition{Name: "ui", Description: "Inspect first"}
	profile := buildProfile(
		AgentConfig{Instruction: "base", AdditionalPrompt: "extra"},
		NewSkillManager(index),
		[]langtools.Tool{&stubTool{name: "screenshot", description: "Capture screen."}},
		agentRoleRules(),
	)

	for _, want := range []string{"base", "extra", "- ui: Inspect first", "You are the Aiden agent."} {
		if !strings.Contains(profile.SystemPrompt, want) {
			t.Fatalf("agent prompt missing %q:\n%s", want, profile.SystemPrompt)
		}
	}
	for _, unexpected := range []string{"enter_plan_mode", "commit_plan", "cancel_plan", "planner role", "executor role", "verifier role"} {
		if strings.Contains(profile.SystemPrompt, unexpected) {
			t.Fatalf("agent prompt should not contain old delegated role wording %q:\n%s", unexpected, profile.SystemPrompt)
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
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use direct tools."}),
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
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Use tools."}),
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

func TestSingleAgentUsesUIFallbackAfterDisconnectedOpenApp(t *testing.T) {
	model := &disconnectedOpenAppFallbackModel{}
	bridge := NewPhoneBridge(nil, nil)
	bridge.mu.Lock()
	bridge.platform = "ios"
	bridge.appState = "background"
	bridge.returnEntry = "dynamic_island"
	bridge.returnEntrySeen = true
	bridge.returnEntryOK = true
	bridge.mu.Unlock()
	screenshot := &stubTool{
		name:        "screenshot",
		description: "Capture the current phone screen.",
		visual:      true,
		output:      `{"format":"jpeg","width":1,"height":1,"size":0}`,
	}
	searchLaunch := &stubTool{
		name:        "search_launch_app",
		description: "Open an app through visible system search.",
		output:      `{"ok":true,"opened":true}`,
	}
	toolSet := &ToolSet{
		phoneBridge: bridge,
		tools: map[string]langtools.Tool{
			"bridge_open_app":       NewOpenAppTool(bridge, nil),
			"request_human_handoff": NewHumanHandoffTool(),
			"screenshot":            screenshot,
			"search_launch_app":     searchLaunch,
		},
	}
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Operate the connected phone."}),
		&testModelResolver{model: model},
		NewMemoryManager(""),
		toolSet,
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "打开小红书"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "已通过屏幕搜索继续打开小红书。" {
		t.Fatalf("Output = %q, want UI fallback completion", result.Output)
	}
	if model.callCount != 4 {
		t.Fatalf("model calls = %d, want open_app failure, screenshot, UI fallback, and completion", model.callCount)
	}
	if len(screenshot.inputs) != 1 || len(searchLaunch.inputs) != 1 {
		t.Fatalf("fallback calls: screenshot=%v search_launch_app=%v", screenshot.inputs, searchLaunch.inputs)
	}
}

type disconnectedOpenAppFallbackModel struct {
	callCount int
}

func (m *disconnectedOpenAppFallbackModel) GenerateContent(_ context.Context, messages []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	m.callCount++
	switch m.callCount {
	case 1:
		return toolCallResponse("call_open", "bridge_open_app", `{"app":"小红书"}`), nil
	case 2:
		if modelMessagesContain(messages, "call screenshot first") {
			return toolCallResponse("call_screen", "screenshot", `{}`), nil
		}
	case 3:
		if modelMessagesContainToolResult(messages, "screenshot") {
			return toolCallResponse("call_search", "search_launch_app", `{"app":"小红书","platform":"ios"}`), nil
		}
	case 4:
		return contentResponse("已通过屏幕搜索继续打开小红书。"), nil
	}
	return contentResponse("手机连接断开，无法继续。"), nil
}

func (m *disconnectedOpenAppFallbackModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	panic("unexpected Call invocation")
}

func modelMessagesContain(messages []llms.MessageContent, want string) bool {
	for _, message := range messages {
		for _, part := range message.Parts {
			switch typed := part.(type) {
			case llms.TextContent:
				if strings.Contains(typed.Text, want) {
					return true
				}
			case llms.ToolCallResponse:
				if strings.Contains(typed.Content, want) {
					return true
				}
			}
		}
	}
	return false
}

func modelMessagesContainToolResult(messages []llms.MessageContent, name string) bool {
	for _, message := range messages {
		for _, part := range message.Parts {
			if result, ok := part.(llms.ToolCallResponse); ok && result.Name == name {
				return true
			}
		}
	}
	return false
}

func singleAgentLLMToolsContain(tools []llms.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == name {
			return true
		}
	}
	return false
}
