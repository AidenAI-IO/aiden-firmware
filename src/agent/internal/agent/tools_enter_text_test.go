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
	plans      map[string][]string
	partitions map[string][]string
}

type concurrentPartPlanningVision struct {
	*stubTextInputVision
	started chan string
	release chan struct{}
}

type concurrentPartPartitionVision struct {
	*stubTextInputVision
	started chan string
	release chan struct{}
}

func (v *concurrentPartPartitionVision) PartitionComposition(ctx context.Context, text string) ([]string, error) {
	v.started <- text
	select {
	case <-v.release:
		runes := []rune(text)
		return []string{string(runes[:3]), string(runes[3:])}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
		_, err := engine.planCompositionSegmentsForChunks(context.Background(), []textInputChunk{{text: "甲"}, {text: "乙"}})
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

func TestLongIMEPartsArePartitionedConcurrently(t *testing.T) {
	vision := &concurrentPartPartitionVision{
		stubTextInputVision: &stubTextInputVision{},
		started:             make(chan string, 2),
		release:             make(chan struct{}),
	}
	engine := newTextInputEngine(textInputHardwareDeps{}, vision)
	resultCh := make(chan error, 1)
	go func() {
		_, err := engine.partitionIMEChunks(context.Background(), []textInputChunk{
			{text: "甲乙丙丁戊己"},
			{text: "，", input: ",", ascii: true},
			{text: "庚辛壬癸子丑"},
		})
		resultCh <- err
	}()
	for index := 0; index < 2; index++ {
		select {
		case <-vision.started:
		case <-time.After(time.Second):
			t.Fatal("long IME part partitioning did not start concurrently")
		}
	}
	close(vision.release)
	if err := <-resultCh; err != nil {
		t.Fatalf("partitionIMEChunks() error = %v", err)
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
	result, err := engine.RunSegmented(ctx, textInputArgs{Text: "你"})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want concurrent probe/planning success", result, err)
	}
}

func (v *plannedTextInputVision) PlanComposition(_ context.Context, text string) ([]string, error) {
	return v.plans[text], nil
}

func (v *plannedTextInputVision) PartitionComposition(_ context.Context, text string) ([]string, error) {
	if parts, ok := v.partitions[text]; ok {
		return parts, nil
	}
	return []string{text}, nil
}

func TestLongIMEChunksArePartitionedBeforeCompositionPlanning(t *testing.T) {
	vision := &plannedTextInputVision{
		stubTextInputVision: &stubTextInputVision{},
		partitions: map[string][]string{
			"经理不是技术出身的": {"经理", "不是", "技术", "出身", "的"},
		},
	}
	chunks, err := splitTextInputChunks("A经理不是技术出身的，B")
	if err != nil {
		t.Fatal(err)
	}
	partitioned, err := newTextInputEngine(textInputHardwareDeps{}, vision).partitionIMEChunks(context.Background(), chunks)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A", "经理", "不是", "技术", "出身", "的", "，", "B"}
	got := make([]string, len(partitioned))
	for index := range partitioned {
		got[index] = partitioned[index].text
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("partitioned parts=%v, want %v", got, want)
	}
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

	result, err := engine.RunSegmented(context.Background(), textInputArgs{
		Text: "hello test", Focus: focusPointArgs{X: 10, Y: 10},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented()=%+v err=%v", result, err)
	}
	if len(kbText.calls) != 3 || len(kbTap.calls) != 2 || !strings.Contains(kbTap.calls[0], `"meta"`) || !strings.Contains(kbTap.calls[0], `"z"`) || !strings.Contains(kbTap.calls[1], "space") {
		t.Fatalf("keyboard_text=%v keyboard_tap=%v", kbText.calls, kbTap.calls)
	}
	if len(mouse.calls) != 0 {
		t.Fatalf("mouse_click calls=%v, want none", mouse.calls)
	}
}

func TestTextInputProbeWaitsForConfiguredSettleDelayBeforeCapture(t *testing.T) {
	var delays []time.Duration
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}}}, func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	})

	mode, _, err := engine.probeTextInputMode(context.Background(), "ios", focusPointArgs{})
	if err != nil || mode != textInputModeASCII {
		t.Fatalf("probeTextInputMode() mode=%s err=%v", mode, err)
	}
	if len(delays) == 0 || delays[0] != textInputProbeSettleDelay {
		t.Fatalf("probe delays=%v, want first delay %s", delays, textInputProbeSettleDelay)
	}
}

