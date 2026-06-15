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

	want := "Current date: 2026-06-01 (2026年06月01日 星期一)"
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
			"### Environment",
			"### Default Behavior",
			"Default to replying in Simplified Chinese",
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
			"Use wait_for_stable_screen only while operating a visible target UI",
			"Do not call it for text-only reasoning",
			"Do not repeat the same click",
			"prefer search over blind scrolling",
			"US-keyboard ASCII",
			"prefer the audio_volume tool",
			"Prefer coord_space:\"normalized\"",
			"Use coord_space:\"pixel\" only when calibrated",
			"prefer quick_action",
			"quick_action {\"action\":\"back\",\"platform\":\"android\"}",
			"Fall back to lower-level tools",
			"type back/home",
			"request confirmation",
			"Swipe strategy",
			"Precision swipe loop",
			"probe once with medium",
			"strength/direction -> UI movement",
			"Downshift when close to the target",
			"if oscillating, use only tiny",
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
			t.Fatalf("%s system prompt should not contain benchmark-specific memory trigger:\n%s", profile.Name, profile.SystemPrompt)
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

func testBoolPtr(v bool) *bool        { return &v }
func testIntPtr(v int) *int           { return &v }
func testFloatPtr(v float64) *float64 { return &v }
