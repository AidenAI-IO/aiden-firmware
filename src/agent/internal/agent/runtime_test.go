package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
	fakellm "github.com/tmc/langchaingo/llms/fake"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

func TestEffectiveMaxIterationsDefaultsAndUnlimited(t *testing.T) {
	if got := effectiveMaxIterations(-1); got != math.MaxInt {
		t.Fatalf("effectiveMaxIterations(-1) = %d, want math.MaxInt", got)
	}
	if got := effectiveMaxIterations(0); got != math.MaxInt {
		t.Fatalf("effectiveMaxIterations(0) = %d, want math.MaxInt", got)
	}
	if got := effectiveMaxIterations(10); got != 10 {
		t.Fatalf("effectiveMaxIterations(10) = %d, want 10", got)
	}
}

type testModelResolver struct {
	model llms.Model
	calls int
	spec  ModelSpec
}

func (r *testModelResolver) Get() (llms.Model, error) {
	r.calls++
	return r.model, nil
}

func (r *testModelResolver) CallOptions() []chains.ChainCallOption {
	return nil
}

func (r *testModelResolver) Spec() ModelSpec {
	return r.spec
}

func TestRuntimeRun(t *testing.T) {
	cfg := Config{
		Model:       ModelConfig{Provider: "fake"},
		Instruction: "Answer directly.",
	}

	resolver := &testModelResolver{
		model: fakellm.NewFakeLLM([]string{
			"completed",
		}),
	}

	runtime := NewRuntimeWithDeps(cfg, resolver, NewMemoryManager(""), NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}), NewSkillIndex())
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

func TestRuntimeRunIncludesAvailableSkillCatalog(t *testing.T) {
	index := NewSkillIndex()
	index.skills["planner"] = &SkillDefinition{
		Name:         "planner",
		Description:  "Plan before acting",
		Instructions: "Make a plan.",
	}
	model := &scriptedModel{responses: []*llms.ContentResponse{{Choices: []*llms.ContentChoice{{Content: "ok"}}}}}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		index,
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !runtimeModelCallContains(model.messages[0], "Available skills:") {
		t.Fatalf("run missing available skills heading")
	}
	if !runtimeModelCallContains(model.messages[0], "- planner: Plan before acting") {
		t.Fatalf("run missing planner skill catalog entry")
	}
	if runtimeModelCallContains(model.messages[0], "[planner] Make a plan.") {
		t.Fatalf("inactive skill instructions should not be injected")
	}
}

