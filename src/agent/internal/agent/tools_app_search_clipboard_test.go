package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOpenAppSearchPastesBackgroundClipboardBeforeTyping(t *testing.T) {
	for _, platform := range []string{"ios", "android"} {
		for _, query := range []string{"微信", "WeChat"} {
			t.Run(platform+"/"+query, func(t *testing.T) {
				pb := newTestPhoneBridge(t)
				pb.platform = platform
				pb.appState = "background"
				pb.appStateAt = time.Now().Add(-phoneBridgeBackgroundStateMaxAge - time.Second)
				pb.pipBridgeSeen, pb.pipBridgeEnabled = true, platform == "ios"
				pb.fgsBridgeSeen, pb.fgsBridgeEnabled = true, platform == "android"
				pb.fgsBridgeAt = time.Now()
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
				vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{FieldText: query}}}
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
}

func TestSearchQueryDoesNotRestoreCompanionOrUseUnsupportedClipboardRoute(t *testing.T) {
	for _, state := range []string{"no_pip", "foreground", "stale_fgs"} {
		t.Run(state, func(t *testing.T) {
			pb := newTestPhoneBridge(t)
			pb.platform, pb.appState = "ios", "background"
			pb.appStateAt = time.Now()
			pb.returnEntry, pb.returnEntryOK, pb.returnEntrySeen = "dynamic_island", true, true
			if state == "foreground" {
				pb.appState, pb.connected = "active", true
			}
			if state == "stale_fgs" {
				pb.platform = "android"
				pb.fgsBridgeSeen, pb.fgsBridgeEnabled = true, true
				pb.fgsBridgeAt = time.Now().Add(-time.Minute)
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

func TestSearchQueryClearsUnverifiedPasteBeforeLocalFallback(t *testing.T) {
	pb := newTestPhoneBridge(t)
	pb.platform, pb.appState = "ios", "background"
	pb.appStateAt = time.Now()
	pb.pipBridgeSeen, pb.pipBridgeEnabled = true, true
	keyboard := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	keys := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	quick := &recordingTextInputTool{name: "quick_action", out: `{"ok":true}`}
	hw := &textInputHardwareDeps{
		deviceTypeFn: func() string { return "ios" },
		keyboardText: keyboard, keyboardTap: keys, quickAction: quick,
		screenshot: textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{FieldText: "old clipboard text"}, {ObservedMode: textInputModeASCII},
	}}
	entry := &EnterTextTool{
		engine: newFastTextInputEngine(*hw, vision), allowIOSKeyboardIsolationBypass: true,
		bridgeTool: &textInputBridge{
			hw: hw, bridgeFn: func() *PhoneBridge { return pb }, sleep: testNoWaitSleep,
			clipboardWriteFn: func(context.Context, *PhoneBridge, string) error { return nil },
		},
	}
	if err := enterSearchQuery(context.Background(), appSearchOpenFlowConfig{hw: hw, vision: vision, entryTool: entry, sleep: testNoWaitSleep}, "Aiden", false); err != nil {
		t.Fatal(err)
	}
	if len(quick.calls) != 1 || !strings.Contains(quick.calls[0], "paste") || len(keyboard.calls) != 2 {
		t.Fatalf("quick=%v keyboard=%v, want paste followed by local probe/query", quick.calls, keyboard.calls)
	}
	if len(keys.calls) < 2 || !strings.Contains(keys.calls[0], `"meta"`) || !strings.Contains(keys.calls[0], `"a"`) || !strings.Contains(keys.calls[1], "backspace") {
		t.Fatalf("keys=%v, want select-all/backspace before local fallback", keys.calls)
	}
	for _, call := range keys.calls {
		if strings.Contains(call, "escape") {
			t.Fatal("Escape can dismiss system search during paste recovery")
		}
	}
}

func TestSearchClipboardVerificationUsesFocusedQueryInsteadOfCoordinates(t *testing.T) {
	prompt := buildTextInputAnalysisPrompt(textInputScreenAnalysisRequest{TargetText: "微信", FocusedSystemSearch: true})
	if !strings.Contains(prompt, "already-focused system search query field") || strings.Contains(prompt, "Focus (normalized") {
		t.Fatalf("unexpected search verification prompt: %s", prompt)
	}
}
