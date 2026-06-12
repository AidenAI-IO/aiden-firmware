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

func TestPhoneBridgeToolsExposedToAgentOnly(t *testing.T) {
	for _, name := range []string{"open_app", "clipboard", "calendar", "contacts", "notification"} {
		if isHTTPToolExposed(name) {
			t.Fatalf("expected %s hidden from Tool Lab HTTP catalog", name)
		}
		if !isAgentToolExposed(name) {
			t.Fatalf("expected %s available to conversational agent", name)
		}
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

func TestResolveToolsIncludesPhoneBridgeToolsAfterRegistration(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{},
		nil,
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	runtime.tools.RegisterPhoneBridge(NewPhoneBridge(nil))

	tools := runtime.resolveTools(ResolvedSkills{})
	names := toolNamesFromTools(tools)
	for _, want := range []string{"open_app", "clipboard", "calendar", "contacts", "notification"} {
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("resolveTools missing %s: %v", want, names)
		}
	}
}

func TestResolveToolsIncludesAllowedPhoneBridgeTool(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{},
		nil,
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	runtime.tools.RegisterPhoneBridge(NewPhoneBridge(nil))

	tools := runtime.resolveTools(ResolvedSkills{
		HasToolRestriction: true,
		AllowedTools:       map[string]struct{}{"open_app": {}},
	})
	names := toolNamesFromTools(tools)
	for _, name := range names {
		if name == "open_app" {
			return
		}
	}
	t.Fatalf("resolveTools with allowed_tools missing open_app: %v", names)
}