func TestRuntimeRunOmitsArchivedSkillsFromAvailableCatalog(t *testing.T) {
	configDir := t.TempDir()
	skillsDir := filepath.Join(configDir, "skills")
	writeSKILL(t, skillsDir, "alpha", testSkillA)
	writeSKILL(t, skillsDir, "beta", testSkillB)
	saveSkillUsage(filepath.Join(configDir, "skill-state", "usage.json"), map[string]SkillUsageEntry{
		"beta": {State: SkillUsageStateArchived},
	})
	index, err := LoadSkillsFromDirs([]string{skillsDir})
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedModel{responses: []*llms.ContentResponse{{Choices: []*llms.ContentChoice{{Content: "ok"}}}}}
	runtime := NewRuntimeWithDeps(
		Config{ConfigDir: configDir, SkillsDirs: []string{skillsDir}, Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		index,
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !runtimeModelCallContains(model.messages[0], "- alpha: Alpha skill") {
		t.Fatalf("run missing active alpha skill catalog entry")
	}
	if runtimeModelCallContains(model.messages[0], "- beta: Beta skill") {
		t.Fatalf("run included archived beta skill catalog entry")
	}
}

func TestToolDescriptorsIncludeSkillToolMetadata(t *testing.T) {
	configDir := t.TempDir()
	tools := &ToolSet{tools: map[string]langtools.Tool{}}
	tools.RegisterSkillTools(filepath.Join(configDir, "skills"), filepath.Join(configDir, "skill-state", ".bundled_manifest.json"))
	runtime := NewRuntimeWithDeps(Config{}, nil, nil, tools, NewSkillIndex())

	for _, name := range []string{"skill_list", "skill_read", "skill_mark_used"} {
		desc, ok := runtime.ToolDescriptorByName(name)
		if !ok {
			t.Fatalf("expected descriptor for %s", name)
		}
		if desc.Category != "skills" {
			t.Fatalf("%s category = %q, want skills", name, desc.Category)
		}
		if desc.InputMode != toolInputModeJSON {
			t.Fatalf("%s input mode = %q, want json", name, desc.InputMode)
		}
		if strings.TrimSpace(desc.ExampleInput) == "" {
			t.Fatalf("%s missing example input", name)
		}
	}
}

func TestParseChunkStructuredSummaryJSONRejectsProseWrappedJSON(t *testing.T) {
	_, err := parseChunkStructuredSummaryJSON(`Here is the JSON:
{"summary":"summary","decisions":["decision"]}`)
	if err == nil {
		t.Fatalf("expected prose-wrapped structured summary JSON to be rejected")
	}
}

func TestSkillCatalogSummaryLimitsEntriesAndDescriptionLength(t *testing.T) {
	index := NewSkillIndex()
	longDesc := strings.Repeat("长", maxSkillCatalogDescriptionRunes+10)
	for i := 0; i < maxSkillCatalogEntries+2; i++ {
		name := fmt.Sprintf("skill-%02d", i)
		index.skills[name] = &SkillDefinition{Name: name, Description: longDesc}
	}
	manager := NewSkillManager(index)
	catalog := manager.CatalogSummary()
	if strings.Count(catalog, "- skill-") != maxSkillCatalogEntries {
		t.Fatalf("expected %d catalog entries, got catalog:\n%s", maxSkillCatalogEntries, catalog)
	}
	if !strings.Contains(catalog, "more skills hidden. Use skill_list to search") {
		t.Fatalf("expected hidden skills hint, got:\n%s", catalog)
	}
	if strings.Contains(catalog, strings.Repeat("长", maxSkillCatalogDescriptionRunes+1)) {
		t.Fatalf("expected long descriptions to be truncated")
	}
}

func TestResolveToolsKeepsSkillMetaToolsWhenRestricted(t *testing.T) {
	tools := &ToolSet{tools: map[string]langtools.Tool{
		"screenshot":      &stubTool{name: "screenshot", description: "Take screenshot."},
		"skill_list":      NewSkillListTool(t.TempDir()),
		"skill_read":      NewSkillReadTool(t.TempDir()),
		"skill_mark_used": NewSkillMarkUsedTool(t.TempDir(), ""),
		"skill_manage":    NewSkillManageTool(t.TempDir(), ""),
		"recall_memory":   &stubTool{name: "recall_memory", description: "Recall memory."},
	}}
	runtime := NewRuntimeWithDeps(Config{}, nil, nil, tools, NewSkillIndex())
	resolved := ResolvedSkills{
		AllowedTools:       map[string]struct{}{"screenshot": {}},
		HasToolRestriction: true,
	}
	resolvedTools := runtime.resolveTools(resolved)
	names := map[string]bool{}
	for _, tool := range resolvedTools {
		names[tool.Name()] = true
	}
	for _, name := range []string{"screenshot", "skill_list", "skill_read", "skill_manage", "skill_mark_used"} {
		if !names[name] {
			t.Fatalf("expected %s to be available under tool restrictions; got %#v", name, names)
		}
	}
	for _, name := range []string{"recall_memory"} {
		if names[name] {
			t.Fatalf("did not expect %s without explicit allowed_tools entry; got %#v", name, names)
		}
	}
}

func TestRuntimeRunReloadsSkillsWhenMarkedDirty(t *testing.T) {
	configDir := t.TempDir()
	skillsDir := filepath.Join(configDir, "skills")
	v1 := "---\nname: alpha\ndescription: Alpha\n---\n\nUse alpha v1.\n"
	v2 := "---\nname: alpha\ndescription: Alpha\n---\n\nUse alpha v2.\n"
	writeSKILL(t, skillsDir, "alpha", v1)
	index, err := LoadSkillsFromDirs([]string{skillsDir})
	if err != nil {
		t.Fatal(err)
	}

	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{Choices: []*llms.ContentChoice{{Content: "first"}}},
			{Choices: []*llms.ContentChoice{{Content: "second"}}},
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			SkillsDirs:    []string{skillsDir},
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		index,
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello", Skills: []string{"alpha"}}); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if !runtimeModelCallContains(model.messages[0], "Use alpha v1.") {
		t.Fatalf("first run missing v1 skill instructions")
	}

	writeSKILL(t, skillsDir, "alpha", v2)
	runtime.MarkSkillsDirty()

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello again", Skills: []string{"alpha"}}); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if !runtimeModelCallContains(model.messages[1], "Use alpha v2.") {
		t.Fatalf("second run missing reloaded v2 skill instructions")
	}
	if runtimeModelCallContains(model.messages[1], "Use alpha v1.") {
		t.Fatalf("second run still contains stale v1 skill instructions")
	}
}

func TestRuntimeRunSnapshotUnaffectedByConcurrentReload(t *testing.T) {
	configDir := t.TempDir()
	skillsDir := filepath.Join(configDir, "skills")
	v1 := "---\nname: alpha\ndescription: Alpha\n---\n\nUse alpha v1.\n"
	v2 := "---\nname: alpha\ndescription: Alpha\n---\n\nUse alpha v2.\n"
	writeSKILL(t, skillsDir, "alpha", v1)
	index, err := LoadSkillsFromDirs([]string{skillsDir})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntimeWithDeps(
		Config{ConfigDir: configDir, SkillsDirs: []string{skillsDir}},
		&testModelResolver{model: fakellm.NewFakeLLM([]string{"unused"})},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		index,
	)

	if err := runtime.reloadSkillsIfDirty(); err != nil {
		t.Fatal(err)
	}
	runSkills := runtime.skills.Snapshot()
	if err := runSkills.Activate(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}

	writeSKILL(t, skillsDir, "alpha", v2)
	runtime.MarkSkillsDirty()
	if err := runtime.reloadSkillsIfDirty(); err != nil {
		t.Fatal(err)
	}

	resolved, err := runSkills.Resolve([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resolved.CombinedInstructions(), "Use alpha v1.") {
		t.Fatalf("in-progress run snapshot lost v1 instructions: %s", resolved.CombinedInstructions())
	}
	if strings.Contains(resolved.CombinedInstructions(), "Use alpha v2.") {
		t.Fatalf("in-progress run snapshot saw reloaded v2 instructions: %s", resolved.CombinedInstructions())
	}
}

func runtimeModelCallContains(messages []llms.MessageContent, want string) bool {
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if text, ok := part.(llms.TextContent); ok && strings.Contains(text.Text, want) {
				return true
			}
		}
	}
	return false
}

