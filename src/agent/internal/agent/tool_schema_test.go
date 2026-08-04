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
		NewRecallSessionChunksTool(nil, nil),
		NewRecallMemoryTool(nil),
		NewSaveMemoryTool(nil),
		NewForgetMemoryTool(nil),
		NewRecallDeviceMemoryTool(nil),
		NewInspectEpisodeTool(nil),
		NewSkillListTool(t.TempDir()),
		NewSkillReadTool(t.TempDir()),
		NewSkillMarkUsedTool(t.TempDir(), ""),
		NewSkillManageTool(t.TempDir(), ""),
		NewOpenAppTool(nil, nil, nil),
		NewOpenURLTool(nil, nil),
		NewClipboardTool(nil, nil),
		NewCalendarTool(nil, nil),
		NewContactsTool(nil, nil),
		NewNotificationTool(nil, nil),
	)

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
				t.Fatalf("schema exposes tool-call metadata description property: %#v", schema)
			}
			if _, ok := props["speech"]; ok {
				t.Fatalf("schema exposes tool-call speech metadata: %#v", schema)
			}
		})
	}
}

func TestSessionRecallTelemetryToolForwardsStructuredSchema(t *testing.T) {
	inner := NewRecallSessionChunksTool(nil, nil)
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

func TestNumberArgSchema_WithExamples(t *testing.T) {
	schema := numberArgSchema("Test number", 500, 300.5)

	if schema["type"] != "number" {
		t.Errorf("expected type number, got %v", schema["type"])
	}

	if schema["description"] != "Test number" {
		t.Errorf("expected description 'Test number', got %v", schema["description"])
	}

	examples, ok := schema["examples"]
	if !ok {
		t.Fatal("expected examples field when examples provided")
	}

	examplesSlice, ok := examples.([]float64)
	if !ok {
		t.Fatalf("expected examples to be []float64, got %T", examples)
	}

	if len(examplesSlice) != 2 {
		t.Errorf("expected 2 examples, got %d", len(examplesSlice))
	}

	if examplesSlice[0] != 500 {
		t.Errorf("expected first example to be 500, got %v", examplesSlice[0])
	}
}

func TestNumberArgSchema_WithoutExamples(t *testing.T) {
	schema := numberArgSchema("Test number")

	if _, ok := schema["examples"]; ok {
		t.Error("expected no examples field when no examples provided")
	}

	if schema["type"] != "number" {
		t.Errorf("expected type number, got %v", schema["type"])
	}
}

func TestStringArrayArgSchema_WithExamples(t *testing.T) {
	schema := stringArrayArgSchema("Keys", []string{"ctrl", "c"})

	examples, ok := schema["examples"]
	if !ok {
		t.Fatal("expected examples field")
	}

	examplesSlice, ok := examples.([][]string)
	if !ok {
		t.Fatalf("expected examples to be [][]string, got %T", examples)
	}

	if len(examplesSlice) != 1 {
		t.Errorf("expected 1 example, got %d", len(examplesSlice))
	}

	if len(examplesSlice[0]) != 2 || examplesSlice[0][0] != "ctrl" {
		t.Errorf("unexpected example content: %v", examplesSlice[0])
	}
}

func TestIntegerArgSchema_WithExamples(t *testing.T) {
	schema := integerArgSchema("Count", 10, 20)

	examples, ok := schema["examples"]
	if !ok {
		t.Fatal("expected examples field")
	}

	examplesSlice, ok := examples.([]int)
	if !ok {
		t.Fatalf("expected examples to be []int, got %T", examples)
	}

	if len(examplesSlice) != 2 {
		t.Errorf("expected 2 examples, got %d", len(examplesSlice))
	}
}

func TestRangedIntegerArgSchema_PreservesMinMax(t *testing.T) {
	schema := rangedIntegerArgSchema("Volume", 0, 100, 70)

	if schema["minimum"] != 0 {
		t.Errorf("expected minimum 0, got %v", schema["minimum"])
	}

	if schema["maximum"] != 100 {
		t.Errorf("expected maximum 100, got %v", schema["maximum"])
	}

	examples, ok := schema["examples"]
	if !ok {
		t.Fatal("expected examples field")
	}

	examplesSlice, ok := examples.([]int)
	if !ok || len(examplesSlice) == 0 || examplesSlice[0] != 70 {
		t.Errorf("unexpected examples: %v", examples)
	}
}
