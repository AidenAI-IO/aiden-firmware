package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOpenAppSearchPastesBackgroundClipboardBeforeTyping(t *testing.T) {
	const platform = "ios"
	for _, query := range []string{"微信", "WeChat"} {
		t.Run(platform+"/"+query, func(t *testing.T) {
			pb := newTestPhoneBridge(t)
			pb.platform = platform
			pb.appState = "background"
			pb.appStateAt = time.Now().Add(-phoneBridgeBackgroundStateMaxAge - time.Second)
			pb.pipBridgeSeen, pb.pipBridgeEnabled = true, true
			// Search must refresh the clipboard even if the board remembers
			// the same text: another app may have changed the pasteboard.
			pb.NoteClipboardWrite(query)
			quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
			touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
			keyboard := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
			hw := &textInputHardwareDeps{
				deviceTypeFn: func() string { return platform },
				quickAction:  quick, touchGesture: touch, keyboardText: keyboard,
				keyboardTap: &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
				screenshot:  textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
			}
			vision := unexpectedSearchFieldVision{t: t}
			entry := &EnterTextTool{
				engine:     newFastTextInputEngine(*hw, vision),
				bridgeTool: &textInputBridge{hw: hw, vision: vision, bridgeFn: func() *PhoneBridge { return pb }, sleep: testNoWaitSleep},
			}
			search := &appSearchOpenTool{
				hw: hw, vision: vision, entryTool: entry, deviceTypeFn: hw.deviceTypeFn, sleep: testNoWaitSleep,
				findAppTapFn: func(context.Context, screenshotResult, string) (bridgeSearchResult, error) {
					return bridgeSearchResult{Found: true, TapPoint: &focusPointArgs{X: 200, Y: 300}}, nil
				},
				confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
					return bridgeAppOpenResult{Opened: true}, nil
				},
			}
			queueResult := make(chan error, 1)
			go func() {
				cmd, err := waitForQueuedBridgeCommand(pb.queue, platform, time.Second)
				if err != nil {
					queueResult <- err
					return
				}
				var payload struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(cmd.Payload, &payload); err != nil || cmd.Type != "clipboard_write" || payload.Text != query {
					queueResult <- fmt.Errorf("unexpected clipboard command: %+v", cmd)
					return
				}
				queueResult <- pb.queue.SubmitResult(BridgeCommandResponse{ID: cmd.ID, Method: "clipboard_write"})
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			out, err := NewOpenAppTool(pb, nil, search).Call(ctx, jsonString(map[string]string{"app": query}))
			if queueErr := <-queueResult; queueErr != nil {
				t.Fatal(queueErr)
			}
			if err != nil || !enterTextOutputOK(out, err) {
				t.Fatalf("open_app = %s, %v", out, err)
			}
			if len(keyboard.calls) != 0 {
				t.Fatalf("keyboard_text = %v, want no typing after verified paste", keyboard.calls)
			}
			if len(quick.calls) != 2 || !strings.Contains(quick.calls[0], "spotlight_search") || !strings.Contains(quick.calls[1], "paste") {
				t.Fatalf("quick actions = %v, want search then paste without app switching", quick.calls)
			}
			if len(touch.calls) != 1 || !strings.Contains(touch.calls[0], "200") {
				t.Fatalf("touch calls = %v, want only app result tap", touch.calls)
			}
		})
	}
}

// A successful paste must proceed to app lookup without field OCR or an
// input-mode probe, even if an OCR model would disagree about the field text.
type unexpectedSearchFieldVision struct{ t *testing.T }

func (v unexpectedSearchFieldVision) AnalyzeScreen(context.Context, screenshotResult, textInputScreenAnalysisRequest) (textInputScreenAnalysis, error) {
	v.t.Fatal("search paste must be verified by app results, not field OCR")
	return textInputScreenAnalysis{}, nil
}

