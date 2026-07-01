package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	return strings.TrimSpace(`Enter target text into a focused input field with automatic IME handling, candidate selection, and verification. ` +
		`One call runs: focus → type romanization → merged vision read (field + IME + candidates) → select candidates if needed → retry with IME switch on mismatch → verify committed text. ` +
		`For search boxes only, set mode:"search" to type one pass and hand control back quickly for higher-level search strategy decisions. ` +
		`Returns committed:true ONLY when the exact target text is fully committed inside the input field (not merely visible in IME candidates/preedit). ` +
		`Report success only when committed:true and field_text matches target exactly. ` +
		`For composition/CJK text (Chinese, Japanese, Korean), segments (IME romanization syllables) are REQUIRED — e.g. segments:["ni","hao"] for 你好, segments:["kon","ni","chi","wa"] for こんにちは. ` +
		`For simple ASCII text, segments can be omitted or pass the full text as a single segment. ` +
		`ALWAYS prefer enter_text_in_field over keyboard_text for any input field entry, especially for non-ASCII text or when field verification is needed. ` +
		`keyboard_text is ASCII-only HID keyboard simulation without IME support or verification; use it only for standalone typing outside input fields. ` +
		`Focus coordinates use the same coord_space system as touch/click tools: prefer coord_space:"normalized" (0-1000 range) over "pixel" unless calibrated. ` +
		`Example: {"text":"你好","platform":"android","focus":{"x":450,"y":105,"coord_space":"normalized"},"segments":["ni","hao"]}.`)
}

func (t *EnterTextInFieldTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"text":              stringArgSchema("Exact text that must appear in the field when done."),
		"platform":          stringEnumArgSchema("Target platform.", "ios", "android", "mac"),
		"mode":              stringEnumArgSchema("Interaction mode. Use \"search\" for quick handoff in search boxes; omit for normal form entry.", "form", "search"),
		"artifact_id":       stringArgSchema("Required when consuming a committed-plan target_text artifact prepared before target-app navigation."),
		"send_after_commit": boolArgSchema("Set true only when the focused field is the final message/form field and the text should be sent after verification."),
		"focus":             focusPointArgSchema("Input field coordinates."),
		"segments":          stringArrayArgSchema("Required for composition/CJK: IME romanization syllables in order, e.g. [\"ni\",\"hao\"] for a CJK greeting."),
		"max_attempts":      map[string]any{"type": "integer", "description": "Retry attempts on verify failure (default 3)."},
	}, "text", "focus")
}

func (t *EnterTextInFieldTool) Call(ctx context.Context, input string) (string, error) {
	if t == nil || t.engine == nil {
		return toolErrorResultString(ctx, CodeModuleUnavailable, "enter_text_in_field is not fully configured"), nil
	}
	var args enterTextInFieldArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		return toolErrorResultf(ctx, CodeInvalidArguments, "invalid input: %v", err), nil
	}
	if t.platformFn != nil {
		if override := strings.TrimSpace(t.platformFn()); override != "" {
			args.Platform = override
		}
	}
	result, err := t.engine.Run(ctx, args)
	if err != nil {
		var toolErr *ToolError
		if errors.As(err, &toolErr) {
			SetToolError(ctx, toolErr)
			return toolErrorString(toolErr), nil
		}
		return toolErrorResultf(ctx, CodeToolExecutionFailed, "%v", err), nil
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
