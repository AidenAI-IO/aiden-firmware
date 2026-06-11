package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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
		model: &scriptedModel{responses: roleDirectResponses("completed")},
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
	model := &scriptedModel{responses: roleDirectResponses("ok")}
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
	model := &scriptedModel{responses: roleDirectResponses("ok")}
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

func TestToolDescriptorsIncludeMemoryToolMetadata(t *testing.T) {
	tools := &ToolSet{tools: map[string]langtools.Tool{}}
	tools.RegisterMemoryTools(t.TempDir(), nil, 3, nil)
	runtime := NewRuntimeWithDeps(Config{}, nil, nil, tools, NewSkillIndex())

	for _, name := range []string{"recall_device_memory", "inspect_episode"} {
		desc, ok := runtime.ToolDescriptorByName(name)
		if !ok {
			t.Fatalf("expected descriptor for %s", name)
		}
		if desc.Category != "memory" {
			t.Fatalf("%s category = %q, want memory", name, desc.Category)
		}
		if desc.InputMode != toolInputModeJSON {
			t.Fatalf("%s input mode = %q, want json", name, desc.InputMode)
		}
		if strings.TrimSpace(desc.ExampleInput) == "" {
			t.Fatalf("%s missing example input", name)
		}
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
		responses: append(roleDirectResponses("first"), roleDirectResponses("second")...),
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
	// Each run issues planner/executor/verifier calls, so the second run's planner
	// prompt is the fourth recorded model call (index 3).
	secondRunPlannerPrompt := model.messages[3]
	if !runtimeModelCallContains(secondRunPlannerPrompt, "Use alpha v2.") {
		t.Fatalf("second run missing reloaded v2 skill instructions")
	}
	if runtimeModelCallContains(secondRunPlannerPrompt, "Use alpha v1.") {
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

type staticTool struct {
	name   string
	output string
}

func (t *staticTool) Name() string { return t.name }

func (t *staticTool) Description() string { return "static test tool" }

func (t *staticTool) Call(context.Context, string) (string, error) { return t.output, nil }

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

func contentResponse(content string) *llms.ContentResponse {
	return contentResponseWithInfo(content, nil)
}

func contentResponseWithInfo(content string, info map[string]any) *llms.ContentResponse {
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content:        content,
			GenerationInfo: info,
		}},
	}
}

func plannerResponse(nextStep string, plan ...string) *llms.ContentResponse {
	if len(plan) == 0 {
		plan = []string{nextStep}
	}
	payload, _ := json.Marshal(map[string]any{
		"objective":           "test objective",
		"completion_criteria": []string{"test request is satisfied"},
		"plan":                plan,
		"next_step":           nextStep,
		"reason":              "test plan",
	})
	return contentResponse(string(payload))
}

func verifierFinishResponse(finalAnswer string) *llms.ContentResponse {
	return verifierFinishResponseWithInfo(finalAnswer, nil)
}

func verifierFinishResponseWithInfo(finalAnswer string, info map[string]any) *llms.ContentResponse {
	payload, _ := json.Marshal(map[string]any{
		"can_finish":   true,
		"final_answer": finalAnswer,
		"reason":       "test verified",
	})
	return contentResponseWithInfo(string(payload), info)
}

func verifierContinueResponse(reason string) *llms.ContentResponse {
	payload, _ := json.Marshal(map[string]any{
		"can_finish":   false,
		"needs_replan": true,
		"reason":       reason,
	})
	return contentResponse(string(payload))
}

func toolCallResponse(id, name, arguments string) *llms.ContentResponse {
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			ToolCalls: []llms.ToolCall{{
				ID:   id,
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      name,
					Arguments: arguments,
				},
			}},
		}},
	}
}

func multiToolCallResponse(calls ...llms.ToolCall) *llms.ContentResponse {
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			ToolCalls: calls,
		}},
	}
}

func roleDirectResponses(finalAnswer string) []*llms.ContentResponse {
	return []*llms.ContentResponse{
		plannerResponse("answer directly"),
		contentResponse(finalAnswer),
		verifierFinishResponse(finalAnswer),
	}
}

