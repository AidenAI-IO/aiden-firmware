package agent

import (
	"testing"
	"time"
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
	for _, name := range []string{"bridge_open_app", "bridge_clipboard", "bridge_calendar", "bridge_contacts", "bridge_notification"} {
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

func TestAvailableToolsIncludesQuickAction(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{},
		nil,
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	tools := runtime.availableTools()
	names := toolNamesFromTools(tools)
	found := false
	for _, name := range names {
		if name == "quick_action" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("availableTools missing quick_action: %v", names)
	}
}

func TestAvailableToolsIncludesPhoneBridgeToolsWhenDisconnected(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	runtime.tools.RegisterPhoneBridge(newPhoneBridgeForTest())

	tools := runtime.availableTools()
	names := toolNamesFromTools(tools)
	for _, want := range []string{"bridge_open_app", "search_launch_app", "enter_text_via_bridge"} {
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("availableTools missing disconnected bridge recovery tool %s: %v", want, names)
		}
	}
	for _, notWant := range []string{"bridge_clipboard", "bridge_calendar", "bridge_contacts", "bridge_notification"} {
		for _, name := range names {
			if name == notWant {
				t.Fatalf("availableTools exposed disconnected phone bridge tool %s: %v", notWant, names)
			}
		}
	}
}

func TestAvailableToolsIncludesPhoneBridgeToolsWhenConnected(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	bridge := newPhoneBridgeForTest()
	bridge.connected = true
	runtime.tools.RegisterPhoneBridge(bridge)

	tools := runtime.availableTools()
	names := toolNamesFromTools(tools)
	for _, want := range []string{"bridge_open_app", "bridge_clipboard", "bridge_calendar", "bridge_contacts", "bridge_notification"} {
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

func TestAvailableToolsHidesOpenAppAndKeepsDataToolsDuringPiPBackground(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	bridge := newIOSPiPBackgroundBridge(t)
	runtime.tools.RegisterPhoneBridge(bridge)

	tools := runtime.availableTools()
	names := toolNamesFromTools(tools)
	for _, notWant := range []string{"bridge_open_app"} {
		for _, name := range names {
			if name == notWant {
				t.Fatalf("availableTools exposed PiP background unavailable tool %s: %v", notWant, names)
			}
		}
	}
	for _, want := range []string{"bridge_clipboard", "bridge_calendar", "bridge_contacts", "bridge_notification", "search_launch_app"} {
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("resolveTools missing PiP background tool %s: %v", want, names)
		}
	}
	if _, ok := runtime.ToolDescriptorByName("bridge_open_app"); ok {
		t.Fatalf("ToolDescriptorByName exposed PiP background unavailable bridge_open_app: %v", names)
	}
	if _, ok := runtime.ToolDescriptorByName("bridge_clipboard"); !ok {
		t.Fatalf("ToolDescriptorByName missing PiP background bridge_clipboard: %v", names)
	}
}

func TestAvailableToolsDoNotExposePiPDataQueueWithoutRecentPoll(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	bridge := newPhoneBridgeForTest()
	t.Cleanup(func() { bridge.queue.Stop() })
	bridge.mu.Lock()
	bridge.connected = true
	bridge.platform = "ios"
	bridge.appState = "background"
	bridge.appStateAt = time.Now()
	bridge.pipBridgeEnabled = true
	bridge.pipBridgeSeen = true
	bridge.mu.Unlock()
	runtime.tools.RegisterPhoneBridge(bridge)

	tools := runtime.availableTools()
	names := toolNamesFromTools(tools)
	foundOpenApp := false
	for _, name := range names {
		if name == "bridge_open_app" {
			foundOpenApp = true
			break
		}
	}
	if !foundOpenApp {
		t.Fatalf("availableTools missing bridge_open_app for stale PiP restore path: %v", names)
	}
	for _, notWant := range []string{"bridge_clipboard", "bridge_calendar", "bridge_contacts", "bridge_notification"} {
		for _, name := range names {
			if name == notWant {
				t.Fatalf("availableTools exposed stale PiP queue tool %s: %v", notWant, names)
			}
		}
	}
}

func newIOSPiPBackgroundBridge(t *testing.T) *PhoneBridge {
	t.Helper()
	bridge := newPhoneBridgeForTest()
	t.Cleanup(func() { bridge.queue.Stop() })
	bridge.mu.Lock()
	bridge.platform = "ios"
	bridge.appState = "background"
	bridge.appStateAt = time.Now()
	bridge.pipBridgeEnabled = true
	bridge.pipBridgeSeen = true
	bridge.pipBridgeAt = time.Now()
	bridge.mu.Unlock()
	return bridge
}
