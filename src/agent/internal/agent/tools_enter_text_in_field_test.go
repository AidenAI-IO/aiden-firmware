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
		"latest screenshot",
		"actual editable field",
		"folder/list view",
		"create/new button",
		"guessed blank-space coordinate",
		"search fields",
		"contact lookup",
		"already-open composer",
		"runtime app_text_entry_strategy",
		"target-preserving",
		"do not use this",
		"call enter_text_via_bridge directly",
		"send_after_commit=true",
		"enter_text_via_bridge",
		"committed:true",
		"field_text is diagnostic visual transcription",
		"normalized coordinates",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
	// mode and segments mechanics moved to the schema fields.
	props, _ := (&EnterTextInFieldTool{}).ArgsSchema()["properties"].(map[string]any)
	if _, found := props["platform"]; found {
		t.Fatal("enter_text_in_field must infer the platform from HID configuration")
	}
	modeSchema, _ := props["mode"].(map[string]any)
	if modeDesc, _ := modeSchema["description"].(string); !strings.Contains(modeDesc, "search") {
		t.Fatalf("mode schema missing search semantics:\n%v", modeSchema)
	}
	segSchema, _ := props["segments"].(map[string]any)
	if segDesc, _ := segSchema["description"].(string); !strings.Contains(segDesc, "romanization") {
		t.Fatalf("segments schema missing romanization semantics:\n%v", segSchema)
	}
	focusSchema, _ := props["focus"].(map[string]any)
	if focusDesc, _ := focusSchema["description"].(string); !strings.Contains(focusDesc, "actual editable field") || !strings.Contains(focusDesc, "blank space") {
		t.Fatalf("focus schema missing input-readiness guard:\n%v", focusSchema)
	}
}

func TestEnterTextInFieldBridgePreferenceUsesInteractionAndBridgeState(t *testing.T) {
	pb := newTestPhoneBridge(t)
	pb.platform = "ios"
	pb.appState = "background"
	pb.appStateAt = time.Now()
	pb.pipBridgeEnabled = true
	pb.pipBridgeSeen = true
	tool := &EnterTextInFieldTool{bridgeTool: &EnterTextViaBridgeTool{hw: &textInputHardwareDeps{pointerMode: "absolute"}, bridgeFn: func() *PhoneBridge { return pb }}}

	if tool.shouldPreferBridgeClipboard(enterTextInFieldArgs{
		Text: "小红书", Mode: "search", SendAfterCommit: true,
	}) {
		t.Fatal("search input should stay on IME even when PiP clipboard queue is available")
	}
	if !tool.shouldPreferBridgeClipboard(enterTextInFieldArgs{
		Text: "桥接测试",
	}) {
		t.Fatal("short non-search CJK input should use a fresh target-preserving PiP clipboard route")
	}
	if !tool.shouldPreferBridgeClipboard(enterTextInFieldArgs{
		Text: "请确认桥接输入结果。", SendAfterCommit: true,
	}) {
		t.Fatal("final non-ASCII composer text should use fresh target-preserving PiP clipboard route")
	}

	pb.appStateAt = time.Now().Add(-phoneBridgeBackgroundStateMaxAge - time.Second)
	if tool.shouldPreferBridgeClipboard(enterTextInFieldArgs{
		Text: "请确认桥接输入结果。", SendAfterCommit: true,
	}) {
		t.Fatal("stale PiP state should not select bridge clipboard route")
	}
}

func TestEnterTextInFieldUsesConnectedAndroidBackgroundClipboardRoute(t *testing.T) {
	pb := newTestPhoneBridge(t)
	pb.connected = true
	pb.platform = "android"
	pb.appState = "background"
	tool := &EnterTextInFieldTool{bridgeTool: &EnterTextViaBridgeTool{hw: &textInputHardwareDeps{pointerMode: "touchscreen"}, bridgeFn: func() *PhoneBridge { return pb }}}
	args := enterTextInFieldArgs{Text: "桥接测试"}

	if !tool.shouldPreferBridgeClipboard(args) {
		t.Fatal("connected Android background bridge should expose clipboard_write as a target-preserving route")
	}
	if strategy := phoneBridgeTextEntryState(pb.getStatus()); strategy != phoneBridgeTextEntryTargetPreserving {
		t.Fatalf("strategy = %q, want %q", strategy, phoneBridgeTextEntryTargetPreserving)
	}
}

func TestEnterTextInFieldAutoRoutesShortCJKThroughFreshPiPClipboard(t *testing.T) {
	message := "桥接测试"
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
		command, err := waitForQueuedBridgeCommand(pb.queue, "ios", 500*time.Millisecond)
		if err != nil {
			queueResult <- err.Error()
			return
		}
		if command.Type != "clipboard_write" {
			queueResult <- "short CJK input did not enqueue one clipboard_write command"
			return
		}
		if err := pb.queue.SubmitResult(BridgeCommandResponse{ID: command.ID, Method: "queued"}); err != nil {
			queueResult <- err.Error()
			return
		}
		queueResult <- ""
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := tool.Call(ctx, `{"text":"`+message+`","platform":"ios","focus":{"x":300,"y":940,"coord_space":"normalized"},"segments":["qiao","jie","ce","shi"]}`)
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
	if len(kbTap.calls) != 0 {
		t.Fatalf("already committed ASCII must not receive Enter confirmation: %v", kbTap.calls)
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
		},
		// after keyboard-confirming candidate: committed
		{FieldText: "你好"},
	}, actions: []textInputCandidateAction{{Action: textInputCandidateActionSelect, Offset: 0, Text: "你好"}}}
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

