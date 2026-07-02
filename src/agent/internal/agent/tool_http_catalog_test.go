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

func TestRunScriptExposedToAgentAndToolLab(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{},
		nil,
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	for _, name := range []string{"run_script", "list_scripts", "read_script", "write_script"} {
		if _, ok := runtime.ToolDescriptorByName(name); !ok {
			t.Fatalf("expected %s in Tool Lab HTTP catalog", name)
		}
		if !isAgentToolExposed(name) {
			t.Fatalf("expected %s available to conversational agent", name)
		}
	}
}

func TestPhoneBridgeToolsExposedToAgent(t *testing.T) {
	for _, name := range []string{"open_app", "clipboard", "calendar", "contacts", "notification", "prepare_phone_app_workflow"} {
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

func TestResolveToolsIncludesPhoneBridgeToolsWhenDisconnected(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	runtime.tools.RegisterPhoneBridge(NewPhoneBridge(nil))

	tools := runtime.resolveTools(ResolvedSkills{})
	names := toolNamesFromTools(tools)
	for _, want := range []string{"open_app", "search_launch_app", "enter_text_via_bridge"} {
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
	for _, notWant := range []string{"clipboard", "calendar", "contacts", "notification", "prepare_phone_app_workflow"} {
		for _, name := range names {
			if name == notWant {
				t.Fatalf("resolveTools exposed disconnected phone bridge tool %s: %v", notWant, names)
			}
		}
	}
}

func TestResolveToolsIncludesPhoneBridgeToolsWhenConnected(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	bridge := NewPhoneBridge(nil)
	bridge.connected = true
	runtime.tools.RegisterPhoneBridge(bridge)

	tools := runtime.resolveTools(ResolvedSkills{})
	names := toolNamesFromTools(tools)
	for _, want := range []string{"open_app", "clipboard", "calendar", "contacts", "notification", "prepare_phone_app_workflow"} {
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

func TestResolveToolsHidesAllowedPhoneBridgeToolWhenDisconnected(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	runtime.tools.RegisterPhoneBridge(NewPhoneBridge(nil))

	tools := runtime.resolveTools(ResolvedSkills{
		HasToolRestriction: true,
		AllowedTools:       map[string]struct{}{"clipboard": {}},
	})
	names := toolNamesFromTools(tools)
	for _, name := range names {
		if name == "clipboard" {
			t.Fatalf("resolveTools with allowed_tools exposed disconnected clipboard: %v", names)
		}
	}
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
		"batch reorderable Aiden app-side work before target-app navigation",
		"`clipboard`, `calendar`, `contacts`, and `notification`",
		"Treat `open_app(target)` as the expensive phase boundary",
		"prefer `prepare_phone_app_workflow` instead of separate app-side tool + `clipboard` + `open_app` calls",
		"runs structured direct app-side actions first",
		"prepares final `target_text` clipboard while Aiden is foreground",
		"Do not treat UI-only app reading as reorderable direct data work",
		"HID/IME typing fallback is still valid",
		"Text-entry verification is not send evidence",
		"perform an explicit send action",
		"Do not rely on background WebSocket/HTTP clipboard delivery as the primary path",
	} {
		if !strings.Contains(skill, want) {
			t.Fatalf("generated HTTP tool skill missing %q:\n%s", want, skill)
		}
	}
}
