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
	msg := buildFunctionAgentSystemMessage(AgentConfig{}, ResolvedSkills{}, nil)
	if !strings.Contains(msg, want) {
		t.Fatalf("function system message missing current date %q:\n%s", want, msg)
	}

	prompt := buildPrompt("aiden", AgentConfig{}, ResolvedSkills{}, nil)
	if !strings.Contains(prompt.Template, "{{.current_date}}") {
		t.Fatalf("ReAct prompt template should include current_date variable:\n%s", prompt.Template)
	}
	if got := prompt.PartialVariables["current_date"]; got != want {
		t.Fatalf("current_date partial = %q, want %q", got, want)
	}
}

func TestPromptIncludesRealHostRuntimeInfo(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("Hostname() error = %v", err)
	}
	operatingSystem := mustUname(t, "-s")
	architecture := mustUname(t, "-m")
	wantLine := "宿主机: os=" + operatingSystem + ", hostname=" + hostname + ", arch=" + architecture
	wantEnvironmentLine := "- 你运行在 Aiden 硬件控制器上（" + wantLine + "）；不是截图中显示的设备。"

	msg := buildFunctionAgentSystemMessage(AgentConfig{}, ResolvedSkills{}, nil)
	if !strings.Contains(msg, wantEnvironmentLine) {
		t.Fatalf("function system message missing host info in environment guidance %q:\n%s", wantEnvironmentLine, msg)
	}
	if strings.Contains(msg, "kernel=") {
		t.Fatalf("system message should not include kernel info:\n%s", msg)
	}

	prompt := buildPrompt("aiden", AgentConfig{}, ResolvedSkills{}, nil)
	if strings.Contains(prompt.Template, "{{.host_runtime_info}}") {
		t.Fatalf("ReAct prompt template should not keep a separate host_runtime_info variable:\n%s", prompt.Template)
	}
	if _, ok := prompt.PartialVariables["host_runtime_info"]; ok {
		t.Fatalf("host_runtime_info should be folded into default_behavior partial: %#v", prompt.PartialVariables["host_runtime_info"])
	}
	defaultBehavior, ok := prompt.PartialVariables["default_behavior"].(string)
	if !ok {
		t.Fatalf("default_behavior partial has type %T, want string", prompt.PartialVariables["default_behavior"])
	}
	if !strings.Contains(defaultBehavior, wantEnvironmentLine) {
		t.Fatalf("default_behavior partial missing host info in environment guidance %q:\n%s", wantEnvironmentLine, defaultBehavior)
	}
}

func TestFunctionAgentSystemMessageIdentifiesAidenAI(t *testing.T) {
	msg := buildFunctionAgentSystemMessage(AgentConfig{}, ResolvedSkills{}, nil)
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
		nil,
	)

	for _, want := range []string{
		"base instruction",
		"extra prompt",
		"默认用简体中文回答",
		"Aiden 硬件控制器",
		"不是截图中显示的设备",
		"shell、本地文件、进程和系统命令只作用于 Aiden 硬件控制器",
		"不要根据宿主机的 OS 或架构推断目标设备信息",
		"不要用本地系统命令代替目标控制工具",
		"目标设备和目标 OS 根据截图、连接元数据、进行行为探测或用户输入推断",
		"弱先验，不是已检测事实",
		"shell 工具只在 Aiden 硬件控制器上执行",
		"不会操作截图中的目标 UI",
		"recall_memory",
		"不要直接凭常识回答",
		"适合 TTS",
		"device-operator",
		"可见目标 UI",
		"不要重复同一个点击",
		"优先使用搜索",
		"US-keyboard ASCII",
		"优先使用 coord_space:\"normalized\"",
		"仅在已校准时使用 coord_space:\"pixel\"",
		"type \"back\"",
		"type \"home\"",
		"先请求确认",
		"滑动操作策略",
		"精准滑动闭环",
		"先用 medium 做一次试探滑动",
		"strength/direction -> UI移动量",
		"接近目标必须降档",
		"反复横跳，只用 tiny",
		"save_memory 记录 app 名、控件位置、方向、strength/distance、对应变化量",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("system message missing %q:\n%s", want, msg)
		}
	}

	for _, unwanted := range []string{
		"默认用简洁自然的英文回答",
		"需要中文时，改用拼音",
		"不要因为没有单独的拨打电话工具就说做不到",
		"osascript",
		"AppleScript",
		"PowerShell",
		"xdotool",
		"平台包管理器",
		"运行时 OS 是 Linux",
		"不一定是截图中显示的设备",
		"kernel=",
		"宿主机的 OS、内核或架构",
		"谨慎行为探测或用户输入推断",
	} {
		if strings.Contains(msg, unwanted) {
			t.Fatalf("system message should not contain old localized guidance %q:\n%s", unwanted, msg)
		}
	}

	if strings.Contains(msg, "Use long-term memory if relevant") {
		t.Fatalf("system message should not contain benchmark-specific memory trigger:\n%s", msg)
	}
}

func TestReActPromptRequiresJSONToolInput(t *testing.T) {
	prompt := buildPrompt("aiden", AgentConfig{}, ResolvedSkills{}, nil)
	if !strings.Contains(prompt.Template, "Action Input: a valid JSON string for the selected tool") {
		t.Fatalf("ReAct prompt should require JSON tool input:\n%s", prompt.Template)
	}
	if strings.Contains(prompt.Template, "Action Input: a plain string input") {
		t.Fatalf("ReAct prompt should not describe tool input as plain string:\n%s", prompt.Template)
	}
}

func TestCombinedAgentInstructionFallsBackWhenEmpty(t *testing.T) {
	if got := combinedAgentInstruction(AgentConfig{}); got != "(none)" {
		t.Fatalf("combinedAgentInstruction() = %q, want (none)", got)
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
	msg := buildFunctionAgentSystemMessage(AgentConfig{}, skills, nil)

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
		nil,
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
		"clipboard and calendar tools are available",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtime context missing %q:\n%s", want, got)
		}
	}
}

func TestPhoneBridgeRuntimeContextDisconnected(t *testing.T) {
	got := phoneBridgeRuntimeContext(PhoneBridgeStatus{})

	for _, want := range []string{
		"Phone bridge status:",
		"- connected: false",
		"The phone companion app is not connected",
		"Do not assume open_app, clipboard, or calendar tools can control the phone",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtime context missing %q:\n%s", want, got)
		}
	}
}
