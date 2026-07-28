package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type plannedTextInputVision struct {
	*stubTextInputVision
	plans map[string][]string
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
		{text: "-42", input: "-42", ascii: true},
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
		{ObservedMode: textInputModeASCII, FieldText: "hello"},
		{ObservedMode: textInputModeASCII, FieldText: "hello test"},
	}}
	kbText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	kbTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  kbTap,
		keyboardText: kbText,
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), enterTextInFieldArgs{
		Text: "hello test", Platform: "android", Focus: focusPointArgs{X: 10, Y: 10},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented()=%+v err=%v", result, err)
	}
	if result.VLMCalls != 2 {
		t.Fatalf("VLM calls=%d, want only the two non-space parts", result.VLMCalls)
	}
	if len(kbText.calls) != 2 || len(kbTap.calls) != 1 || !strings.Contains(kbTap.calls[0], "space") {
		t.Fatalf("keyboard_text=%v keyboard_tap=%v", kbText.calls, kbTap.calls)
	}
}

func TestSplitTextInputChunksSeparatesMixedCJKASCIIParts(t *testing.T) {
	const target = "你好我是Aiden，是一个硬件智能Agent助手，现在我在混合输入各种内容，例如英文Hello和数字12345"
	chunks, err := splitTextInputChunks(target)
	if err != nil {
		t.Fatalf("splitTextInputChunks() error = %v", err)
	}
	want := []textInputChunk{
		{text: "你好我是", ascii: false},
		{text: "Aiden，", input: "Aiden,", ascii: true},
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
	wantCalls := []string{jsonString(map[string]string{"text": "A"}), jsonString(map[string]string{"text": "ni"}), jsonString(map[string]string{"text": "hao"}), jsonString(map[string]string{"text": "B"})}
	if !reflect.DeepEqual(keyboard.calls, wantCalls) {
		t.Fatalf("keyboard calls = %#v, want %#v", keyboard.calls, wantCalls)
	}
}

func TestTextInputEngineRunSegmentedPlansIMESegmentsInternally(t *testing.T) {
	keyboard := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	vision := &plannedTextInputVision{
		stubTextInputVision: &stubTextInputVision{analyses: []textInputScreenAnalysis{
			{ObservedMode: textInputModeASCII, FieldText: "A"},
			{ObservedMode: textInputModeComposition, FieldText: "A你好"},
			{ObservedMode: textInputModeASCII, FieldText: "A你好B"},
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
		Text: "A你好B", Platform: "android", Focus: focusPointArgs{X: 10, Y: 10},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want internally planned committed result", result, err)
	}
	if want := []string{jsonString(map[string]string{"text": "A"}), jsonString(map[string]string{"text": "ni"}), jsonString(map[string]string{"text": "hao"}), jsonString(map[string]string{"text": "B"})}; !reflect.DeepEqual(keyboard.calls, want) {
		t.Fatalf("keyboard calls = %#v, want %#v", keyboard.calls, want)
	}
}

func TestTextInputEngineWaitsForPostInputFrameBeforeDirectPartVerification(t *testing.T) {
	waitStable := &recordingTextInputTool{name: "wait_for_stable_screen", out: `{"ok":true,"stable":true}`}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeComposition, FieldText: "你"},
		{ObservedMode: textInputModeASCII, FieldText: "你A"},
	}}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		waitStable:   waitStable,
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), enterTextInFieldArgs{
		Text: "你A", Platform: "ios", Focus: focusPointArgs{X: 10, Y: 10}, Segments: []string{"ni"},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want committed result", result, err)
	}
	if result.IMESwitches != 2 {
		t.Fatalf("IME switches = %d, want English before ASCII and composition before IME", result.IMESwitches)
	}
	if want := []string{"{}"}; !reflect.DeepEqual(waitStable.calls, want) {
		t.Fatalf("stable-screen calls = %#v, want %#v", waitStable.calls, want)
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
	var args enterTextArgs
	if err := json.Unmarshal([]byte(`{"text":"你好","focus":{"x":1,"y":1},"segments":["ni","hao"]}`), &args); err != nil {
		t.Fatal(err)
	}
	if engineArgs := args.toEngineArgs(); len(engineArgs.Segments) != 0 {
		t.Fatalf("public JSON must not populate internal segments: %#v", engineArgs.Segments)
	}
}

func TestEnterTextResultHasNoModeOrStepsFields(t *testing.T) {
	encoded := jsonString(enterTextInFieldResult{OK: true})
	var fields map[string]any
	if err := json.Unmarshal([]byte(encoded), &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"mode", "steps"} {
		if _, found := fields[field]; found {
			t.Fatalf("public enter_text result must omit %q: %s", field, encoded)
		}
	}
}

func TestTextInputEngineRunSegmentedCommitsASCIIPreeditOnlyWhenVisionApproves(t *testing.T) {
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeComposition, FieldText: "A", TargetMatched: true, CompositionPending: true},
		{ObservedMode: textInputModeComposition, FieldText: "A你", TargetMatched: true},
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
	if result.IMESwitches != 0 {
		t.Fatalf("IME switches = %d, want no proactive switch", result.IMESwitches)
	}
	if want := []string{
		jsonString(map[string][]string{"keys": {"enter"}}),
	}; !reflect.DeepEqual(keyboardTap.calls, want) {
		t.Fatalf("keyboard_tap calls = %#v, want %#v", keyboardTap.calls, want)
	}
}

func TestTextInputEngineRunSegmentedVerifiesFinalASCIIPartAfterEnter(t *testing.T) {
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{
		{ObservedMode: textInputModeComposition, FieldText: "你", TargetMatched: true},
		{ObservedMode: textInputModeComposition, FieldText: "你A", TargetMatched: true, CompositionPending: true},
		{ObservedMode: textInputModeASCII, FieldText: "你A", TargetMatched: true},
	}}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  keyboardTap,
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), enterTextInFieldArgs{
		Text: "你A", Platform: "android", Focus: focusPointArgs{X: 10, Y: 10}, Segments: []string{"ni"},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want committed result", result, err)
	}
	if result.VLMCalls != 3 {
		t.Fatalf("VLM calls = %d, want IME verification plus ASCII pre/post-Enter verification", result.VLMCalls)
	}
	if want := []string{
		jsonString(map[string][]string{"keys": {"enter"}}),
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
		Text: "10086", Platform: "android", Focus: focusPointArgs{X: 10, Y: 10},
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
	tool := &EnterTextTool{bridgeTool: &EnterTextViaBridgeTool{bridgeFn: func() *PhoneBridge { return pb }}}
	if !tool.bridgeAvailable(enterTextInFieldArgs{Text: "ASCII is bridged too", Platform: "android"}) {
		t.Fatal("available Android bridge should be preferred before local entry")
	}
	if tool.bridgeAvailable(enterTextInFieldArgs{Text: "hello", Platform: "mac"}) {
		t.Fatal("mac should not select the phone bridge clipboard route")
	}
}
