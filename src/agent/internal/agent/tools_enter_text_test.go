package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type plannedTextInputVision struct {
	*stubTextInputVision
	plans map[string][]string
}

type concurrentPartPlanningVision struct {
	*stubTextInputVision
	started chan string
	release chan struct{}
}

func (v *concurrentPartPlanningVision) PlanComposition(ctx context.Context, text string) ([]string, error) {
	v.started <- text
	select {
	case <-v.release:
		if text == "甲" {
			return []string{"jia"}, nil
		}
		return []string{"yi"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type probePlanningConcurrentVision struct {
	planStarted  chan struct{}
	probeStarted chan struct{}
	analyses     []textInputScreenAnalysis
}

func (v *probePlanningConcurrentVision) PlanComposition(ctx context.Context, _ string) ([]string, error) {
	close(v.planStarted)
	select {
	case <-v.probeStarted:
		return []string{"ni"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (v *probePlanningConcurrentVision) ProbeInputMode(ctx context.Context, _ screenshotResult, _ string, _ focusPointArgs) (textInputProbeAnalysis, error) {
	close(v.probeStarted)
	select {
	case <-v.planStarted:
		return textInputProbeAnalysis{Mode: textInputModeComposition}, nil
	case <-ctx.Done():
		return textInputProbeAnalysis{}, ctx.Err()
	}
}

func (v *probePlanningConcurrentVision) AnalyzeScreen(_ context.Context, _ screenshotResult, _ textInputScreenAnalysisRequest) (textInputScreenAnalysis, error) {
	out := v.analyses[0]
	v.analyses = v.analyses[1:]
	return out, nil
}

func (v *probePlanningConcurrentVision) DecideCandidateAction(_ context.Context, _ screenshotResult, _ textInputScreenAnalysisRequest) (textInputCandidateAction, error) {
	return textInputCandidateAction{Action: textInputCandidateActionSelect, Text: "你"}, nil
}

func TestIMEPartsArePlannedConcurrently(t *testing.T) {
	vision := &concurrentPartPlanningVision{
		stubTextInputVision: &stubTextInputVision{},
		started:             make(chan string, 2),
		release:             make(chan struct{}),
	}
	engine := newTextInputEngine(textInputHardwareDeps{}, vision)
	resultCh := make(chan error, 1)
	go func() {
		_, err := engine.planCompositionSegmentsForChunks(context.Background(), []textInputChunk{{text: "甲"}, {text: "乙"}}, nil)
		resultCh <- err
	}()
	for index := 0; index < 2; index++ {
		select {
		case <-vision.started:
		case <-time.After(time.Second):
			t.Fatal("IME part planning did not start concurrently")
		}
	}
	close(vision.release)
	if err := <-resultCh; err != nil {
		t.Fatalf("planCompositionSegmentsForChunks() error = %v", err)
	}
}

func TestProbeAndIMEPlanningRunConcurrently(t *testing.T) {
	vision := &probePlanningConcurrentVision{
		planStarted:  make(chan struct{}),
		probeStarted: make(chan struct{}),
		analyses: []textInputScreenAnalysis{
			{ObservedMode: textInputModeComposition, CompositionPending: true},
			{ObservedMode: textInputModeComposition, FieldText: "你", TargetMatched: true},
		},
	}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := engine.RunSegmented(ctx, enterTextInFieldArgs{Text: "你"})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want concurrent probe/planning success", result, err)
	}
}

func (v *plannedTextInputVision) PlanComposition(_ context.Context, text string) ([]string, error) {
	return v.plans[text], nil
}

func TestSplitTextInputChunksPreservesASCIIAndIMERuns(t *testing.T) {
	chunks, err := splitTextInputChunks("A你好-42世界")
	if err != nil {
		t.Fatalf("splitTextInputChunks() error = %v", err)
	}
	want := []textInputChunk{
		{text: "A", input: "A", ascii: true},
		{text: "你好", ascii: false},
		{text: "-", input: "-", ascii: true},
		{text: "42", input: "42", ascii: true},
		{text: "世界", ascii: false},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("chunks = %#v, want %#v", chunks, want)
	}
	segments, err := splitSegmentsForTextChunks(chunks, []string{"ni", "hao", "shi", "jie"})
	if err != nil {
		t.Fatalf("splitSegmentsForTextChunks() error = %v", err)
	}
	if want := [][]string{nil, {"ni", "hao"}, nil, nil, {"shi", "jie"}}; !reflect.DeepEqual(segments, want) {
		t.Fatalf("segments = %#v, want %#v", segments, want)
	}
}

func TestSplitTextInputChunksMakesEachSpaceAStandalonePart(t *testing.T) {
	chunks, err := splitTextInputChunks("HDMI 4k60 test")
	if err != nil {
		t.Fatal(err)
	}
	want := []textInputChunk{
		{text: "HDMI", input: "HDMI", ascii: true},
		{text: " ", input: " ", ascii: true, space: true},
		{text: "4k60", input: "4k60", ascii: true},
		{text: " ", input: " ", ascii: true, space: true},
		{text: "test", input: "test", ascii: true},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("chunks=%#v, want %#v", chunks, want)
	}
}

func TestRunSegmentedTypesSpaceWithoutVisionVerification(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeASCII, FieldText: "a"},
	}}
	kbText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	mouse := &recordingTextInputTool{name: "mouse_click", out: "ok"}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   mouse,
		keyboardTap:  kbTap,
		keyboardText: kbText,
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), enterTextInFieldArgs{
		Text: "hello test", Focus: focusPointArgs{X: 10, Y: 10},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented()=%+v err=%v", result, err)
	}
	if result.VLMCalls != 1 {
		t.Fatalf("VLM calls=%d, want only the input-mode probe", result.VLMCalls)
	}
	if len(kbText.calls) != 3 || len(kbTap.calls) != 2 || !strings.Contains(kbTap.calls[0], "backspace") || !strings.Contains(kbTap.calls[1], "space") {
		t.Fatalf("keyboard_text=%v keyboard_tap=%v", kbText.calls, kbTap.calls)
	}
	if len(mouse.calls) != 0 {
		t.Fatalf("mouse_click calls=%v, want none", mouse.calls)
	}
}

func TestTextInputProbeWaitsFiveHundredMillisecondsBeforeCapture(t *testing.T) {
	var delays []time.Duration
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}}}, func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	})

	mode, _, _, err := engine.probeTextInputMode(context.Background(), "ios", enterTextInFieldArgs{})
	if err != nil || mode != textInputModeASCII {
		t.Fatalf("probeTextInputMode() mode=%s err=%v", mode, err)
	}
	if len(delays) == 0 || delays[0] != 500*time.Millisecond {
		t.Fatalf("probe delays=%v, want first delay 500ms", delays)
	}
}

