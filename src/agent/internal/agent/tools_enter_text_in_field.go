package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type EnterTextInFieldTool struct {
	engine     *textInputEngine
	bridgeTool *EnterTextViaBridgeTool
	platformFn func() string
}

func (t *EnterTextInFieldTool) SetPlatformFn(fn func() string) {
	if t != nil {
		t.platformFn = fn
	}
}

func (t *EnterTextInFieldTool) Name() string { return "enter_text_in_field" }

func (t *EnterTextInFieldTool) Description() string {
	return strings.TrimSpace(`Enter target text into a focused input field with automatic input strategy selection and verification. ` +
		`On iOS/Android, this tool prefers clipboard/paste for CJK, emoji, multiline, or long text when Phone Bridge can provide a reliable current-app clipboard path, then falls back to HID/IME input if clipboard input fails. ` +
		`On iOS, if the target text was already prepared with clipboard write, it only focuses the current field, pastes, and verifies; it does not restore Aiden from an already open target app just to write clipboard. ` +
		`For message composition fields with prepared clipboard text, set send_after_commit=true only after the correct chat is open; clipboard mode will focus, paste, verify the field, keyboard-send, then verify the input cleared or changed. ` +
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
		"text":         stringArgSchema("Exact text that must appear in the field when done."),
		"platform":     stringEnumArgSchema("Target platform.", "ios", "android", "mac"),
		"mode":         stringEnumArgSchema("Interaction mode. Use \"search\" for quick handoff in search boxes; omit for normal form entry.", "form", "search"),
		"focus":        focusPointArgSchema("Input field coordinates."),
		"segments":          stringArrayArgSchema("Required for composition/CJK: IME romanization syllables in order, e.g. [\"ni\",\"hao\"] for 你好."),
		"max_attempts":      integerArgSchema("Retry attempts on verify failure (default 3)."),
		"send_after_commit": boolArgSchema("After the exact target text is verified in the field, press send/submit and verify the input cleared or changed. Prefer with prepared clipboard text in an already-open message chat."),
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
	if t.shouldPreferBridgeClipboard(args) {
		bridgeResult, attempted := t.bridgeTool.runClipboardFirstResult(ctx, args)
		if attempted && bridgeResult.Committed {
			return jsonString(finalizeEnterTextInFieldResult(bridgeResult, args.SendAfterCommit)), nil
		}
		if attempted && strings.TrimSpace(bridgeResult.FieldText) != "" {
			args.ClearBeforeInput = true
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
		if attempted {
			result = mergeClipboardFallbackResult(bridgeResult, result)
		}
		return jsonString(finalizeEnterTextInFieldResult(result, args.SendAfterCommit)), nil
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
	return jsonString(finalizeEnterTextInFieldResult(result, args.SendAfterCommit)), nil
}

func finalizeEnterTextInFieldResult(result enterTextInFieldResult, sendAfterCommit bool) enterTextInFieldResult {
	if !result.Committed {
		result.OK = false
		if result.Reason == "" {
			result.Reason = "text entry not verified in field"
		}
		return result
	}
	if sendAfterCommit && !result.SendVerified {
		result.OK = false
		result.Reason = "field verified but send was not verified"
		return result
	}
	result.OK = true
	return result
}

func mergeClipboardFallbackResult(clipboardResult, fallbackResult enterTextInFieldResult) enterTextInFieldResult {
	merged := fallbackResult
	merged.VLMCalls += clipboardResult.VLMCalls
	steps := append([]string{}, clipboardResult.Steps...)
	reason := strings.TrimSpace(clipboardResult.Reason)
	if reason != "" {
		steps = append(steps, "clipboard-first: falling back to HID/IME: "+reason)
	} else {
		steps = append(steps, "clipboard-first: falling back to HID/IME")
	}
	steps = append(steps, fallbackResult.Steps...)
	merged.Steps = steps
	if !merged.Committed && reason != "" {
		fallbackReason := strings.TrimSpace(merged.Reason)
		guidance := "clipboard path failed; continue with same target field and latest screenshot evidence"
		if fallbackReason != "" {
			merged.Reason = fallbackReason + "; safe clipboard failed earlier: " + reason + "; " + guidance
		} else {
			merged.Reason = "safe clipboard failed earlier: " + reason + "; " + guidance
		}
	}
	return merged
}

func (t *EnterTextInFieldTool) shouldPreferBridgeClipboard(args enterTextInFieldArgs) bool {
	if t == nil || t.bridgeTool == nil {
		return false
	}
	platform := strings.ToLower(strings.TrimSpace(args.Platform))
	if platform == "" {
		platform = "android"
	}
	if platform != "ios" && platform != "android" {
		return false
	}
	text := strings.TrimSpace(args.Text)
	if text == "" || !textShouldPreferBridgeClipboard(text) {
		return false
	}
	return t.bridgeTool.canUseClipboardFirst(platform, text)
}

func textShouldPreferBridgeClipboard(text string) bool {
	if needsCompositionInput(text) {
		return true
	}
	if strings.ContainsAny(text, "\r\n\t") {
		return true
	}
	return len([]rune(strings.TrimSpace(text))) >= 24
}
