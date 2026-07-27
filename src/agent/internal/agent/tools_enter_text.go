package agent

import (
	"context"
	"encoding/json"
	"strings"
)

// EnterTextTool is the single public text-entry tool. It prefers Phone Bridge
// clipboard paste when that route is currently usable, then falls back to a
// local mixed ASCII/IME entry strategy.
type EnterTextTool struct {
	engine               *textInputEngine
	bridgeTool           *EnterTextViaBridgeTool
	platformFn           func() string
	iosKeyboardIsolation *iosKeyboardIsolationController
}

func (t *EnterTextTool) SetPlatformFn(fn func() string) {
	if t != nil {
		t.platformFn = fn
	}
}

func (t *EnterTextTool) Name() string { return "enter_text" }

func (t *EnterTextTool) Description() string {
	return `Enter exact text into a visible, focused input field or composer. ` +
		`First prefers the Phone Bridge clipboard route when it is currently usable, including long, multiline, CJK, and non-ASCII text; it pastes and verifies the complete target. ` +
		`If Bridge is unavailable, it preserves text order by sending HID-compatible ASCII runs with keyboard_text logic and non-ASCII runs with enter_text_in_field IME/candidate-selection logic. ` +
		`Precondition: the latest screenshot must clearly show the editable field or composer, and focus coordinates must be inside it; never use a guessed blank-space coordinate. ` +
		`For CJK/non-ASCII input without Bridge, provide IME romanization segments in text order. If multiple non-ASCII runs are separated by ASCII, provide one segment per Han character for each run. ` +
		`Returns committed:true only when the exact full target is verified. Set send_after_commit=true only when the user asked to send/submit from an already-open composer.`
}

func (t *EnterTextTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"text":              stringArgSchema("Exact text that must appear in the field when done."),
		"platform":          stringEnumArgSchema("Target platform.", "ios", "android", "mac"),
		"focus":             focusPointArgSchema("Coordinates inside a clearly visible editable field or composer."),
		"segments":          stringArrayArgSchema("IME romanization syllables for non-ASCII text, in text order. Use [] for ASCII-only text."),
		"max_attempts":      integerArgSchema("Retry attempts for a single-mode local entry (default 3)."),
		"send_after_commit": boolArgSchema("After the exact target is verified, press send/submit and verify it was sent."),
	}, "text", "focus")
}

func (t *EnterTextTool) Call(ctx context.Context, input string) (string, error) {
	var controller *iosKeyboardIsolationController
	if t != nil {
		controller = t.iosKeyboardIsolation
	}
	return withIOSKeyboardIsolationBatchCall(ctx, controller, func(batchCtx context.Context) (string, error) {
		if t == nil || t.engine == nil {
			return toolErrorResultString(batchCtx, CodeModuleUnavailable, "enter_text is not fully configured"), nil
		}
		var args enterTextInFieldArgs
		if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
			return toolErrorResultf(batchCtx, CodeInvalidArguments, "invalid input: %v", err), nil
		}
		if t.platformFn != nil {
			if platform := strings.TrimSpace(t.platformFn()); platform != "" {
				args.Platform = platform
			}
		}
		args.Text = strings.TrimSpace(args.Text)
		if args.Text == "" {
			return toolErrorResultString(batchCtx, CodeInvalidArguments, "text is required"), nil
		}
		if t.bridgeAvailable(args) {
			bridgeResult, attempted := t.bridgeTool.runClipboardFirstResult(batchCtx, args)
			if attempted {
				return jsonString(bridgeResult), nil
			}
		}
		result, err := t.engine.RunSegmented(batchCtx, args)
		if err != nil {
			return toolErrorResultf(batchCtx, CodeToolExecutionFailed, "%v", err), nil
		}
		return jsonString(result), nil
	})
}

func (t *EnterTextTool) bridgeAvailable(args enterTextInFieldArgs) bool {
	if t == nil || t.bridgeTool == nil {
		return false
	}
	platform := strings.ToLower(strings.TrimSpace(args.Platform))
	if platform == "" {
		platform = "android"
	}
	return t.bridgeTool.canUseClipboardFirst(platform, args.Text)
}