func TestSearchPiPClipboardFailureFallsBackToLocal(t *testing.T) {
	pb := newTestPhoneBridge(t)
	pb.platform, pb.appState = "ios", "background"
	pb.pipBridgeSeen, pb.pipBridgeEnabled = true, true
	keyboard := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	keys := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	touch := &recordingTextInputTool{name: "touch_gesture", out: "ok"}
	hw := &textInputHardwareDeps{
		deviceTypeFn: func() string { return "ios" },
		keyboardText: keyboard, keyboardTap: keys, quickAction: quick, touchGesture: touch,
		screenshot: textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}}}
	writes := 0
	entry := &EnterTextTool{
		engine: newFastTextInputEngine(*hw, vision), allowIOSKeyboardIsolationBypass: true,
		bridgeTool: &textInputBridge{
			hw: hw, bridgeFn: func() *PhoneBridge { return pb }, sleep: testNoWaitSleep,
			clipboardWriteFn: func(context.Context, *PhoneBridge, string) error {
				writes++
				return fmt.Errorf("clipboard unavailable")
			},
		},
	}
	result, err := runAppSearchOpenFlow(context.Background(), appSearchOpenFlowConfig{
		hw: hw, vision: vision, platform: "ios", searchTerm: "Demo", entryTool: entry, sleep: testNoWaitSleep,
		findAppTapFn: func(context.Context, screenshotResult, string) (bridgeSearchResult, error) {
			return bridgeSearchResult{Found: true, TapPoint: &focusPointArgs{X: 200, Y: 300}}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			return bridgeAppOpenResult{Opened: true}, nil
		},
	})
	if err != nil || !result.Opened || writes != 1 || len(keyboard.calls) != 2 || len(touch.calls) != 1 {
		t.Fatalf("result=%+v err=%v writes=%d keyboard=%v taps=%v", result, err, writes, keyboard.calls, touch.calls)
	}
	if len(keys.calls) < 2 || !strings.Contains(keys.calls[0], `"meta"`) || !strings.Contains(keys.calls[0], `"a"`) || !strings.Contains(keys.calls[1], "backspace") {
		t.Fatalf("keys=%v, want clear before local fallback", keys.calls)
	}
	for _, call := range keys.calls {
		if strings.Contains(call, "escape") {
			t.Fatal("Escape can dismiss system search during recovery")
		}
	}
}

func TestSearchQueryDoesNotRestoreCompanionOrUseUnsupportedClipboardRoute(t *testing.T) {
	for _, state := range []string{"no_pip", "foreground", "android_fgs"} {
		t.Run(state, func(t *testing.T) {
			pb := newTestPhoneBridge(t)
			pb.platform, pb.appState = "ios", "background"
			pb.appStateAt = time.Now()
			pb.returnEntry, pb.returnEntryOK, pb.returnEntrySeen = "dynamic_island", true, true
			if state == "foreground" {
				pb.appState, pb.connected = "active", true
			}
			if state == "android_fgs" {
				pb.platform = "android"
				pb.fgsBridgeSeen, pb.fgsBridgeEnabled = true, true
				pb.fgsBridgeAt = time.Now()
			}
			pb.NoteClipboardWrite("Aiden")
			keyboard := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
			hw := &textInputHardwareDeps{
				deviceTypeFn: func() string { return pb.platform },
				keyboardText: keyboard,
				keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
				screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
			}
			vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}}}
			entry := &EnterTextTool{
				engine:                          newFastTextInputEngine(*hw, vision),
				allowIOSKeyboardIsolationBypass: true,
				bridgeTool: &textInputBridge{
					bridgeFn: func() *PhoneBridge { return pb },
					clipboardWriteFn: func(context.Context, *PhoneBridge, string) error {
						t.Fatal("clipboard write attempted without a target-preserving route")
						return nil
					},
				},
			}
			if err := enterSearchQuery(context.Background(), appSearchOpenFlowConfig{hw: hw, vision: vision, entryTool: entry, sleep: testNoWaitSleep}, "Aiden", false); err != nil {
				t.Fatal(err)
			}
			if len(keyboard.calls) != 2 {
				t.Fatalf("keyboard calls = %v, want local probe and query", keyboard.calls)
			}
		})
	}
}

