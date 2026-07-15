package agent

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func testPromptProfile(cfg AgentConfig) RoleProfile {
	return buildProfile(cfg, NewSkillManager(NewSkillIndex()), nil, agentRoleRules())
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
		"Default to replying in Simplified Chinese",
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
		"Use bridge_open_app as the primary path",
		"bridge_clipboard, bridge_calendar, bridge_contacts, and bridge_notification tools are available",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtime context missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n\n- "+phoneBridgeDisconnectedRecoveryGuidance) {
		t.Fatalf("runtime context contains a blank line before disconnected recovery guidance:\n%s", got)
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
	if strings.Contains(got, "Use bridge_open_app as the primary path") {
		t.Fatalf("backgrounded app context should not present direct bridge_open_app as immediately available:\n%s", got)
	}
}

func TestPhoneBridgeRuntimeContextPiPBackgroundDisablesOpenApp(t *testing.T) {
	lastHeartbeat := time.Date(2026, 6, 1, 2, 3, 4, 0, time.UTC)
	enabled := true
	got := phoneBridgeRuntimeContext(PhoneBridgeStatus{
		Connected:            false,
		Platform:             "ios",
		LastHeartbeatAt:      &lastHeartbeat,
		AppState:             "background",
		AppStateUpdatedAt:    &lastHeartbeat,
		ReturnEntry:          "dynamic_island",
		ReturnEntryAvailable: testBoolPtr(true),
		PipBridgeEnabled:     &enabled,
	})

	for _, want := range []string{
		"- pip_bridge:",
		"available=false hidden_by_pip=true",
		"PiP Bridge mode is enabled while Aiden is backgrounded",
		"iOS gives PiP priority over the Dynamic Island",
		"Dynamic Island return entry is not visible",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtime context missing %q:\n%s", want, got)
		}
	}
	for _, notWant := range []string{
		"will first tap the Aiden Dynamic Island entry",
		"Use bridge_open_app as the primary path",
		"bridge_open_app is intentionally unavailable",
		"Use bridge_clipboard, bridge_calendar, bridge_contacts, and bridge_notification only",
	} {
		if strings.Contains(got, notWant) {
			t.Fatalf("PiP background context should not include %q:\n%s", notWant, got)
		}
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
		"call screenshot first",
		"then try search_launch_app",
		"request_human_handoff only after",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtime context missing %q:\n%s", want, got)
		}
	}
}

func testBoolPtr(v bool) *bool        { return &v }
func testIntPtr(v int) *int           { return &v }
func testFloatPtr(v float64) *float64 { return &v }
