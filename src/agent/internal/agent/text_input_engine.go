package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
)

type textInputHardwareDeps struct {
	mouseClick   langtools.Tool
	keyboardTap  langtools.Tool
	keyboardText langtools.Tool
	quickAction  langtools.Tool
	screenshot   langtools.Tool
}

type textInputEngine struct {
	hw     textInputHardwareDeps
	vision textInputVision
}

type enterTextInFieldArgs struct {
	Text        string         `json:"text"`
	Platform    string         `json:"platform,omitempty"`
	Focus       focusPointArgs `json:"focus"`
	MaxAttempts int            `json:"max_attempts,omitempty"`
	Segments    []string       `json:"segments,omitempty"`
}

type enterTextInFieldResult struct {
	OK           bool     `json:"ok"`
	Committed    bool     `json:"committed"`
	TargetText   string   `json:"target_text"`
	FieldText    string   `json:"field_text,omitempty"`
	RequiredMode string   `json:"required_mode"`
	Attempts     int      `json:"attempts"`
	IMESwitches  int      `json:"ime_switches"`
	VLMCalls     int      `json:"vlm_calls"`
	Reason       string   `json:"reason,omitempty"`
	Steps        []string `json:"steps,omitempty"`
}

func newTextInputEngine(hw textInputHardwareDeps, vision textInputVision) *textInputEngine {
	return &textInputEngine{hw: hw, vision: vision}
}

func (e *textInputEngine) Run(ctx context.Context, args enterTextInFieldArgs) (enterTextInFieldResult, error) {
	if e == nil || e.vision == nil {
		return enterTextInFieldResult{}, fmt.Errorf("text input engine not configured")
	}
	args.Text = strings.TrimSpace(args.Text)
	if args.Text == "" {
		return enterTextInFieldResult{Reason: "text is required"}, nil
	}
	platform := strings.ToLower(strings.TrimSpace(args.Platform))
	if platform == "" {
		platform = "android"
	}
	maxAttempts := args.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = textInputMaxAttempts
	}
	requiredMode := requiredTextInputMode(args.Text)
	steps := make([]string, 0, 16)
	imeSwitches := 0
	vlmCalls := 0
	retryWrongIME := false

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		steps = append(steps, fmt.Sprintf("attempt %d begin", attempt))
		retype := attempt == 1 || retryWrongIME
		if attempt == 1 || retype {
			if err := e.applyFocus(ctx, args.Focus); err != nil {
				return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredMode), Attempts: attempt, Reason: err.Error(), Steps: steps, VLMCalls: vlmCalls}, nil
			}
		}
		if attempt > 1 && retryWrongIME {
			keyLabel, err := e.cycleIME(ctx, platform)
			if err != nil {
				steps = append(steps, "ime switch failed: "+err.Error())
				continue
			}
			imeSwitches++
			steps = append(steps, fmt.Sprintf("switched IME (keyboard_tap %s)", keyLabel))
			if err := e.clearField(ctx, platform); err != nil {
				steps = append(steps, "clear field failed: "+err.Error())
			}
			retryWrongIME = false
		} else if attempt > 1 {
			steps = append(steps, "retry analyze/candidates without retype")
		}

		segments := []string(nil)
		var committed bool
		var fieldText string
		var wrongIME bool
		var switches, calls int
		var stepNotes []string
		var err error

		if retype {
			if requiredMode == textInputModeASCII {
				if err := e.typeASCII(ctx, args.Text); err != nil {
					steps = append(steps, "keyboard_text failed: "+err.Error())
					continue
				}
			} else {
				segments, err = compositionSegmentsForText(args.Text, args.Segments)
				if err != nil {
					return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredMode), Attempts: attempt, Reason: err.Error(), Steps: steps, VLMCalls: vlmCalls}, nil
				}
				committed, fieldText, wrongIME, calls, stepNotes, err = e.typeCompositionWithCandidateSelection(ctx, platform, args, segments)
			}
		} else {
			if requiredMode == textInputModeComposition {
				segments, err = compositionSegmentsForText(args.Text, args.Segments)
				if err != nil {
					return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredMode), Attempts: attempt, Reason: err.Error(), Steps: steps, VLMCalls: vlmCalls}, nil
				}
			}
		}
		if retype && requiredMode == textInputModeComposition {
			vlmCalls += calls
			steps = append(steps, stepNotes...)
			if err != nil {
				steps = append(steps, "vision analyze failed: "+err.Error())
				retryWrongIME = wrongIME
				if wrongIME {
					_ = e.clearField(ctx, platform)
				}
				continue
			}
			if committed {
				return enterTextInFieldResult{
					OK: true, Committed: true, TargetText: args.Text, FieldText: fieldText,
					RequiredMode: string(requiredMode), Attempts: attempt, IMESwitches: imeSwitches,
					VLMCalls: vlmCalls, Reason: "field verified", Steps: steps,
				}, nil
			}
			if wrongIME {
				steps = append(steps, fmt.Sprintf("verify failed: field=%q", fieldText))
				retryWrongIME = true
				_ = e.clearField(ctx, platform)
				continue
			}
		}

		if !committed {
			committed, fieldText, wrongIME, switches, calls, stepNotes, err = e.analyzeActVerify(ctx, platform, args, requiredMode, segments)
		}
		vlmCalls += calls
		imeSwitches += switches
		steps = append(steps, stepNotes...)
		if err != nil {
			steps = append(steps, "vision analyze failed: "+err.Error())
			retryWrongIME = wrongIME
			if wrongIME {
				_ = e.clearField(ctx, platform)
			}
			continue
		}
		if committed {
			return enterTextInFieldResult{
				OK:           true,
				Committed:    true,
				TargetText:   args.Text,
				FieldText:    fieldText,
				RequiredMode: string(requiredMode),
				Attempts:     attempt,
				IMESwitches:  imeSwitches,
				VLMCalls:     vlmCalls,
				Reason:       "field verified",
				Steps:        steps,
			}, nil
		}
		steps = append(steps, fmt.Sprintf("verify failed: field=%q", fieldText))
		retryWrongIME = wrongIME
		if wrongIME {
			_ = e.clearField(ctx, platform)
		}
	}

	return enterTextInFieldResult{
		OK:           false,
		Committed:    false,
		TargetText:   args.Text,
		RequiredMode: string(requiredMode),
		Attempts:     maxAttempts,
		IMESwitches:  imeSwitches,
		VLMCalls:     vlmCalls,
		Reason:       "exhausted retries without verified field text",
		Steps:        steps,
	}, nil
}