func TestCommitPendingSearchQueryCommitsBeforePointerRestore(t *testing.T) {
	keys := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	screenshot := &recordingTextInputTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeComposition, CompositionPending: true},
		{ObservedMode: textInputModeComposition, FieldText: "微信", TargetMatched: true},
	}, actions: []textInputCandidateAction{{Action: textInputCandidateActionSelect, Text: "微信", CompletesPart: true}}}
	controller := newTestIOSKeyboardIsolationController(&[]string{})
	hw := &textInputHardwareDeps{keyboardTap: keys, screenshot: screenshot}
	entry := &EnterTextTool{iosKeyboardIsolation: controller}
	cfg := appSearchOpenFlowConfig{
		hw: hw, vision: vision, platform: "ios", entryTool: entry, sleep: testNoWaitSleep,
	}
	if err := commitPendingSearchQuery(context.Background(), cfg, "微信"); err != nil {
		t.Fatalf("commitPendingSearchQuery() error = %v", err)
	}
	if len(keys.calls) != 1 || !strings.Contains(keys.calls[0], "space") {
		t.Fatalf("keyboard calls = %v, want one candidate commit", keys.calls)
	}
}

func TestOpenAppSearchRecoversUnverifiedInputBeforeRestoringHID(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending=%t", pending), func(t *testing.T) {
			events := []string{}
			controller := newTestIOSKeyboardIsolationController(&events)
			keys := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
			vision := &plannedTextInputVision{
				stubTextInputVision: &stubTextInputVision{analyses: []textInputScreenAnalysis{
					{ObservedMode: textInputModeComposition}, // Probe.
					{ObservedMode: textInputModeUnknown},     // Entry verification fails.
					{ObservedMode: textInputModeComposition, CompositionPending: pending},
				}, actions: []textInputCandidateAction{{Action: textInputCandidateActionSelect, Text: "豆包", CompletesPart: true}}},
				plans: map[string][]string{"豆包": {"dou", "bao"}},
			}
			if pending {
				vision.analyses = append(vision.analyses, textInputScreenAnalysis{ObservedMode: textInputModeComposition, TargetMatched: true, FieldText: "豆包"})
			}
			hw := &textInputHardwareDeps{
				deviceTypeFn: func() string { return "ios" },
				quickAction:  textInputStubTool{name: "quick_action", out: `{"ok":true}`},
				keyboardTap:  keys, keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
				touchGesture: iosIsolationPointerTestTool{controller: controller, events: &events},
				screenshot:   textInputStubTool{name: "screenshot", out: `{"data":"abc"}`},
			}
			entry := &EnterTextTool{engine: newFastTextInputEngine(*hw, vision), iosKeyboardIsolation: controller}
			confirmed := false
			tool := &appSearchOpenTool{
				hw: hw, vision: vision, entryTool: entry, iosKeyboardIsolation: controller, sleep: testNoWaitSleep,
				findAppTapFn: func(ctx context.Context, _ screenshotResult, _ string) (bridgeSearchResult, error) {
					if batch := controller.batchFromContext(ctx); batch == nil || !batch.isolated {
						t.Fatal("HID was restored before recovery and result lookup")
					}
					if len(vision.analyses) != 0 {
						t.Fatal("result lookup ran before composition verification")
					}
					space := false
					for _, call := range keys.calls {
						space = space || strings.Contains(call, `"space"`)
					}
					if space != pending {
						t.Fatalf("candidate committed=%t, pending=%t", space, pending)
					}
					return bridgeSearchResult{Found: true, Label: "豆包", TapPoint: &focusPointArgs{X: 150, Y: 160}}, nil
				},
				confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
					confirmed = true
					return bridgeAppOpenResult{Opened: true}, nil
				},
			}
			out, err := tool.Call(context.Background(), `{"app":"豆包"}`)
			if err != nil || !enterTextOutputOK(out, err) || !confirmed {
				t.Fatalf("open_app=%s, %v; confirmed=%t", out, err, confirmed)
			}
			if !reflect.DeepEqual(events, []string{"isolate", "restore", "pointer"}) {
				t.Fatalf("HID events=%v", events)
			}
		})
	}
}

