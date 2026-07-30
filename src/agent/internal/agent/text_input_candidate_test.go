package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

type textInputStubTool struct {
	name string
	out  string
}

func (s textInputStubTool) Name() string        { return s.name }
func (s textInputStubTool) Description() string { return s.name }
func (s textInputStubTool) Call(context.Context, string) (string, error) {
	return s.out, nil
}

type recordingTextInputTool struct {
	name  string
	out   string
	calls []string
}

func (s *recordingTextInputTool) Name() string        { return s.name }
func (s *recordingTextInputTool) Description() string { return s.name }
func (s *recordingTextInputTool) Call(_ context.Context, input string) (string, error) {
	s.calls = append(s.calls, input)
	return s.out, nil
}

func TestIsRomanizationOnlyField(t *testing.T) {
	if !isRomanizationOnlyField("NIHAO", []string{"ni", "hao"}) {
		t.Fatal("expected romanization-only")
	}
	if isRomanizationOnlyField("你好", []string{"ni", "hao"}) {
		t.Fatal("han text should not be romanization-only")
	}
}

func TestEvaluateFieldCommitRejectsCandidateOnly(t *testing.T) {
	committed, _ := evaluateFieldCommit(textInputScreenAnalysis{
		FieldText:          "你好",
		CompositionPending: true,
	}, "你好")
	if committed {
		t.Fatal("candidate/preedit only should not commit")
	}
	committed, _ = evaluateFieldCommit(textInputScreenAnalysis{
		FieldText:     "你好",
		TargetMatched: true,
	}, "你好")
	if !committed {
		t.Fatal("vision-confirmed target match should commit")
	}
}

func TestShouldSuspectWrongIMESkipsDuringCandidateAction(t *testing.T) {
	analysis := textInputScreenAnalysis{
		FieldText:          "nihao",
		CompositionPending: true,
		ObservedMode:       textInputModeASCII,
	}
	if shouldSuspectWrongIME(analysis, analysis.FieldText, []string{"ni", "hao"}, textInputModeComposition) {
		t.Fatal("pending/candidate state should not trigger wrong IME")
	}
}

func TestEvaluateFieldCommitAcceptsASCIIDespitePendingFlag(t *testing.T) {
	committed, _ := evaluateFieldCommit(textInputScreenAnalysis{
		FieldText:          "hello",
		TargetMatched:      true,
		CompositionPending: true,
	}, "hello")
	if !committed {
		t.Fatal("vision-confirmed field match should win over a contradictory pending flag")
	}
}

func TestCompositionExpandsCandidatesBeforeSelectingHiddenTarget(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{
			ObservedMode:       textInputModeComposition,
			CompositionPending: true,
		},
		{
			ObservedMode:       textInputModeComposition,
			CompositionPending: true,
		},
		{FieldText: "是一个硬件智能", TargetMatched: true},
	}, actions: []textInputCandidateAction{
		{Action: textInputCandidateActionExpand},
		{Action: textInputCandidateActionSelect, Offset: 2, Text: "是一个硬件智能"},
	}}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	engine := newFastTextInputEngine(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  kbTap,
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)

	committed, _, _, _, err := engine.typeCompositionWithCandidateSelection(
		context.Background(),
		"ios",
		textInputArgs{Text: "是一个硬件智能"},
		[]string{"shi", "yi", "ge", "ying", "jian", "zhi", "neng"},
	)
	if err != nil || !committed {
		t.Fatalf("typeCompositionWithCandidateSelection() committed=%v err=%v", committed, err)
	}
	if len(kbTap.calls) != 4 {
		t.Fatalf("keyboard taps=%v, want Down, Right, Right, Space", kbTap.calls)
	}
	for index, want := range []string{"down", "right", "right", "space"} {
		if !strings.Contains(kbTap.calls[index], want) {
			t.Fatalf("keyboard tap %d=%q, want %q; all=%v", index, kbTap.calls[index], want, kbTap.calls)
		}
	}
}