func (e *textInputEngine) analyzeActVerify(ctx context.Context, platform string, args enterTextInFieldArgs, requiredMode textInputMode, segments []string) (committed bool, fieldText string, wrongIME bool, imeSwitches, vlmCalls int, steps []string, err error) {
	analysis, calls, stepNotes, err := e.analyzeScreen(ctx, platform, args, segments)
	vlmCalls += calls
	steps = append(steps, stepNotes...)
	if err != nil {
		return false, "", false, imeSwitches, vlmCalls, steps, err
	}

	if committed, fieldText := evaluateFieldCommit(analysis, args.Text); committed {
		return true, fieldText, false, imeSwitches, vlmCalls, steps, nil
	}
	if analysis.CompositionPending {
		steps = append(steps, "composition pending; target not committed to input field yet")
	}

	if requiredMode == textInputModeComposition {
		if clicks := analysisToClicks(analysis.Candidates); len(clicks) > 0 {
			steps = append(steps, fmt.Sprintf("click first candidate of %d", len(clicks)))
			// Click only the first candidate
			if err := e.applyFocus(ctx, clicks[0]); err != nil {
				return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
			}
			time.Sleep(textInputFocusRestoreDelay)
			analysis, calls, stepNotes, err = e.analyzeScreen(ctx, platform, args, segments)
			vlmCalls += calls
			steps = append(steps, stepNotes...)
			if err != nil {
				return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
			}
			if committed, fieldText := evaluateFieldCommit(analysis, args.Text); committed {
				return true, fieldText, false, imeSwitches, vlmCalls, steps, nil
			}
		} else if analysis.CompositionPending {
			// No matching candidate visible — try paging through candidate list
			for page := 0; page < textInputCandidatePageMax; page++ {
				steps = append(steps, fmt.Sprintf("candidate page down %d", page+1))
				if err := e.tapKeys(ctx, []string{"down"}); err != nil {
					steps = append(steps, "page down failed: "+err.Error())
					break
				}
				time.Sleep(textInputCandidatePageDelay)
				analysis, calls, stepNotes, err = e.analyzeScreen(ctx, platform, args, segments)
				vlmCalls += calls
				steps = append(steps, stepNotes...)
				if err != nil {
					return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
				}
				if committed, fieldText := evaluateFieldCommit(analysis, args.Text); committed {
					return true, fieldText, false, imeSwitches, vlmCalls, steps, nil
				}
				if clicks := analysisToClicks(analysis.Candidates); len(clicks) > 0 {
					steps = append(steps, fmt.Sprintf("click first candidate of %d after paging", len(clicks)))
					// Click only the first candidate
					if err := e.applyFocus(ctx, clicks[0]); err != nil {
						return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
					}
					time.Sleep(textInputFocusRestoreDelay)
					analysis, calls, stepNotes, err = e.analyzeScreen(ctx, platform, args, segments)
					vlmCalls += calls
					steps = append(steps, stepNotes...)
					if err != nil {
						return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
					}
					if committed, fieldText := evaluateFieldCommit(analysis, args.Text); committed {
						return true, fieldText, false, imeSwitches, vlmCalls, steps, nil
					}
					break
				}
				if !analysis.CompositionPending {
					steps = append(steps, "composition no longer pending after paging; stopping")
					break
				}
			}
		}
	}

	fieldText = analysis.FieldText
	wrongIME = shouldSuspectWrongIME(analysis, fieldText, segments, requiredMode)
	if wrongIME {
		steps = append(steps, "wrong IME suspected; will switch and retry")
	}
	return false, fieldText, wrongIME, imeSwitches, vlmCalls, steps, nil
}

