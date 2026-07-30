package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

	langtools "github.com/tmc/langchaingo/tools"
)

func textInputLogf(format string, args ...any) {
	log.Printf("[text-input] "+format, args...)
}

type textInputHardwareDeps struct {
	pointerMode  string
	mouseClick   langtools.Tool
	touchGesture langtools.Tool
	keyboardTap  langtools.Tool
	keyboardText langtools.Tool
	quickAction  langtools.Tool
	screenshot   langtools.Tool
	waitStable   langtools.Tool
}

func (d textInputHardwareDeps) platform() string {
	// An empty dependency is the same as the default HID configuration.
	if strings.EqualFold(strings.TrimSpace(d.pointerMode), "") || strings.EqualFold(strings.TrimSpace(d.pointerMode), "absolute") {
		return "ios"
	}
	return "android"
}

func textInputHardwarePlatform(hw *textInputHardwareDeps) string {
	if hw == nil {
		return "android"
	}
	return hw.platform()
}

type textInputEngine struct {
	hw     textInputHardwareDeps
	vision textInputVision
	sleep  func(context.Context, time.Duration) error
}

type enterTextInFieldArgs struct {
	Text        string         `json:"text"`
	Mode        string         `json:"mode,omitempty"`
	SkipFocus   bool           `json:"skip_focus,omitempty"`
	Focus       focusPointArgs `json:"focus"`
	MaxAttempts int            `json:"max_attempts,omitempty"`
	// Segments is a compatibility override used only by legacy internal callers.
	// The public enter_text tool decodes a separate schema and never populates it.
	Segments         []string `json:"segments,omitempty"`
	LastDirectInput  string   `json:"-"`
	LastDirectTarget string   `json:"-"`
	CurrentIMEPart   string   `json:"-"`
	VerifyTextSuffix bool     `json:"-"`
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
	platform := e.hw.platform()
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
				return enterTextInFieldResult{OK: false, Committed: false, Interrupted: true, TargetText: args.Text, FieldText: analysis.FieldText, RequiredMode: string(requiredMode), Attempts: attempt, IMESwitches: imeSwitches, VLMCalls: vlmCalls, ObservedMode: string(analysis.ObservedMode), CompositionPending: analysis.CompositionPending, WrongIMESuspected: wrongIME || analysis.WrongIMESuspected || analysis.SuggestSwitchIME, Reason: "search handoff after composition input"}, nil
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

// RunSegmented enters mixed text without relying on a clipboard route. The
// caller keeps the keyboard isolated for this entire operation. The engine
// probes the active input mode once, maintains that state while switching
// between direct and composition parts, and never uses pointer input.
func (e *textInputEngine) RunSegmented(ctx context.Context, args enterTextInFieldArgs) (enterTextInFieldResult, error) {
	if e == nil || e.vision == nil {
		return enterTextInFieldResult{}, fmt.Errorf("text input engine not configured")
	}
	if strings.TrimSpace(args.Text) == "" {
		return enterTextInFieldResult{Reason: "text is required"}, nil
	}
	initialChunks, err := splitTextInputChunks(args.Text)
	if err != nil {
		return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Reason: err.Error()}, nil
	}
	platform := e.hw.platform()
	type compositionPlanResult struct {
		chunks   []textInputChunk
		segments [][]string
		err      error
	}
	planCtx, cancelPlanning := context.WithCancel(ctx)
	defer cancelPlanning()
	planResultCh := make(chan compositionPlanResult, 1)
	go func() {
		chunks, partitionErr := e.partitionIMEChunks(planCtx, initialChunks, args.Segments)
		if partitionErr != nil {
			planResultCh <- compositionPlanResult{err: partitionErr}
			return
		}
		segments, planErr := e.planCompositionSegmentsForChunks(planCtx, chunks, args.Segments)
		planResultCh <- compositionPlanResult{chunks: chunks, segments: segments, err: planErr}
	}()
	textInputLogf("local start platform=%s target_len=%d initial_parts=%d", platform, len([]rune(args.Text)), len(initialChunks))
	textInputLogf("probe and IME partition/planning started concurrently initial_parts=%d", len(initialChunks))
	currentMode, vlmCalls, probeSteps, err := e.probeTextInputMode(ctx, platform, args)
	if err != nil {
		cancelPlanning()
		textInputLogf("probe failed platform=%s err=%v", platform, err)
		return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, VLMCalls: vlmCalls, Reason: err.Error()}, nil
	}
	textInputLogf("probe state recorded mode=%s vlm_calls=%d", currentMode, vlmCalls)

	planResult := <-planResultCh
	if planResult.err != nil {
		return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, Reason: planResult.err.Error()}, nil
	}
	chunks := planResult.chunks
	compositionSegments := planResult.segments
	textInputLogf("IME partition and planning complete initial_parts=%d final_parts=%d", len(initialChunks), len(chunks))
	for index, chunk := range chunks {
		kind := "ime"
		if chunk.ascii {
			kind = "ascii"
			if chunk.text != chunk.input {
				kind = "ime-direct-punctuation"
			}
		}
		textInputLogf("part planned index=%d/%d kind=%s target=%q input=%q", index+1, len(chunks), kind, truncateForLog(chunk.text, 80), truncateForLog(chunk.input, 80))
	}
	steps := append([]string{"split input into direct-input and partitioned IME parts"}, probeSteps...)
	lastFieldText := ""
	imeSwitches := 0
	for index, chunk := range chunks {
		if chunk.ascii {
			targetMode := textInputModeASCII
			if chunk.text != chunk.input {
				// Full-width punctuation is sent through an ASCII HID key, but the
				// active IME is what turns that key into the requested target rune.
				targetMode = textInputModeComposition
			}
			textInputLogf("part begin index=%d/%d kind=direct current_mode=%s target_mode=%s target=%q input=%q", index+1, len(chunks), currentMode, targetMode, truncateForLog(chunk.text, 80), truncateForLog(chunk.input, 80))
			var switched bool
			currentMode, switched, err = e.ensureTextInputMode(ctx, platform, currentMode, targetMode)
			if err != nil {
				textInputLogf("part mode adjustment failed index=%d current_mode=%s target_mode=%s err=%v", index+1, currentMode, targetMode, err)
				return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, IMESwitches: imeSwitches, VLMCalls: vlmCalls, Reason: err.Error()}, nil
			}
			if switched {
				imeSwitches++
			}
			textInputLogf("part mode ready index=%d mode=%s switched=%t total_switches=%d", index+1, currentMode, switched, imeSwitches)
			if err := e.typeASCII(ctx, chunk.input); err != nil {
				textInputLogf("part direct input failed index=%d mode=%s err=%v", index+1, currentMode, err)
				return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, IMESwitches: imeSwitches, VLMCalls: vlmCalls, Reason: err.Error()}, nil
			}
			textInputLogf("part direct input complete index=%d mode=%s", index+1, currentMode)
			steps = append(steps, fmt.Sprintf("typed direct-input part %d without verification: %q", index+1, chunk.text))
			continue
		}
		textInputLogf("part begin index=%d/%d kind=ime current_mode=%s target=%q segments=%d", index+1, len(chunks), currentMode, truncateForLog(chunk.text, 80), len(compositionSegments[index]))
		var switched bool
		currentMode, switched, err = e.ensureTextInputMode(ctx, platform, currentMode, textInputModeComposition)
		if err != nil {
			textInputLogf("part mode adjustment failed index=%d current_mode=%s target_mode=%s err=%v", index+1, currentMode, textInputModeComposition, err)
			return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, IMESwitches: imeSwitches, VLMCalls: vlmCalls, Reason: err.Error()}, nil
		}
		if switched {
			imeSwitches++
		}
		textInputLogf("part mode ready index=%d mode=%s switched=%t total_switches=%d", index+1, currentMode, switched, imeSwitches)
		if err := e.sleepFor(ctx, textInputCompositionReadyDelay); err != nil {
			return enterTextInFieldResult{TargetText: args.Text, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, VLMCalls: vlmCalls, Reason: err.Error()}, nil
		}
		partialArgs := args
		partialArgs.Text = chunk.text
		partialArgs.CurrentIMEPart = chunk.text
		partialArgs.VerifyTextSuffix = true
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
			textInputLogf("part IME input failed index=%d committed=%t wrong_ime=%t field=%q err=%v", index+1, committed, wrongIME, truncateForLog(fieldText, 120), err)
			return enterTextInFieldResult{TargetText: args.Text, FieldText: fieldText, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, VLMCalls: vlmCalls, WrongIMESuspected: wrongIME, Reason: reason}, nil
		}
		textInputLogf("part IME input complete index=%d field=%q", index+1, truncateForLog(fieldText, 120))
		lastFieldText = fieldText
	}
	result := enterTextInFieldResult{OK: true, Committed: true, TargetText: args.Text, FieldText: lastFieldText, RequiredMode: string(requiredTextInputMode(args.Text)), Attempts: 1, IMESwitches: imeSwitches, VLMCalls: vlmCalls, Reason: "all text parts entered"}
	textInputLogf("local complete parts=%d final_mode=%s ime_switches=%d vlm_calls=%d", len(chunks), currentMode, imeSwitches, vlmCalls)
	return finalizeEnterTextInFieldResult(result, args.SendAfterCommit), nil
}

