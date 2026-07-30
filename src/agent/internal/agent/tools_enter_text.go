package agent

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"
)

// EnterTextTool is the single public text-entry tool. It prefers Phone Bridge
// clipboard paste when that route is currently usable, then falls back to a
// local mixed ASCII/IME entry strategy.
type EnterTextTool struct {
	engine               *textInputEngine
	bridgeTool           *EnterTextViaBridgeTool
	iosKeyboardIsolation *iosKeyboardIsolationController
}

// enterTextArgs is deliberately separate from enterTextInFieldArgs. The latter
// is still reused by internal legacy paths, whereas enter_text accepts only the
// original target and owns IME planning itself.
type enterTextArgs struct {
	Text            string         `json:"text"`
	Focus           focusPointArgs `json:"focus"`
	MaxAttempts     int            `json:"max_attempts,omitempty"`
	SendAfterCommit bool           `json:"send_after_commit,omitempty"`
}

type enterTextToolResult struct {
	OK         bool   `json:"ok"`
	Suggestion string `json:"suggestion,omitempty"`
}

func (a enterTextArgs) toEngineArgs() enterTextInFieldArgs {
	return enterTextInFieldArgs{
		Text:            a.Text,
		Focus:           a.Focus,
		MaxAttempts:     a.MaxAttempts,
		SendAfterCommit: a.SendAfterCommit,
	}
}

func (t *EnterTextTool) Name() string { return "enter_text" }

func (t *EnterTextTool) Description() string {
	return `Enter exact text into a visible, focused input field or composer. ` +
		`First prefers the Phone Bridge clipboard route when it is currently usable, including long, multiline, CJK, and non-ASCII text; it pastes and verifies the complete target. ` +
		`If Bridge is unavailable, the field must already be focused. The local path holds the pointer-free keyboard isolation profile for the whole operation, probes the current ENG/IME state with a temporary "a", and maintains that state while entering parts in order. ` +
		`ASCII parts are typed directly in ENG mode without a vision verification step. IME parts are typed in IME mode and use vision-guided candidate selection until the part is committed. The local path never uses mouse input. ` +
		`Precondition: the latest screenshot must clearly show the editable field or composer, and focus coordinates must identify that field for bridge paste and visual analysis; never use a guessed blank-space coordinate. ` +
		`Provide the exact original text only: this tool detects ASCII and IME parts and derives required IME keystrokes internally. Set send_after_commit=true only when the user asked to send/submit from an already-open composer. ` +
		`The result contains only ok, plus a next-step suggestion when ok is false.`
}

func (t *EnterTextTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"text":              stringArgSchema("Exact text that must appear in the field when done."),
		"focus":             focusPointArgSchema("Coordinates inside a clearly visible editable field or composer."),
		"max_attempts":      integerArgSchema("Retry attempts for a single-mode local entry (default 3)."),
		"send_after_commit": boolArgSchema("After the exact target is verified, press send/submit and verify it was sent."),
	}, "text", "focus")
}

