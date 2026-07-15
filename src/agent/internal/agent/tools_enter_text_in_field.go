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
	return `Enter text into a focused input field with automatic strategy selection and verification. ` +
		`Prefer this over keyboard_text for any input field entry; keyboard_text is ASCII-only and has no field verification. ` +
		`Use this for search fields, contact lookup, short text when no target-preserving Phone Bridge clipboard path is available, and normal IME/pinyin entry. When runtime Phone Bridge status reports a usable target-preserving clipboard path, non-search CJK or other non-ASCII text is automatically routed through enter_text_via_bridge for clipboard write and verified paste. For a user request to send, reply, or message from an already-open composer, set send_after_commit=true. ` +
		`Returns committed:true only when the exact target text is fully committed in the input field. Report success only when committed:true and field_text matches target exactly. ` +
		`Focus coordinates use the same coord_space system as touch/click tools; prefer normalized coordinates from the latest screenshot.`
}

func (t *EnterTextInFieldTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"text":              stringArgSchema("Exact text that must appear in the field when done."),
		"platform":          stringEnumArgSchema("Target platform.", "ios", "android", "mac"),
		"mode":              stringEnumArgSchema("Interaction mode. Use \"search\" for quick handoff in search boxes; omit for normal form entry.", "form", "search"),
		"focus":             focusPointArgSchema("Input field coordinates."),
		"segments":          stringArrayArgSchema("IME romanization syllables for CJK (e.g. [\"ni\",\"hao\"] for 你好). Pass [] for pure ASCII text."),
		"max_attempts":      integerArgSchema("Retry attempts on verify failure (default 3)."),
		"send_after_commit": boolArgSchema("After the exact target text is verified in the field, press send/submit and verify the input cleared or changed. Set true when the user asked to send, reply, or message and the target chat/composer is already open."),
	}, "text", "focus", "segments")
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
	if strings.EqualFold(strings.TrimSpace(args.Mode), "search") {
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
	if len([]rune(strings.TrimSpace(text))) >= 24 {
		return true
	}
	return false
}