func (e *textInputEngine) partitionIMEChunks(ctx context.Context, chunks []textInputChunk, override []string) ([]textInputChunk, error) {
	if len(normalizeCompositionSegments(override)) > 0 {
		return chunks, nil
	}
	partitioner, ok := e.vision.(textInputCompositionPartitioner)
	type partResult struct {
		index int
		parts []string
		err   error
	}
	partitioned := make([][]string, len(chunks))
	results := make(chan partResult, len(chunks))
	semaphore := make(chan struct{}, textInputPlanConcurrency)
	partitionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := 0
	for index, chunk := range chunks {
		if chunk.ascii || len([]rune(chunk.text)) < textInputIMEPartitionMinRunes {
			continue
		}
		if !ok {
			return nil, fmt.Errorf("IME partitioner is not configured for %q", chunk.text)
		}
		jobs++
		go func(index int, target string) {
			select {
			case semaphore <- struct{}{}:
			case <-partitionCtx.Done():
				results <- partResult{index: index, err: partitionCtx.Err()}
				return
			}
			defer func() { <-semaphore }()
			textInputLogf("IME partition begin index=%d/%d target=%q", index+1, len(chunks), truncateForLog(target, 80))
			parts, err := partitioner.PartitionComposition(partitionCtx, target)
			if err == nil {
				_, err = validateTextInputPartition(target, parts)
			}
			if err != nil {
				textInputLogf("IME partition failed index=%d/%d target=%q err=%v", index+1, len(chunks), truncateForLog(target, 80), err)
			} else {
				textInputLogf("IME partition complete index=%d/%d target=%q parts=%d", index+1, len(chunks), truncateForLog(target, 80), len(parts))
			}
			results <- partResult{index: index, parts: parts, err: err}
		}(index, chunk.text)
	}
	var firstErr error
	for completed := 0; completed < jobs; completed++ {
		part := <-results
		if part.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("partition IME part: %w", part.err)
				cancel()
			}
			continue
		}
		partitioned[part.index] = part.parts
	}
	if firstErr != nil {
		return nil, firstErr
	}
	result := make([]textInputChunk, 0, len(chunks)+jobs)
	for index, chunk := range chunks {
		if len(partitioned[index]) == 0 {
			result = append(result, chunk)
			continue
		}
		for _, part := range partitioned[index] {
			result = append(result, textInputChunk{text: part})
		}
	}
	return result, nil
}

