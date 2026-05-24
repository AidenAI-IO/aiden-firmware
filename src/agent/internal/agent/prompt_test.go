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
		"TTS",
		"拨打电话",
		"没有单独的拨打电话工具",
		"type \"back\"",
		"type \"home\"",
		"start.x=0.001",
		"start.y=0.999",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("system message missing %q:\n%s", want, msg)
		}
	}
}

func TestCombinedAgentInstructionFallsBackWhenEmpty(t *testing.T) {
	if got := combinedAgentInstruction(AgentConfig{}); got != "(none)" {
		t.Fatalf("combinedAgentInstruction() = %q, want (none)", got)
	}
}
