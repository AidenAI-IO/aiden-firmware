package agent

import "testing"

func TestQuickActionExposedInToolLabButNotAgent(t *testing.T) {
	if !isHTTPToolExposed("quick_action") {
		t.Fatal("expected quick_action in Tool Lab HTTP catalog")
	}
	if isAgentToolExposed("quick_action") {
		t.Fatal("expected quick_action withheld from conversational agent")
	}
	if !isAgentToolExposed("keyboard_tap") {
		t.Fatal("expected keyboard_tap available to agent")
	}
}

func TestResolveToolsOmitsExperimentalQuickAction(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{},
		nil,
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	tools := runtime.resolveTools(ResolvedSkills{})
	names := toolNamesFromTools(tools)
	for _, name := range names {
		if name == "quick_action" {
			t.Fatalf("resolveTools included quick_action: %v", names)
		}
	}
}