func (e *textInputEngine) analyzeScreen(ctx context.Context, platform string, args enterTextInFieldArgs, segments []string) (analysis textInputScreenAnalysis, vlmCalls int, steps []string, err error) {
	shot, err := e.captureScreenshot(ctx)
	if err != nil {
		return textInputScreenAnalysis{}, 0, nil, err
	}
	analysis, err = e.vision.AnalyzeScreen(ctx, shot, textInputScreenAnalysisRequest{
		Phase:      textInputPhaseAfterType,
		Platform:   platform,
		TargetText: args.Text,
		Focus:      args.Focus,
		Segments:   segments,
	})
	vlmCalls++
	if err != nil {
		return textInputScreenAnalysis{}, vlmCalls, nil, err
	}
	steps = append(steps, fmt.Sprintf("analyze: mode=%s field=%q pending=%v candidates=%d wrong_ime=%v",
		analysis.ObservedMode, analysis.FieldText, analysis.CompositionPending, len(analysis.Candidates), analysis.WrongIMESuspected))
	return analysis, vlmCalls, steps, nil
}

func (e *textInputEngine) applyFocus(ctx context.Context, focus focusPointArgs) error {
	coordSpace := strings.TrimSpace(focus.CoordSpace)
	if coordSpace == "" {
		coordSpace = "normalized"
	}
	_, err := callTextInputTool(ctx, e.hw.mouseClick, jsonString(map[string]any{
		"x": focus.X, "y": focus.Y, "coord_space": coordSpace,
	}))
	if err != nil {
		return err
	}
	time.Sleep(textInputFocusRestoreDelay)
	return nil
}

func (e *textInputEngine) tapKeys(ctx context.Context, keys []string) error {
	if e == nil || e.hw.keyboardTap == nil {
		return fmt.Errorf("keyboard_tap is not configured")
	}
	out, err := callTextInputTool(ctx, e.hw.keyboardTap, jsonString(map[string]any{"keys": keys}))
	if err != nil {
		return err
	}
	return interpretTextInputToolOutput(out)
}

func callTextInputTool(ctx context.Context, tool langtools.Tool, input string) (string, error) {
	toolCtx, _ := WithToolError(ctx)
	out, err := tool.Call(toolCtx, input)
	if err != nil {
		return out, err
	}
	if te := ToolErrorFromContext(toolCtx); te != nil {
		return out, te
	}
	return out, nil
}

func (e *textInputEngine) cycleIME(ctx context.Context, platform string) (string, error) {
	keys, err := textInputKeyboardKeysForIMESwitch(platform)
	if err != nil {
		return "", err
	}
	if err := e.tapKeys(ctx, keys); err != nil {
		return "", err
	}
	time.Sleep(textInputIMESwitchSettleDelay)
	return strings.Join(keys, "+"), nil
}

