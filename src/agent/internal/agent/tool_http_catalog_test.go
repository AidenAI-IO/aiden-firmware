package agent

import (
	"testing"

	langtools "github.com/tmc/langchaingo/tools"
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

func newRuntimeWithAllCatalogGroups(t *testing.T) *Runtime {
	t.Helper()
	configDir := t.TempDir()
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools.RegisterEnterTextInFieldTool(&testModelResolver{model: &scriptedModel{}}, nil)
	tools.RegisterMemoryTools(t.TempDir(), nil, 0, nil)
	tools.RegisterSkillTools(configDir, "")
	bridge := NewPhoneBridge(nil)
	bridge.connected = true
	tools.RegisterPhoneBridge(bridge)
	return NewRuntimeWithDeps(
		Config{},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		tools,
		NewSkillIndex(),
	)
}

func TestToolExposureMetadataSeparatesDefaultAndScopedTools(t *testing.T) {
	if !toolHasExposure("quick_action", ToolExposureAgentDefault) {
		t.Fatal("expected quick_action in default agent exposure")
	}
	if toolHasExposure("keyboard_text", ToolExposureAgentDefault) {
		t.Fatal("keyboard_text should not be in default agent exposure")
	}
	if !toolHasExposure("keyboard_text", ToolExposureSkillScoped) {
		t.Fatal("expected keyboard_text to remain skill-scoped")
	}
	if !toolHasExposure("shell", ToolExposureHTTP) || toolHasExposure("shell", ToolExposureAgentDefault) {
		t.Fatal("shell should be HTTP/debug available but not default-agent visible")
	}
	if toolHasExposure("skill_manage", ToolExposureHTTP) || isAgentToolExposed("skill_manage") {
		t.Fatal("skill_manage should be admin-only by default")
	}
	if toolHasExposure("skill_mark_used", ToolExposureHTTP) || isAgentToolExposed("skill_mark_used") {
		t.Fatal("skill_mark_used should be admin-only by default")
	}
}

func TestDefaultResolveToolsUsesCoreAndDeviceOperatorTools(t *testing.T) {
	runtime := newRuntimeWithAllCatalogGroups(t)
	names := toolNameSet(runtime.resolveTools(ResolvedSkills{}))

	for _, want := range []string{
		"current_time",
		"recall_session_chunks",
		"recall_memory",
		"save_memory",
		"forget_memory",
		"skill_read",
		"request_human_handoff",
		"screenshot",
		"wait_for_stable_screen",
		"quick_action",
		"touch_gesture",
		"keyboard_tap",
		"enter_text_in_field",
		"open_app",
		"search_launch_app",
		"run_script",
	} {
		requireToolPresent(t, names, want)
	}

	for _, notWant := range []string{
		"shell",
		"mouse_click",
		"mouse_move",
		"mouse_scroll",
		"keyboard_text",
		"enter_text_via_bridge",
		"image_diff",
		"web_search",
		"wikipedia",
		"web_scraper",
		"weather",
		"calculator",
		"clipboard",
		"calendar",
		"contacts",
		"notification",
		"list_scripts",
		"read_script",
		"write_script",
		"skill_list",
		"skill_manage",
		"skill_mark_used",
		"recall_device_memory",
		"inspect_episode",
	} {
		requireToolAbsent(t, names, notWant)
	}
}

func TestResolveToolsUsesSkillScopedAllowedToolsInsteadOfDefaultDeviceTools(t *testing.T) {
	runtime := newRuntimeWithAllCatalogGroups(t)
	names := toolNameSet(runtime.resolveTools(ResolvedSkills{
		HasToolRestriction: true,
		AllowedTools: map[string]struct{}{
			"web_search":  {},
			"wikipedia":   {},
			"web_scraper": {},
			"calculator":  {},
		},
	}))

	for _, want := range []string{"current_time", "skill_read", "recall_memory", "web_search", "wikipedia", "web_scraper", "calculator"} {
		requireToolPresent(t, names, want)
	}
	for _, notWant := range []string{"screenshot", "touch_gesture", "open_app", "quick_action", "shell"} {
		requireToolAbsent(t, names, notWant)
	}
}

func TestResolveToolsIncludesPhoneDataOnlyWhenSkillAllowsAndBridgeConnected(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	bridge := NewPhoneBridge(nil)
	bridge.connected = true
	runtime.tools.RegisterPhoneBridge(bridge)

	names := toolNameSet(runtime.resolveTools(ResolvedSkills{
		HasToolRestriction: true,
		AllowedTools: map[string]struct{}{
			"clipboard":    {},
			"calendar":     {},
			"contacts":     {},
			"notification": {},
		},
	}))
	for _, want := range []string{"clipboard", "calendar", "contacts", "notification"} {
		requireToolPresent(t, names, want)
	}
}

func TestResolveToolsHidesDisconnectedPhoneDataEvenWhenAllowed(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	runtime.tools.RegisterPhoneBridge(NewPhoneBridge(nil))

	names := toolNameSet(runtime.resolveTools(ResolvedSkills{
		HasToolRestriction: true,
		AllowedTools:       map[string]struct{}{"clipboard": {}},
	}))
	requireToolAbsent(t, names, "clipboard")
}

func TestResolveToolsIncludesAllowedOpenAppWhenDisconnected(t *testing.T) {
	runtime := newRuntimeWithTextEntryTools()
	runtime.tools.RegisterPhoneBridge(NewPhoneBridge(nil))

	names := toolNameSet(runtime.resolveTools(ResolvedSkills{
		HasToolRestriction: true,
		AllowedTools:       map[string]struct{}{"open_app": {}},
	}))
	requireToolPresent(t, names, "open_app")
}

func TestHTTPDescriptorExposesHTTPExposureAndHidesSkillManage(t *testing.T) {
	configDir := t.TempDir()
	tools := &ToolSet{tools: map[string]langtools.Tool{}}
	tools.RegisterSkillTools(configDir, "")
	runtime := NewRuntimeWithDeps(Config{}, nil, nil, tools, NewSkillIndex())

	desc, ok := runtime.ToolDescriptorByName("skill_read")
	if !ok {
		t.Fatal("expected skill_read descriptor")
	}
	if !descriptorHasExposure(desc, ToolExposureHTTP) || !descriptorHasExposure(desc, ToolExposureAlwaysCore) {
		t.Fatalf("unexpected skill_read exposure: %#v", desc.Exposure)
	}
	if _, ok := runtime.ToolDescriptorByName("skill_manage"); ok {
		t.Fatal("skill_manage should not be in default HTTP catalog")
	}
	if _, ok := runtime.ToolDescriptorByName("skill_mark_used"); ok {
		t.Fatal("skill_mark_used should not be in default HTTP catalog")
	}
}

func toolNameSet(tools []langtools.Tool) map[string]bool {
	names := map[string]bool{}
	for _, tool := range tools {
		if tool != nil {
			names[tool.Name()] = true
		}
	}
	return names
}

func requireToolPresent(t *testing.T, names map[string]bool, name string) {
	t.Helper()
	if !names[name] {
		t.Fatalf("expected %s in resolved tools, got %#v", name, names)
	}
}

func requireToolAbsent(t *testing.T, names map[string]bool, name string) {
	t.Helper()
	if names[name] {
		t.Fatalf("did not expect %s in resolved tools, got %#v", name, names)
	}
}

func descriptorHasExposure(desc ToolDescriptor, exposure ToolExposure) bool {
	for _, value := range desc.Exposure {
		if value == exposure {
			return true
		}
	}
	return false
}