type scriptedModel struct {
	responses    []*llms.ContentResponse
	callCount    int
	sawStreaming []bool
	messages     [][]llms.MessageContent
	tools        [][]llms.Tool
}

func (m *scriptedModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	var callOptions llms.CallOptions
	for _, option := range options {
		option(&callOptions)
	}
	m.sawStreaming = append(m.sawStreaming, callOptions.StreamingFunc != nil)
	m.messages = append(m.messages, messages)
	m.tools = append(m.tools, callOptions.Tools)

	if callOptions.StreamingFunc != nil && m.callCount < len(m.responses) {
		content := m.responses[m.callCount].Choices[0].Content
		if content != "" {
			if err := callOptions.StreamingFunc(ctx, []byte("chunk:"+content)); err != nil {
				return nil, err
			}
		}
	}

	if m.callCount >= len(m.responses) {
		t := &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: ""}}}
		return t, nil
	}

	response := m.responses[m.callCount]
	m.callCount++
	return response, nil
}

func (m *scriptedModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	panic("unexpected Call invocation")
}

type stubTool struct {
	name        string
	description string
	output      string
	err         error
	visual      bool
	inputs      []string
}

func (t *stubTool) Name() string { return t.name }

func (t *stubTool) Description() string { return t.description }

func (t *stubTool) ReturnsVisualObservation() bool { return t.visual }

func (t *stubTool) Call(_ context.Context, input string) (string, error) {
	t.inputs = append(t.inputs, input)
	if t.err != nil {
		return "", t.err
	}
	return t.output, nil
}

func TestBuildLLMStructuredSummarizeFnParsesStrictJSON(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{{Choices: []*llms.ContentChoice{{Content: `{
		"summary":"讨论 MiniCPM 局域网 VLM",
		"user_goals":["测试局域网 VLM"],
		"confirmed_facts":["主模型负责语音链路"],
		"decisions":["VLM 使用 model_vision"],
		"proposals":["screen_memory_summarizer 优先读取 model_vision"],
		"open_tasks":["实现配置解析"],
		"risks_or_pitfalls":["不要替换主模型"],
		"memory_candidates":["语音模型和 VLM 分离配置"]
	}`}}}}}
	fn := buildLLMStructuredSummarizeFn(&testModelResolver{model: model})
	got := fn(context.Background(), []SessionEvent{{Role: "user", Content: "测试 MiniCPM"}})
	if got.Summary != "讨论 MiniCPM 局域网 VLM" {
		t.Fatalf("summary = %q", got.Summary)
	}
	if got.Decisions[0] != "VLM 使用 model_vision" || got.OpenTasks[0] != "实现配置解析" {
		t.Fatalf("unexpected structured summary: %#v", got)
	}
}

func TestBuildLLMStructuredSummarizeFnFallsBackOnInvalidJSON(t *testing.T) {
	model := &scriptedModel{responses: []*llms.ContentResponse{{Choices: []*llms.ContentChoice{{Content: `not json`}}}}}
	fn := buildLLMStructuredSummarizeFn(&testModelResolver{model: model})
	got := fn(context.Background(), []SessionEvent{{Role: "user", Content: "hello"}})
	if !got.Empty() {
		t.Fatalf("expected empty structured summary on invalid JSON, got %#v", got)
	}
}

func TestRuntimeRunOpenRouterUsesToolsWithoutStreaming(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "audio_volume",
							Arguments: `{"__arg1":"{}"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "The current audio volume is 42.",
				}},
			},
		},
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use tools when external state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "当前音量是多少？"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "The current audio volume is 42." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "{}" {
		t.Fatalf("unexpected tool inputs: %#v", tool.inputs)
	}
	if len(model.sawStreaming) != 2 || model.sawStreaming[0] || model.sawStreaming[1] {
		t.Fatalf("expected non-streaming tool calls, got %#v", model.sawStreaming)
	}
}

func TestRuntimeRunFakeProviderUsesFunctionAgentToolCalls(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "audio_volume",
							Arguments: `{"__arg1":"{}"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "The current audio volume is 42.",
				}},
			},
		},
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when external state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "当前音量是多少？"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "The current audio volume is 42." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "{}" {
		t.Fatalf("unexpected tool inputs: %#v", tool.inputs)
	}
}

