package agent

import "testing"

func TestQuickActionExposedToAgentAndToolLab(t *testing.T) {
	if !isHTTPToolExposed("quick_action") {
		t.Fatal("expected quick_action in Tool Lab HTTP catalog")
	}
	if !isAgentToolExposed("quick_action") {
		t.Fatal("expected quick_action available to conversational agent")
	}
	if !isAgentToolExposed("keyboard_tap") {
		t.Fatal("expected keyboard_tap available to agent")
	}
}

func TestResolveToolsIncludesQuickAction(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{},
		nil,
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	tools := runtime.resolveTools(ResolvedSkills{})
	names := toolNamesFromTools(tools)
	found := false
	for _, name := range names {
		if name == "quick_action" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("resolveTools missing quick_action: %v", names)
	}
}
