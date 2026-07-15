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

func TestEnterTextInFieldDescriptionDocumentsStrategyAndVerification(t *testing.T) {
	desc := (&EnterTextInFieldTool{}).Description()
	// Description keeps only the load-bearing rules: keyboard_text disambiguation,
	// the committed-only success contract, and the chat-message clipboard handoff.
	// Field mechanics live in ArgsSchema.
	for _, want := range []string{
		"keyboard_text",
		"search fields",
		"contact lookup",
		"already-open composer",
		"runtime Phone Bridge status",
		"target-preserving",
		"automatically routed",
		"send_after_commit=true",
		"enter_text_via_bridge",
		"committed:true",
		"field_text matches target exactly",
		"normalized coordinates",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
	// mode and segments mechanics moved to the schema fields.
	props, _ := (&EnterTextInFieldTool{}).ArgsSchema()["properties"].(map[string]any)
	modeSchema, _ := props["mode"].(map[string]any)
	if modeDesc, _ := modeSchema["description"].(string); !strings.Contains(modeDesc, "search") {
		t.Fatalf("mode schema missing search semantics:\n%v", modeSchema)
	}
	segSchema, _ := props["segments"].(map[string]any)
	if segDesc, _ := segSchema["description"].(string); !strings.Contains(segDesc, "romanization") {
		t.Fatalf("segments schema missing romanization semantics:\n%v", segSchema)
	}
}

func TestEnterTextInFieldBridgePreferenceUsesInteractionAndBridgeState(t *testing.T) {
	pb := newTestPhoneBridge(t)
	pb.platform = "ios"
	pb.appState = "background"
	pb.appStateAt = time.Now()
	pb.pipBridgeEnabled = true
	pb.pipBridgeSeen = true
	tool := &EnterTextInFieldTool{bridgeTool: &EnterTextViaBridgeTool{bridgeFn: func() *PhoneBridge { return pb }}}

	if tool.shouldPreferBridgeClipboard(enterTextInFieldArgs{
		Text: "小红书", Platform: "ios", Mode: "search", SendAfterCommit: true,
	}) {
		t.Fatal("search input should stay on IME even when PiP clipboard queue is available")
	}
	if !tool.shouldPreferBridgeClipboard(enterTextInFieldArgs{
		Text: "联系人", Platform: "ios",
	}) {
		t.Fatal("short non-search CJK input should use a fresh target-preserving PiP clipboard route")
	}
	if !tool.shouldPreferBridgeClipboard(enterTextInFieldArgs{
		Text: "可以，我们就按这个方案继续处理。", Platform: "ios", SendAfterCommit: true,
	}) {
		t.Fatal("final non-ASCII composer text should use fresh target-preserving PiP clipboard route")
	}

	pb.appStateAt = time.Now().Add(-phoneBridgeBackgroundStateMaxAge - time.Second)
	if tool.shouldPreferBridgeClipboard(enterTextInFieldArgs{
		Text: "可以，我们就按这个方案继续处理。", Platform: "ios", SendAfterCommit: true,
	}) {
		t.Fatal("stale PiP state should not select bridge clipboard route")
	}
}

func TestEnterTextInFieldAutoRoutesShortCJKThroughFreshPiPClipboard(t *testing.T) {
	message := "中午吃食堂是不是"
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeComposition,
		FieldText:    message,
	}}}
	pb := newTestPhoneBridge(t)
	pb.platform = "ios"
	pb.appState = "background"
	pb.appStateAt = time.Now()
	pb.pipBridgeEnabled = true
	pb.pipBridgeSeen = true
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	hw := &textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		touchGesture: &recordingTextInputTool{name: "touch_gesture", out: "ok"},
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: keyboardText,
		quickAction:  &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}
	bridgeTool := &EnterTextViaBridgeTool{
		hw:       hw,
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		sleep:    testNoWaitSleep,
	}
	tool := &EnterTextInFieldTool{
		engine:     newFastTextInputEngine(*hw, vision),
		bridgeTool: bridgeTool,
	}

	queueResult := make(chan string, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		commands := pb.queue.PollForPhone("ios", "", 10)
		if len(commands) != 1 || commands[0].Type != "clipboard_write" {
			queueResult <- "short CJK input did not enqueue one clipboard_write command"
			return
		}
		if err := pb.queue.SubmitResult(BridgeCommandResponse{ID: commands[0].ID, Method: "queued"}); err != nil {
			queueResult <- err.Error()
			return
		}
		queueResult <- ""
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := tool.Call(ctx, `{"text":"`+message+`","platform":"ios","focus":{"x":300,"y":940,"coord_space":"normalized"},"segments":["zhong","wu"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if queueErr := <-queueResult; queueErr != "" {
		t.Fatal(queueErr)
	}
	for _, want := range []string{`"committed": true`, "wrote clipboard through background bridge queue", "quick_action-pasted clipboard"} {
		if !strings.Contains(out, want) {
			t.Fatalf("unexpected output, missing %q: %s", want, out)
		}
	}
	if len(keyboardText.calls) != 0 {
		t.Fatalf("keyboard_text calls=%v, want no IME typing for fresh PiP short CJK route", keyboardText.calls)
	}
}

func TestEnterTextInFieldASCII(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		FieldText: "hello",
	}}}
	kbText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	engine := newFastTextInputEngine(textInputHardwareDeps{
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

func TestEnterTextInFieldSearchModeHandsOffAfterASCIIInput(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeASCII,
		FieldText:    "Aid",
	}}}
	kbText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	engine := newFastTextInputEngine(textInputHardwareDeps{
		mouseClick:   textInputStubTool{name: "mouse_click", out: "ok"},
		keyboardTap:  textInputStubTool{name: "keyboard_tap", out: "ok"},
		keyboardText: kbText,
		quickAction:  textInputStubTool{name: "quick_action", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision)
	tool := &EnterTextInFieldTool{engine: engine}
	out, err := tool.Call(context.Background(), `{"text":"Aiden","mode":"search","focus":{"x":500,"y":100}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"interrupted": true`) || !strings.Contains(out, `search handoff after ascii input`) {
		t.Fatalf("unexpected output: %s", out)
	}
	if len(kbText.calls) != 1 {
		t.Fatalf("keyboard_text calls=%v", kbText.calls)
	}
}

func TestEnterTextInFieldASCIIWithSpaces(t *testing.T) {
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		FieldText: "hello test",
	}}}
	kbText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	engine := newFastTextInputEngine(textInputHardwareDeps{
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
	engine := newFastTextInputEngine(textInputHardwareDeps{
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
	engine := newFastTextInputEngine(textInputHardwareDeps{
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
	engine := newFastTextInputEngine(textInputHardwareDeps{
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
	engine := newFastTextInputEngine(textInputHardwareDeps{
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

func TestEnterTextInFieldUsesPreparedClipboardInCurrentIOSApp(t *testing.T) {
	message := "Example Contact number 555-0101 and 555-0102 still active?"
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeASCII,
		FieldText:    message,
	}}}
	pb := newTestPhoneBridge(t)
	pb.NoteClipboardWrite(message)
	pb.platform = "ios"
	pb.appState = "background"
	pb.appStateAt = time.Now()

	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	mouse := &recordingTextInputTool{name: "mouse_click", out: "ok"}
	hw := &textInputHardwareDeps{
		mouseClick:   mouse,
		touchGesture: touch,
		keyboardTap:  keyboardTap,
		keyboardText: keyboardText,
		quickAction:  quick,
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}
	bridgeTool := &EnterTextViaBridgeTool{
		hw:       hw,
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		sleep:    testNoWaitSleep,
	}
	tool := &EnterTextInFieldTool{
		engine:     newFastTextInputEngine(*hw, vision),
		bridgeTool: bridgeTool,
	}

	out, err := tool.Call(context.Background(), `{"text":"`+message+`","platform":"ios","focus":{"x":400,"y":950,"coord_space":"normalized"}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"committed": true`,
		"clipboard-first: using prepared clipboard in current app",
		"clipboard-first: quick_action-pasted clipboard",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unexpected output, missing %q: %s", want, out)
		}
	}
	if len(quick.calls) != 1 || !strings.Contains(quick.calls[0], `"action": "paste"`) {
		t.Fatalf("quick_action calls=%v", quick.calls)
	}
	if len(keyboardTap.calls) != 0 {
		t.Fatalf("keyboard_tap calls=%v", keyboardTap.calls)
	}
	if len(touch.calls) != 0 {
		t.Fatalf("touch_gesture calls=%v", touch.calls)
	}
	if len(keyboardText.calls) != 0 {
		t.Fatalf("keyboard_text calls=%v", keyboardText.calls)
	}
	if len(mouse.calls) != 1 {
		t.Fatalf("mouse_click calls=%v", mouse.calls)
	}
}

func TestEnterTextInFieldFallbackDoesNotReportSendAfterCommitSuccess(t *testing.T) {
	message := "Example Contact number 555-0101 still active?"
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeASCII,
		FieldText:    "partial paste",
	}, {
		ObservedMode: textInputModeASCII,
		FieldText:    "partial paste",
	}, {
		ObservedMode: textInputModeASCII,
		FieldText:    message,
	}}}
	pb := newTestPhoneBridge(t)
	pb.NoteClipboardWrite(message)
	pb.platform = "ios"
	pb.appState = "background"
	pb.appStateAt = time.Now()

	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	hw := &textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		touchGesture: &recordingTextInputTool{name: "touch_gesture", out: "ok"},
		keyboardTap:  keyboardTap,
		keyboardText: keyboardText,
		quickAction:  quick,
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}
	bridgeTool := &EnterTextViaBridgeTool{
		hw:       hw,
		vision:   vision,
		bridgeFn: func() *PhoneBridge { return pb },
		sleep:    testNoWaitSleep,
	}
	tool := &EnterTextInFieldTool{
		engine:     newFastTextInputEngine(*hw, vision),
		bridgeTool: bridgeTool,
	}

	out, err := tool.Call(context.Background(), `{"text":"`+message+`","platform":"ios","focus":{"x":400,"y":950,"coord_space":"normalized"},"send_after_commit":true}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"ok": false`,
		`"committed": true`,
		"field verified but send was not verified",
		"cleared field before input",
		"clipboard-first: falling back to HID/IME",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unexpected output, missing %q: %s", want, out)
		}
	}
	if len(keyboardText.calls) == 0 {
		t.Fatalf("keyboard_text calls=%v", keyboardText.calls)
	}
	if len(keyboardTap.calls) == 0 {
		t.Fatalf("keyboard_tap calls=%v, want clear-field backspaces before fallback input", keyboardTap.calls)
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
	engine := newFastTextInputEngine(textInputHardwareDeps{
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
	engine := newFastTextInputEngine(textInputHardwareDeps{
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

func TestEnterTextInFieldFirstCompositionAttemptWaitsForIME(t *testing.T) {
	originalDelay := textInputCompositionReadyDelay
	textInputCompositionReadyDelay = 0
	defer func() { textInputCompositionReadyDelay = originalDelay }()

	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		FieldText: "你好",
	}}}
	engine := newFastTextInputEngine(textInputHardwareDeps{
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
	if !strings.Contains(out, "wait 0s for IME to settle before first composition input") {
		t.Fatalf("expected IME settle step in output, got: %s", out)
	}
}

func TestTextInputEngineFirstCompositionAttemptRespectsContextCancelDuringIMEWait(t *testing.T) {
	originalDelay := textInputCompositionReadyDelay
	textInputCompositionReadyDelay = time.Second
	defer func() { textInputCompositionReadyDelay = originalDelay }()

	engine := newTextInputEngine(textInputHardwareDeps{
		mouseClick:   textInputStubTool{name: "mouse_click", out: "ok"},
		keyboardTap:  textInputStubTool{name: "keyboard_tap", out: "ok"},
		keyboardText: textInputStubTool{name: "keyboard_text", out: "ok"},
		quickAction:  textInputStubTool{name: "quick_action", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, &stubTextInputVision{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	result, err := engine.Run(ctx, enterTextInFieldArgs{
		Text:        "你好",
		SkipFocus:   true,
		MaxAttempts: 1,
		Segments:    []string{"ni", "hao"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) >= textInputCompositionReadyDelay/2 {
		t.Fatalf("Run blocked too long after context cancellation")
	}
	if result.Reason != context.Canceled.Error() {
		t.Fatalf("Reason=%q, want %q", result.Reason, context.Canceled.Error())
	}
	if len(result.Steps) == 0 || !strings.Contains(result.Steps[len(result.Steps)-1], "IME to settle") {
		t.Fatalf("expected IME settle step in result, got: %v", result.Steps)
	}
	if result.Committed {
		t.Fatalf("Committed=%v, want false", result.Committed)
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
	engine := newFastTextInputEngine(textInputHardwareDeps{
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