func TestTextInputProbeSupportsWindowsAndLinuxPlatforms(t *testing.T) {
	for _, platform := range []string{"windows", "linux"} {
		t.Run(platform, func(t *testing.T) {
			engine := newTextInputEngineWithSleep(textInputHardwareDeps{
				keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
				keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
				screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
			}, &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}}}, func(_ context.Context, _ time.Duration) error {
				return nil
			})

			mode, _, err := engine.probeTextInputMode(context.Background(), platform, focusPointArgs{})
			if err != nil || mode != textInputModeASCII {
				t.Fatalf("probeTextInputMode(%q) mode=%s err=%v", platform, mode, err)
			}
		})
	}
}

func TestTextInputKeyboardKeysForPlatforms(t *testing.T) {
	tests := []struct {
		platform  string
		imeSwitch []string
		selectAll []string
		undo      []string
	}{
		{platform: "ios", imeSwitch: []string{"capslock"}, selectAll: []string{"meta", "a"}, undo: []string{"meta", "z"}},
		{platform: "mac", imeSwitch: []string{"ctrl", "space"}, selectAll: []string{"meta", "a"}, undo: []string{"meta", "z"}},
		{platform: "android", imeSwitch: []string{"KEYCODE_LANGUAGE_SWITCH"}, selectAll: []string{"ctrl", "a"}, undo: []string{"ctrl", "z"}},
		{platform: "windows", imeSwitch: []string{"alt", "shift"}, selectAll: []string{"ctrl", "a"}, undo: []string{"ctrl", "z"}},
		{platform: "linux", imeSwitch: []string{"ctrl", "space"}, selectAll: []string{"ctrl", "a"}, undo: []string{"ctrl", "z"}},
	}
	for _, test := range tests {
		t.Run(test.platform, func(t *testing.T) {
			gotSwitch, err := textInputKeyboardKeysForIMESwitch(test.platform)
			if err != nil || strings.Join(gotSwitch, ",") != strings.Join(test.imeSwitch, ",") {
				t.Errorf("IME switch keys = %v, %v; want %v, nil", gotSwitch, err, test.imeSwitch)
			}
			gotSelectAll, err := textInputKeyboardKeysForSelectAll(test.platform)
			if err != nil || strings.Join(gotSelectAll, ",") != strings.Join(test.selectAll, ",") {
				t.Errorf("select all keys = %v, %v; want %v, nil", gotSelectAll, err, test.selectAll)
			}
			gotUndo, err := textInputKeyboardKeysForUndo(test.platform)
			if err != nil || strings.Join(gotUndo, ",") != strings.Join(test.undo, ",") {
				t.Errorf("undo keys = %v, %v; want %v, nil", gotUndo, err, test.undo)
			}
		})
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
	vision := &plannedTextInputVision{
		stubTextInputVision: &stubTextInputVision{analyses: []textInputScreenAnalysis{
			{ObservedMode: textInputModeASCII, FieldText: "a"},
			{ObservedMode: textInputModeComposition, CompositionPending: true},
			{ObservedMode: textInputModeComposition, FieldText: "A你好", TargetMatched: true},
		}, actions: []textInputCandidateAction{{Action: textInputCandidateActionSelect, Offset: 0, Text: "你好"}}},
		plans: map[string][]string{"你好": {"ni", "hao"}},
	}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: keyboard,
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), textInputArgs{
		Text: "A你好B", Focus: focusPointArgs{X: 10, Y: 10},
	})
	if err != nil {
		t.Fatalf("RunSegmented() error = %v", err)
	}
	if !result.Committed || result.FieldText != "A你好" {
		t.Fatalf("result = %+v, want ordered input", result)
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

	result, err := engine.RunSegmented(context.Background(), textInputArgs{
		Text: "A你好B", Focus: focusPointArgs{X: 10, Y: 10},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want internally planned committed result", result, err)
	}
	if want := []string{jsonString(map[string]string{"text": "a"}), jsonString(map[string]string{"text": "A"}), jsonString(map[string]string{"text": "ni"}), jsonString(map[string]string{"text": "hao"}), jsonString(map[string]string{"text": "B"})}; !reflect.DeepEqual(keyboard.calls, want) {
		t.Fatalf("keyboard calls = %#v, want %#v", keyboard.calls, want)
	}
}

func TestRunSegmentedVerifiesCurrentIMEPartAtCommittedFieldSuffix(t *testing.T) {
	keyboard := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	vision := &plannedTextInputVision{
		stubTextInputVision: &stubTextInputVision{analyses: []textInputScreenAnalysis{
			{ObservedMode: textInputModeComposition, CompositionPending: true},
			{
				ObservedMode:       textInputModeComposition,
				FieldText:          "Aiden 是一个接在手机上的硬件 Agent。它通过 USB 把自己模拟成",
				CompositionPending: true,
			},
			{
				ObservedMode: textInputModeASCII,
				FieldText:    "Aiden 是一个接在手机上的硬件 Agent。它通过 USB 把自己模拟成键盘",
			},
		}, actions: []textInputCandidateAction{{Action: textInputCandidateActionSelect, Text: "键盘"}}},
		plans: map[string][]string{"模拟成键盘": {"mo", "ni", "cheng", "jian", "pan"}},
	}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		pointerMode:  "absolute",
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: keyboard,
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), textInputArgs{
		Text: "模拟成键盘",
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented()=%+v err=%v, want suffix verification success", result, err)
	}
}

