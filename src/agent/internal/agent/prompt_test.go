package agent

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"aiden-agent/internal/agent/contextmanager"
	"github.com/tmc/langchaingo/llms"
	langtools "github.com/tmc/langchaingo/tools"
)

func testPromptProfile(cfg AgentConfig) RoleProfile {
	return buildProfile(cfg, NewSkillManager(NewSkillIndex()), nil, agentRoleRules())
}

func newPromptTestContextManager(t *testing.T) *contextmanager.ContextManager {
	t.Helper()
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	return manager
}

func TestRolePromptsIncludeCurrentDate(t *testing.T) {
	originalNow := promptNow
	promptNow = func() time.Time {
		return time.Date(2026, time.June, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	}
	t.Cleanup(func() { promptNow = originalNow })

	want := "Current date: 2026-06-01 (星期一)"
	profile := testPromptProfile(AgentConfig{})
	if !strings.Contains(profile.SystemPrompt, want) {
		t.Fatalf("system prompt missing current date %q:\n%s", want, profile.SystemPrompt)
	}
}

func TestRolePromptIncludesConfiguredResponseLocaleInSystemPrompt(t *testing.T) {
	manager := NewSkillManager(NewSkillIndex())
	zh := buildProfile(AgentConfig{Locale: "zh-CN"}, manager, nil, agentRoleRules())
	en := buildProfile(AgentConfig{Locale: "en-US"}, manager, nil, agentRoleRules())

	if zh.SystemPrompt == en.SystemPrompt {
		t.Fatalf("system prompt did not change with locale:\nzh=%q\nen=%q", zh.SystemPrompt, en.SystemPrompt)
	}
	for _, want := range []string{"configured response locale is zh-CN", "Simplified Chinese", "from the first generated token"} {
		if !strings.Contains(zh.SystemPrompt, want) {
			t.Fatalf("Chinese system prompt missing %q:\n%s", want, zh.SystemPrompt)
		}
	}
	for _, want := range []string{"configured response locale is en-US", "English", "from the first generated token"} {
		if !strings.Contains(en.SystemPrompt, want) {
			t.Fatalf("English system prompt missing %q:\n%s", want, en.SystemPrompt)
		}
	}
}

func TestStateHookDoesNotInjectResponseLocale(t *testing.T) {
	runtime := NewRuntimeWithDeps(Config{Locale: "en-US"}, nil, nil, nil, NewSkillIndex())
	manager := newPromptTestContextManager(t)
	runtime.contextManager = manager
	manager.AddAppendMessageHook(runtime.getStateHook())
	if err := manager.AppendMessage(contextmanager.Message{Role: contextmanager.MessageRoleUser, Content: "hello"}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}

	messages := manager.MessageListDump().Messages
	if len(messages) != 1 || messages[0].Role != contextmanager.MessageRoleUser {
		t.Fatalf("messages = %#v, want only the user message", messages)
	}
}

func TestStateHookAttachesFreshScreenshotToUserTurn(t *testing.T) {
	imageData := []byte("current screenshot")
	screenshot := &stubTool{
		name:        "screenshot",
		description: "Capture screenshot.",
		output: `{"width":390,"height":844,"format":"jpeg","size":18,"data":"` +
			base64.StdEncoding.EncodeToString(imageData) + `"}`,
	}
	runtime := NewRuntimeWithDeps(Config{}, nil, nil, &ToolSet{tools: map[string]langtools.Tool{
		"screenshot": screenshot,
	}}, NewSkillIndex())
	manager := newPromptTestContextManager(t)
	runtime.contextManager = manager
	manager.AddAppendMessageHook(runtime.getStateHook())

	if err := manager.AppendMessage(contextmanager.Message{Role: contextmanager.MessageRoleUser, Content: "hello"}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}

	messages := manager.MessageListDump().Messages
	if len(messages) != 2 || messages[0].Role != contextmanager.MessageRoleState || messages[1].Role != contextmanager.MessageRoleUser {
		t.Fatalf("messages = %#v, want state screenshot followed by user message", messages)
	}
	if len(messages[0].Attachments) != 1 {
		t.Fatalf("state attachments = %#v, want one screenshot", messages[0].Attachments)
	}
	attachment := messages[0].Attachments[0]
	if attachment.MIMEType != "image/jpeg" || attachment.Source != contextmanager.AttachmentSourceScreenshotObservation {
		t.Fatalf("state screenshot attachment = %#v", attachment)
	}

	standard := contextmanager.ConvertMessageList(messages)
	foundImage := false
	for _, part := range standard[0].Parts {
		if binary, ok := part.(llms.BinaryContent); ok && binary.MIMEType == "image/jpeg" && bytes.Equal(binary.Data, imageData) {
			foundImage = true
		}
	}
	if !foundImage {
		t.Fatalf("converted state message has no screenshot binary: %#v", standard[0].Parts)
	}
	if len(screenshot.inputs) != 1 || screenshot.inputs[0] != "{}" {
		t.Fatalf("screenshot inputs = %#v, want one empty-object call", screenshot.inputs)
	}
}

func TestStateHookKeepsTextStateWhenScreenshotFails(t *testing.T) {
	screenshot := &stubTool{
		name:        "screenshot",
		description: "Capture screenshot.",
		err:         errors.New("capture unavailable"),
	}
	runtime := NewRuntimeWithDeps(Config{}, nil, nil, &ToolSet{tools: map[string]langtools.Tool{
		"screenshot": screenshot,
	}}, NewSkillIndex())
	runtime.stateManager.SetState("device", "connected")
	manager := newPromptTestContextManager(t)
	runtime.contextManager = manager
	manager.AddAppendMessageHook(runtime.getStateHook())

	if err := manager.AppendMessage(contextmanager.Message{Role: contextmanager.MessageRoleUser, Content: "hello"}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}

	messages := manager.MessageListDump().Messages
	if len(messages) != 2 || messages[0].Role != contextmanager.MessageRoleState {
		t.Fatalf("messages = %#v, want text state followed by user message", messages)
	}
	if !strings.Contains(messages[0].Content, "device: connected") {
		t.Fatalf("state content = %q, want device state", messages[0].Content)
	}
	if len(messages[0].Attachments) != 0 {
		t.Fatalf("state attachments = %#v, want none after screenshot failure", messages[0].Attachments)
	}
}

func TestStateHookOmitsEmptyStateValues(t *testing.T) {
	runtime := NewRuntimeWithDeps(Config{}, nil, nil, nil, NewSkillIndex())
	runtime.stateManager.SetState("app_connected", "false")
	runtime.stateManager.SetState("app_platform", "")
	manager := newPromptTestContextManager(t)
	runtime.contextManager = manager
	manager.AddAppendMessageHook(runtime.getStateHook())

	if err := manager.AppendMessage(contextmanager.Message{Role: contextmanager.MessageRoleUser, Content: "hello"}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}

	messages := manager.MessageListDump().Messages
	if len(messages) != 2 || messages[0].Role != contextmanager.MessageRoleState {
		t.Fatalf("messages = %#v, want filtered state followed by user message", messages)
	}
	if !strings.Contains(messages[0].Content, "app_connected: false") {
		t.Fatalf("state content = %q, want app_connected", messages[0].Content)
	}
	if strings.Contains(messages[0].Content, "app_platform") {
		t.Fatalf("state content = %q, want empty app_platform omitted", messages[0].Content)
	}
}

func TestRolePromptsIncludeRealHostRuntimeInfo(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("Hostname() error = %v", err)
	}
	operatingSystem := mustUname(t, "-s")
	architecture := mustUname(t, "-m")
	wantLine := "Host: os=" + operatingSystem + ", hostname=" + hostname + ", arch=" + architecture
	wantEnvironmentLine := "- You run on the Aiden hardware controller (" + wantLine + "); you are not the device shown in screenshots."

	profile := testPromptProfile(AgentConfig{})
	if !strings.Contains(profile.SystemPrompt, wantEnvironmentLine) {
		t.Fatalf("system prompt missing host info in environment guidance %q:\n%s", wantEnvironmentLine, profile.SystemPrompt)
	}
	if strings.Contains(profile.SystemPrompt, "kernel=") {
		t.Fatalf("system prompt should not include kernel info:\n%s", profile.SystemPrompt)
	}
}