func TestCommitPendingSearchQueryRejectsUnconfirmedComposition(t *testing.T) {
	keys := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeUnknown}, {ObservedMode: textInputModeUnknown}, {ObservedMode: textInputModeUnknown},
	}}
	cfg := appSearchOpenFlowConfig{
		hw:     &textInputHardwareDeps{keyboardTap: keys, screenshot: textInputStubTool{name: "screenshot", out: `{"data":"abc"}`}},
		vision: vision, platform: "ios", sleep: testNoWaitSleep,
		entryTool: &EnterTextTool{iosKeyboardIsolation: newTestIOSKeyboardIsolationController(&[]string{})},
	}
	if err := commitPendingSearchQuery(context.Background(), cfg, "豆包"); err == nil {
		t.Fatal("unknown composition must not allow a result tap")
	}
	if len(keys.calls) != 0 {
		t.Fatalf("sent unverified candidate selection: %v", keys.calls)
	}
}

func TestOpenAppSearchUsesVerifiedEntryWithoutAnotherIMECheck(t *testing.T) {
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)
	keys := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	entryVision := &plannedTextInputVision{
		stubTextInputVision: &stubTextInputVision{
			analyses: []textInputScreenAnalysis{
				{ObservedMode: textInputModeComposition},
				{ObservedMode: textInputModeComposition, CompositionPending: true},
				{ObservedMode: textInputModeComposition, TargetMatched: true, FieldText: "豆包"},
			},
			actions: []textInputCandidateAction{{Action: textInputCandidateActionSelect, Text: "豆包", CompletesPart: true}},
		},
		plans: map[string][]string{"豆包": {"dou", "bao"}},
	}
	hw := &textInputHardwareDeps{
		deviceTypeFn: func() string { return "ios" },
		quickAction:  textInputStubTool{name: "quick_action", out: `{"ok":true}`},
		keyboardTap:  keys, keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		touchGesture: iosIsolationPointerTestTool{controller: controller, events: &events},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"data":"abc"}`},
	}
	confirmed := false
	tool := &appSearchOpenTool{
		hw: hw, vision: unexpectedSearchFieldVision{t: t},
		entryTool:            &EnterTextTool{engine: newFastTextInputEngine(*hw, entryVision), iosKeyboardIsolation: controller},
		iosKeyboardIsolation: controller, sleep: testNoWaitSleep,
		findAppTapFn: func(ctx context.Context, _ screenshotResult, _ string) (bridgeSearchResult, error) {
			if len(entryVision.analyses) != 0 || len(entryVision.actions) != 0 {
				t.Fatal("candidate was not selected and verified before app lookup")
			}
			if batch := controller.batchFromContext(ctx); batch == nil || !batch.isolated {
				t.Fatal("keyboard batch restored before app lookup")
			}
			return bridgeSearchResult{Found: true, Label: "豆包", TapPoint: &focusPointArgs{X: 150, Y: 160}}, nil
		},
		confirmAppOpenFn: func(context.Context, screenshotResult, string) (bridgeAppOpenResult, error) {
			confirmed = true
			return bridgeAppOpenResult{Opened: true}, nil
		},
	}
	out, err := tool.Call(context.Background(), `{"app":"豆包"}`)
	if err != nil || !enterTextOutputOK(out, err) || !confirmed {
		t.Fatalf("open_app=%s, %v; confirmed=%t", out, err, confirmed)
	}
	if !reflect.DeepEqual(events, []string{"isolate", "restore", "pointer"}) {
		t.Fatalf("HID events=%v", events)
	}
}

func TestAppSearchResultRetriesMissingDecisionAndAcceptsMetadata(t *testing.T) {
	model := &textInputVisionRecordingModel{contents: []string{`{}`, `{"type":"json_object","found":true,"tap_point":{"x":125,"y":160},"label":"豆包"}`}}
	vision := &llmTextInputVision{models: model}
	hw := &textInputHardwareDeps{screenshot: textInputStubTool{name: "screenshot", out: `{"data":"abc"}`}}
	result, calls, err := findSearchOpenAppResult(context.Background(), appSearchOpenFlowConfig{hw: hw, vision: vision}, newFastTextInputEngine(*hw, vision), "豆包")
	if err != nil || !result.Found || result.TapPoint == nil || result.TapPoint.X != 125 || calls != 2 {
		t.Fatalf("result=%+v calls=%d error=%v", result, calls, err)
	}
}

func TestAppSearchResultRejectsMissingOrInvalidCoordinates(t *testing.T) {
	for _, raw := range []string{
		`{"type":"json_object"}`, `{"found":true}`, `{"found":true,"tap_point":{}}`,
		`{"found":true,"tap_point":{"x":125}}`, `{"found":true,"tap_point":{"x":-1,"y":160}}`,
		`{"found":true,"tap_point":{"x":125,"y":1001}}`, `{"found":"true"}`,
	} {
		if _, err := parseAppSearchResult(raw); err == nil {
			t.Fatalf("accepted invalid app result %s", raw)
		}
	}
	if result, err := parseAppSearchResult(`{"found":false}`); err != nil || result.Found {
		t.Fatalf("valid not-found=%+v, %v", result, err)
	}
}

func TestAppOpenConfirmationRetriesMissingDecision(t *testing.T) {
	model := &textInputVisionRecordingModel{contents: []string{`{}`, `{"opened":true,"reason":"app screen is visible"}`}}
	vision := &llmTextInputVision{models: model}
	hw := &textInputHardwareDeps{screenshot: textInputStubTool{name: "screenshot", out: `{"data":"abc"}`}}
	result, calls, err := confirmSearchOpenApp(context.Background(), appSearchOpenFlowConfig{hw: hw, vision: vision}, newFastTextInputEngine(*hw, vision), "豆包")
	if err != nil || !result.Opened || calls != 2 {
		t.Fatalf("confirmation=%+v calls=%d error=%v", result, calls, err)
	}
	if _, err := parseAppOpenConfirmation(`{"type":"json_object"}`); err == nil {
		t.Fatal("missing opened must not become opened=false and trigger another tap")
	}
}

func TestSearchQueryClearsProbeBeforeTypingWithoutUndo(t *testing.T) {
	for _, platform := range []string{"ios", "android"} {
		for _, failClear := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail_clear=%t", platform, failClear), func(t *testing.T) {
				var events []string
				keyboard := &stubTextInputCallTool{tool: textInputStubTool{name: "keyboard_text"}, fn: func(_ context.Context, input string) (string, error) {
					var args struct{ Text string }
					if err := json.Unmarshal([]byte(input), &args); err != nil {
						t.Fatal(err)
					}
					events = append(events, "type:"+args.Text)
					return "ok", nil
				}}
				keys := &stubTextInputCallTool{tool: textInputStubTool{name: "keyboard_tap"}, fn: func(_ context.Context, input string) (string, error) {
					var args struct{ Keys []string }
					if err := json.Unmarshal([]byte(input), &args); err != nil {
						t.Fatal(err)
					}
					events = append(events, strings.Join(args.Keys, "+"))
					if failClear && reflect.DeepEqual(args.Keys, []string{"backspace"}) {
						return "", fmt.Errorf("clear failed")
					}
					return "ok", nil
				}}
				vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}}}
				hw := &textInputHardwareDeps{
					deviceTypeFn: func() string { return platform }, keyboardText: keyboard, keyboardTap: keys,
					screenshot: textInputStubTool{name: "screenshot", out: `{"data":"abc"}`},
				}
				entry := &EnterTextTool{engine: newFastTextInputEngine(*hw, vision), allowIOSKeyboardIsolationBypass: true}
				local, err := enterSearchQueryWithRoute(context.Background(), appSearchOpenFlowConfig{hw: hw, vision: vision, entryTool: entry}, "WeChat", false)
				if !local || (err != nil) != failClear {
					t.Fatalf("local=%t error=%v", local, err)
				}
				modifier := "meta"
				if platform == "android" {
					modifier = "ctrl"
				}
				want := []string{"type:a", modifier + "+a", "backspace"}
				if !failClear {
					want = append(want, "type:WeChat")
				}
				if !reflect.DeepEqual(events, want) {
					t.Fatalf("events=%v, want %v; search must not undo or type before clearing", events, want)
				}
			})
		}
	}
}