func TestTextInputEngineDoesNotVerifyDirectPartsOrUseMouse(t *testing.T) {
	mouse := &recordingTextInputTool{name: "mouse_click", out: "ok"}
	vision := &plannedTextInputVision{
		stubTextInputVision: &stubTextInputVision{analyses: []textInputScreenAnalysis{
			{ObservedMode: textInputModeComposition, CompositionPending: true},
			{ObservedMode: textInputModeComposition, FieldText: "你", TargetMatched: true},
		}},
		plans: map[string][]string{"你": {"ni"}},
	}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		pointerMode:  "absolute",
		mouseClick:   mouse,
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), textInputArgs{
		Text: "你A", Focus: focusPointArgs{X: 10, Y: 10},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want committed result", result, err)
	}
	if len(mouse.calls) != 0 {
		t.Fatalf("mouse=%v, want no pointer input", mouse.calls)
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
		t.Fatal("enter_text must infer the platform from runtime device state")
	}
	if _, found := props["max_attempts"]; found {
		t.Fatal("enter_text must not expose max_attempts")
	}
	if _, found := props["send_after_commit"]; found {
		t.Fatal("enter_text must not expose send_after_commit")
	}
}

func TestTextInputPlatformDefaultsToDeviceType(t *testing.T) {
	if got := (textInputHardwareDeps{pointerMode: "absolute"}).platform(); got != "ios" {
		t.Fatalf("absolute pointer mode platform = %q, want default device_type ios", got)
	}
	if got := (textInputHardwareDeps{pointerMode: "touchscreen"}).platform(); got != "ios" {
		t.Fatalf("touchscreen pointer mode platform = %q, want default device_type ios", got)
	}
}

