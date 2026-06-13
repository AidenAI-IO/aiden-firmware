package agent

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPromptIncludesCurrentChineseDate(t *testing.T) {
	originalNow := promptNow
	promptNow = func() time.Time {
		return time.Date(2026, time.June, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	}
	t.Cleanup(func() { promptNow = originalNow })

	want := "今天的日期是: 2026年06月01日 星期一"
	msg := buildFunctionAgentSystemMessage(AgentConfig{}, ResolvedSkills{})
	if !strings.Contains(msg, want) {
		t.Fatalf("function system message missing current date %q:\n%s", want, msg)
	}
}

func TestPromptIncludesRealHostRuntimeInfo(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("Hostname() error = %v", err)
	}
	operatingSystem := mustUname(t, "-s")
	architecture := mustUname(t, "-m")
	wantLine := "Host: os=" + operatingSystem + ", hostname=" + hostname + ", arch=" + architecture
	wantEnvironmentLine := "- You run on the Aiden hardware controller (" + wantLine + "); you are not the device shown in screenshots."

	msg := buildFunctionAgentSystemMessage(AgentConfig{}, ResolvedSkills{})
	if !strings.Contains(msg, wantEnvironmentLine) {
		t.Fatalf("function system message missing host info in environment guidance %q:\n%s", wantEnvironmentLine, msg)
	}
	if strings.Contains(msg, "kernel=") {
		t.Fatalf("system message should not include kernel info:\n%s", msg)
	}
}

func TestFunctionAgentSystemMessageIdentifiesAidenAI(t *testing.T) {
	msg := buildFunctionAgentSystemMessage(AgentConfig{}, ResolvedSkills{})
	if !strings.HasPrefix(msg, "You are Aiden AI agent.\n") {
		t.Fatalf("system message should identify Aiden AI agent, got:\n%s", msg)
	}
	if strings.Contains(msg, "You are agent.\n") {
		t.Fatalf("system message should not use generic agent identity:\n%s", msg)
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

func TestFunctionAgentSystemMessageIncludesGlobalEnvironmentAndDeviceGuidance(t *testing.T) {
	msg := buildFunctionAgentSystemMessage(
		AgentConfig{
			Instruction:      "base instruction",
			AdditionalPrompt: "extra prompt",
		},
		ResolvedSkills{},
	)

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
		if !strings.Contains(msg, want) {
			t.Fatalf("system message missing %q:\n%s", want, msg)
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
		if strings.Contains(msg, unwanted) {
			t.Fatalf("system message should not contain old localized guidance %q:\n%s", unwanted, msg)
		}
	}

	if strings.Contains(msg, "Use long-term memory if relevant") {
		t.Fatalf("system message should not contain benchmark-specific memory trigger:\n%s", msg)
	}
}

func TestCombinedAgentInstructionFallsBackWhenEmpty(t *testing.T) {
	if got := combinedAgentInstruction(AgentConfig{}); got != "" {
		t.Fatalf("combinedAgentInstruction() = %q, want empty string", got)
	}
}

func TestFunctionAgentSystemMessageGuidesSkillCatalogAndPreloadedSkills(t *testing.T) {
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
	msg := buildFunctionAgentSystemMessage(AgentConfig{}, skills)

	for _, want := range []string{
		"## Skills",
		"Skills 是可复用的操作流程，不是 memory",
		"适合 App 操作、排障、设备流程、表单/授权/支付、重复任务",
		"### 可用信息",
		"### 使用规则",
		"### 维护规则",
		"Available skills:",
		"- planner: Plan before acting",
		"Active skills:",
		"[planner] Make a plan.",
		"行动前先查看 Available skills",
		"如果 Available skills 不够判断，再用 skill_list 搜索",
		"找到相关 skill 后，先 skill_read，再执行",
		"不要读取所有 skill",
		"已加载 skill 是本次任务 SOP",
		"用户指令、安全规则、当前屏幕状态或工具结果冲突",
		"实际按某个 skill 执行后，如果有 skill_mark_used 工具，就用该 skill 名称调用它",
		"只有可复用流程才写入或更新 skill",
		"不要保存一次性进度、临时状态、秘密、原始日志或个人事实",
		"修改已有 skill 前必须先 skill_read",
		"小改优先 skill_manage action=patch",
		"skill_manage 只能维护 configDir/skills",
		"不要直接修改 bundled source 或 configDir/skill-state 文件",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("system message missing %q:\n%s", want, msg)
		}
	}
}

func TestFunctionAgentSystemMessageIncludesRuntimeContext(t *testing.T) {
	runtimeContext := "Phone bridge status:\n- connected: true"
	msg := buildFunctionAgentSystemMessage(
		AgentConfig{RuntimeContext: runtimeContext},
		ResolvedSkills{},
	)

	if !strings.Contains(msg, "Runtime context:\n"+runtimeContext) {
		t.Fatalf("system message missing runtime context:\n%s", msg)
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
		"Phone environment summary:",
		"- environment_updated_at: 2026-06-01T02:03:05Z",
		"- system: iOS, 18.5, tablet=false",
		"- locale: zh-Hans-CN, language=zh, region=CN, timezone=Asia/Shanghai, utc_offset=+08:00, 24h_clock=true",
		"- screen: 1179x2556 px, scale=3.00",
		"- confirmed_launchable_third_party_apps: WeChat, Alipay",
		"apps not listed may still be installed or openable",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtime context missing %q:\n%s", want, got)
		}
	}
	for _, notWant := range []string{
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

	for _, want := range []string{
		"Phone bridge status:",
		"- connected: false",
		"The phone companion app is not connected",
		"Do not assume open_app, clipboard, calendar, contacts, or notification tools can control the phone",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtime context missing %q:\n%s", want, got)
		}
	}
}

func testBoolPtr(v bool) *bool        { return &v }
func testIntPtr(v int) *int           { return &v }
func testFloatPtr(v float64) *float64 { return &v }