func TestEnterTextInFieldPreservesUnverifiedBridgeTextBeforeCorrection(t *testing.T) {
	message := "bridge verification target"
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{
		ObservedMode: textInputModeASCII,
		FieldText:    "partial paste",
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

	out, err := tool.Call(context.Background(), `{"text":"`+message+`","platform":"ios","focus":{"x":400,"y":950,"coord_space":"normalized"}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"ok": false`,
		`"committed": false`,
		`"field_text": "partial paste"`,
		"preserving unverified field text",
		"fresh observation required before corrective input",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unexpected output, missing %q: %s", want, out)
		}
	}
	if len(keyboardText.calls) != 0 {
		t.Fatalf("keyboard_text calls=%v, want no corrective input before fresh observation", keyboardText.calls)
	}
	if len(keyboardTap.calls) != 0 {
		t.Fatalf("keyboard_tap calls=%v, want field content preserved", keyboardTap.calls)
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

func TestEvaluateFieldCommitUsesVisualMatchInsteadOfFieldTextEquality(t *testing.T) {
	committed, fieldText := evaluateFieldCommit(textInputScreenAnalysis{
		FieldText:     "你好我是Aiden,",
		TargetMatched: true,
	}, "你好我是Aiden，")
	if !committed {
		t.Fatal("visually confirmed punctuation match should commit despite transcription code-point differences")
	}
	if fieldText != "你好我是Aiden," {
		t.Fatalf("fieldText = %q, want diagnostic transcription preserved", fieldText)
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
	}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		// attempt 1: before committing, analyze sees the matching candidate
		pending,
		// after clicking candidate: committed
		{FieldText: "你好"},
	}, actions: []textInputCandidateAction{{Action: textInputCandidateActionSelect, Offset: 0, Text: "你好"}}}
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
	if len(kbTap.calls) != 1 || !strings.Contains(kbTap.calls[0], "space") {
		t.Fatalf("candidate selection should confirm the highlighted match with Space, keyboard_tap calls=%v", kbTap.calls)
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
	if result.Committed {
		t.Fatalf("Committed=%v, want false", result.Committed)
	}
}

func TestEnterTextInFieldCandidatePaging(t *testing.T) {
	// Simulate: after typing, composition pending but no candidates visible,
	// then after paging down once, candidate appears and gets clicked, then field committed.
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		// first analyze after typing segment "ni"
		{ObservedMode: textInputModeComposition, FieldText: "", CompositionPending: true},
		// after first page-down: still no match
		{ObservedMode: textInputModeComposition, FieldText: "", CompositionPending: true},
		// after second page-down: candidate found
		{ObservedMode: textInputModeComposition, FieldText: "", CompositionPending: true},
		// after keyboard-confirming candidate: committed
		{FieldText: "你"},
		// second segment "hao" typed, analyze
		{ObservedMode: textInputModeComposition, FieldText: "你", CompositionPending: true},
		// after keyboard-confirming: committed
		{FieldText: "你好"},
	}, actions: []textInputCandidateAction{
		{Action: textInputCandidateActionExpand},
		{Action: textInputCandidateActionExpand},
		{Action: textInputCandidateActionSelect, Offset: 0, Text: "你"},
		{Action: textInputCandidateActionSelect, Offset: 0, Text: "好"},
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

	committed, _, _, _, _, err := engine.typeCompositionWithCandidateSelection(
		context.Background(),
		"ios",
		enterTextInFieldArgs{Text: "是一个硬件智能"},
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

	committed, _, _, _, _, err := engine.typeCompositionWithCandidateSelection(
		context.Background(),
		"ios",
		enterTextInFieldArgs{Text: "经理"},
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

	committed, _, _, vlmCalls, _, err := engine.typeCompositionWithCandidateSelection(
		context.Background(),
		"ios",
		enterTextInFieldArgs{Text: "把自己", CurrentIMEPart: "把自己"},
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

	committed, _, _, _, _, err := engine.typeCompositionWithCandidateSelection(
		context.Background(),
		"ios",
		enterTextInFieldArgs{Text: "目标文本"},
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

	committed, _, _, _, _, err := engine.typeCompositionWithCandidateSelection(
		context.Background(),
		"ios",
		enterTextInFieldArgs{Text: "目标文本"},
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

	committed, _, _, _, _, err := engine.typeCompositionWithCandidateSelection(
		context.Background(),
		"ios",
		enterTextInFieldArgs{Text: "我们大概率"},
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
