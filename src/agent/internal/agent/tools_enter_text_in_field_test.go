package agent

import (
	"context"
	"strings"
	"testing"
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

func TestEnterTextInFieldASCII(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		FieldText: "hello",
	}}}
	kbText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	engine := newTextInputEngine(textInputHardwareDeps{
		mouseClick:   textInputStubTool{name: "mouse_click", out: "ok"},
		keyboardTap:  kbTap,
		keyboardText: kbText,
		quickAction:  textInputStubTool{name: "quick_action", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)
	tool := &EnterTextInFieldTool{engine: engine}
	out, err := tool.Call(context.Background(), `{"text":"hello","focus":{"x":500,"y":100}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": true`) || !strings.Contains(out, `"vlm_calls": 1`) {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestEnterTextInFieldASCIIWithSpaces(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		FieldText: "hello test",
	}}}
	kbText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	engine := newTextInputEngine(textInputHardwareDeps{
		mouseClick:   textInputStubTool{name: "mouse_click", out: "ok"},
		keyboardTap:  kbTap,
		keyboardText: kbText,
		quickAction:  textInputStubTool{name: "quick_action", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)
	tool := &EnterTextInFieldTool{engine: engine}
	out, err := tool.Call(context.Background(), `{"text":"hello test","focus":{"x":500,"y":100},"max_attempts":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": true`) {
		t.Fatalf("unexpected output: %s", out)
	}
	if len(kbText.calls) != 2 {
		t.Fatalf("keyboard_text calls=%v", kbText.calls)
	}
	if len(kbTap.calls) != 1 || !strings.Contains(kbTap.calls[0], "space") {
		t.Fatalf("keyboard_tap calls=%v", kbTap.calls)
	}
}

func TestEnterTextInFieldRejectsRomanizationField(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		FieldText:         "nihao",
		WrongIMESuspected: true,
		SuggestSwitchIME:  true,
	}}}
	engine := newTextInputEngine(textInputHardwareDeps{
		mouseClick:   textInputStubTool{name: "mouse_click", out: "ok"},
		keyboardTap:  textInputStubTool{name: "keyboard_tap", out: "ok"},
		keyboardText: textInputStubTool{name: "keyboard_text", out: "ok"},
		quickAction:  textInputStubTool{name: "quick_action", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)
	tool := &EnterTextInFieldTool{engine: engine}
	out, err := tool.Call(context.Background(), `{"text":"你好","focus":{"x":500,"y":100},"segments":["ni","hao"],"max_attempts":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": false`) {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestEnterTextInFieldCompositionRequiresSegments(t *testing.T) {
	engine := newTextInputEngine(textInputHardwareDeps{
		mouseClick:   textInputStubTool{name: "mouse_click", out: "ok"},
		keyboardTap:  textInputStubTool{name: "keyboard_tap", out: "ok"},
		keyboardText: textInputStubTool{name: "keyboard_text", out: "ok"},
		quickAction:  textInputStubTool{name: "quick_action", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, &stubTextInputVision{})
	tool := &EnterTextInFieldTool{engine: engine}
	out, err := tool.Call(context.Background(), `{"text":"你好","focus":{"x":500,"y":100},"max_attempts":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": false`) || !strings.Contains(out, "requires segments") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestEnterTextInFieldStructuredKeyboardErrorDoesNotPoisonRetriableResult(t *testing.T) {
	keyboardErr := NewToolError(CodeInvalidArguments, "keyboard_text failed")
	engine := newTextInputEngine(textInputHardwareDeps{
		mouseClick:  textInputStubTool{name: "mouse_click", out: "ok"},
		keyboardTap: textInputStubTool{name: "keyboard_tap", out: "ok"},
		keyboardText: &stubTextInputCallTool{
			tool: textInputStubTool{name: "keyboard_text"},
			fn: func(ctx context.Context, _ string) (string, error) {
				SetToolError(ctx, keyboardErr)
				return keyboardErr.Message, nil
			},
		},
		quickAction: textInputStubTool{name: "quick_action", out: "ok"},
		screenshot:  textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, &stubTextInputVision{})
	tool := &EnterTextInFieldTool{engine: engine}
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"text":"hello","focus":{"x":500,"y":100},"max_attempts":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := ToolErrorFromContext(ctx); got != nil {
		t.Fatalf("ToolError = %+v, want nil for retriable keyboard_text failure", got)
	}
	if !strings.Contains(out, `"committed": false`) || !strings.Contains(out, "exhausted retries") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestEnterTextInFieldCompositionSuccess(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		// after all segments typed + space: pending with candidate
		{
			ObservedMode:       textInputModeComposition,
			FieldText:          "nihao",
			CompositionPending: true,
			Candidates:         []textInputCandidateClick{{X: 500, Y: 800, Text: "你好"}},
		},
		// after clicking candidate: committed
		{FieldText: "你好"},
	}}
	engine := newTextInputEngine(textInputHardwareDeps{
		mouseClick:   textInputStubTool{name: "mouse_click", out: "ok"},
		keyboardTap:  textInputStubTool{name: "keyboard_tap", out: "ok"},
		keyboardText: textInputStubTool{name: "keyboard_text", out: "ok"},
		quickAction:  textInputStubTool{name: "quick_action", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)
	tool := &EnterTextInFieldTool{engine: engine}
	out, err := tool.Call(context.Background(), `{"text":"你好","focus":{"x":500,"y":100},"segments":["ni","hao"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": true`) {
		t.Fatalf("unexpected output: %s", out)
	}
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
		FieldText: "你好",
	}, "你好")
	if !committed {
		t.Fatal("exact field match should commit")
	}
}

func TestShouldSuspectWrongIMESkipsWhenCandidatesVisible(t *testing.T) {
	analysis := textInputScreenAnalysis{
		FieldText:          "nihao",
		CompositionPending: true,
		Candidates:         []textInputCandidateClick{{X: 500, Y: 800, Text: "你好"}},
		ObservedMode:       textInputModeASCII,
	}
	if shouldSuspectWrongIME(analysis, analysis.FieldText, []string{"ni", "hao"}, textInputModeComposition) {
		t.Fatal("pending/candidate state should not trigger wrong IME")
	}
}

func TestEvaluateFieldCommitAcceptsASCIIDespitePendingFlag(t *testing.T) {
	committed, _ := evaluateFieldCommit(textInputScreenAnalysis{
		FieldText:          "hello",
		CompositionPending: true,
	}, "hello")
	if !committed {
		t.Fatal("ascii field match should commit even when composition_pending is true")
	}
}

func TestEnterTextInFieldASCIIRetryWithoutRefocus(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		FieldText: "hell",
	}, {
		FieldText: "hello",
	}}}
	mouse := &recordingTextInputTool{name: "mouse_click", out: "ok"}
	kbText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	engine := newTextInputEngine(textInputHardwareDeps{
		mouseClick:   mouse,
		keyboardTap:  textInputStubTool{name: "keyboard_tap", out: "ok"},
		keyboardText: kbText,
		quickAction:  textInputStubTool{name: "quick_action", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)
	tool := &EnterTextInFieldTool{engine: engine}
	out, err := tool.Call(context.Background(), `{"text":"hello","focus":{"x":500,"y":100},"max_attempts":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": true`) {
		t.Fatalf("unexpected output: %s", out)
	}
	if len(kbText.calls) != 1 {
		t.Fatalf("expected single keyboard_text call, got %v", kbText.calls)
	}
}

func TestEnterTextInFieldRetryWithoutRetype(t *testing.T) {
	pending := textInputScreenAnalysis{
		FieldText:          "nihao",
		CompositionPending: true,
		Candidates:         []textInputCandidateClick{{X: 500, Y: 800, Text: "你好"}},
	}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		// attempt 1: after space commit, analyze sees pending with candidate
		pending,
		// after clicking candidate: committed
		{FieldText: "你好"},
	}}
	kbText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	engine := newTextInputEngine(textInputHardwareDeps{
		mouseClick:   textInputStubTool{name: "mouse_click", out: "ok"},
		keyboardTap:  textInputStubTool{name: "keyboard_tap", out: "ok"},
		keyboardText: kbText,
		quickAction:  textInputStubTool{name: "quick_action", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)
	tool := &EnterTextInFieldTool{engine: engine}
	out, err := tool.Call(context.Background(), `{"text":"你好","focus":{"x":500,"y":100},"segments":["ni","hao"],"max_attempts":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": true`) {
		t.Fatalf("unexpected output: %s", out)
	}
	if len(kbText.calls) != 2 {
		t.Fatalf("expected single type pass (ni+hao), got keyboard_text calls=%v", kbText.calls)
	}
}

func TestEnterTextInFieldCandidatePaging(t *testing.T) {
	// Simulate: after typing, composition pending but no candidates visible,
	// then after paging down once, candidate appears and gets clicked, then field committed.
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		// first analyze after typing segment "ni"
		{ObservedMode: textInputModeComposition, FieldText: "", CompositionPending: true, Candidates: nil},
		// after first page-down: still no match
		{ObservedMode: textInputModeComposition, FieldText: "", CompositionPending: true, Candidates: nil},
		// after second page-down: candidate found
		{ObservedMode: textInputModeComposition, FieldText: "", CompositionPending: true, Candidates: []textInputCandidateClick{{X: 500, Y: 800, Text: "你"}}},
		// after clicking candidate: committed
		{FieldText: "你"},
		// second segment "hao" typed, analyze
		{ObservedMode: textInputModeComposition, FieldText: "你", CompositionPending: true, Candidates: []textInputCandidateClick{{X: 500, Y: 800, Text: "好"}}},
		// after clicking: committed
		{FieldText: "你好"},
	}}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	engine := newTextInputEngine(textInputHardwareDeps{
		mouseClick:   textInputStubTool{name: "mouse_click", out: "ok"},
		keyboardTap:  kbTap,
		keyboardText: textInputStubTool{name: "keyboard_text", out: "ok"},
		quickAction:  textInputStubTool{name: "quick_action", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)
	tool := &EnterTextInFieldTool{engine: engine}
	out, err := tool.Call(context.Background(), `{"text":"你好","focus":{"x":500,"y":100},"segments":["ni","hao"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"committed": true`) {
		t.Fatalf("expected committed, got: %s", out)
	}
	// Verify page-down keys were tapped
	hasDown := false
	for _, call := range kbTap.calls {
		if strings.Contains(call, "down") {
			hasDown = true
			break
		}
	}
	if !hasDown {
		t.Fatalf("expected page-down tap, keyboard_tap calls=%v", kbTap.calls)
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
	engine := newTextInputEngine(textInputHardwareDeps{
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
