package agent

import (
	"encoding/json"
	"testing"

	langtools "github.com/tmc/langchaingo/tools"
)

func TestAgentExposedToolsDoNotExposeLegacyArg1Schema(t *testing.T) {
	toolSet := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools := append([]langtools.Tool{}, toolSet.All()...)
	tools = append(tools,
		NewRecallSessionChunksTool(nil),
		NewRecallMemoryTool(nil),
		NewSaveMemoryTool(nil),
		NewForgetMemoryTool(nil),
		NewRecallDeviceMemoryTool(nil),
		NewInspectEpisodeTool(nil),
		NewSkillListTool(t.TempDir()),
		NewSkillReadTool(t.TempDir()),
		NewSkillMarkUsedTool(t.TempDir(), ""),
		NewSkillManageTool(t.TempDir(), ""),
		NewOpenAppTool(nil),
		NewClipboardTool(nil),
		NewCalendarTool(nil),
		NewContactsTool(nil),
		NewNotificationTool(nil),
	)
	tools = appendLoopMetaTools(tools)
	tools = appendExecutorMetaTools(tools)

	for _, tool := range tools {
		if tool == nil {
			continue
		}
		t.Run(tool.Name(), func(t *testing.T) {
			schema := NewToolSpec(tool).LLMSchema()
			if schemaContainsKey(schema, "__arg1") {
				encoded, _ := json.Marshal(schema)
				t.Fatalf("schema exposes legacy __arg1: %s", encoded)
			}
			props, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("schema missing properties: %#v", schema)
			}
			if _, ok := props["description"]; ok {
				t.Fatalf("schema exposes tool-call speech description property: %#v", schema)
			}
			if _, ok := props["speech"]; ok {
				t.Fatalf("schema exposes speech while tool-call speech is disabled: %#v", schema)
			}
			enabledSchema := NewToolSpec(tool).LLMSchemaWithSpeech(true)
			enabledProps, ok := enabledSchema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("enabled schema missing properties: %#v", enabledSchema)
			}
			if _, ok := enabledProps["speech"]; !ok {
				t.Fatalf("enabled schema missing speech property: %#v", enabledSchema)
			}
		})
	}
}

func TestSessionRecallTelemetryToolForwardsStructuredSchema(t *testing.T) {
	inner := NewRecallSessionChunksTool(nil)
	wrapped := &sessionRecallTelemetryTool{inner: inner}
	schema := wrapped.ArgsSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("forwarded schema missing properties: %#v", schema)
	}
	if _, ok := props["chunk_ids"]; !ok {
		t.Fatalf("forwarded schema missing chunk_ids: %#v", props)
	}
}

func schemaContainsKey(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for k, v := range typed {
			if k == key || schemaContainsKey(v, key) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if schemaContainsKey(item, key) {
				return true
			}
		}
	case []string:
		for _, item := range typed {
			if item == key {
				return true
			}
		}
	}
	return false
}