func (e *textInputEngine) typeASCII(ctx context.Context, text string) error {
	if !strings.Contains(text, " ") {
		return e.typeASCIIChunk(ctx, text)
	}
	parts := strings.Split(text, " ")
	for i, part := range parts {
		if part != "" {
			if err := e.typeASCIIChunk(ctx, part); err != nil {
				return err
			}
		}
		if i < len(parts)-1 {
			if err := e.tapKeys(ctx, []string{"space"}); err != nil {
				return err
			}
			time.Sleep(textInputKeystrokeGap)
		}
	}
	return nil
}

func (e *textInputEngine) typeASCIIChunk(ctx context.Context, text string) error {
	out, err := callTextInputTool(ctx, e.hw.keyboardText, jsonString(map[string]string{"text": text}))
	if err != nil {
		return err
	}
	return interpretTextInputToolOutput(out)
}

func (e *textInputEngine) typeCompositionWithCandidateSelection(ctx context.Context, platform string, args enterTextInFieldArgs, segments []string) (committed bool, fieldText string, wrongIME bool, vlmCalls int, steps []string, err error) {
	for i, segment := range segments {
		steps = append(steps, fmt.Sprintf("type segment %d: %q", i+1, segment))
		_, err := callTextInputTool(ctx, e.hw.keyboardText, jsonString(map[string]string{"text": segment}))
		if err != nil {
			return false, fieldText, false, vlmCalls, steps, err
		}
		time.Sleep(textInputKeystrokeGap)
	}
	// All segments typed; press space to commit the current IME composition
	steps = append(steps, "press space to commit composition")
	if err := e.tapKeys(ctx, []string{"space"}); err != nil {
		steps = append(steps, "space commit failed: "+err.Error())
	}
	time.Sleep(textInputFocusRestoreDelay)

	var calls int
	var notes []string
	committed, fieldText, wrongIME, _, calls, notes, err = e.analyzeActVerify(ctx, platform, args, textInputModeComposition, segments)
	vlmCalls += calls
	steps = append(steps, notes...)
	if err != nil {
		return false, fieldText, wrongIME, vlmCalls, steps, err
	}
	return committed, fieldText, wrongIME, vlmCalls, steps, nil
}

func (e *textInputEngine) clearField(ctx context.Context, platform string) error {
	// Take a screenshot to determine how many characters to delete
	shot, err := e.captureScreenshot(ctx)
	if err != nil {
		// Fallback: use escape to dismiss composition then moderate backspaces
		_ = e.tapKeys(ctx, []string{"escape"})
		time.Sleep(textInputKeystrokeGap)
		for i := 0; i < textInputClearBackspaceFallback; i++ {
			_ = e.tapKeys(ctx, []string{"backspace"})
			time.Sleep(textInputKeystrokeGap)
		}
		return nil
	}
	analysis, err := e.vision.AnalyzeScreen(ctx, shot, textInputScreenAnalysisRequest{
		Phase:    textInputPhaseAfterType,
		Platform: platform,
	})
	if err != nil {
		_ = e.tapKeys(ctx, []string{"escape"})
		time.Sleep(textInputKeystrokeGap)
		for i := 0; i < textInputClearBackspaceFallback; i++ {
			_ = e.tapKeys(ctx, []string{"backspace"})
			time.Sleep(textInputKeystrokeGap)
		}
		return nil
	}

	// Dismiss any active composition in candidate bar first
	if analysis.CompositionPending {
		_ = e.tapKeys(ctx, []string{"escape"})
		time.Sleep(textInputKeystrokeGap)
	}

	// Backspace once per rune in the committed field text
	fieldLen := len([]rune(strings.TrimSpace(analysis.FieldText)))
	if fieldLen == 0 {
		return nil
	}
	for i := 0; i < fieldLen; i++ {
		if err := e.tapKeys(ctx, []string{"backspace"}); err != nil {
			return err
		}
		time.Sleep(textInputKeystrokeGap)
	}
	return nil
}

func (e *textInputEngine) captureScreenshot(ctx context.Context) (screenshotResult, error) {
	out, err := e.hw.screenshot.Call(ctx, "{}")
	if err != nil {
		return screenshotResult{}, err
	}
	var result screenshotResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return screenshotResult{}, fmt.Errorf("invalid screenshot JSON: %w", err)
	}
	if strings.TrimSpace(result.Data) == "" {
		return screenshotResult{}, fmt.Errorf("screenshot missing image data")
	}
	return result, nil
}