func TestSplitTextInputChunksMakesEachPunctuationMarkAStandalonePart(t *testing.T) {
	const target = "你好我是Aiden，是一个硬件智能Agent助手，现在我在混合输入各种内容，例如英文Hello和数字12345"
	chunks, err := splitTextInputChunks(target)
	if err != nil {
		t.Fatalf("splitTextInputChunks() error = %v", err)
	}
	want := []textInputChunk{
		{text: "你好我是", ascii: false},
		{text: "Aiden", input: "Aiden", ascii: true},
		{text: "，", input: ",", ascii: true},
		{text: "是一个硬件智能", ascii: false},
		{text: "Agent", input: "Agent", ascii: true},
		{text: "助手", ascii: false},
		{text: "，", input: ",", ascii: true},
		{text: "现在我在混合输入各种内容", ascii: false},
		{text: "，", input: ",", ascii: true},
		{text: "例如英文", ascii: false},
		{text: "Hello", input: "Hello", ascii: true},
		{text: "和数字", ascii: false},
		{text: "12345", input: "12345", ascii: true},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("chunks = %#v, want %#v", chunks, want)
	}
}

func TestSplitTextInputChunksSeparatesASCIIPunctuation(t *testing.T) {
	if !isTextInputPunctuation('(') {
		t.Fatal("opening parenthesis must be treated as punctuation")
	}
	chunks, err := splitTextInputChunks("hello, world! (test)")
	if err != nil {
		t.Fatal(err)
	}
	want := []textInputChunk{
		{text: "hello", input: "hello", ascii: true},
		{text: ",", input: ",", ascii: true},
		{text: " ", input: " ", ascii: true, space: true},
		{text: "world", input: "world", ascii: true},
		{text: "!", input: "!", ascii: true},
		{text: " ", input: " ", ascii: true, space: true},
		{text: "(", input: "(", ascii: true},
		{text: "test", input: "test", ascii: true},
		{text: ")", input: ")", ascii: true},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("chunks=%#v, want %#v", chunks, want)
	}
}

