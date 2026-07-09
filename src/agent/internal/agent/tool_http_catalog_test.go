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
	}
	if !isAgentToolExposed("run_script") {
		t.Fatal("expected run_script available to conversational agent")
	}
	for _, name := range []string{"list_scripts", "read_script", "write_script"} {
		if !isAgentToolExposed(name) {
			continue
		}
		t.Fatalf("did not expect %s in the default conversational agent catalog", name)
	}
}

func TestPhoneBridgeToolsExposedToAgent(t *testing.T) {
	for _, name := range []string{"bridge_open_app", "bridge_clipboard", "bridge_calendar", "bridge_contacts", "bridge_notification"} {
		if !isAgentToolExposed(name) {
			t.Fatalf("expected %s available to conversational agent", name)
		}
	}
}

func TestUnknownToolsDefaultToHTTPVisible(t *testing.T) {
	// Injected tools without built-in metadata stay visible by default so tests,
	// extensions, and environment bridges do not need extra catalog plumbing.
	spec := NewToolSpec(&stubTool{name: "unregistered_tool", description: "Unregistered."})
	if spec.Name != "unregistered_tool" {
		t.Fatalf("unexpected tool spec name: %q", spec.Name)
	}
	if !spec.HTTPExposed {
		t.Fatal("expected unregistered tool to remain HTTP-visible by default")
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
	runtime.tools.RegisterPhoneBridge(NewPhoneBridge(nil))

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
	bridge := NewPhoneBridge(nil)
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
	for _, notWant := range []string{"clipboard", "calendar", "contacts", "notification"} {
		for _, name := range names {
			if name == notWant {
				t.Fatalf("availableTools exposed phone data tool %s: %v", notWant, names)
			}
		}
	}
}

func TestDefaultAgentCatalogSlimsSpecializedTools(t *testing.T) {
	for _, name := range []string{
		"shell",
		"image_diff",
		"mouse_click",
		"mouse_move",
		"mouse_scroll",
		"keyboard_text",
		"web_search",
		"wikipedia",
		"web_scraper",
		"weather",
		"calculator",
		"recall_device_memory",
		"inspect_episode",
		"skill_manage",
		"skill_mark_used",
	} {
		if isAgentToolExposed(name) {
			t.Fatalf("did not expect %s in the default conversational agent catalog", name)
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

func newIOSPiPBackgroundBridge(t *testing.T) *PhoneBridge {
	t.Helper()
	bridge := NewPhoneBridge(nil)
	t.Cleanup(func() { bridge.queue.Stop() })
	bridge.mu.Lock()
	bridge.platform = "ios"
	bridge.appState = "background"
	bridge.appStateAt = time.Now()
	bridge.pipBridgeEnabled = true
	bridge.pipBridgeSeen = true
	bridge.mu.Unlock()
	return bridge
}
