package agent

import (
	"strings"
	"testing"
)

func TestWaitStableScreenToolDynamicExampleInput(t *testing.T) {
	t.Parallel()

	// Create tool with custom defaults
	defaults := ScreenStableDefaults{
		TimeoutMs:     3500,
		StableMs:      500,
		DiffThreshold: 2.0,
	}
	tool := NewWaitStableScreenTool("/tmp/test.sock", defaults)

	// Create tool spec
	spec := NewToolSpec(tool)

	// Verify ExampleInput uses the configured values, not hardcoded 2200
	if !strings.Contains(spec.ExampleInput, "3500") {
		t.Errorf("ExampleInput should contain configured timeout_ms 3500, got: %s", spec.ExampleInput)
	}
	if !strings.Contains(spec.ExampleInput, "500") {
		t.Errorf("ExampleInput should contain configured stable_ms 500, got: %s", spec.ExampleInput)
	}
	if !strings.Contains(spec.ExampleInput, "2") {
		t.Errorf("ExampleInput should contain configured diff_threshold 2, got: %s", spec.ExampleInput)
	}

	// Verify it's valid JSON
	expected := `{"timeout_ms":3500,"stable_ms":500,"diff_threshold":2}`
	if spec.ExampleInput != expected {
		t.Errorf("ExampleInput = %q, want %q", spec.ExampleInput, expected)
	}
}

func TestWaitStableScreenToolDynamicExampleInputUsesDefaults(t *testing.T) {
	t.Parallel()

	// Create tool with zero defaults (should use code defaults)
	defaults := ScreenStableDefaults{}
	tool := NewWaitStableScreenTool("/tmp/test.sock", defaults)

	// Create tool spec
	spec := NewToolSpec(tool)

	// Should use code defaults: 2000, 250, 6.0
	if !strings.Contains(spec.ExampleInput, "2000") {
		t.Errorf("ExampleInput should contain default timeout_ms 2000, got: %s", spec.ExampleInput)
	}
	if !strings.Contains(spec.ExampleInput, "250") {
		t.Errorf("ExampleInput should contain default stable_ms 250, got: %s", spec.ExampleInput)
	}
	if !strings.Contains(spec.ExampleInput, "6") {
		t.Errorf("ExampleInput should contain default diff_threshold 6, got: %s", spec.ExampleInput)
	}
}

func TestWaitStableScreenToolDescriptionShowsConfiguredValues(t *testing.T) {
	t.Parallel()

	defaults := ScreenStableDefaults{
		TimeoutMs:     3500,
		StableMs:      500,
		DiffThreshold: 2.0,
	}
	tool := NewWaitStableScreenTool("/tmp/test.sock", defaults)

	desc := tool.Description()

	// Description should show the configured values
	if !strings.Contains(desc, "3500") {
		t.Errorf("Description should contain timeout_ms 3500, got: %s", desc)
	}
	if !strings.Contains(desc, "500") {
		t.Errorf("Description should contain stable_ms 500, got: %s", desc)
	}
	if !strings.Contains(desc, "Configured timeouts") {
		t.Errorf("Description should mention configured timeouts")
	}
}