func validateTextInputPartition(target string, parts []string) ([]string, error) {
	raw, err := json.Marshal(struct {
		Parts []string `json:"parts"`
	}{Parts: parts})
	if err != nil {
		return nil, err
	}
	return parseTextInputPartition(target, string(raw))
}

func (e *textInputEngine) planCompositionSegmentsForChunks(ctx context.Context, chunks []textInputChunk, override []string) ([][]string, error) {
	if len(normalizeCompositionSegments(override)) > 0 {
		return splitSegmentsForTextChunks(chunks, override)
	}
	segments := make([][]string, len(chunks))
	type partResult struct {
		index    int
		segments []string
		err      error
	}
	jobs := 0
	results := make(chan partResult, len(chunks))
	semaphore := make(chan struct{}, textInputPlanConcurrency)
	planCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for index, chunk := range chunks {
		if chunk.ascii {
			continue
		}
		jobs++
		go func(index int, target string) {
			select {
			case semaphore <- struct{}{}:
			case <-planCtx.Done():
				results <- partResult{index: index, err: planCtx.Err()}
				return
			}
			defer func() { <-semaphore }()
			textInputLogf("IME planning begin index=%d/%d target=%q", index+1, len(chunks), truncateForLog(target, 80))
			planned, planErr := e.compositionSegmentsForText(planCtx, target, nil)
			if planErr != nil {
				textInputLogf("IME planning failed index=%d/%d target=%q err=%v", index+1, len(chunks), truncateForLog(target, 80), planErr)
			} else {
				textInputLogf("IME planning part complete index=%d/%d target=%q segments=%d", index+1, len(chunks), truncateForLog(target, 80), len(planned))
			}
			results <- partResult{index: index, segments: planned, err: planErr}
		}(index, chunk.text)
	}
	var firstErr error
	for completed := 0; completed < jobs; completed++ {
		result := <-results
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
				cancel()
			}
			continue
		}
		segments[result.index] = result.segments
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return segments, nil
}