func TestRuntimeRunExecutesOnlyFirstToolCallPerIteration(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{
						{
							ID:   "call_1",
							Type: "function",
							FunctionCall: &llms.FunctionCall{
								Name:      "slow_a",
								Arguments: `{"__arg1":"{}"}`,
							},
						},
						{
							ID:   "call_2",
							Type: "function",
							FunctionCall: &llms.FunctionCall{
								Name:      "slow_b",
								Arguments: `{"__arg1":"{}"}`,
							},
						},
					},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "done",
				}},
			},
		},
	}
	toolA := &stubTool{name: "slow_a", description: "First tool.", output: `{"ok":true}`}
	toolB := &stubTool{name: "slow_b", description: "Second tool.", output: `{"ok":true}`}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"slow_a": toolA,
			"slow_b": toolB,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "run both"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(toolA.inputs) != 1 || toolA.inputs[0] != "{}" {
		t.Fatalf("first tool inputs = %#v, want one empty JSON call", toolA.inputs)
	}
	if len(toolB.inputs) != 0 {
		t.Fatalf("second tool inputs = %#v, want no calls", toolB.inputs)
	}
	if len(model.messages) < 2 {
		t.Fatalf("model calls = %d, want at least 2", len(model.messages))
	}
	var toolCallNames []string
	for _, msg := range model.messages[1] {
		for _, part := range msg.Parts {
			toolCall, ok := part.(llms.ToolCall)
			if !ok || toolCall.FunctionCall == nil {
				continue
			}
			toolCallNames = append(toolCallNames, toolCall.FunctionCall.Name)
		}
	}
	if len(toolCallNames) != 1 || toolCallNames[0] != "slow_a" {
		t.Fatalf("scratchpad tool calls = %#v, want only slow_a", toolCallNames)
	}
}

func TestRuntimeRunFeedsToolErrorsBackToModel(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "screenshot",
							Arguments: `{"__arg1":"{}"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "屏幕暂时获取失败，frame service 正在恢复。",
				}},
			},
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": &stubTool{
				name:        "screenshot",
				description: "Capture a screenshot from the connected display.",
				visual:      true,
				err:         errors.New("frame service: SERVICE_RECOVERING"),
			},
		}},
		NewSkillIndex(),
	)

	var events []RunEvent
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "看看屏幕",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "屏幕暂时获取失败，frame service 正在恢复。" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.messages) < 2 {
		t.Fatalf("expected second model call with tool observation, got %d calls", len(model.messages))
	}
	var toolObservation string
	for _, msg := range model.messages[1] {
		if msg.Role != llms.ChatMessageTypeTool {
			continue
		}
		if len(msg.Parts) == 1 {
			if part, ok := msg.Parts[0].(llms.ToolCallResponse); ok {
				toolObservation = part.Content
			}
		}
	}
	if !strings.Contains(toolObservation, "error: screenshot failed: frame service: SERVICE_RECOVERING") {
		t.Fatalf("unexpected tool observation: %q", toolObservation)
	}
	if len(events) < 2 || events[1].Type != "tool_result" || !events[1].IsError {
		t.Fatalf("expected error tool_result event, got %#v", events)
	}
}

func TestRuntimeAllowsNearRepeatedMouseClick(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "mouse_click",
							Arguments: `{"x":"0.5","y":"0.08","coord_space":"normalized"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_2",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "mouse_click",
							Arguments: `{"x":0.5,"y":0.12,"coord_space":"normalized"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "我会换一个方式继续。",
				}},
			},
		},
	}
	tool := &stubTool{
		name:        "mouse_click",
		description: "Move mouse to a position and click.",
		output:      "ok",
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"mouse_click": tool,
		}},
		NewSkillIndex(),
	)

	var events []RunEvent
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "tap the field",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "我会换一个方式继续。" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(tool.inputs) != 2 {
		t.Fatalf("expected repeated click attempts to reach the tool, got inputs %#v", tool.inputs)
	}
	if !strings.Contains(tool.inputs[0], `"x":"0.5"`) {
		t.Fatalf("first click should preserve model input, got %q", tool.inputs[0])
	}
	if !strings.Contains(tool.inputs[1], `"x":0.5`) {
		t.Fatalf("second click should reach the tool, got %q", tool.inputs[1])
	}

	var resultCount int
	for _, event := range events {
		if event.Type == "tool_result" && event.ToolName == "mouse_click" {
			resultCount++
			if event.IsError {
				t.Fatalf("repeated click should not be marked as an error: %#v", event)
			}
		}
	}
	if resultCount != 2 {
		t.Fatalf("expected two mouse_click result events, got %#v", events)
	}
}

func TestRuntimeAllowsRepeatedKeyboardText(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "keyboard_text",
							Arguments: `{"text":"yuanshen"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_2",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "keyboard_text",
							Arguments: `{"text":"yuanshen"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "我不会重复输入。",
				}},
			},
		},
	}
	tool := &stubTool{
		name:        "keyboard_text",
		description: "Type text.",
		output:      "ok",
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"keyboard_text": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "type twice"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "我不会重复输入。" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(tool.inputs) != 2 {
		t.Fatalf("expected repeated keyboard_text attempts to reach the tool, got inputs %#v", tool.inputs)
	}
}

func TestRuntimeRunReportsEnterSleepToolRequest(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "enter_sleep",
							Arguments: `{"__arg1":"{\"reason\":\"user asked\"}"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "I will wait for the next wakeup.",
				}},
			},
		},
	}
	controller := NewSleepController()
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when external state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"enter_sleep": NewEnterSleepTool(controller),
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "go to sleep"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.SleepRequested || result.SleepReason != "user asked" {
		t.Fatalf("sleep request = %v %q, want true user asked", result.SleepRequested, result.SleepReason)
	}
	if result.Output != "I will wait for the next wakeup." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestRuntimeRunEmitsToolDescriptionEventAndStripsToolInput(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "audio_volume",
							Arguments: `{"__arg1":"{}","description":"我先读取当前音量。"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "The current audio volume is 42.",
				}},
			},
		},
	}
	tool := &stubTool{
		name:        "audio_volume",
		description: "Get the current audio playback volume.",
		output:      `{"volume":42}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use tools when external state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"audio_volume": tool,
		}},
		NewSkillIndex(),
	)

	var events []RunEvent
	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "当前音量是多少？",
		EventHandler: func(event RunEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "The current audio volume is 42." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(tool.inputs) != 1 || tool.inputs[0] != "{}" {
		t.Fatalf("unexpected tool inputs: %#v", tool.inputs)
	}
	if len(events) < 1 || events[0].Type != "tool_call" {
		t.Fatalf("expected first event to be tool_call, got %#v", events)
	}
	if events[0].Description != "我先读取当前音量。" || events[0].Content != events[0].Description {
		t.Fatalf("unexpected tool description event: %#v", events[0])
	}
	if events[0].ToolInput != "{}" {
		t.Fatalf("tool_call event input = %q, want stripped input", events[0].ToolInput)
	}
}

