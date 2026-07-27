package agent

import (
	"context"
	"reflect"
	"testing"
)

func TestSplitTextInputChunksPreservesASCIIAndIMERuns(t *testing.T) {
	chunks, err := splitTextInputChunks("A你好-42世界")
	if err != nil {
		t.Fatalf("splitTextInputChunks() error = %v", err)
	}
	want := []textInputChunk{
		{text: "A", ascii: true},
		{text: "你好", ascii: false},
		{text: "-42", ascii: true},
		{text: "世界", ascii: false},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("chunks = %#v, want %#v", chunks, want)
	}
	segments, err := splitSegmentsForTextChunks(chunks, []string{"ni", "hao", "shi", "jie"})
	if err != nil {
		t.Fatalf("splitSegmentsForTextChunks() error = %v", err)
	}
	if want := [][]string{nil, {"ni", "hao"}, nil, {"shi", "jie"}}; !reflect.DeepEqual(segments, want) {
		t.Fatalf("segments = %#v, want %#v", segments, want)
	}
}

func TestTextInputEngineRunSegmentedUsesKeyboardAndIMEInOrder(t *testing.T) {
	keyboard := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeASCII, FieldText: "A"},
		{ObservedMode: textInputModeComposition, FieldText: "A你好"},
		{ObservedMode: textInputModeASCII, FieldText: "A你好B"},
	}}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: keyboard,
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), enterTextInFieldArgs{
		Text: "A你好B", Platform: "android", Focus: focusPointArgs{X: 10, Y: 10}, Segments: []string{"ni", "hao"},
	})
	if err != nil {
		t.Fatalf("RunSegmented() error = %v", err)
	}
	if !result.Committed || result.FieldText != "A你好B" {
		t.Fatalf("result = %+v, want verified full target", result)
	}
	wantCalls := []string{`{"text":"A"}`, `{"text":"ni"}`, `{"text":"hao"}`, `{"text":"B"}`}
	if !reflect.DeepEqual(keyboard.calls, wantCalls) {
		t.Fatalf("keyboard calls = %#v, want %#v", keyboard.calls, wantCalls)
	}
}

func TestTextInputEngineRunSegmentedCommitsASCIIPreeditOnlyWhenVisionApproves(t *testing.T) {
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeComposition, FieldText: "", CompositionPending: true, CommitWithEnter: true},
		{ObservedMode: textInputModeASCII, FieldText: "A"},
		{ObservedMode: textInputModeComposition, FieldText: "A你"},
	}}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  keyboardTap,
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), enterTextInFieldArgs{
		Text: "A你", Platform: "android", Focus: focusPointArgs{X: 10, Y: 10}, Segments: []string{"ni"},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want committed result", result, err)
	}
	if want := []string{`{"keys":["enter"]}`, `{"keys":["space"]}`}; !reflect.DeepEqual(keyboardTap.calls, want) {
		t.Fatalf("keyboard_tap calls = %#v, want %#v", keyboardTap.calls, want)
	}
}

func TestTextInputEngineRunCommitsPureASCIIPreeditOnlyWhenVisionApproves(t *testing.T) {
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeComposition, FieldText: "", CompositionPending: true, CommitWithEnter: true},
		{ObservedMode: textInputModeASCII, FieldText: "10086"},
	}}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  keyboardTap,
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.Run(context.Background(), enterTextInFieldArgs{
		Text: "10086", Platform: "android", Focus: focusPointArgs{X: 10, Y: 10},
	})
	if err != nil || !result.Committed || result.Attempts != 1 {
		t.Fatalf("Run() = %+v, %v; want first-attempt committed result", result, err)
	}
	if want := []string{`{"keys":["enter"]}`}; !reflect.DeepEqual(keyboardTap.calls, want) {
		t.Fatalf("keyboard_tap calls = %#v, want %#v", keyboardTap.calls, want)
	}
}

func TestEnterTextToolPrefersAvailableBridge(t *testing.T) {
	pb := newTestPhoneBridge(t)
	pb.platform = "android"
	pb.connected = true
	pb.appState = "background"
	tool := &EnterTextTool{bridgeTool: &EnterTextViaBridgeTool{bridgeFn: func() *PhoneBridge { return pb }}}
	if !tool.bridgeAvailable(enterTextInFieldArgs{Text: "ASCII is bridged too", Platform: "android"}) {
		t.Fatal("available Android bridge should be preferred before local entry")
	}
	if tool.bridgeAvailable(enterTextInFieldArgs{Text: "hello", Platform: "mac"}) {
		t.Fatal("mac should not select the phone bridge clipboard route")
	}
}
