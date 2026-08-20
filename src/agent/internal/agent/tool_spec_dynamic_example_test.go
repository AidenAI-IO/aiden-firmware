package agent

import (
	"testing"
)

func TestWaitStableScreenToolExampleInputIsEmpty(t *testing.T) {
	t.Parallel()

	// Create tool with custom defaults
	defaults := ScreenStableDefaults{
		TimeoutMs:     3500,
		StableMs:      500,
		DiffThreshold: 2.0,
	}
	tool := NewWaitStableScreenTool(nil, defaults)

	// Create tool spec
	spec := NewToolSpec(tool)

	// ExampleInput should be empty JSON, regardless of config
	expected := "{}"
	if spec.ExampleInput != expected {
		t.Errorf("ExampleInput = %q, want %q (tool accepts no parameters)", spec.ExampleInput, expected)
	}
}

func TestWaitStableScreenToolExampleInputWithDefaults(t *testing.T) {
	t.Parallel()

	// Create tool with zero defaults (uses code defaults)
	defaults := ScreenStableDefaults{}
	tool := NewWaitStableScreenTool(nil, defaults)

	// Create tool spec
	spec := NewToolSpec(tool)

	// Should still be empty JSON
	expected := "{}"
	if spec.ExampleInput != expected {
		t.Errorf("ExampleInput = %q, want %q (tool accepts no parameters)", spec.ExampleInput, expected)
	}
}