func mustUname(t *testing.T, flag string) string {
	t.Helper()
	out, err := exec.Command("uname", flag).Output()
	if err != nil {
		t.Fatalf("uname %s error = %v", flag, err)
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		t.Fatalf("uname %s returned empty output", flag)
	}
	return value
}

func TestRolePromptsIncludeGlobalEnvironmentAndDeviceGuidance(t *testing.T) {
	profile := testPromptProfile(AgentConfig{
		Instruction:      "base instruction",
		AdditionalPrompt: "extra prompt",
	})

	for _, want := range []string{
		"base instruction",
		"extra prompt",
		"## Environment",
		"## Default behavior",
		"configured response locale is zh-CN",
		"Always respond in Simplified Chinese",
		"from the first generated token",
		"Most user input arrives as voice transcribed by STT",
		"homophone, near-sound, segmentation, or named-entity errors",
		"choose likely canonical keywords and try reasonable alternate terms",
		"do not mention or hint at internal automation implementation details",
		"run_script",
		"JSONL",
		"Aiden hardware controller",
		"not the device shown in screenshots",
		"shell, local files, processes, and system commands only affect the Aiden hardware controller",
		"Do not infer target device information from the host OS or architecture",
		"do not use local system commands instead of target control tools",
		"Infer the target device and target OS from screenshots",
		"weak prior, not a detected fact",
		"Use shell on the Aiden controller",
		"precise controller clock or timezone queries",
		"deterministic calculations",
		"use shell utilities on the Aiden controller",
		"do not treat controller-local results as target-device state",
		"do not operate the target UI in screenshots",
		"recall_memory",
		"do not answer from general knowledge alone",
		"For text-only arithmetic, comparison, summarization, translation, or simple Q&A tasks",
		"do not observe, wait on, or operate the connected display",
		"<tts>...</tts>",
		"device-operator",
		"visible target UI",
		"discovery summaries only",
		"before the first screenshot or UI action",
		"Prefer direct or semantic tools",
		"request confirmation",
		"Keep detailed UI playbooks in skills",
	} {
		if !strings.Contains(profile.SystemPrompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, profile.SystemPrompt)
		}
	}

	for _, unwanted := range []string{
		"## 环境",
		"## 默认行为",
		"宿主机:",
		"默认用简体中文回答",
		"Aiden 硬件控制器",
		"滑动操作策略",
		"不要因为没有单独的拨打电话工具就说做不到",
		"osascript",
		"AppleScript",
		"PowerShell",
		"xdotool",
		"kernel=",
	} {
		if strings.Contains(profile.SystemPrompt, unwanted) {
			t.Fatalf("system prompt should not contain old localized guidance %q:\n%s", unwanted, profile.SystemPrompt)
		}
	}

	if strings.Contains(profile.SystemPrompt, "Use long-term memory if relevant") {
		t.Fatalf("system prompt should not contain legacy memory trigger:\n%s", profile.SystemPrompt)
	}
}

