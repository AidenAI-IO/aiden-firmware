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