func (e *textInputEngine) probeTextInputMode(ctx context.Context, platform string, args enterTextInFieldArgs) (mode textInputMode, vlmCalls int, steps []string, err error) {
	steps = append(steps, `probe input mode with "a"`)
	textInputLogf("probe begin platform=%s action=type_a", platform)
	if err = e.typeASCIIChunk(ctx, "a"); err != nil {
		textInputLogf("probe type_a failed err=%v", err)
		return textInputModeUnknown, vlmCalls, steps, fmt.Errorf("input mode probe: type a: %w", err)
	}
	textInputLogf("probe type_a complete")
	defer func() {
		textInputLogf("probe cleanup begin action=backspace")
		deleteErr := e.tapKeys(ctx, []string{"backspace"})
		if deleteErr == nil {
			deleteErr = e.sleepFor(ctx, textInputKeystrokeGap)
		}
		err = errors.Join(err, deleteErr)
		if deleteErr != nil {
			textInputLogf("probe cleanup failed action=backspace err=%v", deleteErr)
		} else {
			textInputLogf("probe cleanup complete action=backspace")
		}
	}()
	textInputLogf("probe settle begin delay=%s", textInputProbeSettleDelay)
	if err = e.sleepFor(ctx, textInputProbeSettleDelay); err != nil {
		return textInputModeUnknown, vlmCalls, steps, err
	}
	textInputLogf("probe settle complete delay=%s", textInputProbeSettleDelay)
	shot, captureErr := e.captureScreenshot(ctx)
	if captureErr != nil {
		textInputLogf("probe screenshot failed err=%v", captureErr)
		return textInputModeUnknown, vlmCalls, steps, captureErr
	}
	probeVision, ok := e.vision.(textInputProbeVision)
	if !ok {
		return textInputModeUnknown, vlmCalls, steps, fmt.Errorf("input mode probe vision is not configured")
	}
	analysis, analyzeErr := probeVision.ProbeInputMode(ctx, shot, platform, args.Focus)
	vlmCalls++
	if analyzeErr != nil {
		textInputLogf("probe analysis failed vlm_calls=%d err=%v", vlmCalls, analyzeErr)
		return textInputModeUnknown, vlmCalls, steps, analyzeErr
	}
	textInputLogf("probe analysis mode=%s typed_a=%t inline_preedit=%t candidate_popup=%t cjk_candidate=%t onscreen_keyboard=%t evidence=%q", analysis.Mode, analysis.TypedAVisible, analysis.InlinePreeditVisible, analysis.CandidatePopupVisible, analysis.CJKCandidateVisible, analysis.OnscreenKeyboardVisible, truncateForLog(analysis.Evidence, 160))
	mode = analysis.Mode
	if mode != textInputModeASCII && mode != textInputModeComposition {
		textInputLogf("probe analysis unresolved mode=%s evidence=%q", mode, truncateForLog(analysis.Evidence, 160))
		return textInputModeUnknown, vlmCalls, steps, fmt.Errorf("input mode probe returned %q", mode)
	}
	textInputLogf("probe complete recorded_mode=%s", mode)
	steps = append(steps, fmt.Sprintf("input mode probe detected %s", mode))
	return mode, vlmCalls, steps, nil
}