func TestRuntimeCallbackRemovesPendingActionWithNormalizedToolInput(t *testing.T) {
	handler := &runtimeCallbackHandler{}
	handler.pushPendingAction(schema.AgentAction{
		Tool:      "audio_volume",
		ToolInput: "{}\nObservation:",
	})

	handler.removePendingAction("AUDIO_VOLUME", "{}")

	if action, ok := handler.popPendingAction(); ok {
		t.Fatalf("pending action was not removed: %#v", action)
	}
}

func TestRuntimeRunOpenRouterStreamsOnlyWhenRequested(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					Content: "completed",
				}},
			},
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Answer directly.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	var stream bytes.Buffer
	result, err := runtime.Run(context.Background(), RunRequest{
		Input:        "hello",
		StreamWriter: &stream,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Output != "completed" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.sawStreaming) != 1 || !model.sawStreaming[0] {
		t.Fatalf("expected streaming call, got %#v", model.sawStreaming)
	}
	if stream.String() != "chunk:completed" {
		t.Fatalf("unexpected stream output: %q", stream.String())
	}
}

func TestRuntimeRunScreenshotAddsBinaryImageObservation(t *testing.T) {
	jpegBytes := []byte("fake-jpeg-binary")
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "screenshot",
							Arguments: `{"__arg1":"{}"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "The screenshot shows a UI.",
				}},
			},
		},
	}
	tool := &stubTool{
		name:        "screenshot",
		description: "Capture a screenshot from the connected display.",
		visual:      true,
		output: `{"width":800,"height":600,"format":"jpeg","size":16,"data":"` +
			base64.StdEncoding.EncodeToString(jpegBytes) + `"}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use tools when visual state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "屏幕上有什么？"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "The screenshot shows a UI." {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.messages) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(model.messages))
	}

	secondCall := model.messages[1]
	var foundToolResponse bool
	var foundImageURL bool

	for _, msg := range secondCall {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.ToolCallResponse:
				if p.ToolCallID == "call_1" {
					foundToolResponse = true
					if p.Content == tool.output {
						t.Fatalf("expected screenshot tool response to be summarized, got raw payload")
					}
				}
			case llms.ImageURLContent:
				foundImageURL = true
				expectedPrefix := "data:image/jpeg;base64,"
				if !strings.HasPrefix(p.URL, expectedPrefix) {
					t.Fatalf("unexpected image URL prefix: %q", p.URL)
				}
				if p.URL != expectedPrefix+base64.StdEncoding.EncodeToString(jpegBytes) {
					t.Fatalf("unexpected image URL payload: %q", p.URL)
				}
			}
		}
	}

	if !foundToolResponse {
		t.Fatalf("expected screenshot tool response in second model call")
	}
	if !foundImageURL {
		t.Fatalf("expected screenshot image URL in second model call")
	}
}

func TestRuntimeRunScreenshotImageSurvivesCallbackToolWrapping(t *testing.T) {
	jpegBytes := []byte("fake-jpeg-binary")
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "screenshot",
							Arguments: `{"__arg1":"{}"}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "The screenshot shows a UI.",
				}},
			},
		},
	}
	tool := &stubTool{
		name:        "screenshot",
		description: "Capture a screenshot from the connected display.",
		visual:      true,
		output: `{"width":800,"height":600,"format":"jpeg","size":16,"data":"` +
			base64.StdEncoding.EncodeToString(jpegBytes) + `"}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use tools when visual state is requested.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"screenshot": tool,
		}},
		NewSkillIndex(),
	)

	var streamBuf bytes.Buffer
	if _, err := runtime.Run(context.Background(), RunRequest{
		Input:        "屏幕上有什么？",
		StreamWriter: &streamBuf,
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(model.messages) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(model.messages))
	}

	var foundToolResponse, foundImageURL bool
	for _, msg := range model.messages[1] {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.ToolCallResponse:
				if p.ToolCallID == "call_1" {
					foundToolResponse = true
					if p.Content == tool.output {
						t.Fatalf("expected screenshot tool response to be summarized when wrapped by callbackTool, got raw payload")
					}
				}
			case llms.ImageURLContent:
				foundImageURL = true
				expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes)
				if p.URL != expected {
					t.Fatalf("unexpected image URL: %q", p.URL)
				}
			}
		}
	}
	if !foundToolResponse {
		t.Fatalf("expected screenshot tool response when wrapped by callbackTool")
	}
	if !foundImageURL {
		t.Fatalf("expected screenshot image URL when wrapped by callbackTool")
	}
}

func TestRuntimeRunKeyboardToolAddsPostActionImageObservation(t *testing.T) {
	jpegBytes := []byte("keyboard-post-action-jpeg")
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					ToolCalls: []llms.ToolCall{{
						ID:   "call_1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      "keyboard_tap",
							Arguments: `{"keys":["enter"]}`,
						},
					}},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "The keyboard action updated the UI.",
				}},
			},
		},
	}
	tool := &stubTool{
		name:        "keyboard_tap",
		description: "Press and release keyboard keys.",
		visual:      true,
		output: `{"action_output":"ok","width":800,"height":600,"format":"jpeg","size":25,"data":"` +
			base64.StdEncoding.EncodeToString(jpegBytes) + `"}`,
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use input tools when needed.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{
			"keyboard_tap": tool,
		}},
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "press enter"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "The keyboard action updated the UI." {
		t.Fatalf("unexpected output: %q", result.Output)
	}

	var foundToolResponse, foundImageURL bool
	for _, msg := range model.messages[1] {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llms.ToolCallResponse:
				if p.ToolCallID == "call_1" {
					foundToolResponse = true
					if p.Content == tool.output {
						t.Fatalf("expected keyboard tool response to be summarized, got raw screenshot payload")
					}
					if !strings.Contains(p.Content, `keyboard_tap completed with output "ok"`) {
						t.Fatalf("unexpected keyboard tool response summary: %q", p.Content)
					}
				}
			case llms.ImageURLContent:
				foundImageURL = true
				expected := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegBytes)
				if p.URL != expected {
					t.Fatalf("unexpected image URL: %q", p.URL)
				}
			}
		}
	}
	if !foundToolResponse {
		t.Fatalf("expected keyboard tool response in second model call")
	}
	if !foundImageURL {
		t.Fatalf("expected keyboard post-action screenshot image URL in second model call")
	}
}