func TestTextInputEngineRunSegmentedUsesKeyboardAndIMEInOrder(t *testing.T) {
	keyboard := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeASCII, FieldText: "a"},
		{ObservedMode: textInputModeComposition, CompositionPending: true},
		{ObservedMode: textInputModeComposition, FieldText: "A你好", TargetMatched: true},
	}, actions: []textInputCandidateAction{{Action: textInputCandidateActionSelect, Offset: 0, Text: "你好"}}}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: keyboard,
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), enterTextInFieldArgs{
		Text: "A你好B", Focus: focusPointArgs{X: 10, Y: 10}, Segments: []string{"ni", "hao"},
	})
	if err != nil {
		t.Fatalf("RunSegmented() error = %v", err)
	}
	if !result.Committed || result.FieldText != "A你好" || result.IMESwitches != 2 {
		t.Fatalf("result = %+v, want ordered input with two maintained mode switches", result)
	}
	wantCalls := []string{jsonString(map[string]string{"text": "a"}), jsonString(map[string]string{"text": "A"}), jsonString(map[string]string{"text": "ni"}), jsonString(map[string]string{"text": "hao"}), jsonString(map[string]string{"text": "B"})}
	if !reflect.DeepEqual(keyboard.calls, wantCalls) {
		t.Fatalf("keyboard calls = %#v, want %#v", keyboard.calls, wantCalls)
	}
}

func TestTextInputEngineRunSegmentedPlansIMESegmentsInternally(t *testing.T) {
	keyboard := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	vision := &plannedTextInputVision{
		stubTextInputVision: &stubTextInputVision{analyses: []textInputScreenAnalysis{
			{ObservedMode: textInputModeASCII, FieldText: "a"},
			{ObservedMode: textInputModeComposition, FieldText: "A你好", TargetMatched: true},
		}},
		// The planner may return a pinyin phrase in one array entry; the engine
		// must split it before passing it to keyboard_text.
		plans: map[string][]string{"你好": {"ni hao"}},
	}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: keyboard,
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), enterTextInFieldArgs{
		Text: "A你好B", Focus: focusPointArgs{X: 10, Y: 10},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want internally planned committed result", result, err)
	}
	if want := []string{jsonString(map[string]string{"text": "a"}), jsonString(map[string]string{"text": "A"}), jsonString(map[string]string{"text": "ni"}), jsonString(map[string]string{"text": "hao"}), jsonString(map[string]string{"text": "B"})}; !reflect.DeepEqual(keyboard.calls, want) {
		t.Fatalf("keyboard calls = %#v, want %#v", keyboard.calls, want)
	}
}

func TestTextInputEngineDoesNotVerifyDirectPartsOrUseMouse(t *testing.T) {
	waitStable := &recordingTextInputTool{name: "wait_for_stable_screen", out: `{"ok":true,"stable":true}`}
	mouse := &recordingTextInputTool{name: "mouse_click", out: "ok"}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeComposition, CompositionPending: true},
		{ObservedMode: textInputModeComposition, FieldText: "你", TargetMatched: true},
	}}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		pointerMode:  "absolute",
		mouseClick:   mouse,
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		waitStable:   waitStable,
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), enterTextInFieldArgs{
		Text: "你A", Focus: focusPointArgs{X: 10, Y: 10}, Segments: []string{"ni"},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want committed result", result, err)
	}
	if result.IMESwitches != 1 {
		t.Fatalf("IME switches = %d, want one switch to ENG for the final ASCII part", result.IMESwitches)
	}
	if len(waitStable.calls) != 0 || len(mouse.calls) != 0 {
		t.Fatalf("waitStable=%v mouse=%v, want neither for direct parts", waitStable.calls, mouse.calls)
	}
}

func TestEnterTextToolSchemaKeepsIMESegmentsInternal(t *testing.T) {
	props, ok := (&EnterTextTool{}).ArgsSchema()["properties"].(map[string]any)
	if !ok {
		t.Fatal("enter_text schema properties missing")
	}
	if _, found := props["segments"]; found {
		t.Fatal("enter_text must not expose IME segments")
	}
	if _, found := props["platform"]; found {
		t.Fatal("enter_text must infer the platform from HID configuration")
	}
	var args enterTextArgs
	if err := json.Unmarshal([]byte(`{"text":"你好","focus":{"x":1,"y":1},"segments":["ni","hao"]}`), &args); err != nil {
		t.Fatal(err)
	}
	if engineArgs := args.toEngineArgs(); len(engineArgs.Segments) != 0 {
		t.Fatalf("public JSON must not populate internal segments: %#v", engineArgs.Segments)
	}
}

func TestTextInputPlatformUsesHIDPointerMode(t *testing.T) {
	if got := (textInputHardwareDeps{pointerMode: "absolute"}).platform(); got != "ios" {
		t.Fatalf("absolute pointer mode platform = %q, want ios", got)
	}
	if got := (textInputHardwareDeps{pointerMode: "touchscreen"}).platform(); got != "android" {
		t.Fatalf("touchscreen pointer mode platform = %q, want android", got)
	}
}

