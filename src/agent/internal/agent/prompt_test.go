package agent

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRolePromptsIncludeCurrentDate(t *testing.T) {
	originalNow := promptNow
	promptNow = func() time.Time {
		return time.Date(2026, time.June, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	}
	t.Cleanup(func() { promptNow = originalNow })

	want := "Current date: 2026-06-01 (星期一)"
	profiles := buildRoleProfiles(AgentConfig{}, ResolvedSkills{}, nil, MemoryContext{})
	for _, profile := range []RoleProfile{profiles.Planner, profiles.Executor, profiles.Verifier} {
		if !strings.Contains(profile.SystemPrompt, want) {
			t.Fatalf("%s system prompt missing current date %q:\n%s", profile.Name, want, profile.SystemPrompt)
		}
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

	profiles := buildRoleProfiles(AgentConfig{}, ResolvedSkills{}, nil, MemoryContext{})
	for _, profile := range []RoleProfile{profiles.Planner, profiles.Executor} {
		if !strings.Contains(profile.SystemPrompt, wantEnvironmentLine) {
			t.Fatalf("%s system prompt missing host info in environment guidance %q:\n%s", profile.Name, wantEnvironmentLine, profile.SystemPrompt)
		}
		if strings.Contains(profile.SystemPrompt, "kernel=") {
			t.Fatalf("%s system prompt should not include kernel info:\n%s", profile.Name, profile.SystemPrompt)
		}
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
	profiles := buildRoleProfiles(
		AgentConfig{
			Instruction:      "base instruction",
			AdditionalPrompt: "extra prompt",
		},
		ResolvedSkills{},
		nil,
		MemoryContext{},
	)

	for _, profile := range []RoleProfile{profiles.Planner, profiles.Executor} {
		for _, want := range []string{
			"base instruction",
			"extra prompt",
			"## Environment",
			"## Default behavior",
			"Default to replying in Simplified Chinese",
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
			"Use shell only on the Aiden controller",
			"do not operate the target UI in screenshots",
			"recall_memory",
			"do not answer from general knowledge alone",
			"For text-only arithmetic, comparison, summarization, translation, or simple Q&A tasks",
			"do not observe, wait on, or operate the connected display",
			"suitable for TTS",
			"device-operator",
			"visible target UI",
			"wait_for_stable_screen screenshot",
			"Do not repeat the same click",
			"prefer search over blind scrolling",
			"Base visible UI actions on the latest screenshot",
			"Prefer direct or semantic tools",
			"repeated swipes or scrolling",
			"image_diff feedback",
			"request confirmation",
			"probe once with medium",
			"save_memory with app name, control location, direction, strength/distance, and delta",
		} {
			if !strings.Contains(profile.SystemPrompt, want) {
				t.Fatalf("%s system prompt missing %q:\n%s", profile.Name, want, profile.SystemPrompt)
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
				t.Fatalf("%s system prompt should not contain old localized guidance %q:\n%s", profile.Name, unwanted, profile.SystemPrompt)
			}
		}

		if strings.Contains(profile.SystemPrompt, "Use long-term memory if relevant") {
			t.Fatalf("%s system prompt should not contain legacy memory trigger:\n%s", profile.Name, profile.SystemPrompt)
		}
	}
}

func TestDefaultAgentBehaviorExcludesEnvironmentGuidance(t *testing.T) {
	behavior := defaultAgentBehavior(AgentConfig{})

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

func TestGlobalPromptsExcludeKeyboardTextInputDetails(t *testing.T) {
	for name, prompt := range map[string]string{
		"defaultAgentBehavior": defaultAgentBehavior(AgentConfig{}),
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
	behavior := defaultAgentBehavior(AgentConfig{})
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
	} {
		if strings.Contains(behavior, unexpected) {
			t.Fatalf("defaultAgentBehavior should not include migrated tool detail %q:\n%s", unexpected, behavior)
		}
	}
}

func TestRolePromptsRequireToolCallSpeechForExternalStateTools(t *testing.T) {
	enabled := true
	profiles := buildRoleProfiles(
		AgentConfig{
			VoiceToolCallSpeech: &enabled,
			TTSConfigured:       true,
		},
		ResolvedSkills{},
		nil,
		MemoryContext{},
	)

	for _, profile := range []RoleProfile{profiles.Planner, profiles.Executor} {
		for _, want := range []string{
			"The system prompt already includes the current date and weekday",
			"Do not call current_time for ordinary date or weekday questions",
			"when a precise clock time, timezone conversion, offset, timestamp, or elapsed-time calculation is required",
			"Put a brief assistant content message before every tool call that observes, waits for, reads, or changes external state",
			"screenshot, wait_for_stable_screen, quick_action, mouse_click, touch_gesture, keyboard_text, keyboard_tap, open_app, recall_memory",
			"assistant content is spoken aloud by the runtime before the tool runs",
			"Do not put the final answer in tool-call assistant content",
			"TTS is configured, so user-facing text output will be spoken aloud",
			"Do not duplicate the same sentence in tool-call assistant content and the final answer",
		} {
			if !strings.Contains(profile.SystemPrompt, want) {
				t.Fatalf("%s prompt missing tool-call speech requirement %q:\n%s", profile.Name, want, profile.SystemPrompt)
			}
		}
		if strings.Contains(profile.SystemPrompt, "user-visible tool") {
			t.Fatalf("%s prompt should not use ambiguous user-visible tool wording:\n%s", profile.Name, profile.SystemPrompt)
		}
		if strings.Contains(profile.SystemPrompt, "When voice tool-call speech is enabled") {
			t.Fatalf("%s prompt should not ask the model to reason about whether voice tool-call speech is enabled:\n%s", profile.Name, profile.SystemPrompt)
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
	if err := manager.Activate(nil, "planner"); err != nil {
		t.Fatal(err)
	}
	skills, err := manager.Resolve([]string{"planner"})
	if err != nil {
		t.Fatal(err)
	}
	profiles := buildRoleProfiles(AgentConfig{}, skills, nil, MemoryContext{})

	for _, profile := range []RoleProfile{profiles.Planner, profiles.Executor} {
		for _, want := range []string{
			"## Available skills",
			"- planner: Plan before acting",
			"## Active skills",
			"[planner] Make a plan.",
		} {
			if !strings.Contains(profile.SystemPrompt, want) {
				t.Fatalf("%s system prompt missing %q:\n%s", profile.Name, want, profile.SystemPrompt)
			}
		}
	}
}

func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// TestRuntimeContextStaysAfterCacheableRoleRules documents the prompt-cache
// effect of keeping volatile runtime context after the static role rules. Even
// when heartbeat timestamps change across turns, the cacheable static prefix is
// byte-identical.
func TestRuntimeContextStaysAfterCacheableRoleRules(t *testing.T) {
	hb1 := time.Date(2026, 6, 1, 2, 3, 4, 0, time.UTC)
	hb2 := hb1.Add(12 * time.Second) // next turn, fresh heartbeat
	status := func(hb time.Time) PhoneBridgeStatus {
		return PhoneBridgeStatus{Connected: true, Platform: "ios", LastHeartbeatAt: &hb}
	}

	ctx1 := phoneBridgeRuntimeContext(status(hb1))
	ctx2 := phoneBridgeRuntimeContext(status(hb2))
	if ctx1 == ctx2 {
		t.Fatalf("runtime context should still reflect heartbeat timestamp changes; test is not exercising volatile context")
	}

	build := func(rc string) RoleProfile {
		return buildRoleProfiles(
			AgentConfig{RuntimeContext: rc},
			ResolvedSkills{},
			nil,
			MemoryContext{},
		).Planner
	}
	profile1, profile2 := build(ctx1), build(ctx2)
	if profile1.SystemPromptCachePrefix != profile2.SystemPromptCachePrefix {
		t.Fatalf("cacheable static prefix should stay byte-identical across volatile runtime context:\nturn1:\n%s\nturn2:\n%s", profile1.SystemPromptCachePrefix, profile2.SystemPromptCachePrefix)
	}
	if profile1.SystemPromptDynamicSuffix == profile2.SystemPromptDynamicSuffix {
		t.Fatalf("dynamic suffix should reflect runtime context changes")
	}
	for _, unwanted := range []string{"## Runtime context", "last_heartbeat_at"} {
		if strings.Contains(profile1.SystemPromptCachePrefix, unwanted) {
			t.Fatalf("cacheable static prefix should not include volatile runtime marker %q:\n%s", unwanted, profile1.SystemPromptCachePrefix)
		}
	}

	base := build("").SystemPrompt
	insertAt := strings.Index(base, "## Role rules")
	if insertAt < 0 {
		t.Fatalf("base prompt missing role rules section:\n%s", base)
	}
	legacy := func(rc string) string {
		return base[:insertAt] + "## Runtime context\n" + rc + "\n\n" + base[insertAt:]
	}
	legacyTurn1, legacyTurn2 := legacy(ctx1), legacy(ctx2)
	if legacyTurn1 == legacyTurn2 {
		t.Fatalf("legacy reconstruction should differ across turns; test is not exercising the regression")
	}

	legacyPrefix := commonPrefixLen(legacyTurn1, legacyTurn2)
	if legacyPrefix >= len(profile1.SystemPromptCachePrefix) {
		t.Fatalf("legacy prompt prefix (%d) should be shorter than new cacheable static prefix (%d)", legacyPrefix, len(profile1.SystemPromptCachePrefix))
	}
	t.Logf("cacheable prefix bytes: new=%d/%d, legacy=%d/%d (%.1f%%)",
		len(profile1.SystemPromptCachePrefix), len(profile1.SystemPrompt),
		legacyPrefix, len(legacyTurn1),
		100*float64(legacyPrefix)/float64(len(legacyTurn1)))
}

func TestRolePromptsKeepVolatileSectionsAfterStaticRoleRules(t *testing.T) {
	profiles := buildRoleProfiles(
		AgentConfig{RuntimeContext: "Phone bridge status:\n- connected: true"},
		ResolvedSkills{},
		nil,
		MemoryContext{Planner: RoleMemoryContext{SessionSummary: "session memory tail"}},
	)

	for _, profile := range []RoleProfile{profiles.Planner, profiles.Executor} {
		roleRulesAt := strings.Index(profile.SystemPrompt, "## Role rules")
		runtimeAt := strings.Index(profile.SystemPrompt, "## Runtime context")
		if roleRulesAt < 0 || runtimeAt < 0 {
			t.Fatalf("%s prompt missing role rules or runtime context section:\n%s", profile.Name, profile.SystemPrompt)
		}
		if runtimeAt < roleRulesAt {
			t.Fatalf("%s prompt should place volatile runtime context after static role rules for prompt-cache stability:\n%s", profile.Name, profile.SystemPrompt)
		}
	}

	memoryAt := strings.Index(profiles.Planner.SystemPrompt, "session memory tail")
	planRuntimeAt := strings.Index(profiles.Planner.SystemPrompt, "## Runtime context")
	if memoryAt < 0 || memoryAt < planRuntimeAt {
		t.Fatalf("planner prompt should keep memory context as the trailing volatile section:\n%s", profiles.Planner.SystemPrompt)
	}
}

func TestRolePromptsIncludeRuntimeContext(t *testing.T) {
	runtimeContext := "Phone bridge status:\n- connected: true"
	profiles := buildRoleProfiles(
		AgentConfig{RuntimeContext: runtimeContext},
		ResolvedSkills{},
		nil,
		MemoryContext{},
	)

	for _, profile := range []RoleProfile{profiles.Planner, profiles.Executor} {
		if !strings.Contains(profile.SystemPrompt, "## Runtime context\n"+runtimeContext) {
			t.Fatalf("%s system prompt missing runtime context:\n%s", profile.Name, profile.SystemPrompt)
		}
	}
}

func TestPhoneBridgeRuntimeContextConnected(t *testing.T) {
	lastHeartbeat := time.Date(2026, 6, 1, 2, 3, 4, 0, time.UTC)
	got := phoneBridgeRuntimeContext(PhoneBridgeStatus{
		Connected:       true,
		Platform:        "ios",
		LastHeartbeatAt: &lastHeartbeat,
	})

	for _, want := range []string{
		"Phone bridge status:",
		"- connected: true",
		"- platform: ios",
		"- last_heartbeat_at: 2026-06-01T02:03:04Z",
		"The phone companion app is connected",
		"Use open_app as the primary path",
		"clipboard, calendar, contacts, and notification tools are available",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtime context missing %q:\n%s", want, got)
		}
	}
}

func TestPhoneBridgeRuntimeContextBackgroundAppGuidesDynamicIslandRecovery(t *testing.T) {
	lastHeartbeat := time.Date(2026, 6, 1, 2, 3, 4, 0, time.UTC)
	got := phoneBridgeRuntimeContext(PhoneBridgeStatus{
		Connected:            true,
		Platform:             "ios",
		LastHeartbeatAt:      &lastHeartbeat,
		AppState:             "background",
		ReturnEntry:          "dynamic_island",
		ReturnEntryAvailable: testBoolPtr(true),
	})

	for _, want := range []string{
		"- app_state: background",
		"- return_entry: dynamic_island available=true",
		"The Aiden companion app is backgrounded or inactive",
		"will first tap the Aiden Dynamic Island entry",
		"then send the command",
		"For lock-screen Live Activity entries, use screenshot/HID fallback",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtime context missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Use open_app as the primary path") {
		t.Fatalf("backgrounded app context should not present direct open_app as immediately available:\n%s", got)
	}
}

func TestPhoneBridgeRuntimeContextIncludesPhoneEnvironment(t *testing.T) {
	lastHeartbeat := time.Date(2026, 6, 1, 2, 3, 4, 0, time.UTC)
	environmentUpdatedAt := time.Date(2026, 6, 1, 2, 3, 5, 0, time.UTC)
	got := phoneBridgeRuntimeContext(PhoneBridgeStatus{
		Connected:            true,
		Platform:             "ios",
		LastHeartbeatAt:      &lastHeartbeat,
		EnvironmentUpdatedAt: &environmentUpdatedAt,
		Environment: &PhoneEnvironment{
			CapturedAt:      "2026-06-01T02:03:05Z",
			Platform:        "ios",
			SystemName:      "iOS",
			SystemVersion:   "18.5",
			IsTablet:        testBoolPtr(false),
			Locale:          "zh-Hans-CN",
			Language:        "zh",
			Region:          "CN",
			TimeZone:        "Asia/Shanghai",
			UTCOffset:       "+08:00",
			Uses24HourClock: testBoolPtr(true),
			Manufacturer:    "Apple",
			Brand:           "Apple",
			Model:           "iPhone16,2",
			DeviceName:      "User device",
			Screen:          PhoneScreenInfo{WidthPixels: testIntPtr(1179), HeightPixels: testIntPtr(2556), Scale: testFloatPtr(3)},
			Battery:         PhoneBatteryInfo{Level: testFloatPtr(0.87), Charging: testBoolPtr(true), State: "charging"},
			SystemApps:      []AvailableAppInfo{{Name: "Camera", Available: true}, {Name: "Contacts", Available: true}},
			ThirdPartyApps:  []AvailableAppInfo{{Name: "WeChat", Available: true}, {Name: "Douyin", Available: false}, {Name: "Alipay", Available: true}},
		},
	})

	for _, want := range []string{
		"device environment is available in World State for structured use",
		"- environment_updated_at: 2026-06-01T02:03:05Z",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtime context missing %q:\n%s", want, got)
		}
	}
	for _, notWant := range []string{
		"Phone environment summary:",
		"- system:",
		"- locale:",
		"- screen:",
		"confirmed_launchable_third_party_apps",
		"User device",
		"- device:",
		"- battery:",
		"- system_apps:",
	} {
		if strings.Contains(got, notWant) {
			t.Fatalf("runtime context should not include %q:\n%s", notWant, got)
		}
	}
}

func TestPhoneBridgeRuntimeContextDisconnected(t *testing.T) {
	got := phoneBridgeRuntimeContext(PhoneBridgeStatus{})
	if got != "" {
		t.Fatalf("disconnected phone bridge should not add runtime context, got:\n%s", got)
	}
}

func TestPhoneBridgeRuntimeContextDisconnectedBackgroundAppGuidesRecovery(t *testing.T) {
	got := phoneBridgeRuntimeContext(PhoneBridgeStatus{
		Connected:            false,
		Platform:             "ios",
		AppState:             "background",
		ReturnEntry:          "live_activity",
		ReturnEntryAvailable: testBoolPtr(true),
	})

	for _, want := range []string{
		"- connected: false",
		"- platform: ios",
		"- app_state: background",
		"- return_entry: live_activity available=true",
		"Phone Bridge commands may time out until Aiden returns to foreground",
		"return_entry=dynamic_island",
		"For lock-screen Live Activity entries, use screenshot/HID fallback",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtime context missing %q:\n%s", want, got)
		}
	}
}

func testBoolPtr(v bool) *bool        { return &v }
func testIntPtr(v int) *int           { return &v }
func testFloatPtr(v float64) *float64 { return &v }