func TestDefaultAgentBehaviorExcludesEnvironmentGuidance(t *testing.T) {
	behavior := defaultAgentBehavior()

	for _, unexpected := range []string{
		"Environment",
		"Aiden hardware controller",
		"not the device shown in screenshots",
		hostRuntimeInfoContext(),
	} {
		if strings.Contains(behavior, unexpected) {
			t.Fatalf("defaultAgentBehavior should not include environment guidance %q:\n%s", unexpected, behavior)
		}
	}
}

func TestDefaultAgentBehaviorRequiresUnifiedLeadingTTS(t *testing.T) {
	behavior := defaultAgentBehavior()
	for _, want := range []string{
		"Every assistant response must begin with exactly one <tts>...</tts> block before any whitespace or visible text",
		"The literal <tts> start tag must be the first generated bytes",
		"Never emit a second <tts> block in the same response",
		"may be a concise spoken form and does not need to match the visible text exactly",
		"When invoking tools, begin the assistant content with the same single leading <tts>...</tts> block",
		"then add brief visible progress text before the tool call",
	} {
		if !strings.Contains(behavior, want) {
			t.Fatalf("defaultAgentBehavior missing %q:\n%s", want, behavior)
		}
	}
	for _, unwanted := range []string{
		"ordinary user-facing text, followed by exactly one <tts>",
		"put brief user-facing progress text first",
		"Tool-call TTS stays trailing",
	} {
		if strings.Contains(behavior, unwanted) {
			t.Fatalf("defaultAgentBehavior still requests trailing TTS with %q:\n%s", unwanted, behavior)
		}
	}
}

func TestGlobalPromptsExcludeKeyboardTextInputDetails(t *testing.T) {
	for name, prompt := range map[string]string{
		"defaultAgentBehavior": defaultAgentBehavior(),
		"defaultInstruction":   defaultInstruction,
	} {
		for _, unexpected := range []string{
			`{"text":"App Store"}`,
			"US-keyboard ASCII",
			"must receive JSON",
			"不要传裸字符串",
			"非键盘字符",
			"拼音/英文关键词",
		} {
			if strings.Contains(prompt, unexpected) {
				t.Fatalf("%s should not include keyboard_text input detail %q:\n%s", name, unexpected, prompt)
			}
		}
	}
}