func TestTextInputPlatformUsesDeviceTypeProviderBeforeRuntimePlatform(t *testing.T) {
	if got := (textInputHardwareDeps{
		pointerMode:  "absolute",
		deviceTypeFn: func() string { return "Android" },
		platformFn:   func() string { return "ios" },
	}).platform(); got != "android" {
		t.Fatalf("device_type platform = %q, want android", got)
	}
	if got := (textInputHardwareDeps{
		pointerMode:  "touchscreen",
		deviceTypeFn: func() string { return "macOS" },
		platformFn:   func() string { return "android" },
	}).platform(); got != "mac" {
		t.Fatalf("device_type platform = %q, want mac", got)
	}
}

func TestTextInputPlatformFallsBackToRuntimePlatformProvider(t *testing.T) {
	if got := (textInputHardwareDeps{
		pointerMode: "absolute",
		platformFn:  func() string { return "Android" },
	}).platform(); got != "android" {
		t.Fatalf("runtime platform = %q, want android", got)
	}
}

func TestEnterTextResultContainsOnlySuccessStatus(t *testing.T) {
	encoded := enterTextToolResultString(textInputResult{
		OK: true, Committed: true, FieldText: "secret",
	})
	var fields map[string]any
	if err := json.Unmarshal([]byte(encoded), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || fields["ok"] != true {
		t.Fatalf("public enter_text success result = %s, want only ok=true", encoded)
	}
}

func TestEnterTextOutputOKReadsCompactResult(t *testing.T) {
	if !enterTextOutputOK(`{"ok":true}`, nil) {
		t.Fatal("ok result must be recognized")
	}
	if enterTextOutputOK(`{"ok":false,"suggestion":"retry"}`, nil) {
		t.Fatal("failed result must not be recognized as successful")
	}
	if enterTextOutputOK(`{"ok":true}`, context.Canceled) {
		t.Fatal("call error must override output success")
	}
}

func TestEnterTextFailureResultContainsOnlyStatusAndSuggestion(t *testing.T) {
	encoded := enterTextToolResultString(textInputResult{
		Reason: "internal diagnostic", FieldText: "wrong text",
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
	vision := &plannedTextInputVision{
		stubTextInputVision: &stubTextInputVision{analyses: []textInputScreenAnalysis{
			{ObservedMode: textInputModeComposition, FieldText: "a", CompositionPending: true},
			{ObservedMode: textInputModeComposition, FieldText: "A你", TargetMatched: true},
		}},
		plans: map[string][]string{"你": {"ni"}},
	}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  keyboardTap,
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), textInputArgs{
		Text: "A你", Focus: focusPointArgs{X: 10, Y: 10},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want committed result", result, err)
	}
	if want := []string{
		jsonString(map[string][]string{"keys": {"meta", "z"}}),
		jsonString(map[string]any{"keys": []string{"capslock"}, "hold_ms": textInputIMESwitchHoldMs}),
		jsonString(map[string]any{"keys": []string{"capslock"}, "hold_ms": textInputIMESwitchHoldMs}),
	}; !reflect.DeepEqual(keyboardTap.calls, want) {
		t.Fatalf("keyboard_tap calls = %#v, want %#v", keyboardTap.calls, want)
	}
}

func TestTextInputEngineRunSegmentedDoesNotVerifyFinalASCIIPart(t *testing.T) {
	keyboardTap := &recordingTextInputTool{name: "keyboard_tap", out: "ok"}
	vision := &plannedTextInputVision{
		stubTextInputVision: &stubTextInputVision{analyses: []textInputScreenAnalysis{
			{ObservedMode: textInputModeComposition, FieldText: "a", CompositionPending: true},
			{ObservedMode: textInputModeComposition, FieldText: "你", TargetMatched: true},
		}},
		plans: map[string][]string{"你": {"ni"}},
	}
	engine := newTextInputEngineWithSleep(textInputHardwareDeps{
		mouseClick:   &recordingTextInputTool{name: "mouse_click", out: "ok"},
		keyboardTap:  keyboardTap,
		keyboardText: &recordingTextInputTool{name: "keyboard_text", out: "ok"},
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}, vision, testNoWaitSleep)

	result, err := engine.RunSegmented(context.Background(), textInputArgs{
		Text: "你A", Focus: focusPointArgs{X: 10, Y: 10},
	})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v; want committed result", result, err)
	}
	if want := []string{
		jsonString(map[string][]string{"keys": {"meta", "z"}}),
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

	result, err := engine.RunSegmented(context.Background(), textInputArgs{Text: "A，B"})
	if err != nil || !result.Committed {
		t.Fatalf("RunSegmented() = %+v, %v", result, err)
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
		jsonString(map[string][]string{"keys": {"meta", "z"}}),
		jsonString(map[string]any{"keys": []string{"capslock"}, "hold_ms": textInputIMESwitchHoldMs}),
		jsonString(map[string]any{"keys": []string{"capslock"}, "hold_ms": textInputIMESwitchHoldMs}),
	}; !reflect.DeepEqual(keyboardTap.calls, want) {
		t.Fatalf("keyboard_tap calls = %#v, want %#v", keyboardTap.calls, want)
	}
}

func TestEnterTextToolPrefersAvailableBridge(t *testing.T) {
	pb := newTestPhoneBridge(t)
	pb.platform = "android"
	pb.connected = true
	pb.appState = "background"
	tool := &EnterTextTool{bridgeTool: &textInputBridge{hw: &textInputHardwareDeps{pointerMode: "absolute"}, bridgeFn: func() *PhoneBridge { return pb }}}
	tool.SetDeviceTypeFunc(func() string { return "Android" })
	if !tool.bridgeAvailable(textInputArgs{Text: "ASCII is bridged too"}) {
		t.Fatal("available Android bridge should be preferred before local entry")
	}
	tool.bridgeTool.hw.pointerMode = "absolute"
	tool.SetPlatformFn(func() string { return "android" })
	if !tool.bridgeAvailable(textInputArgs{Text: "global device state keeps Android bridge route"}) {
		t.Fatal("runtime platform provider should override HID pointer_mode fallback")
	}
	tool.SetDeviceTypeFunc(func() string { return "iOS" })
	if tool.bridgeAvailable(textInputArgs{Text: "hello"}) {
		t.Fatal("iOS device_type state should not select an Android bridge clipboard route")
	}
}

func TestEnterTextToolCallKeepsBridgePathEnabled(t *testing.T) {
	pb := newTestPhoneBridge(t)
	pb.platform = "android"
	pb.connected = true
	pb.appState = "background"
	keyboardText := &recordingTextInputTool{name: "keyboard_text", out: "ok"}
	hw := &textInputHardwareDeps{
		pointerMode:  "touchscreen",
		deviceTypeFn: func() string { return "Android" },
		keyboardTap:  &recordingTextInputTool{name: "keyboard_tap", out: "ok"},
		keyboardText: keyboardText,
		screenshot:   textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
	}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{ObservedMode: textInputModeASCII}}}
	bridgeWrites := 0
	tool := &EnterTextTool{
		engine: newFastTextInputEngine(*hw, vision),
		bridgeTool: &textInputBridge{
			hw:       hw,
			vision:   vision,
			bridgeFn: func() *PhoneBridge { return pb },
			clipboardWriteFn: func(context.Context, *PhoneBridge, string) error {
				bridgeWrites++
				return context.Canceled
			},
		},
	}
	tool.SetDeviceTypeFunc(hw.deviceTypeFn)

	out, err := tool.Call(context.Background(), `{"text":"Aiden","focus":{"x":500,"y":120,"coord_space":"normalized"}}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if bridgeWrites != 1 {
		t.Fatalf("bridge writes = %d, want public enter_text to attempt Bridge once", bridgeWrites)
	}
	if len(keyboardText.calls) != 0 {
		t.Fatalf("keyboard_text calls = %v, want no local fallback after attempted Bridge path", keyboardText.calls)
	}
	if enterTextOutputOK(out, nil) {
		t.Fatalf("Call() output = %s, want failed Bridge result", out)
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