func TestCompositionCanReturnToFirstCandidateRow(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeComposition, CompositionPending: true},
		{ObservedMode: textInputModeComposition, CompositionPending: true},
		{ObservedMode: textInputModeComposition, CompositionPending: true},
		{FieldText: "经理", TargetMatched: true},
	}, actions: []textInputCandidateAction{
		{Action: textInputCandidateActionExpand},
		{Action: textInputCandidateActionUp},
		{Action: textInputCandidateActionSelect, Offset: 0, Text: "经理"},
	}}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	engine := newFastTextInputEngine(textInputHardwareDeps{
		keyboardTap:  kbTap,
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)

	committed, _, _, _, err := engine.typeCompositionWithCandidateSelection(
		context.Background(),
		"ios",
		textInputArgs{Text: "经理"},
		[]string{"jing", "li"},
	)
	if err != nil || !committed {
		t.Fatalf("typeCompositionWithCandidateSelection() committed=%v err=%v", committed, err)
	}
	if len(kbTap.calls) != 3 {
		t.Fatalf("keyboard taps=%v, want Down, Up, Space", kbTap.calls)
	}
	for index, want := range []string{"down", "up", "space"} {
		if !strings.Contains(kbTap.calls[index], want) {
			t.Fatalf("keyboard tap %d=%q, want %q; all=%v", index, kbTap.calls[index], want, kbTap.calls)
		}
	}
}

