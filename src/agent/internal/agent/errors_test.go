package agent

import (
	"encoding/json"
	"testing"
)

func TestRegistryHasAllRequiredCodes(t *testing.T) {
	required := []string{
		// invalid_input
		"invalid_arguments", "tool_not_found", "unknown_app",
		"quick_action_unknown", "app_no_launch_target",
		// precondition_failed
		"bridge_not_connected", "app_not_installed", "module_unavailable",
		// user_action_required
		"permission_denied", "app_backgrounded",
		// unsupported
		"quick_action_reserved", "quick_action_unsupported_platform",
		// transient
		"bridge_timeout", "bridge_write_failed", "bridge_connection_closed",
		"app_launch_failed", "canceled", "deadline_exceeded",
		// internal
		"command_marshal_failed", "command_id_collision",
		"quick_action_invalid_binding", "subtool_failed", "tool_execution_failed",
		"native_module_failed", "unknown_error_code",
	}
	for _, code := range required {
		if _, ok := codeRegistry[code]; !ok {
			t.Errorf("codeRegistry missing required code %q", code)
		}
	}
}

func TestRegistryCategoriesAreValid(t *testing.T) {
	valid := map[string]bool{
		CategoryInvalidInput: true, CategoryPreconditionFailed: true,
		CategoryUserActionRequired: true, CategoryUnsupported: true,
		CategoryTransient: true, CategoryInternal: true,
	}
	for code, spec := range codeRegistry {
		if !valid[spec.Category] {
			t.Errorf("code %q has invalid category %q", code, spec.Category)
		}
	}
}

func TestNewToolErrorSetsCategoryFromRegistry(t *testing.T) {
	e := NewToolError("app_not_installed", "WeChat is not installed")
	if e.Category != CategoryPreconditionFailed {
		t.Errorf("Category = %q, want %q", e.Category, CategoryPreconditionFailed)
	}
	if e.Code != "app_not_installed" {
		t.Errorf("Code = %q", e.Code)
	}
	if e.Message != "WeChat is not installed" {
		t.Errorf("Message = %q", e.Message)
	}
}

func TestNewToolErrorUnknownCodeDegradesSafely(t *testing.T) {
	e := NewToolError("totally_made_up_code", "boom")
	if e.Category != CategoryInternal {
		t.Errorf("unknown code Category = %q, want %q", e.Category, CategoryInternal)
	}
	if e.Code != "unknown_error_code" {
		t.Errorf("unknown code Code = %q, want unknown_error_code", e.Code)
	}
	if got, _ := e.Details["original_code"].(string); got != "totally_made_up_code" {
		t.Errorf("Details[original_code] = %v, want totally_made_up_code", e.Details["original_code"])
	}
}

func TestToolErrorJSONRoundTrip(t *testing.T) {
	orig := NewToolError("bridge_timeout", "websocket timed out after 5s")
	orig.Details = map[string]any{"timeout_ms": 5000.0}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var got ToolError
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != orig.Code || got.Category != orig.Category || got.Message != orig.Message {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
	if got.Details["timeout_ms"].(float64) != 5000.0 {
		t.Errorf("Details not preserved: %+v", got.Details)
	}
}

func TestUnknownCodeJSONInUnmarshalsWithoutLossButOriginalKept(t *testing.T) {
	wire := []byte(`{"category":"internal","code":"unknown_error_code","message":"x","details":{"original_code":"future_code"}}`)
	var e ToolError
	if err := json.Unmarshal(wire, &e); err != nil {
		t.Fatal(err)
	}
	if e.Details["original_code"] != "future_code" {
		t.Errorf("original_code lost: %+v", e.Details)
	}
}