func TestRuntimeCallbackHandlerCapturesUsageMetrics(t *testing.T) {
	metrics := &RunMetrics{}
	handler := &runtimeCallbackHandler{metrics: metrics}

	handler.HandleLLMGenerateContentEnd(context.Background(), &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			GenerationInfo: map[string]any{
				"prompt_tokens":     12,
				"completion_tokens": 34,
				"total_tokens":      46,
			},
		}},
	})

	if metrics.PromptTokens != 12 || metrics.CompletionTokens != 34 || metrics.TotalTokens != 46 {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
}

func TestRuntimeRunCapturesUsageMetricsFromDirectModelCall(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{{
			Choices: []*llms.ContentChoice{{
				Content: "completed",
				GenerationInfo: map[string]any{
					"prompt_tokens":     600,
					"completion_tokens": 40,
					"total_tokens":      640,
				},
			}},
		}},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Metrics == nil || result.Metrics.PromptTokens != 600 || result.Metrics.CompletionTokens != 40 || result.Metrics.TotalTokens != 640 {
		t.Fatalf("unexpected metrics: %#v", result.Metrics)
	}
}

func TestRuntimeRunResetsPromptTokensWhenUsageUnavailable(t *testing.T) {
	manager := NewMemoryManager("")
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					Content: "with usage",
					GenerationInfo: map[string]any{
						"prompt_tokens": 600,
					},
				}},
			},
			{
				Choices: []*llms.ContentChoice{{
					Content: "without usage",
				}},
			},
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly."},
		&testModelResolver{model: model},
		manager,
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "first"}); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if got := manager.LastPromptTokens(); got != 600 {
		t.Fatalf("expected first run prompt tokens 600, got %d", got)
	}

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "second"}); err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if got := manager.LastPromptTokens(); got != 0 {
		t.Fatalf("expected missing usage to reset prompt tokens, got %d", got)
	}
}

