package agent

import (
	"strings"
	"testing"
)

func newRuntimeWithTextEntryTools() *Runtime {
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools.RegisterEnterTextInFieldTool(&testModelResolver{model: &scriptedModel{}}, nil)
	return NewRuntimeWithDeps(
		Config{},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		tools,
		NewSkillIndex(),
	)
}

func TestQuickActionExposedToAgentAndToolLab(t *testing.T) {
	if !isAgentToolExposed("quick_action") {
		t.Fatal("expected quick_action available to conversational agent")
	}
	if !isAgentToolExposed("keyboard_tap") {
		t.Fatal("expected keyboard_tap available to agent")
	}
}

func TestWaitForWakeupExposedToAgentAndToolLab(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{},
		nil,
		NewMemoryManager(""),
		NewBuiltinToolSet(
			HIDConfig{},
			AudioConfig{},
			SearchConfig{},
			ProxyConfig{},
			WithWaitForWakeupController(NewWaitForWakeupController()),
		),
		NewSkillIndex(),
	)
	if _, ok := runtime.ToolDescriptorByName("wait_for_wakeup"); !ok {
		t.Fatal("expected wait_for_wakeup in Tool Lab HTTP catalog")
	}
	if !isAgentToolExposed("wait_for_wakeup") {
		t.Fatal("expected wait_for_wakeup available to conversational agent")
	}
}

func TestPhoneBridgeToolsExposedToAgent(t *testing.T) {
	for _, name := range []string{"open_app", "clipboard", "calendar", "contacts", "notification"} {
		if !isAgentToolExposed(name) {
			t.Fatalf("expected %s available to conversational agent", name)
		}
	}
}

func TestAllToolsExposedOverHTTP(t *testing.T) {
	// Every registered tool, including phone bridge and unregistered tools,
	// is now exposed through the HTTP catalog.
	spec := NewToolSpec(&stubTool{name: "unregistered_tool", description: "Unregistered."})
	if spec.Name != "unregistered_tool" {
		t.Fatalf("unexpected tool spec name: %q", spec.Name)
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

func TestResolveToolsKeepsPhoneBridgeToolsVisibleWhenDisconnected(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	runtime.tools.RegisterPhoneBridge(NewPhoneBridge(nil))

	tools := runtime.resolveTools(ResolvedSkills{})
	names := toolNamesFromTools(tools)
	for _, want := range []string{"open_app", "search_launch_app", "clipboard", "calendar", "contacts", "notification", "enter_text_via_bridge"} {
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("resolveTools missing disconnected bridge recovery tool %s: %v", want, names)
		}
	}
}

func TestResolveToolsIncludesRestorablePhoneBridgeToolsWhenDisconnected(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	bridge := NewPhoneBridge(nil)
	bridge.platform = "ios"
	bridge.appState = "background"
	bridge.returnEntry = "dynamic_island"
	bridge.returnEntrySeen = true
	bridge.returnEntryOK = true
	runtime.tools.RegisterPhoneBridge(bridge)

	tools := runtime.resolveTools(ResolvedSkills{})
	names := toolNamesFromTools(tools)
	for _, want := range []string{"open_app", "clipboard", "calendar", "contacts", "notification", "enter_text_via_bridge"} {
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("resolveTools missing restorable phone bridge tool %s: %v", want, names)
		}
	}
}

func TestResolveToolsIncludesAllowedRestorablePhoneBridgeToolWhenDisconnected(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	bridge := NewPhoneBridge(nil)
	bridge.platform = "ios"
	bridge.appState = "background"
	bridge.returnEntry = "dynamic_island"
	bridge.returnEntrySeen = true
	bridge.returnEntryOK = true
	runtime.tools.RegisterPhoneBridge(bridge)

	tools := runtime.resolveTools(ResolvedSkills{
		HasToolRestriction: true,
		AllowedTools:       map[string]struct{}{"contacts": {}},
	})
	names := toolNamesFromTools(tools)
	for _, name := range names {
		if name == "contacts" {
			return
		}
	}
	t.Fatalf("resolveTools with allowed_tools missing restorable contacts: %v", names)
}

func TestResolveToolsIncludesPhoneBridgeToolsWhenConnected(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
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
	runtime := newRuntimeWithTextEntryTools()
	runtime.tools.RegisterPhoneBridge(NewPhoneBridge(nil))

	tools := runtime.resolveTools(ResolvedSkills{
		HasToolRestriction: true,
		AllowedTools:       map[string]struct{}{"clipboard": {}},
	})
	names := toolNamesFromTools(tools)
	for _, name := range names {
		if name == "clipboard" {
			return
		}
	}
	t.Fatalf("resolveTools with allowed_tools missing disconnected clipboard: %v", names)
}

func TestResolveToolsIncludesAllowedOpenAppWhenDisconnected(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
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
	runtime := newRuntimeWithTextEntryTools()
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

func TestGeneratedToolSkillPrefersBridgeTextEntryForPhoneApps(t *testing.T) {
	skill := buildHTTPToolSkillMarkdown("test", "test skill", "http://example.local:8080", nil)
	for _, want := range []string{
		"use `enter_text_in_field`",
		"batch Aiden app-side work before target-app navigation",
		"write the final clipboard text before finishing that lookup step",
		"Treat `open_app(target)` as the phase boundary",
		"without reopening Aiden",
		"Do not rely on background WebSocket/HTTP clipboard delivery as the primary path",
	} {
		if !strings.Contains(skill, want) {
			t.Fatalf("generated HTTP tool skill missing %q:\n%s", want, skill)
		}
	}
}
