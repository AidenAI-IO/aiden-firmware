package agent

import (
	"testing"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
)

func toolAgentExposed(name string) bool {
	return NewToolSpec(&stubTool{name: name, description: name}).AgentExposed
}

func toolNameSet(tools []langtools.Tool) map[string]struct{} {
	names := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if tool != nil {
			names[tool.Name()] = struct{}{}
		}
	}
	return names
}

func toolSpecsForNames(names []string) *ToolSpecs {
	tools := make([]langtools.Tool, 0, len(names))
	for _, name := range names {
		tools = append(tools, &stubTool{name: name, description: name})
	}
	return NewToolSpecs(tools)
}

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
	if !toolAgentExposed("quick_action") {
		t.Fatal("expected quick_action available to conversational agent")
	}
	if !toolAgentExposed("keyboard_tap") {
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
	if !toolAgentExposed("wait_for_wakeup") {
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
	if !toolAgentExposed("run_script") {
		t.Fatal("expected run_script available to conversational agent")
	}
	for _, name := range []string{"list_scripts", "read_script", "write_script"} {
		if !toolAgentExposed(name) {
			continue
		}
		t.Fatalf("did not expect %s in the default conversational agent catalog", name)
	}
}

func TestKeyboardLayoutProbeIsHTTPOnly(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{},
		nil,
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	descriptor, ok := runtime.ToolDescriptorByName("keyboard_layout_probe")
	if !ok {
		t.Fatal("expected keyboard_layout_probe in Tool Lab HTTP catalog")
	}
	if descriptor.HTTP.Path != "/api/tools/keyboard_layout_probe" {
		t.Fatalf("unexpected HTTP path: %q", descriptor.HTTP.Path)
	}
	if toolAgentExposed("keyboard_layout_probe") {
		t.Fatal("keyboard_layout_probe must not be exposed to the conversational agent")
	}
}

func TestTimeAndCalculatorAreNotRegistered(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{},
		nil,
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	for _, name := range []string{"calculator", "current_time"} {
		if _, ok := runtime.tools.Get(name); ok {
			t.Fatalf("removed utility tool %s is still registered", name)
		}
		if _, ok := runtime.ToolDescriptorByName(name); ok {
			t.Fatalf("removed utility tool %s is still in the Tool Lab HTTP catalog", name)
		}
	}
}

func TestPhoneBridgeToolsExposedToAgent(t *testing.T) {
	for _, name := range []string{"bridge_open_app", "bridge_clipboard", "bridge_calendar", "bridge_contacts", "bridge_notification"} {
		if !toolAgentExposed(name) {
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
	if !spec.AgentExposed {
		t.Fatal("expected unregistered tool to remain Agent-visible by default")
	}
	if !spec.HTTPExposed {
		t.Fatal("expected unregistered tool to remain HTTP-visible by default")
	}
}

func TestWheelNudgeIsNotDirectlyHTTPExposed(t *testing.T) {
	spec := NewToolSpec(&WheelNudgeTool{})
	if spec.HTTPExposed {
		t.Fatal("wheel_nudge must run through the Agent's run-scoped execution policy")
	}
}

func TestHTTPDescriptorIncludesStructuredArgsSchema(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{},
		nil,
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)
	descriptor, ok := runtime.ToolDescriptorByName("shell")
	if !ok {
		t.Fatal("expected shell in HTTP catalog")
	}
	properties, ok := descriptor.ArgsSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("shell args_schema missing properties: %#v", descriptor.ArgsSchema)
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("shell args_schema missing action: %#v", properties)
	}
	if _, ok := action["enum"]; !ok {
		t.Fatalf("shell action schema missing enum: %#v", action)
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

	defaultNames := toolNameSet(runtime.availableTools())
	for _, want := range []string{"bridge_open_app", "bridge_clipboard", "bridge_calendar", "bridge_contacts", "bridge_notification"} {
		if _, ok := defaultNames[want]; !ok {
			t.Fatalf("default catalog missing connected phone bridge tool %s: %v", want, defaultNames)
		}
	}

	runtime.config.LoadAllTools = true
	fullNames := toolNameSet(runtime.availableTools())
	for _, want := range []string{"bridge_open_app", "bridge_clipboard", "bridge_calendar", "bridge_contacts", "bridge_notification"} {
		if _, ok := fullNames[want]; !ok {
			t.Fatalf("full catalog missing connected phone bridge tool %s: %v", want, fullNames)
		}
	}
}

func TestToolSpecsAgentCatalogPolicy(t *testing.T) {
	coreTools := []string{
		"audio_volume",
		"image_diff",
		"inspect_episode",
		"keyboard_tap",
		"keyboard_text",
		"enter_text_in_field",
		"mouse_click",
		"mouse_move",
		"mouse_scroll",
		"quick_action",
		"recall_device_memory",
		"recall_memory",
		"save_memory",
		"shell",
		"screenshot",
		"skill_list",
		"skill_manage",
		"skill_mark_used",
		"skill_read",
		"touch_gesture",
		"wait_for_stable_screen",
		"weather",
		"web_search",
		"web_scraper",
		"wikipedia",
		"request_human_handoff",
		"run_script",
		"bridge_open_app",
		"bridge_clipboard",
		"bridge_calendar",
		"bridge_contacts",
		"bridge_notification",
		"search_launch_app",
	}
	omittedTools := []string{
		"list_scripts",
		"read_script",
		"write_script",
	}
	allNames := append(append([]string{}, coreTools...), omittedTools...)
	specs := toolSpecsForNames(allNames)

	defaultNames := toolNameSet(specs.AgentTools(false))
	for _, want := range coreTools {
		if _, ok := defaultNames[want]; !ok {
			t.Errorf("default catalog missing core tool %s", want)
		}
	}
	for _, notWant := range omittedTools {
		if _, ok := defaultNames[notWant]; ok {
			t.Errorf("default catalog exposed omitted tool %s", notWant)
		}
	}

	fullNames := toolNameSet(specs.AgentTools(true))
	for _, want := range allNames {
		if _, ok := fullNames[want]; !ok {
			t.Errorf("full catalog missing tool %s", want)
		}
	}
}

func TestRuntimeLoadAllToolsIncludesScriptAuthoringTools(t *testing.T) {
	runtime := NewRuntimeWithDeps(
		Config{LoadAllTools: true},
		nil,
		NewMemoryManager(""),
		NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{}),
		NewSkillIndex(),
	)

	names := toolNameSet(runtime.availableTools())
	for _, want := range []string{"list_scripts", "read_script", "write_script"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("availableTools with load_all_tools missing %s: %v", want, names)
		}
	}
}

func TestPhoneBridgeDataToolDescriptorsHaveUsefulExamples(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	bridge := newPhoneBridgeForTest()
	bridge.connected = true
	runtime.tools.RegisterPhoneBridge(bridge)

	expected := map[string]string{
		"bridge_clipboard":    `{"action":"read"}`,
		"bridge_calendar":     `{"action":"query","from":"2026-07-10T00:00:00+08:00","to":"2026-07-11T00:00:00+08:00"}`,
		"bridge_contacts":     `{"action":"query","query":"Alice","limit":20}`,
		"bridge_notification": `{"title":"Aiden reminder","body":"Check your phone","sound":true}`,
	}
	for name, want := range expected {
		desc, ok := runtime.ToolDescriptorByName(name)
		if !ok {
			t.Fatalf("ToolDescriptorByName missing %s", name)
		}
		if desc.ExampleInput != want {
			t.Fatalf("%s example_input = %q, want %q", name, desc.ExampleInput, want)
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
	bridge := newPhoneBridgeForTest()
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
