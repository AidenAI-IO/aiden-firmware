package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
)

type textInputHardwareDeps struct {
	mouseClick   langtools.Tool
	touchGesture langtools.Tool
	keyboardTap  langtools.Tool
	keyboardText langtools.Tool
	quickAction  langtools.Tool
	screenshot   langtools.Tool
	waitStable   langtools.Tool
}

type textInputEngine struct {
	hw     textInputHardwareDeps
	vision textInputVision
	sleep  func(context.Context, time.Duration) error
}

type enterTextInFieldArgs struct {
	Text        string         `json:"text"`
	Platform    string         `json:"platform,omitempty"`
	Mode        string         `json:"mode,omitempty"`
	SkipFocus   bool           `json:"skip_focus,omitempty"`
	Focus       focusPointArgs `json:"focus"`
	MaxAttempts int            `json:"max_attempts,omitempty"`
	// Segments is a compatibility override used only by legacy internal callers.
	// The public enter_text tool decodes a separate schema and never populates it.
	Segments         []string `json:"segments,omitempty"`
	LastDirectInput  string   `json:"-"`
	LastDirectTarget string   `json:"-"`
	SendAfterCommit  bool     `json:"send_after_commit,omitempty"`
	ClearBeforeInput bool     `json:"-"`
}

type enterTextInFieldResult struct {
	OK                 bool   `json:"ok"`
	Committed          bool   `json:"committed"`
	Sent               bool   `json:"sent,omitempty"`
	SendVerified       bool   `json:"send_verified,omitempty"`
	Interrupted        bool   `json:"interrupted,omitempty"`
	TargetText         string `json:"target_text"`
	FieldText          string `json:"field_text,omitempty"`
	PostSendFieldText  string `json:"post_send_field_text,omitempty"`
	RequiredMode       string `json:"required_mode"`
	Attempts           int    `json:"attempts"`
	IMESwitches        int    `json:"ime_switches"`
	VLMCalls           int    `json:"vlm_calls"`
	ObservedMode       string `json:"observed_mode,omitempty"`
	CompositionPending bool   `json:"composition_pending,omitempty"`
	CandidatesVisible  int    `json:"candidates_visible,omitempty"`
	WrongIMESuspected  bool   `json:"wrong_ime_suspected,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

func newTextInputEngine(hw textInputHardwareDeps, vision textInputVision) *textInputEngine {
	return &textInputEngine{hw: hw, vision: vision}
}

func newTextInputEngineWithSleep(hw textInputHardwareDeps, vision textInputVision, sleep func(context.Context, time.Duration) error) *textInputEngine {
	engine := newTextInputEngine(hw, vision)
	engine.sleep = sleep
	return engine
}

func (e *textInputEngine) sleepFor(ctx context.Context, delay time.Duration) error {
	sleep := sleepWithContext
	if e != nil && e.sleep != nil {
		sleep = e.sleep
	}
	return sleep(ctx, delay)
}

func (e *textInputEngine) Run(ctx context.Context, args enterTextInFieldArgs) (enterTextInFieldResult, error) {
	if e == nil || e.vision == nil {
		return enterTextInFieldResult{}, fmt.Errorf("text input engine not configured")
	}
	if strings.TrimSpace(args.Text) == "" {
		return enterTextInFieldResult{Reason: "text is required"}, nil
	}
	platform := strings.ToLower(strings.TrimSpace(args.Platform))
	if platform == "" {
		platform = "android"
	}
	interactionMode := normalizeTextInputInteractionMode(args.Mode)
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
		if (attempt == 1 || retype) && !(attempt == 1 && args.SkipFocus) {
			if err := e.applyFocus(ctx, args.Focus); err != nil {
				return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredMode), Attempts: attempt, Reason: err.Error(), VLMCalls: vlmCalls}, nil
			}
		}
		if attempt == 1 && args.ClearBeforeInput {
			if err := e.clearField(ctx, platform); err != nil {
				steps = append(steps, "clear field before input failed: "+err.Error())
			} else {
				steps = append(steps, "cleared field before input")
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
				args.LastDirectInput = args.Text
				args.LastDirectTarget = args.Text
				if interactionMode == textInputModeSearch {
					return enterTextInFieldResult{OK: false, Committed: false, Interrupted: true, TargetText: args.Text, RequiredMode: string(requiredMode), Attempts: attempt, IMESwitches: imeSwitches, VLMCalls: vlmCalls, Reason: "search handoff after ascii input"}, nil
				}
			} else {
				segments, err = e.compositionSegmentsForText(ctx, args.Text, args.Segments)
				if err != nil {
					return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredMode), Attempts: attempt, Reason: err.Error(), VLMCalls: vlmCalls}, nil
				}
				if attempt == 1 {
					steps = append(steps, fmt.Sprintf("wait %s for IME to settle before first composition input", textInputCompositionReadyDelay))
					if err := e.sleepFor(ctx, textInputCompositionReadyDelay); err != nil {
						return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredMode), Attempts: attempt, IMESwitches: imeSwitches, VLMCalls: vlmCalls, Reason: err.Error()}, nil
					}
				}
				if interactionMode == textInputModeSearch {
					committed, fieldText, wrongIME, calls, stepNotes, err = e.typeCompositionSearch(ctx, platform, args, segments)
				} else {
					committed, fieldText, wrongIME, calls, stepNotes, err = e.typeCompositionWithCandidateSelection(ctx, platform, args, segments)
				}
			}
		} else {
			if requiredMode == textInputModeComposition {
				segments, err = e.compositionSegmentsForText(ctx, args.Text, args.Segments)
				if err != nil {
					return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredMode), Attempts: attempt, Reason: err.Error(), VLMCalls: vlmCalls}, nil
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
				result := enterTextInFieldResult{
					OK: true, Committed: true, TargetText: args.Text, FieldText: fieldText,
					RequiredMode: string(requiredMode), Attempts: attempt, IMESwitches: imeSwitches,
					VLMCalls: vlmCalls, Reason: "field verified",
				}
				return finalizeEnterTextInFieldResult(result, args.SendAfterCommit), nil
			}
			if interactionMode == textInputModeSearch {
				analysis, calls, stepNotes, analyzeErr := e.analyzeScreen(ctx, platform, args, segments)
				vlmCalls += calls
				steps = append(steps, stepNotes...)
				if analyzeErr != nil {
					steps = append(steps, "vision analyze failed: "+analyzeErr.Error())
					return enterTextInFieldResult{OK: false, Committed: false, Interrupted: true, TargetText: args.Text, FieldText: fieldText, RequiredMode: string(requiredMode), Attempts: attempt, IMESwitches: imeSwitches, VLMCalls: vlmCalls, WrongIMESuspected: wrongIME, Reason: "search handoff after composition input"}, nil
				}
				return enterTextInFieldResult{OK: false, Committed: false, Interrupted: true, TargetText: args.Text, FieldText: analysis.FieldText, RequiredMode: string(requiredMode), Attempts: attempt, IMESwitches: imeSwitches, VLMCalls: vlmCalls, ObservedMode: string(analysis.ObservedMode), CompositionPending: analysis.CompositionPending, CandidatesVisible: len(analysis.Candidates), WrongIMESuspected: wrongIME || analysis.WrongIMESuspected || analysis.SuggestSwitchIME, Reason: "search handoff after composition input"}, nil
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
			result := enterTextInFieldResult{
				OK:           true,
				Committed:    true,
				TargetText:   args.Text,
				FieldText:    fieldText,
				RequiredMode: string(requiredMode),
				Attempts:     attempt,
				IMESwitches:  imeSwitches,
				VLMCalls:     vlmCalls,
				Reason:       "field verified",
			}
			return finalizeEnterTextInFieldResult(result, args.SendAfterCommit), nil
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
	}, nil
}

// RunSegmented enters mixed text without relying on a clipboard route. ASCII
// runs use the HID keyboard directly; non-ASCII runs are committed through the
// existing IME/candidate-selection flow. This keeps their order intact (for
// example, "Order中文#42") while avoiding IME work for the ASCII portions.
func (e *textInputEngine) RunSegmented(ctx context.Context, args enterTextInFieldArgs) (enterTextInFieldResult, error) {
	if e == nil || e.vision == nil {
		return enterTextInFieldResult{}, fmt.Errorf("text input engine not configured")
	}
	if strings.TrimSpace(args.Text) == "" {
		return enterTextInFieldResult{Reason: "text is required"}, nil
	}
	chunks, err := splitTextInputChunks(args.Text)
	if err != nil {
		return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Reason: err.Error()}, nil
	}
	if len(chunks) == 1 && chunks[0].ascii && !chunks[0].space && chunks[0].input == chunks[0].text {
		return e.Run(ctx, args)
	}
	platform := strings.ToLower(strings.TrimSpace(args.Platform))
	if platform == "" {
		platform = "android"
	}
	if err := e.applyFocus(ctx, args.Focus); err != nil {
		return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, Reason: err.Error()}, nil
	}

	compositionSegments := make([][]string, len(chunks))
	if len(normalizeCompositionSegments(args.Segments)) > 0 {
		compositionSegments, err = splitSegmentsForTextChunks(chunks, args.Segments)
		if err != nil {
			return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, Reason: err.Error()}, nil
		}
	} else {
		for index, chunk := range chunks {
			if chunk.ascii {
				continue
			}
			compositionSegments[index], err = e.compositionSegmentsForText(ctx, chunk.text, nil)
			if err != nil {
				return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, Reason: err.Error()}, nil
			}
		}
	}
	steps := []string{"split input into direct-input and IME runs"}
	prefix := ""
	vlmCalls := 0
	lastFieldText := ""
	for index, chunk := range chunks {
		prefix += chunk.text
		if chunk.space {
			if err := e.tapKeys(ctx, []string{"space"}); err != nil {
				return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, VLMCalls: vlmCalls, Reason: err.Error()}, nil
			}
			if err := e.sleepFor(ctx, textInputKeystrokeGap); err != nil {
				return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, VLMCalls: vlmCalls, Reason: err.Error()}, nil
			}
			continue
		}
		if chunk.ascii {
			if err := e.typeASCII(ctx, chunk.input); err != nil {
				return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, VLMCalls: vlmCalls, Reason: err.Error()}, nil
			}
			steps = append(steps, fmt.Sprintf("typed direct-input run %d: %q", index+1, chunk.text))
			partialArgs := args
			partialArgs.Text = prefix
			partialArgs.LastDirectInput = chunk.input
			partialArgs.LastDirectTarget = chunk.text
			verifyAfterEnter := true
			for next := index + 1; next < len(chunks); next++ {
				if !chunks[next].space {
					verifyAfterEnter = false
					break
				}
			}
			committed, fieldText, calls, notes, err := e.verifyASCIIRun(ctx, platform, partialArgs, verifyAfterEnter)
			vlmCalls += calls
			steps = append(steps, notes...)
			if err != nil || !committed {
				reason := "ASCII run was not verified"
				if err != nil {
					reason = err.Error()
				}
				return enterTextInFieldResult{TargetText: args.Text, FieldText: fieldText, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, VLMCalls: vlmCalls, Reason: reason}, nil
			}
			lastFieldText = fieldText
			continue
		}
		if err := e.sleepFor(ctx, textInputCompositionReadyDelay); err != nil {
			return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, VLMCalls: vlmCalls, Reason: err.Error()}, nil
		}
		partialArgs := args
		partialArgs.Text = prefix
		committed, fieldText, wrongIME, calls, notes, err := e.typeCompositionWithCandidateSelection(ctx, platform, partialArgs, compositionSegments[index])
		vlmCalls += calls
		steps = append(steps, notes...)
		if err != nil || !committed {
			reason := "IME run was not verified"
			if err != nil {
				reason = err.Error()
			} else if wrongIME {
				reason = "wrong IME suspected"
			}
			return enterTextInFieldResult{TargetText: args.Text, FieldText: fieldText, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, VLMCalls: vlmCalls, WrongIMESuspected: wrongIME, Reason: reason}, nil
		}
		lastFieldText = fieldText
	}
	result := enterTextInFieldResult{OK: true, Committed: true, TargetText: args.Text, FieldText: lastFieldText, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, VLMCalls: vlmCalls, Reason: "field verified"}
	return finalizeEnterTextInFieldResult(result, args.SendAfterCommit), nil
}

// verifyASCIIRun asks vision whether HID typing landed as committed field text
// or is still an IME preedit. A pending ASCII preedit is committed with Enter.
func (e *textInputEngine) verifyASCIIRun(ctx context.Context, platform string, args enterTextInFieldArgs, verifyAfterEnter bool) (bool, string, int, []string, error) {
	steps := []string(nil)
	if e.hw.waitStable != nil {
		steps = append(steps, "wait for post-input frame before verification")
		if err := e.waitForPostInputFrame(ctx); err != nil {
			return false, "", 0, steps, err
		}
	}
	analysis, calls, analysisSteps, err := e.analyzeScreen(ctx, platform, args, nil)
	steps = append(steps, analysisSteps...)
	if err != nil {
		return false, "", calls, steps, err
	}
	if !asciiPartNeedsEnter(analysis) {
		if committed, fieldText := evaluateFieldCommit(analysis, args.Text); committed {
			return true, fieldText, calls, steps, nil
		}
		return false, analysis.FieldText, calls, steps, nil
	}
	steps = append(steps, "ASCII preedit pending; press Enter to commit")
	if err := e.tapKeys(ctx, []string{"enter"}); err != nil {
		return false, analysis.FieldText, calls, steps, err
	}
	if !verifyAfterEnter {
		if err := e.sleepFor(ctx, textInputKeystrokeGap); err != nil {
			return false, analysis.FieldText, calls, steps, err
		}
		return true, analysis.FieldText, calls, steps, nil
	}
	if err := e.sleepFor(ctx, textInputFocusRestoreDelay); err != nil {
		return false, analysis.FieldText, calls, steps, err
	}
	analysis, moreCalls, moreSteps, err := e.analyzeScreen(ctx, platform, args, nil)
	calls += moreCalls
	steps = append(steps, moreSteps...)
	if err != nil {
		return false, analysis.FieldText, calls, steps, err
	}
	if analysis.CompositionPending {
		return false, analysis.FieldText, calls, steps, nil
	}
	committed, fieldText := evaluateFieldCommit(analysis, args.Text)
	return committed, fieldText, calls, steps, nil
}

type textInputChunk struct {
	// text is the exact target text verified on screen. input is what the HID
	// keyboard sends; they differ for IME-aware full-width punctuation.
	text  string
	input string
	ascii bool
	space bool
}

func splitTextInputChunks(text string) ([]textInputChunk, error) {
	var chunks []textInputChunk
	for _, r := range text {
		if r == ' ' {
			chunks = append(chunks, textInputChunk{text: " ", input: " ", ascii: true, space: true})
			continue
		}
		input, ascii := directTextInputForRune(r)
		if r <= 0x7f && !ascii {
			return nil, fmt.Errorf("unsupported ASCII character %q for keyboard input", r)
		}
		if len(chunks) == 0 || chunks[len(chunks)-1].space || chunks[len(chunks)-1].ascii != ascii {
			chunks = append(chunks, textInputChunk{ascii: ascii})
		}
		chunks[len(chunks)-1].text += string(r)
		chunks[len(chunks)-1].input += input
	}
	return chunks, nil
}

// directTextInputForRune maps full-width punctuation to the equivalent HID
// key. With a CJK IME active, these keys produce the target punctuation while
// avoiding an unnecessary pinyin/candidate-selection cycle.
func directTextInputForRune(r rune) (string, bool) {
	if r <= 0x7f {
		_, _, ok := charToHIDKey(byte(r))
		return string(r), ok
	}
	if input, ok := map[rune]string{
		'，': ",", '。': ".", '！': "!", '？': "?", '：': ":", '；': ";",
		'（': "(", '）': ")", '【': "[", '】': "]", '「': "[", '」': "]",
		'“': `"`, '”': `"`, '‘': "'", '’': "'",
	}[r]; ok {
		return input, true
	}
	return "", false
}

func splitSegmentsForTextChunks(chunks []textInputChunk, segments []string) ([][]string, error) {
	result := make([][]string, len(chunks))
	compositionCount := 0
	for _, chunk := range chunks {
		if !chunk.ascii {
			compositionCount++
		}
	}
	if compositionCount == 0 {
		return result, nil
	}
	normalized := normalizeCompositionSegments(segments)
	if len(normalized) == 0 {
		return nil, fmt.Errorf("composition input requires segments (IME romanization syllables)")
	}
	if compositionCount == 1 {
		for i, chunk := range chunks {
			if !chunk.ascii {
				result[i] = normalized
			}
		}
		return result, nil
	}
	next := 0
	for i, chunk := range chunks {
		if chunk.ascii {
			continue
		}
		need := 0
		for _, r := range chunk.text {
			if containsHanRunes(string(r)) {
				need++
			}
		}
		if need == 0 || next+need > len(normalized) {
			return nil, fmt.Errorf("mixed text requires IME segments for each non-ASCII run, in text order")
		}
		result[i] = normalized[next : next+need]
		next += need
	}
	if next != len(normalized) {
		return nil, fmt.Errorf("mixed text IME segments do not match its non-ASCII runs")
	}
	return result, nil
}

func (e *textInputEngine) analyzeActVerify(ctx context.Context, platform string, args enterTextInFieldArgs, requiredMode textInputMode, segments []string) (committed bool, fieldText string, wrongIME bool, imeSwitches, vlmCalls int, steps []string, err error) {
	analysis, calls, stepNotes, err := e.analyzeScreen(ctx, platform, args, segments)
	vlmCalls += calls
	steps = append(steps, stepNotes...)
	if err != nil {
		return false, "", false, imeSwitches, vlmCalls, steps, err
	}

	if requiredMode == textInputModeASCII && asciiPartNeedsEnter(analysis) {
		steps = append(steps, "ASCII preedit pending; press Enter to commit")
		if err := e.tapKeys(ctx, []string{"enter"}); err != nil {
			return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
		}
		if err := e.sleepFor(ctx, textInputFocusRestoreDelay); err != nil {
			return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
		}
		analysis, calls, stepNotes, err = e.analyzeScreen(ctx, platform, args, segments)
		vlmCalls += calls
		steps = append(steps, stepNotes...)
		if err != nil {
			return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
		}
		if analysis.CompositionPending {
			return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, nil
		}
		if committed, fieldText := evaluateFieldCommit(analysis, args.Text); committed {
			return true, fieldText, false, imeSwitches, vlmCalls, steps, nil
		}
	}
	if requiredMode != textInputModeComposition || !imeCandidateStateActive(analysis) {
		if committed, fieldText := evaluateFieldCommit(analysis, args.Text); committed {
			return true, fieldText, false, imeSwitches, vlmCalls, steps, nil
		}
	}
	if analysis.CompositionPending {
		steps = append(steps, "composition pending; target not committed to input field yet")
	}

	if requiredMode == textInputModeComposition {
		candidateActions := 0
		pageAttempts := 0
		for candidateActions < textInputCandidateActionMax && imeCandidateStateActive(analysis) {
			if len(analysis.Candidates) > 0 {
				if err := e.selectCandidateByKeyboard(ctx, analysis.Candidates[0]); err != nil {
					return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
				}
				candidateActions++
				pageAttempts = 0
				if err := e.sleepFor(ctx, textInputFocusRestoreDelay); err != nil {
					return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
				}
			} else {
				if !analysis.CandidateExpand && pageAttempts >= textInputCandidatePageMax {
					break
				}
				if err := e.tapKeys(ctx, []string{"down"}); err != nil {
					return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
				}
				candidateActions++
				pageAttempts++
				delay := textInputCandidatePageDelay
				if analysis.CandidateExpand {
					delay = textInputFocusRestoreDelay
				}
				if err := e.sleepFor(ctx, delay); err != nil {
					return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
				}
			}

			analysis, calls, stepNotes, err = e.analyzeScreen(ctx, platform, args, segments)
			vlmCalls += calls
			steps = append(steps, stepNotes...)
			if err != nil {
				return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
			}
			if !imeCandidateStateActive(analysis) {
				if committed, fieldText := evaluateFieldCommit(analysis, args.Text); committed {
					return true, fieldText, false, imeSwitches, vlmCalls, steps, nil
				}
				break
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
	req := textInputScreenAnalysisRequest{
		Phase:            textInputPhaseAfterType,
		Platform:         platform,
		TargetText:       args.Text,
		Focus:            args.Focus,
		Segments:         segments,
		LastDirectInput:  args.LastDirectInput,
		LastDirectTarget: args.LastDirectTarget,
	}
	for attempt := 1; attempt <= textInputVisionParseAttempts; attempt++ {
		analysis, err = e.vision.AnalyzeScreen(ctx, shot, req)
		vlmCalls++
		if err == nil {
			break
		}
		var syntaxErr *json.SyntaxError
		if !errors.As(err, &syntaxErr) || attempt == textInputVisionParseAttempts {
			return textInputScreenAnalysis{}, vlmCalls, nil, err
		}
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
	return e.sleepFor(ctx, textInputFocusRestoreDelay)
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

func (e *textInputEngine) selectCandidateByKeyboard(ctx context.Context, candidate textInputCandidateSelection) error {
	offset := candidate.Offset
	if offset > textInputCandidateMoveMax || offset < -textInputCandidateMoveMax {
		return fmt.Errorf("candidate keyboard offset %d exceeds limit %d", offset, textInputCandidateMoveMax)
	}
	direction := "right"
	if offset < 0 {
		direction = "left"
		offset = -offset
	}
	for i := 0; i < offset; i++ {
		if err := e.tapKeys(ctx, []string{direction}); err != nil {
			return err
		}
		if err := e.sleepFor(ctx, textInputKeystrokeGap); err != nil {
			return err
		}
	}
	return e.tapKeys(ctx, []string{"space"})
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
	if err := e.sleepFor(ctx, textInputIMESwitchSettleDelay); err != nil {
		return "", err
	}
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
			if err := e.sleepFor(ctx, textInputKeystrokeGap); err != nil {
				return err
			}
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

// waitForPostInputFrame waits for the frame service to advance and stabilize
// after HID input. ScreenshotTool by itself asks for latest_frame with
// since_seq=0 and timeout=0, so it can otherwise return the pre-input frame.
func (e *textInputEngine) waitForPostInputFrame(ctx context.Context) error {
	if e == nil || e.hw.waitStable == nil {
		return nil
	}
	out, err := callTextInputTool(ctx, e.hw.waitStable, "{}")
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
		if err := e.sleepFor(ctx, textInputKeystrokeGap); err != nil {
			return false, fieldText, false, vlmCalls, steps, err
		}
	}
	// Do not commit the IME's default candidate blindly. Analyze the live
	// candidate list first; analyzeActVerify will click the candidate that vision
	// identified for the current target prefix and then verify the committed text.
	if err := e.sleepFor(ctx, textInputFocusRestoreDelay); err != nil {
		return false, fieldText, false, vlmCalls, steps, err
	}

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

func (e *textInputEngine) typeCompositionSearch(ctx context.Context, platform string, args enterTextInFieldArgs, segments []string) (committed bool, fieldText string, wrongIME bool, vlmCalls int, steps []string, err error) {
	for i, segment := range segments {
		steps = append(steps, fmt.Sprintf("type segment %d: %q", i+1, segment))
		out, err := e.hw.keyboardText.Call(ctx, jsonString(map[string]string{"text": segment}))
		if err != nil {
			return false, fieldText, false, vlmCalls, steps, err
		}
		if strings.HasPrefix(out, "error:") {
			return false, fieldText, false, vlmCalls, steps, fmt.Errorf("%s", out)
		}
		if err := e.sleepFor(ctx, textInputKeystrokeGap); err != nil {
			return false, fieldText, false, vlmCalls, steps, err
		}
	}
	// Search input follows the same candidate-first rule: inspect and select the
	// intended candidate instead of accepting the IME's default with Space.
	if err := e.sleepFor(ctx, textInputFocusRestoreDelay); err != nil {
		return false, fieldText, false, vlmCalls, steps, err
	}
	var calls int
	var notes []string
	committed, fieldText, wrongIME, _, calls, notes, err = e.analyzeActVerify(ctx, platform, args, textInputModeComposition, segments)
	vlmCalls += calls
	steps = append(steps, notes...)
	return committed, fieldText, wrongIME, vlmCalls, steps, err
}

func (e *textInputEngine) clearField(ctx context.Context, platform string) error {
	// Take a screenshot to determine how many characters to delete
	shot, err := e.captureScreenshot(ctx)
	if err != nil {
		// Fallback: use escape to dismiss composition then moderate backspaces
		_ = e.tapKeys(ctx, []string{"escape"})
		_ = e.sleepFor(ctx, textInputKeystrokeGap)
		for i := 0; i < textInputClearBackspaceFallback; i++ {
			_ = e.tapKeys(ctx, []string{"backspace"})
			_ = e.sleepFor(ctx, textInputKeystrokeGap)
		}
		return nil
	}
	analysis, err := e.vision.AnalyzeScreen(ctx, shot, textInputScreenAnalysisRequest{
		Phase:    textInputPhaseAfterType,
		Platform: platform,
	})
	if err != nil {
		_ = e.tapKeys(ctx, []string{"escape"})
		_ = e.sleepFor(ctx, textInputKeystrokeGap)
		for i := 0; i < textInputClearBackspaceFallback; i++ {
			_ = e.tapKeys(ctx, []string{"backspace"})
			_ = e.sleepFor(ctx, textInputKeystrokeGap)
		}
		return nil
	}

	// Dismiss any active composition in candidate bar first
	if analysis.CompositionPending {
		_ = e.tapKeys(ctx, []string{"escape"})
		_ = e.sleepFor(ctx, textInputKeystrokeGap)
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
		if err := e.sleepFor(ctx, textInputKeystrokeGap); err != nil {
			return err
		}
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