func TestEnterTextResultContainsOnlySuccessStatus(t *testing.T) {
	encoded := enterTextToolResultString(enterTextInFieldResult{
		OK: true, Committed: true, TargetText: "secret", FieldText: "secret",
		RequiredMode: "composition", Attempts: 3, IMESwitches: 2, VLMCalls: 9,
	})
	var fields map[string]any
	if err := json.Unmarshal([]byte(encoded), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || fields["ok"] != true {
		t.Fatalf("public enter_text success result = %s, want only ok=true", encoded)
	}
}

func TestEnterTextFailureResultContainsOnlyStatusAndSuggestion(t *testing.T) {
	encoded := enterTextToolResultString(enterTextInFieldResult{
		Reason: "internal diagnostic", TargetText: "secret", FieldText: "wrong text", VLMCalls: 9,
	})
	var fields map[string]any
	if err := json.Unmarshal([]byte(encoded), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields["ok"] != false || strings.TrimSpace(fields["suggestion"].(string)) == "" {
		t.Fatalf("public enter_text failure result = %s, want only ok=false and suggestion", encoded)
	}
	for _, hidden := range []string{"internal diagnostic", "secret", "wrong text", "vlm_calls", "reason", "field_text"} {
		if strings.Contains(encoded, hidden) {
			t.Fatalf("public enter_text failure result leaked %q: %s", hidden, encoded)
		}
	}
}

func TestTextInputEngineRunSegmentedUsesProbeStateInsteadOfASCIIVerification(t *testing.T) {
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeComposition, FieldText: "a", CompositionPending: true},
		{ObservedMode: textInputModeComposition, FieldText: "A你", TargetMatched: true},
	}}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  keyboardTap,
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), enterTextInFieldArgs{
		Text: "A你", Focus: focusPointArgs{X: 10, Y: 10}, Segments: []string{"ni"},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want committed result", result, err)
	}
	if result.IMESwitches != 2 {
		t.Fatalf("IME switches = %d, want IME->ENG->IME", result.IMESwitches)
	}
	if want := []string{
		jsonString(map[string][]string{"keys": {"backspace"}}),
		jsonString(map[string]any{"keys": []string{"capslock"}, "hold_ms": textInputIMESwitchHoldMs}),
		jsonString(map[string]any{"keys": []string{"capslock"}, "hold_ms": textInputIMESwitchHoldMs}),
	}; !reflect.DeepEqual(keyboardTap.calls, want) {
		t.Fatalf("keyboard_tap calls = %#v, want %#v", keyboardTap.calls, want)
	}
}

func TestTextInputEngineRunSegmentedDoesNotVerifyFinalASCIIPart(t *testing.T) {
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeComposition, FieldText: "a", CompositionPending: true},
		{ObservedMode: textInputModeComposition, FieldText: "你", TargetMatched: true},
	}}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  keyboardTap,
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), enterTextInFieldArgs{
		Text: "你A", Focus: focusPointArgs{X: 10, Y: 10}, Segments: []string{"ni"},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want committed result", result, err)
	}
	if result.VLMCalls != 2 {
		t.Fatalf("VLM calls = %d, want probe plus IME candidate verification only", result.VLMCalls)
	}
	if want := []string{
		jsonString(map[string][]string{"keys": {"backspace"}}),
		jsonString(map[string]any{"keys": []string{"capslock"}, "hold_ms": textInputIMESwitchHoldMs}),
	}; !reflect.DeepEqual(keyboardTap.calls, want) {
		t.Fatalf("keyboard_tap calls = %#v, want %#v", keyboardTap.calls, want)
	}
}

func TestTextInputEngineTypesFullWidthPunctuationInIMEMode(t *testing.T) {
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		pointerMode:  "absolute",
		keyboardTap:  keyboardTap,
		keyboardText: keyboardText,
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}}}, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), enterTextInFieldArgs{Text: "A，B"})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v", result, err)
	}
	if result.IMESwitches != 2 {
		t.Fatalf("IME switches = %d, want ENG->IME->ENG around full-width punctuation", result.IMESwitches)
	}
	if want := []string{
		jsonString(map[string]string{"text": "a"}),
		jsonString(map[string]string{"text": "A"}),
		jsonString(map[string]string{"text": ","}),
		jsonString(map[string]string{"text": "B"}),
	}; !reflect.DeepEqual(keyboardText.calls, want) {
		t.Fatalf("keyboard_text calls = %#v, want %#v", keyboardText.calls, want)
	}
	if want := []string{
		jsonString(map[string][]string{"keys": {"backspace"}}),
		jsonString(map[string]any{"keys": []string{"capslock"}, "hold_ms": textInputIMESwitchHoldMs}),
		jsonString(map[string]any{"keys": []string{"capslock"}, "hold_ms": textInputIMESwitchHoldMs}),
	}; !reflect.DeepEqual(keyboardTap.calls, want) {
		t.Fatalf("keyboard_tap calls = %#v, want %#v", keyboardTap.calls, want)
	}
}