func TestCompletedCandidateSelectionSkipsPostActionVerification(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode:       textInputModeComposition,
		CompositionPending: true,
	}}, actions: []textInputCandidateAction{{
		Action:        textInputCandidateActionSelect,
		Text:          "把自己",
		CompletesPart: true,
	}}}
	screenshot := &recordingTextInputTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`}
	var sleeps []time.Duration
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   screenshot,
	}, vision, func(_ context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return nil
	})

	committed, _, _, vlmCalls, err := engine.typeCompositionWithCandidateSelection(
		context.Background(),
		"ios",
		textInputArgs{Text: "把自己", CurrentIMEPart: "把自己"},
		[]string{"ba", "zi", "ji"},
	)
	if err != nil || !committed {
		t.Fatalf("typeCompositionWithCandidateSelection() committed=%v err=%v", committed, err)
	}
	if vlmCalls != 2 {
		t.Fatalf("vlmCalls=%d, want initial analysis plus candidate decision only", vlmCalls)
	}
	if len(screenshot.calls) != 2 {
		t.Fatalf("screenshot calls=%d, want no post-selection verification screenshot", len(screenshot.calls))
	}
	initialCandidateSettleCount := 0
	for _, delay := range sleeps {
		if delay == textInputInitialCandidateDelay {
			initialCandidateSettleCount++
		}
	}
	if initialCandidateSettleCount != 1 {
		t.Fatalf("initial candidate settle count=%d, want one pre-candidate settle and none after final selection; sleeps=%v", initialCandidateSettleCount, sleeps)
	}
}

func TestCandidateActionSettleDelayIs300Milliseconds(t *testing.T) {
	if textInputCandidateSettleDelay != 300*time.Millisecond {
		t.Fatalf("candidate settle delay=%s, want 300ms", textInputCandidateSettleDelay)
	}
}

func TestLocalIMEFixedWaitsDoNotExceed300Milliseconds(t *testing.T) {
	for name, delay := range map[string]time.Duration{
		"probe":             textInputProbeSettleDelay,
		"composition_ready": textInputCompositionReadyDelay,
		"ime_switch":        textInputIMESwitchSettleDelay,
		"initial_candidate": textInputInitialCandidateDelay,
		"candidate_action":  textInputCandidateSettleDelay,
	} {
		if delay > 300*time.Millisecond {
			t.Fatalf("%s delay=%s, want at most 300ms", name, delay)
		}
	}
}

func TestSelectCandidateByKeyboardUsesLeftForNegativeOffset(t *testing.T) {
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	engine := newFastTextInputEngine(textInputHardwareDeps{keyboardTap: kbTap}, &stubTextInputVision{})
	if err := engine.selectCandidateByKeyboard(context.Background(), textInputCandidateAction{Action: textInputCandidateActionSelect, Offset: -2, Text: "目标"}); err != nil {
		t.Fatal(err)
	}
	if len(kbTap.calls) != 3 {
		t.Fatalf("keyboard taps=%v, want Left, Left, Space", kbTap.calls)
	}
	for index, want := range []string{"left", "left", "space"} {
		if !strings.Contains(kbTap.calls[index], want) {
			t.Fatalf("keyboard tap %d=%q, want %q", index, kbTap.calls[index], want)
		}
	}
}

func TestCompositionStopsWhenModelReturnsNone(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode:       textInputModeComposition,
		FieldText:          "mu biao wen ben",
		CompositionPending: true,
	}}, actions: []textInputCandidateAction{{Action: textInputCandidateActionNone}}}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	engine := newFastTextInputEngine(textInputHardwareDeps{
		keyboardTap:  kbTap,
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)

	committed, _, _, _, err := engine.typeCompositionWithCandidateSelection(
		context.Background(),
		"ios",
		textInputArgs{Text: "目标文本"},
		[]string{"mu", "biao", "wen", "ben"},
	)
	if err != nil {
		t.Fatalf("typeCompositionWithCandidateSelection() error=%v", err)
	}
	if committed {
		t.Fatal("committed=true, want unresolved candidate state")
	}
	for _, call := range kbTap.calls {
		if strings.Contains(call, "down") {
			t.Fatalf("keyboard_tap calls=%v, must not press Down when model action is none", kbTap.calls)
		}
	}
}

func TestCandidateSelectionExecutesModelDecisionWithoutPrefixComparison(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode:       textInputModeComposition,
		FieldText:          "mu biao wen ben",
		CompositionPending: true,
	}}, actions: []textInputCandidateAction{{Action: textInputCandidateActionSelect, Offset: 0, Text: "候选"}}}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	engine := newFastTextInputEngine(textInputHardwareDeps{
		keyboardTap:  kbTap,
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)

	committed, _, _, _, err := engine.typeCompositionWithCandidateSelection(
		context.Background(),
		"ios",
		textInputArgs{Text: "目标文本"},
		[]string{"mu", "biao", "wen", "ben"},
	)
	if err != nil {
		t.Fatalf("typeCompositionWithCandidateSelection() error=%v", err)
	}
	if committed {
		t.Fatal("committed=true, want verification after the model-directed selection")
	}
	selected := false
	for _, call := range kbTap.calls {
		if strings.Contains(call, "space") {
			selected = true
		}
	}
	if !selected {
		t.Fatalf("keyboard_tap calls=%v, want the model's select action executed", kbTap.calls)
	}
}

func TestCompositionContinuesSelectingWhileCandidateStateRemains(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{
			ObservedMode:       textInputModeComposition,
			CompositionPending: true,
		},
		{
			ObservedMode:       textInputModeComposition,
			FieldText:          "我们",
			CompositionPending: true,
		},
		{FieldText: "我们大概率", TargetMatched: true},
	}, actions: []textInputCandidateAction{
		{Action: textInputCandidateActionSelect, Offset: 0, Text: "我们"},
		{Action: textInputCandidateActionSelect, Offset: 1, Text: "大概率"},
	}}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	engine := newFastTextInputEngine(textInputHardwareDeps{
		keyboardTap:  kbTap,
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)

	committed, _, _, _, err := engine.typeCompositionWithCandidateSelection(
		context.Background(),
		"ios",
		textInputArgs{Text: "我们大概率"},
		[]string{"wo", "men", "da", "gai", "lv"},
	)
	if err != nil || !committed {
		t.Fatalf("typeCompositionWithCandidateSelection() committed=%v err=%v", committed, err)
	}
	spaceCount := 0
	for _, call := range kbTap.calls {
		if strings.Contains(call, "space") {
			spaceCount++
		}
	}
	if spaceCount != 2 {
		t.Fatalf("keyboard taps=%v, want two candidate confirmations", kbTap.calls)
	}
}

func TestInterpretTextInputToolOutputRejectsReservedQuickAction(t *testing.T) {
	err := interpretTextInputToolOutput(`{"ok":false,"status":"reserved","message":"reserved on this platform"}`)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved quick_action error, got %v", err)
	}
}

func TestCycleIMEUsesKeyboardTapNotQuickAction(t *testing.T) {
	var tapped []string
	kb := textInputStubTool{name: "keyboard_tap"}
	kbCall := func(_ context.Context, input string) (string, error) {
		tapped = append(tapped, input)
		return "ok", nil
	}
	qa := textInputStubTool{name: "quick_action"}
	qaOut := "ok"
	engine := newFastTextInputEngine(textInputHardwareDeps{
		keyboardTap: &stubTextInputCallTool{tool: kb, fn: kbCall},
		quickAction: &stubTextInputCallTool{tool: qa, fn: func(context.Context, string) (string, error) {
			qaOut = `{"ok":false,"status":"reserved"}`
			return qaOut, nil
		}},
	}, &stubTextInputVision{})
	label, err := engine.cycleIME(context.Background(), "android")
	if err != nil {
		t.Fatal(err)
	}
	if label != "ctrl+shift" {
		t.Fatalf("label=%q", label)
	}
	if len(tapped) != 1 || !strings.Contains(tapped[0], "ctrl") {
		t.Fatalf("keyboard_tap calls=%v", tapped)
	}
}

type stubTextInputCallTool struct {
	tool textInputStubTool
	fn   func(context.Context, string) (string, error)
}

func (s *stubTextInputCallTool) Name() string        { return s.tool.Name() }
func (s *stubTextInputCallTool) Description() string { return s.tool.Description() }
func (s *stubTextInputCallTool) Call(ctx context.Context, input string) (string, error) {
	return s.fn(ctx, input)
}
