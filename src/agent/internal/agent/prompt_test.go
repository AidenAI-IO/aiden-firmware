package agent

import (
	"strings"
	"testing"
)

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
		"默认用简洁自然的英文回答",
		"Aiden 硬件控制器",
		"运行时 OS 是 Linux",
		"不一定是截图中显示的设备",
		"shell、本地文件、进程和系统命令只作用于 Aiden 硬件控制器",
		"目标设备和目标 OS 根据截图、连接元数据、谨慎行为探测或用户输入推断",
		"弱先验，不是已检测事实",
		"shell 工具只在 Aiden 硬件控制器上执行",
		"不会操作截图中的目标 UI",
		"recall_memory",
		"不要直接凭常识回答",
		"适合 TTS",
		"device-operator",
		"可见目标 UI",
		"系统特定自动化",
		"osascript",
		"PowerShell",
		"不要重复同一个点击",
		"优先使用搜索",
		"US-keyboard ASCII",
		"优先使用 coord_space:\"normalized\"",
		"仅在已校准时使用 coord_space:\"pixel\"",
		"type \"back\"",
		"type \"home\"",
		"先请求确认",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("system message missing %q:\n%s", want, msg)
		}
	}

	for _, unwanted := range []string{
		"需要中文时，改用拼音",
		"不要因为没有单独的拨打电话工具就说做不到",
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