func TestRuntimePersistsMemoryUnderConfigDir(t *testing.T) {
	configDir := t.TempDir()

	firstRuntime, err := NewRuntime(Config{
		ConfigDir:     configDir,
		Model:         ModelConfig{Provider: "fake"},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer firstRuntime.Close()

	firstRuntime.models = &testModelResolver{
		model: fakellm.NewFakeLLM([]string{"first"}),
	}

	firstResult, err := firstRuntime.Run(context.Background(), RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if len(firstResult.Memory) != 2 {
		t.Fatalf("expected 2 memory entries after first run, got %d", len(firstResult.Memory))
	}

	memoryPath := filepath.Join(configDir, "memory", "default.json")
	if _, err := os.Stat(memoryPath); err != nil {
		t.Fatalf("expected persisted memory file at %s: %v", memoryPath, err)
	}

	secondRuntime, err := NewRuntime(Config{
		ConfigDir:     configDir,
		Model:         ModelConfig{Provider: "fake"},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() second error = %v", err)
	}
	defer secondRuntime.Close()

	secondRuntime.models = &testModelResolver{
		model: fakellm.NewFakeLLM([]string{"second"}),
	}

	secondResult, err := secondRuntime.Run(context.Background(), RunRequest{Input: "again"})
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if len(secondResult.Memory) != 4 {
		t.Fatalf("expected 4 memory entries after reload, got %d", len(secondResult.Memory))
	}
	if secondResult.Memory[0].Role != "human" || secondResult.Memory[0].Content != "hello" {
		t.Fatalf("expected first persisted message to be restored, got %#v", secondResult.Memory[0])
	}
}

func TestNewRuntimeLoadsBundledSkillsSeededOnFirstStartup(t *testing.T) {
	configDir := t.TempDir()
	bundledDir := t.TempDir()
	writeSKILL(t, bundledDir, "alpha", testSkillA)

	runtime, err := NewRuntime(Config{
		ConfigDir:        configDir,
		BundledSkillsDir: bundledDir,
		Model:            ModelConfig{Provider: "fake"},
		Instruction:      "Answer directly.",
		SkillsDirs:       []string{},
		MaxIterations:    1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()

	if _, ok := runtime.skills.GetIndex().Get("alpha"); !ok {
		t.Fatalf("expected runtime to load skill copied during startup sync")
	}
}

func TestRuntimeRunCompactsRealChatExchangesBeyondWindow(t *testing.T) {
	configDir := t.TempDir()
	responses := make([]string, 30)
	for i := range responses {
		responses[i] = "ok"
	}
	runtime, err := NewRuntime(Config{
		ConfigDir:     configDir,
		Model:         ModelConfig{Provider: "fake", Responses: responses},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()

	inputs := []string{
		"我是硬件产品经理，平时用中文沟通，关注开发板 agent 端到端行为。",
		"记一下，以后处理蓝海报销App超过100元的提交或付款动作，必须先给风险摘要并等我确认。",
	}
	for i := 0; i < 21; i++ {
		inputs = append(inputs, "填充对话轮次")
	}
	for _, input := range inputs {
		if _, err := runtime.Run(context.Background(), RunRequest{Input: input}); err != nil {
			t.Fatalf("Run(%q) error = %v", input, err)
		}
	}

	waitForSessionCompaction(t, configDir)
}

func TestRuntimeRunSchedulesMemoryMaintenanceAsync(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	cfg := DefaultMemoryExtractionConfig()
	cfg.HotWindowEvents = 4

	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC()
	for i := 0; i < 8; i++ {
		if _, err := session.AppendEvent(context.Background(), SessionEvent{
			EventID: fmt.Sprintf("evt_%d", i),
			Ts:      now.Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: "历史消息",
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var released atomic.Bool
	releaseMaintenance := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer releaseMaintenance()
	var startedOnce atomic.Bool
	manager := NewMemoryManager(storageDir, WithExtractionConfig(cfg), WithSummarizeFn(func(ctx context.Context, events []SessionEvent) string {
		if startedOnce.CompareAndSwap(false, true) {
			close(started)
		}
		select {
		case <-ctx.Done():
			return ""
		case <-release:
			return "async summary"
		}
	}))

	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}, Instruction: "Answer directly.", MaxIterations: 1},
		&testModelResolver{model: fakellm.NewFakeLLM([]string{"ok"})},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	defer runtime.Close()

	done := make(chan error, 1)
	go func() {
		_, err := runtime.Run(context.Background(), RunRequest{Input: "hello"})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run() blocked on memory maintenance")
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("async memory maintenance did not start")
	}
	releaseMaintenance()
}

func waitForSessionCompaction(t *testing.T, configDir string) {
	t.Helper()
	session := NewSessionMemoryStore(filepath.Join(configDir, "memory", "session"))
	deadline := time.Now().Add(3 * time.Second)
	var lastEventCount int
	var lastChunkCount int
	var lastErr error

	for time.Now().Before(deadline) {
		events, err := session.readEvents(session.eventsPath())
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		chunks, err := session.RecallChunks(context.Background(), ChunkRecallQuery{Entities: []string{"蓝海报销App"}, Limit: 1})
		if err != nil {
			lastErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		lastEventCount = len(events)
		lastChunkCount = len(chunks)
		if lastEventCount <= 20 && lastChunkCount == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	if lastErr != nil {
		t.Fatalf("waiting for session compaction: %v", lastErr)
	}
	t.Fatalf("expected compacted chunk and hot window events <= 20, got chunks=%d events=%d", lastChunkCount, lastEventCount)
}

func TestRuntimeRegistersMemoryRecallToolsWhenConfigDirSet(t *testing.T) {
	runtime, err := NewRuntime(Config{
		ConfigDir:     t.TempDir(),
		Model:         ModelConfig{Provider: "fake"},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()

	if _, ok := runtime.tools.Get("recall_session_chunks"); !ok {
		t.Fatalf("expected runtime to register recall_session_chunks")
	}
	if _, ok := runtime.tools.Get("recall_memory"); !ok {
		t.Fatalf("expected runtime to register recall_memory")
	}
}

func TestRuntimeRunInjectsMemoryFilesIntoSystemPrompt(t *testing.T) {
	configDir := t.TempDir()
	summary := "SESSION SUMMARY SENTINEL"
	profile := "PROFILE SENTINEL"

	sessionDir := filepath.Join(configDir, "memory", "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("MkdirAll session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.md"), []byte(summary), 0o644); err != nil {
		t.Fatalf("WriteFile summary.md: %v", err)
	}

	longTermDir := filepath.Join(configDir, "memory", "long_term")
	if err := os.MkdirAll(longTermDir, 0o755); err != nil {
		t.Fatalf("MkdirAll long_term: %v", err)
	}
	if err := os.WriteFile(filepath.Join(longTermDir, "profile.md"), []byte(profile), 0o644); err != nil {
		t.Fatalf("WriteFile profile.md: %v", err)
	}

	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					Content: "ok",
				}},
			},
		},
	}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) != 1 || len(model.messages[0]) == 0 {
		t.Fatalf("expected one model call with messages, got %#v", model.messages)
	}

	systemMessage := model.messages[0][0]
	if systemMessage.Role != llms.ChatMessageTypeSystem {
		t.Fatalf("expected first message to be system, got %q", systemMessage.Role)
	}
	var systemText strings.Builder
	for _, part := range systemMessage.Parts {
		text, ok := part.(llms.TextContent)
		if ok {
			systemText.WriteString(text.Text)
		}
	}
	if !strings.Contains(systemText.String(), summary) {
		t.Fatalf("system message missing summary:\n%s", systemText.String())
	}
	if !strings.Contains(systemText.String(), profile) {
		t.Fatalf("system message missing profile:\n%s", systemText.String())
	}
}

func TestRuntimeRunIncludesUserAttachments(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			{
				Choices: []*llms.ContentChoice{{
					Content: "processed",
				}},
			},
		},
	}

	runtime := NewRuntimeWithDeps(
		Config{
			Model:       ModelConfig{Provider: "openrouter"},
			Instruction: "Use the provided media when answering.",
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{
		Input: "Describe the uploaded media.",
		Attachments: []InputAttachment{
			{
				Kind:     AttachmentKindImage,
				Name:     "photo.png",
				MIMEType: "image/png",
				Data:     []byte{0x89, 0x50, 0x4e, 0x47},
			},
			{
				Kind:     AttachmentKindAudio,
				Name:     "note.wav",
				MIMEType: "audio/wav",
				Data:     []byte{0x52, 0x49, 0x46, 0x46},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "processed" {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if len(model.messages) != 1 {
		t.Fatalf("expected 1 model call, got %d", len(model.messages))
	}

	lastCall := model.messages[0]
	if len(lastCall) == 0 {
		t.Fatalf("expected messages in model call")
	}
	userMessage := lastCall[len(lastCall)-1]
	if userMessage.Role != llms.ChatMessageTypeHuman {
		t.Fatalf("expected final message to be human, got %q", userMessage.Role)
	}

	var textContent string
	var imageURL string
	var binaryMIMEs []string
	for _, part := range userMessage.Parts {
		switch p := part.(type) {
		case llms.TextContent:
			textContent = p.Text
		case llms.ImageURLContent:
			imageURL = p.URL
		case llms.BinaryContent:
			binaryMIMEs = append(binaryMIMEs, p.MIMEType)
		}
	}

	if !strings.Contains(textContent, "photo.png") || !strings.Contains(textContent, "note.wav") {
		t.Fatalf("expected attachment names in prompt text, got %q", textContent)
	}
	if imageURL == "" || !strings.HasPrefix(imageURL, "data:image/png;base64,") {
		t.Fatalf("expected image attachment as data URL, got %q", imageURL)
	}
	if len(binaryMIMEs) != 1 || binaryMIMEs[0] != "audio/wav" {
		t.Fatalf("unexpected binary attachment MIME types: %#v", binaryMIMEs)
	}
}

func TestRuntimeClearMemoryRemovesPersistedFile(t *testing.T) {
	configDir := t.TempDir()
	runtime, err := NewRuntime(Config{
		ConfigDir:     configDir,
		Model:         ModelConfig{Provider: "fake"},
		Instruction:   "Answer directly.",
		SkillsDirs:    []string{},
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer runtime.Close()

	runtime.models = &testModelResolver{
		model: fakellm.NewFakeLLM([]string{"first"}),
	}

	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	memoryPath := filepath.Join(configDir, "memory", "default.json")
	if _, err := os.Stat(memoryPath); err != nil {
		t.Fatalf("expected persisted memory file at %s: %v", memoryPath, err)
	}

	if err := runtime.ClearMemory(context.Background()); err != nil {
		t.Fatalf("ClearMemory() error = %v", err)
	}

	if _, err := os.Stat(memoryPath); !os.IsNotExist(err) {
		t.Fatalf("expected memory file to be removed, stat err = %v", err)
	}
}