func TestDefaultAgentBehaviorExcludesMigratedToolDetails(t *testing.T) {
	behavior := defaultAgentBehavior()
	for _, unexpected := range []string{
		"stable=false means",
		"audio_volume tool",
		"Use the Delete key only",
		"coord_space:\"normalized\"",
		"coord_space:\"pixel\"",
		`quick_action {"action":"back","platform":"android"}`,
		"For phone edge navigation",
		"Directional swipe names describe finger movement",
		"Precision swipe loop",
		"Horizontal carousels",
		"In app switchers with overlapping cards",
		"prefer search over blind scrolling",
		"return_entry=dynamic_island",
		"For long text, Chinese, emoji",
		"committed:true. keyboard_text is ASCII-only",
		"Base visible UI actions on the latest screenshot",
		"image_diff feedback",
		"Picker/wheel controls",
		"probe once with medium",
		"save_memory with app name, control location, direction, strength/distance, and delta",
	} {
		if strings.Contains(behavior, unexpected) {
			t.Fatalf("defaultAgentBehavior should not include migrated tool detail %q:\n%s", unexpected, behavior)
		}
	}
}

func TestCombinedAgentInstructionFallsBackWhenEmpty(t *testing.T) {
	if got := combinedAgentInstruction(AgentConfig{}); got != "" {
		t.Fatalf("combinedAgentInstruction() = %q, want empty string", got)
	}
}

func TestRolePromptsGuideSkillCatalogAndPreloadedSkills(t *testing.T) {
	index := NewSkillIndex()
	index.skills["planner"] = &SkillDefinition{Name: "planner", Description: "Plan before acting", Instructions: "Make a plan."}
	manager := NewSkillManager(index)
	profile := buildProfile(AgentConfig{}, manager, nil, agentRoleRules())

	for _, want := range []string{
		"## Available skills",
		"The entries below are discovery summaries only",
		"- planner: Plan before acting",
	} {
		if !strings.Contains(profile.SystemPrompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, profile.SystemPrompt)
		}
	}
}

func TestRolePromptOmitsRuntimeAndMemoryContext(t *testing.T) {
	profile := testPromptProfile(AgentConfig{})

	if !strings.Contains(profile.SystemPrompt, "## Role rules") {
		t.Fatalf("prompt missing role rules section:\n%s", profile.SystemPrompt)
	}
	for _, unwanted := range []string{"## Runtime context", "Phone bridge status", "session memory tail"} {
		if strings.Contains(profile.SystemPrompt, unwanted) {
			t.Fatalf("system prompt should not include dynamic context %q:\n%s", unwanted, profile.SystemPrompt)
		}
	}
}

func TestRolePromptRoutesPlatformShortcutsThroughQuickAction(t *testing.T) {
	profile := testPromptProfile(AgentConfig{})
	for _, want := range []string{"copy, paste, cut, select all, delete backward/forward", "MUST use quick_action", "current run explicitly reports", "Do not infer quick_action unavailability", "unrelated tool failure", "never replay the same binding", "explicitly asks to press those exact physical keys", "app-specific or not cataloged"} {
		if !strings.Contains(profile.SystemPrompt, want) {
			t.Fatalf("system prompt missing shortcut routing guidance %q:\n%s", want, profile.SystemPrompt)
		}
	}
}

func TestRolePromptRoutesAppLaunchInsideOpenApp(t *testing.T) {
	profile := testPromptProfile(AgentConfig{})
	for _, want := range []string{
		"call open_app with a semantic app name",
		"selects Phone Bridge or visible system search internally",
		"call open_url",
		"Before calling open_url, bridge_clipboard, bridge_calendar, bridge_contacts, or bridge_notification",
		"Call open_url when app_connected:true with app_state absent or active",
		"visible iOS Dynamic Island return entry can restore Aiden",
		"app_platform:ios with app_pip_enabled:true",
		"app_platform:android with app_fgs_enabled:true",
	} {
		if !strings.Contains(profile.SystemPrompt, want) {
			t.Fatalf("system prompt missing Phone Bridge routing guidance %q:\n%s", want, profile.SystemPrompt)
		}
	}
}
