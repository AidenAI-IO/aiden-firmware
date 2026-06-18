package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type EnterTextInFieldTool struct {
	engine     *textInputEngine
	platformFn func() string
}

func (t *EnterTextInFieldTool) SetPlatformFn(fn func() string) {
	if t != nil {
		t.platformFn = fn
	}
}

func (t *EnterTextInFieldTool) Name() string { return "enter_text_in_field" }

func (t *EnterTextInFieldTool) Description() string {
	return strings.TrimSpace(`Enter target text into a focused input field with internal IME handling and verification. ` +
		`One call runs: focus → type → merged vision read (field + IME + candidates) → click candidates if needed → retry with IME switch when wrong. ` +
		`Returns committed:true only when the target text is fully committed inside the input field (not merely visible in IME candidates/preedit). ` +
		`For composition/CJK text, segments (IME romanization syllables) are required — e.g. ["ni","hao"] for 你好. ` +
		`Prefer this over keyboard_text for any input field task, especially non-ASCII. ` +
		`Input {"text":"你好","platform":"android","focus":{"x":450,"y":105,"coord_space":"normalized"},"segments":["ni","hao"]}.`)
}

func (t *EnterTextInFieldTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"text":         stringArgSchema("Exact text that must appear in the field when done."),
		"platform":     stringEnumArgSchema("Target platform.", "ios", "android", "mac"),
		"focus":        focusPointArgSchema("Input field coordinates."),
		"segments":     stringArrayArgSchema("Required for composition/CJK: IME romanization syllables in order, e.g. [\"ni\",\"hao\"] for 你好."),
		"max_attempts": map[string]any{"type": "integer", "description": "Retry attempts on verify failure (default 3)."},
	}, "text", "focus")
}

func (t *EnterTextInFieldTool) Call(ctx context.Context, input string) (string, error) {
	if t == nil || t.engine == nil {
		return "error: enter_text_in_field is not fully configured", nil
	}
	var args enterTextInFieldArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v", err), nil
	}
	if t.platformFn != nil {
		if override := strings.TrimSpace(t.platformFn()); override != "" {
			args.Platform = override
		}
	}
	result, err := t.engine.Run(ctx, args)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}
	if !result.Committed {
		result.OK = false
		if result.Reason == "" {
			result.Reason = "text entry not verified in field"
		}
		return jsonString(result), nil
	}
	result.OK = true
	return jsonString(result), nil
}