func TestTextInputEngineRunCommitsPureASCIIPreeditOnlyWhenVisionApproves(t *testing.T) {
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeComposition, FieldText: "", CompositionPending: true},
		{ObservedMode: textInputModeASCII, FieldText: "10086"},
	}}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  keyboardTap,
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.Run(context.Background(), enterTextInFieldArgs{
		Text: "10086", Focus: focusPointArgs{X: 10, Y: 10},
	})
	if err != nil || !result.Committed || result.Attempts != 1 {
		t.Fatalf("Run() = %+v, %v; want first-attempt committed result", result, err)
	}
	if result.IMESwitches != 0 {
		t.Fatalf("IME switches = %d, want no proactive switch", result.IMESwitches)
	}
	if want := []string{
		jsonString(map[string][]string{"keys": {"enter"}}),
	}; !reflect.DeepEqual(keyboardTap.calls, want) {
		t.Fatalf("keyboard_tap calls = %#v, want %#v", keyboardTap.calls, want)
	}
}

func TestEnterTextToolPrefersAvailableBridge(t *testing.T) {
	pb := newTestPhoneBridge(t)
	pb.platform = "android"
	pb.connected = true
	pb.appState = "background"
	tool := &EnterTextTool{bridgeTool: &EnterTextViaBridgeTool{hw: &textInputHardwareDeps{pointerMode: "touchscreen"}, bridgeFn: func() *PhoneBridge { return pb }}}
	if !tool.bridgeAvailable(enterTextInFieldArgs{Text: "ASCII is bridged too"}) {
		t.Fatal("available Android bridge should be preferred before local entry")
	}
	tool.bridgeTool.hw.pointerMode = "absolute"
	if tool.bridgeAvailable(enterTextInFieldArgs{Text: "hello"}) {
		t.Fatal("iOS HID configuration should not select an Android bridge clipboard route")
	}
}

func TestEnterTextToolLocalPathRestoresIsolationOnSuccessAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name        string
		keyboardOut string
		wantOK      bool
	}{
		{name: "success", keyboardOut: "ok", wantOK: true},
		{name: "probe failure", keyboardOut: `{"ok":false,"message":"keyboard unavailable"}`, wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []string{}
			controller := newTestIOSKeyboardIsolationController(&events)
			keyboard := &recordingTextInputTool{name: "keyboard_text", out: tc.keyboardOut}
			mouse := &recordingTextInputTool{name: "mouse_click", out: "ok"}
			engine := newTextInputEngineWithSleep(textInputHardwareDeps{
				pointerMode:  "absolute",
				mouseClick:   mouse,
				keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
				keyboardText: keyboard,
				screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
			}, &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}}}, testNoWaitSleep)
			tool := &EnterTextTool{engine: engine, iosKeyboardIsolation: controller}

			out, err := tool.Call(context.Background(), `{"text":"x","focus":{"x":10,"y":10}}`)
			if err != nil {
				t.Fatalf("Call() error = %v", err)
			}
			if gotOK := strings.Contains(out, `"ok": true`); gotOK != tc.wantOK {
				t.Fatalf("output = %s, want ok=%v", out, tc.wantOK)
			}
			if want := []string{"isolate", "restore"}; !reflect.DeepEqual(events, want) {
				t.Fatalf("isolation events = %v, want %v", events, want)
			}
			if len(mouse.calls) != 0 {
				t.Fatalf("mouse_click calls = %v, want none", mouse.calls)
			}
		})
	}
}

func TestEnterTextToolIOSLocalPathRequiresIsolation(t *testing.T) {
	keyboard := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		pointerMode:  "absolute",
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: keyboard,
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, &stubTextInputVision{}, testNoWaitSleep)
	tool := &EnterTextTool{engine: engine}

	out, err := tool.Call(context.Background(), `{"text":"x","focus":{"x":10,"y":10}}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if out != jsonString(enterTextToolResult{OK: false, Suggestion: "Enable iOS keyboard isolation, then retry enter_text."}) {
		t.Fatalf("output = %s", out)
	}
	if len(keyboard.calls) != 0 {
		t.Fatalf("keyboard_text calls = %v, want no probe before isolation", keyboard.calls)
	}
}
