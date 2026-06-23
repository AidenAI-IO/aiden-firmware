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

func TestWaitForWakeupExposedToAgentAndToolLab(t *testing.T) {
	if !isHTTPToolExposed("wait_for_wakeup") {
		t.Fatal("expected wait_for_wakeup in Tool Lab HTTP catalog")
	}
	if !isAgentToolExposed("wait_for_wakeup") {
		t.Fatal("expected wait_for_wakeup available to conversational agent")
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

func TestUnknownToolsFailClosedForHTTP(t *testing.T) {
	if isHTTPToolExposed("unregistered_tool") {
		t.Fatal("expected unregistered tool hidden from HTTP catalog")
	}

	spec := NewToolSpec(&stubTool{name: "unregistered_tool", description: "Unregistered."})
	if spec.HTTPExposed {
		t.Fatal("expected unregistered tool spec hidden from HTTP catalog")
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

func TestResolveToolsIncludesPhoneBridgeToolsWhenDisconnected(t *testing.T) {
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
			t.Fatalf("resolveTools missing disconnected phone bridge tool %s: %v", want, names)
		}
	}
}

func TestResolveToolsIncludesPhoneBridgeToolsWhenConnected(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{},
		nil,
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	bridge := NewPhoneBridge(nil)
	bridge.connected = true
	runtime.tools.RegisterPhoneBridge(bridge)

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
			t.Fatalf("resolveTools missing connected phone bridge tool %s: %v", want, names)
		}
	}
}

func TestResolveToolsIncludesAllowedPhoneBridgeToolWhenDisconnected(t *testing.T) {
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
	t.Fatalf("resolveTools with allowed_tools missing disconnected open_app: %v", names)
}

func TestResolveToolsIncludesAllowedPhoneBridgeToolWhenConnected(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{},
		nil,
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	bridge := NewPhoneBridge(nil)
	bridge.connected = true
	runtime.tools.RegisterPhoneBridge(bridge)

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
	t.Fatalf("resolveTools with allowed_tools missing connected open_app: %v", names)
}