func (t *EnterTextTool) Call(ctx context.Context, input string) (string, error) {
	started := time.Now()
	ctx, metrics := withTextInputMetrics(ctx)
	var output string
	var callErr error
	defer func() {
		duration := time.Since(started)
		characters := metrics.characters.Load()
		log.Printf(
			"[text-input] enter_text end ok=%t chars=%d duration=%s time_per_char=%s vllm_calls=%d",
			enterTextOutputOK(output, callErr),
			characters,
			duration,
			textInputDurationPerCharacter(duration, characters),
			metrics.vllmCalls.Load(),
		)
	}()
	var controller *iosKeyboardIsolationController
	if t != nil {
		controller = t.iosKeyboardIsolation
	}
	output, callErr = withIOSKeyboardIsolationBatchCall(ctx, controller, func(batchCtx context.Context) (string, error) {
		if t == nil || t.engine == nil {
			return enterTextToolFailure(batchCtx, CodeModuleUnavailable, "Configure enter_text dependencies, then retry."), nil
		}
		var publicArgs enterTextArgs
		if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &publicArgs); err != nil {
			textInputLogf("tool invalid arguments err=%v", err)
			return enterTextToolFailure(batchCtx, CodeInvalidArguments, "Call enter_text again with valid JSON containing text and focus."), nil
		}
		metrics.characters.Store(int64(len([]rune(publicArgs.Text))))
		args := publicArgs.toEngineArgs()
		if strings.TrimSpace(args.Text) == "" {
			return enterTextToolFailure(batchCtx, CodeInvalidArguments, "Provide non-empty text, then retry enter_text."), nil
		}
		platform := t.engine.hw.platform()
		bridgeAvailable := t.bridgeAvailable(args)
		textInputLogf("tool start platform=%s target_len=%d bridge_available=%t isolation_configured=%t", platform, len([]rune(args.Text)), bridgeAvailable, controller != nil)
		if bridgeAvailable {
			textInputLogf("bridge path begin")
			bridgeResult, attempted := t.bridgeTool.runClipboardFirstResult(batchCtx, args)
			textInputLogf("bridge path result attempted=%t committed=%t ok=%t reason=%q", attempted, bridgeResult.Committed, bridgeResult.OK, truncateForLog(bridgeResult.Reason, 160))
			if attempted {
				return enterTextToolResultString(bridgeResult), nil
			}
		}
		if platform == "ios" && controller == nil {
			textInputLogf("local path refused platform=ios reason=isolation_unavailable")
			return enterTextToolFailure(batchCtx, CodeModuleUnavailable, "Enable iOS keyboard isolation, then retry enter_text."), nil
		}
		var result enterTextInFieldResult
		var err error
		runLocal := func() error {
			textInputLogf("local path engine begin")
			result, err = t.engine.RunSegmented(batchCtx, args)
			textInputLogf("local path engine result committed=%t ok=%t ime_switches=%d vlm_calls=%d reason=%q err=%v", result.Committed, result.OK, result.IMESwitches, result.VLMCalls, truncateForLog(result.Reason, 160), err)
			return err
		}
		if controller != nil {
			textInputLogf("local path isolation enter requested")
			err = controller.withKeyboard(batchCtx, true, runLocal)
			textInputLogf("local path isolation scope returned err=%v", err)
		} else {
			textInputLogf("local path running without isolation platform=%s", platform)
			err = runLocal()
		}
		if err != nil {
			textInputLogf("local path execution failed err=%v", err)
			return enterTextToolFailure(batchCtx, CodeToolExecutionFailed, "Inspect the latest screen, refocus the target field, then retry enter_text."), nil
		}
		return enterTextToolResultString(result), nil
	})
	return output, callErr
}

func enterTextOutputOK(output string, callErr error) bool {
	if callErr != nil {
		return false
	}
	var result enterTextToolResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return false
	}
	return result.OK
}

func enterTextToolResultString(result enterTextInFieldResult) string {
	if result.OK {
		return jsonString(enterTextToolResult{OK: true})
	}
	return jsonString(enterTextToolResult{OK: false, Suggestion: enterTextFailureSuggestion(result)})
}

func enterTextToolFailure(ctx context.Context, code, suggestion string) string {
	output := jsonString(enterTextToolResult{OK: false, Suggestion: suggestion})
	SetToolError(ctx, NewToolError(code, output))
	return output
}

func enterTextFailureSuggestion(result enterTextInFieldResult) string {
	if result.WrongIMESuspected {
		return "Switch to the intended IME, refocus the field, then retry enter_text."
	}
	if result.Committed && !result.SendVerified {
		return "Inspect the latest screen and submit the already-entered text manually or retry enter_text with send_after_commit."
	}
	if strings.TrimSpace(result.FieldText) != "" {
		return "Inspect the latest screen and correct or clear the existing field text before retrying enter_text."
	}
	if strings.Contains(strings.ToLower(result.Reason), "deadline") || strings.Contains(strings.ToLower(result.Reason), "timeout") {
		return "Retry enter_text; if it times out again, enter a shorter section at a time."
	}
	return "Inspect the latest screen, refocus the target field, then retry enter_text."
}

func (t *EnterTextTool) bridgeAvailable(args enterTextInFieldArgs) bool {
	if t == nil || t.bridgeTool == nil {
		return false
	}
	var hw *textInputHardwareDeps
	if t.engine != nil {
		hw = &t.engine.hw
	} else if t.bridgeTool != nil {
		hw = t.bridgeTool.hw
	}
	platform := textInputHardwarePlatform(hw)
	return t.bridgeTool.canUseClipboardFirst(platform, args.Text)
}