func (e *textInputEngine) ensureTextInputMode(ctx context.Context, platform string, current, target textInputMode) (textInputMode, bool, error) {
	textInputLogf("mode ensure current=%s target=%s platform=%s", current, target, platform)
	if current == target {
		textInputLogf("mode ensure no-op mode=%s", current)
		return current, false, nil
	}
	if current != textInputModeASCII && current != textInputModeComposition {
		textInputLogf("mode ensure refused current=%s target=%s", current, target)
		return current, false, fmt.Errorf("cannot switch from unknown input mode %q", current)
	}
	if _, err := e.cycleIME(ctx, platform); err != nil {
		textInputLogf("mode switch failed current=%s target=%s err=%v", current, target, err)
		return current, false, err
	}
	textInputLogf("mode switch recorded previous=%s current=%s", current, target)
	return target, true, nil
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
		// Keep punctuation isolated so it is sent in its original position and
		// cannot be absorbed into an adjacent direct-input or IME run.
		if isTextInputPunctuation(r) {
			chunks = append(chunks, textInputChunk{text: string(r), input: input, ascii: ascii})
			continue
		}
		if len(chunks) == 0 || chunks[len(chunks)-1].space || textInputChunkIsPunctuation(chunks[len(chunks)-1]) || chunks[len(chunks)-1].ascii != ascii {
			chunks = append(chunks, textInputChunk{ascii: ascii})
		}
		chunks[len(chunks)-1].text += string(r)
		chunks[len(chunks)-1].input += input
	}
	return chunks, nil
}

func isTextInputPunctuation(r rune) bool {
	if r <= 0x7f {
		return r != ' ' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}
	return unicode.IsPunct(r)
}

