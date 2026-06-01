package agent

import (
	"strings"
	"testing"
)

func TestFunctionAgentSystemMessageIncludesDefaultChinesePhoneAndGestureGuidance(t *testing.T) {
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
		"recall_memory",
		"不要直接凭常识",
		"TTS",
		"拨打电话",
		"没有单独的拨打电话工具",
		"不要连续重复同一个点击",
		"优先使用系统搜索",
		"不能直接输入中文",
		"优先使用 coord_space:\"normalized\"",
		"不要使用 coord_space:\"pixel\"",
		"type \"back\"",
		"type \"home\"",
		"start.x=0.001",
		"start.y=0.999",
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

	if strings.Contains(msg, "Use long-term memory if relevant") {
		t.Fatalf("system message should not contain benchmark-specific memory trigger:\n%s", msg)
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