func roleToolResponses(toolName, arguments, finalAnswer string) []*llms.ContentResponse {
	return []*llms.ContentResponse{
		plannerResponse("use " + toolName),
		toolCallResponse("call_1", toolName, arguments),
		verifierFinishResponse(finalAnswer),
	}
}

func firstRunEventOfType(events []RunEvent, eventType string) (RunEvent, bool) {
	for _, event := range events {
		if event.Type == eventType {
			return event, true
		}
	}
	return RunEvent{}, false
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
	fn := buildLLMStructuredSummarizeFn(&testModelResolver{model: model}, nil)
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
	fn := buildLLMStructuredSummarizeFn(&testModelResolver{model: model}, nil)
	got := fn(context.Background(), []SessionEvent{{Role: "user", Content: "hello"}})
	if !got.Empty() {
		t.Fatalf("expected empty structured summary on invalid JSON, got %#v", got)
	}
}

func TestRuntimeRunOpenRouterUsesToolsWithoutStreaming(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}"}`, "The current audio volume is 42."),
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
	if len(model.sawStreaming) != 3 || model.sawStreaming[0] || model.sawStreaming[1] || model.sawStreaming[2] {
		t.Fatalf("expected non-streaming tool calls, got %#v", model.sawStreaming)
	}
}

func TestRuntimeRunFakeProviderUsesFunctionAgentToolCalls(t *testing.T) {
	model := &scriptedModel{
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}"}`, "The current audio volume is 42."),
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
			plannerResponse("use slow_a"),
			multiToolCallResponse(
				llms.ToolCall{
					ID:   "call_1",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name:      "slow_a",
						Arguments: `{"__arg1":"{}"}`,
					},
				},
				llms.ToolCall{
					ID:   "call_2",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name:      "slow_b",
						Arguments: `{"__arg1":"{}"}`,
					},
				},
			),
			verifierFinishResponse("done"),
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
	if len(model.messages) < 3 {
		t.Fatalf("model calls = %d, want at least 3", len(model.messages))
	}
	var toolCallNames []string
	for _, msg := range model.messages[2] {
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
		responses: roleToolResponses("screenshot", `{"__arg1":"{}"}`, "屏幕暂时获取失败，frame service 正在恢复。"),
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
	if len(model.messages) < 3 {
		t.Fatalf("expected verifier model call with tool observation, got %d calls", len(model.messages))
	}
	var toolObservation string
	for _, msg := range model.messages[2] {
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
	toolResult, ok := firstRunEventOfType(events, "tool_result")
	if !ok || !toolResult.IsError {
		t.Fatalf("expected error tool_result event, got %#v", events)
	}
}

func TestRuntimeAllowsNearRepeatedMouseClick(t *testing.T) {
	model := &scriptedModel{
		responses: []*llms.ContentResponse{
			plannerResponse("click first point"),
			toolCallResponse("call_1", "mouse_click", `{"x":"500","y":"80","coord_space":"normalized"}`),
			verifierContinueResponse("need a second click"),
			plannerResponse("click nearby point"),
			toolCallResponse("call_2", "mouse_click", `{"x":500,"y":120,"coord_space":"normalized"}`),
			verifierFinishResponse("我会换一个方式继续。"),
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
	if !strings.Contains(tool.inputs[0], `"x":"500"`) {
		t.Fatalf("first click should preserve model input, got %q", tool.inputs[0])
	}
	if !strings.Contains(tool.inputs[1], `"x":500`) {
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
			plannerResponse("type first time"),
			toolCallResponse("call_1", "keyboard_text", `{"text":"yuanshen"}`),
			verifierContinueResponse("need a repeated input"),
			plannerResponse("type second time"),
			toolCallResponse("call_2", "keyboard_text", `{"text":"yuanshen"}`),
			verifierFinishResponse("我不会重复输入。"),
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
		responses: roleToolResponses("enter_sleep", `{"__arg1":"{\"reason\":\"user asked\"}"}`, "I will wait for the next wakeup."),
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
		responses: roleToolResponses("audio_volume", `{"__arg1":"{}","description":"我先读取当前音量。"}`, "The current audio volume is 42."),
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
	toolCall, ok := firstRunEventOfType(events, "tool_call")
	if !ok {
		t.Fatalf("expected tool_call event, got %#v", events)
	}
	if toolCall.Description != "我先读取当前音量。" || toolCall.Content != toolCall.Description {
		t.Fatalf("unexpected tool description event: %#v", toolCall)
	}
	if toolCall.ToolInput != "{}" {
		t.Fatalf("tool_call event input = %q, want stripped input", toolCall.ToolInput)
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
		responses: roleDirectResponses("completed"),
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
	if len(model.sawStreaming) != 3 || model.sawStreaming[0] || model.sawStreaming[1] || model.sawStreaming[2] {
		t.Fatalf("expected internal role calls to avoid provider streaming, got %#v", model.sawStreaming)
	}
	if stream.String() != "completed" {
		t.Fatalf("unexpected stream output: %q", stream.String())
	}
}

func TestRuntimeRunScreenshotAddsBinaryImageObservation(t *testing.T) {
	jpegBytes := []byte("fake-jpeg-binary")
	model := &scriptedModel{
		responses: roleToolResponses("screenshot", `{"__arg1":"{}"}`, "The screenshot shows a UI."),
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
	if len(model.messages) != 3 {
		t.Fatalf("expected 3 role model calls, got %d", len(model.messages))
	}

	secondCall := model.messages[2]
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
		responses: roleToolResponses("screenshot", `{"__arg1":"{}"}`, "The screenshot shows a UI."),
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

	if len(model.messages) != 3 {
		t.Fatalf("expected 3 role model calls, got %d", len(model.messages))
	}

	var foundToolResponse, foundImageURL bool
	for _, msg := range model.messages[2] {
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
		responses: roleToolResponses("keyboard_tap", `{"keys":["enter"]}`, "The keyboard action updated the UI."),
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
	for _, msg := range model.messages[2] {
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
		responses: []*llms.ContentResponse{
			plannerResponse("answer directly"),
			contentResponse("completed"),
			verifierFinishResponseWithInfo("completed", map[string]any{
				"prompt_tokens":     600,
				"completion_tokens": 40,
				"total_tokens":      640,
			}),
		},
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
			plannerResponse("answer first"),
			contentResponse("with usage"),
			verifierFinishResponseWithInfo("with usage", map[string]any{
				"prompt_tokens": 600,
			}),
			plannerResponse("answer second"),
			contentResponse("without usage"),
			verifierFinishResponse("without usage"),
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
		model: &scriptedModel{responses: roleDirectResponses("first")},
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
		model: &scriptedModel{responses: roleDirectResponses("second")},
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
	memDir := filepath.Join(configDir, "memory")
	os.MkdirAll(memDir, 0o755)
	os.WriteFile(filepath.Join(memDir, "extraction.yaml"), []byte("hot_window_events: 20\n"), 0o644)

	response := `{"objective":"test objective","completion_criteria":["test request is satisfied"],"plan":["answer directly"],"next_step":"answer directly","can_finish":true,"final_answer":"ok","reason":"test verified"}`
	responses := make([]string, 90)
	for i := range responses {
		responses[i] = response
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
	for i := 0; i < 19; i++ {
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
	for i := 0; i < 9; i++ {
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
		&testModelResolver{model: &scriptedModel{responses: roleDirectResponses("ok")}},
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

func TestRuntimeRunRotatesSessionOnNewBoundary(t *testing.T) {
	configDir := t.TempDir()
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	oldSummary := "OLD SESSION SUMMARY MUST NOT ENTER NEW PROMPT"
	now := time.Now().UTC().Add(-4 * time.Minute)
	for i := 0; i < DefaultBoundaryConfig().SmallSessionEventThreshold+1; i++ {
		if _, err := session.AppendEvent(context.Background(), SessionEvent{
			EventID: fmt.Sprintf("evt_old_%d", i),
			Ts:      now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("查天气旧任务 %d", i),
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(storageDir, "session", "summary.md"), []byte(oldSummary), 0o644); err != nil {
		t.Fatalf("WriteFile old summary.md: %v", err)
	}

	releaseMaintenance := make(chan struct{})
	manager := NewMemoryManager(storageDir, WithSummarizeFn(func(ctx context.Context, events []SessionEvent) string {
		select {
		case <-ctx.Done():
			return ""
		case <-releaseMaintenance:
			return "old task summary"
		}
	}))
	defer func() {
		close(releaseMaintenance)
		waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.WaitMaintenance(waitCtx); err != nil {
			t.Fatalf("WaitMaintenance() error = %v", err)
		}
	}()
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = NewFilesystemMemoryPlane(storageDir, manager.extraction, nil)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "打开微信"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Memory) != 2 || result.Memory[0].Content != "打开微信" {
		t.Fatalf("expected clean memory snapshot for new task, got %#v", result.Memory)
	}

	active := readSessionEvents(t, session.eventsPath())
	if len(active) != 2 || active[0].Content != "打开微信" {
		t.Fatalf("expected active events to contain only current exchange, got %#v", active)
	}
	archiveDirs, err := filepath.Glob(filepath.Join(storageDir, "session_archive", "*"))
	if err != nil {
		t.Fatalf("Glob archived sessions: %v", err)
	}
	if len(archiveDirs) != 1 {
		t.Fatalf("expected one archived rotated session, got %v", archiveDirs)
	}
	archived := readSessionEvents(t, filepath.Join(archiveDirs[0], "events.jsonl"))
	if len(archived) != DefaultBoundaryConfig().SmallSessionEventThreshold+1 || archived[0].EventID != "evt_old_0" {
		t.Fatalf("unexpected archived events: %#v", archived)
	}
	if _, err := os.Stat(filepath.Join(storageDir, "session", "summary.md")); !os.IsNotExist(err) {
		t.Fatalf("active summary.md should be absent after rotation, stat err = %v", err)
	}
	archivedSummary, err := os.ReadFile(filepath.Join(archiveDirs[0], "summary.md"))
	if err != nil {
		t.Fatalf("ReadFile archived summary.md: %v", err)
	}
	if string(archivedSummary) != oldSummary {
		t.Fatalf("old summary not preserved in archive: %q", archivedSummary)
	}
	var promptText strings.Builder
	for _, call := range model.messages {
		for _, message := range call {
			for _, part := range message.Parts {
				if text, ok := part.(llms.TextContent); ok {
					promptText.WriteString(text.Text)
				}
			}
		}
	}
	if strings.Contains(promptText.String(), oldSummary) {
		t.Fatalf("new-session prompt leaked archived summary:\n%s", promptText.String())
	}

	episode, err := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes")).Get(context.Background(), result.EpisodeID)
	if err != nil {
		t.Fatalf("Get episode: %v", err)
	}
	if got := episode.Extra["session_boundary_decision"]; got != BoundaryNew {
		t.Fatalf("session_boundary_decision = %#v, want %q", got, BoundaryNew)
	}
	if got := episode.Extra["session_rotated"]; got != true {
		t.Fatalf("session_rotated = %#v, want true", got)
	}
	if got := numericExtraValue(episode.Extra["pending_chunks_recalled"]); got != 0 {
		t.Fatalf("pending_chunks_recalled = %#v, want 0 without recall tool call", got)
	}
}

func TestRuntimeRunRepairsTruncatedSessionTailBeforeBoundaryRotation(t *testing.T) {
	configDir := t.TempDir()
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC().Add(-4 * time.Minute)
	for i := 0; i < DefaultBoundaryConfig().SmallSessionEventThreshold+1; i++ {
		if _, err := session.AppendEvent(context.Background(), SessionEvent{
			EventID: fmt.Sprintf("evt_old_%d", i),
			Ts:      now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("查天气旧任务 %d", i),
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	file, err := os.OpenFile(session.eventsPath(), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("OpenFile events.jsonl: %v", err)
	}
	if _, err := file.WriteString(`{"event_id":"partial_crash_tail","type":"assistant_output","role":"assistant","content":"cut`); err != nil {
		file.Close()
		t.Fatalf("write truncated event tail: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close events.jsonl: %v", err)
	}

	manager := NewMemoryManager(storageDir)
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: &scriptedModel{responses: roleDirectResponses("ok")}},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = NewFilesystemMemoryPlane(storageDir, manager.extraction, nil)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "打开微信"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Memory) != 2 || result.Memory[0].Content != "打开微信" {
		t.Fatalf("expected clean memory snapshot for new task, got %#v", result.Memory)
	}

	archiveDirs, err := filepath.Glob(filepath.Join(storageDir, "session_archive", "*"))
	if err != nil {
		t.Fatalf("Glob archived sessions: %v", err)
	}
	if len(archiveDirs) != 1 {
		t.Fatalf("expected one archived rotated session, got %v", archiveDirs)
	}
	archivedPath := filepath.Join(archiveDirs[0], "events.jsonl")
	archivedRaw, err := os.ReadFile(archivedPath)
	if err != nil {
		t.Fatalf("ReadFile archived events: %v", err)
	}
	if strings.Contains(string(archivedRaw), "partial_crash_tail") {
		t.Fatalf("runtime boundary rotation archived unrepaired truncated tail: %q", archivedRaw)
	}
	archived := readSessionEvents(t, archivedPath)
	if len(archived) != DefaultBoundaryConfig().SmallSessionEventThreshold+1 {
		t.Fatalf("unexpected archived events after repair: %#v", archived)
	}
}

func TestRuntimeRunKeepsSmallSessionOnUnrelatedInput(t *testing.T) {
	configDir := t.TempDir()
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC().Add(-4 * time.Minute)
	for i := 0; i < DefaultBoundaryConfig().SmallSessionEventThreshold; i++ {
		if _, err := session.AppendEvent(context.Background(), SessionEvent{
			EventID: fmt.Sprintf("evt_small_%d", i),
			Ts:      now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("查天气小会话 %d", i),
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	manager := NewMemoryManager(storageDir)
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = NewFilesystemMemoryPlane(storageDir, manager.extraction, nil)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "打开微信"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Memory) != DefaultBoundaryConfig().SmallSessionEventThreshold+2 {
		t.Fatalf("small session should keep previous context, got %#v", result.Memory)
	}

	archiveDirs, err := filepath.Glob(filepath.Join(storageDir, "session_archive", "*"))
	if err != nil {
		t.Fatalf("Glob archived sessions: %v", err)
	}
	if len(archiveDirs) != 0 {
		t.Fatalf("small session with unrelated input rotated session: %v", archiveDirs)
	}

	episode, err := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes")).Get(context.Background(), result.EpisodeID)
	if err != nil {
		t.Fatalf("Get episode: %v", err)
	}
	if got := episode.Extra["session_boundary_decision"]; got != BoundaryContinue {
		t.Fatalf("session_boundary_decision = %#v, want %q", got, BoundaryContinue)
	}
	if got := episode.Extra["session_boundary_reason"]; got != BoundaryReasonSmallSession {
		t.Fatalf("session_boundary_reason = %#v, want %q", got, BoundaryReasonSmallSession)
	}
}

func TestRuntimeRunKeepsNeutralFollowUpWithActiveEpisode(t *testing.T) {
	configDir := t.TempDir()
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC().Add(-4 * time.Minute)
	for i, content := range []string{"查一下今天天气", "今天多云"} {
		role := "user"
		eventType := "user_input"
		if i == 1 {
			role = "assistant"
			eventType = "assistant_output"
		}
		if _, err := session.AppendEvent(context.Background(), SessionEvent{
			EventID: fmt.Sprintf("evt_prev_%d", i),
			Ts:      now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Type:    eventType,
			Role:    role,
			Content: content,
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	episodeStore := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes"))
	if _, err := episodeStore.AddEpisode(context.Background(), TaskEpisode{
		ID:        "ep_weather_done",
		Status:    "active",
		StartedAt: now.Add(-30 * time.Second).Format(time.RFC3339Nano),
		EndedAt:   now.Add(time.Second).Format(time.RFC3339Nano),
		UserGoal:  "查一下今天天气",
		Outcome:   TaskEpisodeOutcome{Success: true, FinalAnswer: "今天多云"},
	}); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}

	manager := NewMemoryManager(storageDir)
	model := &scriptedModel{responses: roleDirectResponses("ok")}
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = NewFilesystemMemoryPlane(storageDir, manager.extraction, nil)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "好的"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Memory) != 4 || result.Memory[0].Content != "查一下今天天气" {
		t.Fatalf("neutral follow-up should keep previous session context, got %#v", result.Memory)
	}

	archiveDirs, err := filepath.Glob(filepath.Join(storageDir, "session_archive", "*"))
	if err != nil {
		t.Fatalf("Glob archived sessions: %v", err)
	}
	if len(archiveDirs) != 0 {
		t.Fatalf("neutral follow-up with active episode rotated session: %v", archiveDirs)
	}

	episode, err := episodeStore.Get(context.Background(), result.EpisodeID)
	if err != nil {
		t.Fatalf("Get episode: %v", err)
	}
	if got := episode.Extra["session_boundary_decision"]; got != BoundaryContinue {
		t.Fatalf("session_boundary_decision = %#v, want %q", got, BoundaryContinue)
	}
	if got := episode.Extra["session_boundary_reason"]; got != BoundaryReasonActiveEpisode {
		t.Fatalf("session_boundary_reason = %#v, want %q", got, BoundaryReasonActiveEpisode)
	}
}

func TestRecentEpisodeContextIncludesRunningAndRecentActiveEpisodes(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	store := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes"))
	now := time.Now().UTC()

	if _, err := store.StartEpisode(context.Background(), TaskEpisode{
		ID:        "ep_running",
		StartedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
		UserGoal:  "继续处理天气",
	}); err != nil {
		t.Fatalf("StartEpisode() error = %v", err)
	}
	if _, err := store.AddEpisode(context.Background(), TaskEpisode{
		ID:        "ep_active_recent",
		Status:    "active",
		StartedAt: now.Add(-3 * time.Minute).Format(time.RFC3339Nano),
		EndedAt:   now.Add(-time.Minute).Format(time.RFC3339Nano),
		UserGoal:  "查天气",
		Outcome:   TaskEpisodeOutcome{Success: true},
	}); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}

	plane := NewFilesystemMemoryPlane(storageDir, DefaultMemoryExtractionConfig(), nil)
	ctx := recentEpisodeContext(plane, now, 5*time.Minute)
	if !ctx.HasRunning {
		t.Fatalf("expected running episode context")
	}
	if !ctx.HasActive {
		t.Fatalf("expected recent active episode context")
	}
}

func TestRecentEpisodeContextIgnoresOldActiveEpisode(t *testing.T) {
	storageDir := filepath.Join(t.TempDir(), "memory")
	store := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes"))
	now := time.Now().UTC()

	if _, err := store.AddEpisode(context.Background(), TaskEpisode{
		ID:        "ep_active_old",
		Status:    "active",
		StartedAt: now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
		EndedAt:   now.Add(-6 * time.Minute).Format(time.RFC3339Nano),
		UserGoal:  "查天气",
		Outcome:   TaskEpisodeOutcome{Success: true},
	}); err != nil {
		t.Fatalf("AddEpisode() error = %v", err)
	}

	plane := NewFilesystemMemoryPlane(storageDir, DefaultMemoryExtractionConfig(), nil)
	ctx := recentEpisodeContext(plane, now, 5*time.Minute)
	if ctx.HasActive {
		t.Fatalf("old active episode should not bias session boundary")
	}
}

func TestRuntimeRunCanceledWhileQueuedDoesNotRotateSessionOrStartEpisode(t *testing.T) {
	configDir := t.TempDir()
	storageDir := filepath.Join(configDir, "memory")
	session := NewSessionMemoryStore(filepath.Join(storageDir, "session"))
	now := time.Now().UTC().Add(-2 * time.Minute)
	for i := 0; i < 2; i++ {
		if _, err := session.AppendEvent(context.Background(), SessionEvent{
			EventID: fmt.Sprintf("evt_old_%d", i),
			Ts:      now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Type:    "user_input",
			Role:    "user",
			Content: fmt.Sprintf("查天气旧任务 %d", i),
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	model := &queuedCancelModel{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	manager := NewMemoryManager(storageDir)
	runtime := NewRuntimeWithDeps(
		Config{
			ConfigDir:     configDir,
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		manager,
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)
	runtime.memoryPlane = NewFilesystemMemoryPlane(storageDir, manager.extraction, nil)

	firstDone := make(chan error, 1)
	go func() {
		_, err := runtime.Run(context.Background(), RunRequest{Input: "继续查天气"})
		firstDone <- err
	}()
	select {
	case <-model.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first run did not reach model call")
	}

	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := runtime.Run(queuedCtx, RunRequest{Input: "打开微信"})
		secondDone <- err
	}()
	cancelQueued()
	close(model.releaseFirst)

	if err := <-firstDone; err == nil || !strings.Contains(err.Error(), "first run stopped") {
		t.Fatalf("first Run() error = %v, want first run stopped", err)
	}
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued Run() did not return after cancellation")
	}

	archiveDirs, err := filepath.Glob(filepath.Join(storageDir, "session_archive", "*"))
	if err != nil {
		t.Fatalf("Glob archived sessions: %v", err)
	}
	if len(archiveDirs) != 0 {
		t.Fatalf("canceled queued run rotated session: %v", archiveDirs)
	}
	active := readSessionEvents(t, session.eventsPath())
	if len(active) != 2 || active[0].EventID != "evt_old_0" {
		t.Fatalf("canceled queued run changed active session events: %#v", active)
	}

	index, err := NewTaskEpisodeStore(filepath.Join(storageDir, "episodes")).loadIndex()
	if err != nil {
		t.Fatalf("load episode index: %v", err)
	}
	if len(index.Episodes) != 1 {
		t.Fatalf("episode index contains %d entries, want only the first run: %#v", len(index.Episodes), index.Episodes)
	}
	if index.Episodes[0].UserGoal != "继续查天气" {
		t.Fatalf("unexpected episode goal: %#v", index.Episodes[0])
	}
}

type queuedCancelModel struct {
	firstStarted     chan struct{}
	releaseFirst     chan struct{}
	firstStartedOnce atomic.Bool
	callCount        atomic.Int64
}

func (m *queuedCancelModel) GenerateContent(ctx context.Context, _ []llms.MessageContent, _ ...llms.CallOption) (*llms.ContentResponse, error) {
	if m.callCount.Add(1) == 1 {
		if m.firstStartedOnce.CompareAndSwap(false, true) {
			close(m.firstStarted)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-m.releaseFirst:
			return nil, errors.New("first run stopped")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("queued run reached model")
}

func (m *queuedCancelModel) Call(context.Context, string, ...llms.CallOption) (string, error) {
	panic("unexpected Call invocation")
}

func numericExtraValue(v interface{}) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return -1
	}
}

func TestSessionRecallTelemetryCountsPendingResults(t *testing.T) {
	counter := &atomic.Int64{}
	tool := &sessionRecallTelemetryTool{
		inner: &staticTool{
			name:   "recall_session_chunks",
			output: `{"results":[{"chunk_id":"chunk_001"},{"chunk_id":"pending-123"}]}`,
		},
		counter: counter,
	}
	if _, err := tool.Call(context.Background(), `{}`); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got := counter.Load(); got != 1 {
		t.Fatalf("pending recall count = %d, want 1", got)
	}
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
		if lastEventCount <= 21 && lastChunkCount == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	if lastErr != nil {
		t.Fatalf("waiting for session compaction: %v", lastErr)
	}
	t.Fatalf("expected compacted chunk and hot window events <= 21 including pinned root, got chunks=%d events=%d", lastChunkCount, lastEventCount)
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
		responses: roleDirectResponses("ok"),
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
	if len(model.messages) != 3 || len(model.messages[0]) == 0 {
		t.Fatalf("expected three role model calls with messages, got %#v", model.messages)
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

func TestRuntimeMemoryContextIgnoresArchivedSessionSummary(t *testing.T) {
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	archiveSummary := "ARCHIVED SESSION SUMMARY SENTINEL"
	profile := "PROFILE STILL ACTIVE"

	archiveDir := filepath.Join(memoryDir, "session_archive", "closed-session")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("MkdirAll archive: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archiveDir, "summary.md"), []byte(archiveSummary), 0o644); err != nil {
		t.Fatalf("WriteFile archive summary.md: %v", err)
	}
	longTermDir := filepath.Join(memoryDir, "long_term")
	if err := os.MkdirAll(longTermDir, 0o755); err != nil {
		t.Fatalf("MkdirAll long_term: %v", err)
	}
	if err := os.WriteFile(filepath.Join(longTermDir, "profile.md"), []byte(profile), 0o644); err != nil {
		t.Fatalf("WriteFile profile.md: %v", err)
	}

	runtime := &Runtime{config: Config{ConfigDir: configDir}}
	promptContext := runtime.memoryContextForPrompt()
	if strings.Contains(promptContext, archiveSummary) {
		t.Fatalf("memoryContextForPrompt leaked archived summary:\n%s", promptContext)
	}
	if !strings.Contains(promptContext, profile) {
		t.Fatalf("memoryContextForPrompt missing active profile:\n%s", promptContext)
	}

	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	retrieved, err := plane.Retrieve(context.Background(), MemoryRetrieveRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if strings.Contains(retrieved.Common.SessionSummary, archiveSummary) {
		t.Fatalf("Retrieve() leaked archived summary: %q", retrieved.Common.SessionSummary)
	}
	if retrieved.Common.Profile != profile {
		t.Fatalf("Retrieve() profile = %q, want %q", retrieved.Common.Profile, profile)
	}
}

func TestRuntimeRunIncludesRuntimeContextInSystemMessage(t *testing.T) {
	model := &scriptedModel{
		responses: roleDirectResponses("ok"),
	}
	runtime := NewRuntimeWithDeps(
		Config{
			Model:         ModelConfig{Provider: "fake"},
			Instruction:   "Answer directly.",
			MaxIterations: 1,
		},
		&testModelResolver{model: model},
		NewMemoryManager(""),
		&ToolSet{tools: map[string]langtools.Tool{}},
		NewSkillIndex(),
	)

	runtimeContext := "Phone bridge status:\n- connected: true"
	if _, err := runtime.Run(context.Background(), RunRequest{Input: "hello", RuntimeContext: runtimeContext}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.messages) != 3 || len(model.messages[0]) == 0 {
		t.Fatalf("expected three role model calls with messages, got %#v", model.messages)
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
	if !strings.Contains(systemText.String(), "Runtime context:\n"+runtimeContext) {
		t.Fatalf("system message missing runtime context:\n%s", systemText.String())
	}
}

func TestRuntimeRunIncludesUserAttachments(t *testing.T) {
	model := &scriptedModel{
		responses: roleDirectResponses("processed"),
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
	if len(model.messages) != 3 {
		t.Fatalf("expected 3 role model calls, got %d", len(model.messages))
	}

	lastCall := model.messages[1]
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
		model: &scriptedModel{responses: roleDirectResponses("first")},
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