func textInputChunkIsPunctuation(chunk textInputChunk) bool {
	var last rune
	count := 0
	for _, r := range chunk.text {
		last = r
		count++
	}
	return count == 1 && isTextInputPunctuation(last)
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
		textInputLogf("candidate analysis failed target=%q err=%v", truncateForLog(args.Text, 120), err)
		return false, "", false, imeSwitches, vlmCalls, steps, err
	}
	textInputLogf("candidate state analysis target=%q observed_mode=%s pending=%t matched=%t field=%q", truncateForLog(args.Text, 120), analysis.ObservedMode, analysis.CompositionPending, analysis.TargetMatched, truncateForLog(analysis.FieldText, 120))

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
		selectedCandidateText := ""
	candidateLoop:
		for candidateActions < textInputCandidateActionMax && imeCandidateStateActive(analysis) {
			action, calls, err := e.decideCandidateAction(ctx, platform, args, segments, selectedCandidateText)
			vlmCalls += calls
			if err != nil {
				textInputLogf("candidate decision failed action=%d err=%v", candidateActions+1, err)
				return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
			}
			textInputLogf("candidate decision action=%d decision=%s offset=%d text=%q completes_part=%t", candidateActions+1, action.Action, action.Offset, truncateForLog(action.Text, 80), action.CompletesPart)
			switch action.Action {
			case textInputCandidateActionSelect:
				if err := e.selectCandidateByKeyboard(ctx, action); err != nil {
					textInputLogf("candidate select failed action=%d err=%v", candidateActions+1, err)
					return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
				}
				selectedCandidateText += action.Text
				candidateActions++
				pageAttempts = 0
				if action.CompletesPart {
					candidateTarget := args.CurrentIMEPart
					if candidateTarget == "" {
						candidateTarget = args.Text
					}
					textInputLogf("candidate selection accepted without post-action verification actions=%d target=%q", candidateActions, truncateForLog(candidateTarget, 120))
					return true, analysis.FieldText, false, imeSwitches, vlmCalls, steps, nil
				}
				if err := e.sleepFor(ctx, textInputFocusRestoreDelay); err != nil {
					return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
				}
			case textInputCandidateActionExpand:
				if pageAttempts >= textInputCandidatePageMax {
					textInputLogf("candidate paging stopped reason=max_pages page_attempts=%d", pageAttempts)
					break candidateLoop
				}
				textInputLogf("candidate model action=%d decision=expand page_attempt=%d", candidateActions+1, pageAttempts+1)
				if err := e.tapKeys(ctx, []string{"down"}); err != nil {
					return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
				}
				candidateActions++
				pageAttempts++
				if err := e.sleepFor(ctx, textInputFocusRestoreDelay); err != nil {
					return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
				}
			case textInputCandidateActionUp:
				textInputLogf("candidate model action=%d decision=up", candidateActions+1)
				if err := e.tapKeys(ctx, []string{"up"}); err != nil {
					return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
				}
				candidateActions++
				if pageAttempts > 0 {
					pageAttempts--
				}
				if err := e.sleepFor(ctx, textInputFocusRestoreDelay); err != nil {
					return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
				}
			default:
				textInputLogf("candidate model action=%d decision=none pending=%t", candidateActions+1, analysis.CompositionPending)
				break candidateLoop
			}

			analysis, calls, stepNotes, err = e.analyzeScreen(ctx, platform, args, segments)
			vlmCalls += calls
			steps = append(steps, stepNotes...)
			if err != nil {
				textInputLogf("candidate reanalysis failed action=%d err=%v", candidateActions, err)
				return false, analysis.FieldText, false, imeSwitches, vlmCalls, steps, err
			}
			textInputLogf("candidate state reanalysis action=%d observed_mode=%s pending=%t matched=%t field=%q", candidateActions, analysis.ObservedMode, analysis.CompositionPending, analysis.TargetMatched, truncateForLog(analysis.FieldText, 120))
			if !imeCandidateStateActive(analysis) {
				if committed, fieldText := evaluateFieldCommit(analysis, args.Text); committed {
					textInputLogf("candidate selection complete actions=%d field=%q", candidateActions, truncateForLog(fieldText, 120))
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
	textInputLogf("candidate selection incomplete wrong_ime=%t field=%q", wrongIME, truncateForLog(fieldText, 120))
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
		MatchTextSuffix:  args.VerifyTextSuffix,
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
	steps = append(steps, fmt.Sprintf("analyze: mode=%s field=%q pending=%v wrong_ime=%v",
		analysis.ObservedMode, analysis.FieldText, analysis.CompositionPending, analysis.WrongIMESuspected))
	return analysis, vlmCalls, steps, nil
}

func (e *textInputEngine) decideCandidateAction(ctx context.Context, platform string, args enterTextInFieldArgs, segments []string, selectedCandidateText string) (textInputCandidateAction, int, error) {
	vision, ok := e.vision.(textInputCandidateVision)
	if !ok {
		return textInputCandidateAction{}, 0, fmt.Errorf("candidate action vision is not configured")
	}
	shot, err := e.captureScreenshot(ctx)
	if err != nil {
		return textInputCandidateAction{}, 0, err
	}
	action, err := vision.DecideCandidateAction(ctx, shot, textInputScreenAnalysisRequest{
		Phase:                  textInputPhaseAfterType,
		Platform:               platform,
		TargetText:             args.Text,
		CandidateTargetText:    args.CurrentIMEPart,
		CandidateCommittedText: selectedCandidateText,
		Focus:                  args.Focus,
		Segments:               segments,
	})
	return action, 1, err
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
	return e.tapKeysWithHold(ctx, keys, 0)
}

func (e *textInputEngine) tapKeysWithHold(ctx context.Context, keys []string, holdMs int) error {
	if e == nil || e.hw.keyboardTap == nil {
		return fmt.Errorf("keyboard_tap is not configured")
	}
	input := map[string]any{"keys": keys}
	if holdMs > 0 {
		input["hold_ms"] = holdMs
	}
	out, err := callTextInputTool(ctx, e.hw.keyboardTap, jsonString(input))
	if err != nil {
		return err
	}
	return interpretTextInputToolOutput(out)
}

func (e *textInputEngine) selectCandidateByKeyboard(ctx context.Context, action textInputCandidateAction) error {
	offset := action.Offset
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
		textInputLogf("mode switch key resolution failed platform=%s err=%v", platform, err)
		return "", err
	}
	textInputLogf("mode switch keypress begin platform=%s keys=%s hold_ms=%d", platform, strings.Join(keys, "+"), textInputIMESwitchHoldMs)
	if err := e.tapKeysWithHold(ctx, keys, textInputIMESwitchHoldMs); err != nil {
		textInputLogf("mode switch keypress failed platform=%s keys=%s err=%v", platform, strings.Join(keys, "+"), err)
		return "", err
	}
	textInputLogf("mode switch keypress sent platform=%s keys=%s hold_ms=%d settle=%s", platform, strings.Join(keys, "+"), textInputIMESwitchHoldMs, textInputIMESwitchSettleDelay)
	if err := e.sleepFor(ctx, textInputIMESwitchSettleDelay); err != nil {
		textInputLogf("mode switch settle failed platform=%s keys=%s err=%v", platform, strings.Join(keys, "+"), err)
		return "", err
	}
	textInputLogf("mode switch keypress complete platform=%s keys=%s", platform, strings.Join(keys, "+"))
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
		textInputLogf("IME segment input begin index=%d/%d segment=%q", i+1, len(segments), truncateForLog(segment, 80))
		_, err := callTextInputTool(ctx, e.hw.keyboardText, jsonString(map[string]string{"text": segment}))
		if err != nil {
			textInputLogf("IME segment input failed index=%d/%d err=%v", i+1, len(segments), err)
			return false, fieldText, false, vlmCalls, steps, err
		}
		textInputLogf("IME segment input complete index=%d/%d", i+1, len(segments))
		if err := e.sleepFor(ctx, textInputKeystrokeGap); err != nil {
			return false, fieldText, false, vlmCalls, steps, err
		}
	}
	// Do not commit the IME's default candidate blindly. Analyze the live
	// candidate list first; analyzeActVerify will execute the model-selected
	// candidate action. A selection that completes the part returns immediately.
	if err := e.sleepFor(ctx, textInputFocusRestoreDelay); err != nil {
		return false, fieldText, false, vlmCalls, steps, err
	}

	var calls int
	var notes []string
	committed, fieldText, wrongIME, _, calls, notes, err = e.analyzeActVerify(ctx, platform, args, textInputModeComposition, segments)
	vlmCalls += calls
	steps = append(steps, notes...)
	if err != nil {
		textInputLogf("IME candidate phase failed target=%q err=%v", truncateForLog(args.Text, 120), err)
		return false, fieldText, wrongIME, vlmCalls, steps, err
	}
	textInputLogf("IME candidate phase complete target=%q committed=%t wrong_ime=%t field=%q", truncateForLog(args.Text, 120), committed, wrongIME, truncateForLog(fieldText, 120))
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
