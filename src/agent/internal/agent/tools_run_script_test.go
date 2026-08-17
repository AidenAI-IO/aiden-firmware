package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
)

func TestRunScriptExecutesJSONLStepsWithoutLLM(t *testing.T) {
	calledTool := &stubTool{name: "keyboard_text", output: "ok"}
	scriptsDir := t.TempDir()
	tool := NewRunScriptTool(scriptsDir, func(name string) (langtools.Tool, bool) {
		if name == calledTool.name {
			return calledTool, true
		}
		return nil, false
	})

	var waits []time.Duration
	tool.sleeper = func(_ context.Context, duration time.Duration) error {
		waits = append(waits, duration)
		return nil
	}
	spoken := make(chan string, 1)
	tool.SetSpeaker(func(_ context.Context, text string) error {
		spoken <- text
		return nil
	})

	file := "demo.jsonl"
	writeRunScriptTestFile(t, scriptsDir, file, strings.Join([]string{
		`{"type":"wait","ms":250}`,
		`{"type":"tts","text":"正在打开设置"}`,
		`{"type":"call","tool":"keyboard_text","input":{"text":"demo"}}`,
	}, "\n"))

	out, err := tool.Call(context.Background(), `{"file":`+quoteJSON(file)+`}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	var result runScriptResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, out)
	}
	if !result.OK || result.StepsRun != 3 {
		t.Fatalf("result = %#v, output=%s", result, out)
	}
	if len(waits) != 1 || waits[0] != 250*time.Millisecond {
		t.Fatalf("waits = %v, want [250ms]", waits)
	}
	select {
	case got := <-spoken:
		if got != "正在打开设置" {
			t.Fatalf("spoken text = %q, want 正在打开设置", got)
		}
	case <-time.After(time.Second):
		t.Fatal("tts speaker was not called")
	}
	if len(calledTool.inputs) != 1 || calledTool.inputs[0] != `{"text":"demo"}` {
		t.Fatalf("tool inputs = %#v", calledTool.inputs)
	}
	if result.Steps[1].Text != "正在打开设置" || result.Steps[1].Output != "queued" {
		t.Fatalf("tts result = %#v, want text and queued output", result.Steps[1])
	}
}

func TestRunScriptBatchesIOSModifierIsolationAcrossSteps(t *testing.T) {
	skipHIDSleeps(t)

	scriptsDir := t.TempDir()
	dev, _ := newTestHIDDevice(t)
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)
	controller.keyboardDev = dev
	keyboard := testKeyboardTapTool(t, testMNKOpts{keyboard: dev, gate: newIOSKeyboardIsolationProfileGate(controller)})
	tool := NewRunScriptTool(scriptsDir, func(name string) (langtools.Tool, bool) {
		if name == "keyboard_tap" {
			return keyboard, true
		}
		return nil, false
	})
	tool.iosKeyboardIsolation = controller

	file := "modifier-batch.jsonl"
	writeRunScriptTestFile(t, scriptsDir, file, strings.Join([]string{
		`{"type":"call","tool":"keyboard_tap","input":{"keys":["meta","a"]}}`,
		`{"type":"call","tool":"keyboard_tap","input":{"keys":["meta","c"]}}`,
	}, "\n"))

	out, err := tool.Call(context.Background(), `{"file":`+quoteJSON(file)+`}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	var result runScriptResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, out)
	}
	if !result.OK || result.StepsRun != 2 {
		t.Fatalf("result = %#v, output=%s", result, out)
	}
	if want := []string{"isolate", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("profile events = %v, want %v", events, want)
	}
}

func TestRunScriptStopsOnToolError(t *testing.T) {
	first := &stubTool{name: "first", output: "error: failed"}
	second := &stubTool{name: "second", output: "ok"}
	scriptsDir := t.TempDir()
	tool := NewRunScriptTool(scriptsDir, func(name string) (langtools.Tool, bool) {
		switch name {
		case "first":
			return first, true
		case "second":
			return second, true
		default:
			return nil, false
		}
	})
	tool.sleeper = func(context.Context, time.Duration) error { return nil }

	file := "demo.jsonl"
	writeRunScriptTestFile(t, scriptsDir, file, strings.Join([]string{
		`{"type":"call","tool":"first","input":{}}`,
		`{"type":"call","tool":"second","input":{}}`,
	}, "\n"))

	out, err := tool.Call(context.Background(), `{"file":`+quoteJSON(file)+`}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	var result runScriptResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, out)
	}
	if result.OK || result.StepsRun != 1 || !strings.Contains(result.Error, "error: failed") {
		t.Fatalf("result = %#v, output=%s", result, out)
	}
	if len(second.inputs) != 0 {
		t.Fatalf("second tool should not run after failure, inputs=%#v", second.inputs)
	}
}

func TestRunScriptStopsOnStructuredToolError(t *testing.T) {
	toolErr := NewToolError(CodeInvalidArguments, "structured failure")
	first := &contextToolErrorStub{name: "first", toolErr: toolErr}
	second := &stubTool{name: "second", output: "ok"}
	scriptsDir := t.TempDir()
	tool := NewRunScriptTool(scriptsDir, func(name string) (langtools.Tool, bool) {
		switch name {
		case "first":
			return first, true
		case "second":
			return second, true
		default:
			return nil, false
		}
	})
	tool.sleeper = func(context.Context, time.Duration) error { return nil }

	file := "demo.jsonl"
	writeRunScriptTestFile(t, scriptsDir, file, strings.Join([]string{
		`{"type":"call","tool":"first","input":{}}`,
		`{"type":"call","tool":"second","input":{}}`,
	}, "\n"))

	out, err := tool.Call(context.Background(), `{"file":`+quoteJSON(file)+`}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	var result runScriptResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, out)
	}
	if result.OK || result.StepsRun != 1 || result.Error != toolErr.Message {
		t.Fatalf("result = %#v, output=%s", result, out)
	}
	if len(second.inputs) != 0 {
		t.Fatalf("second tool should not run after structured failure, inputs=%#v", second.inputs)
	}
}

func TestRunScriptAcceptsShortJSONLForms(t *testing.T) {
	calledTool := &stubTool{name: "screenshot", output: `{"ok":true}`}
	scriptsDir := t.TempDir()
	tool := NewRunScriptTool(scriptsDir, func(name string) (langtools.Tool, bool) {
		if name == calledTool.name {
			return calledTool, true
		}
		return nil, false
	})
	var waits []time.Duration
	tool.sleeper = func(_ context.Context, duration time.Duration) error {
		waits = append(waits, duration)
		return nil
	}
	spoken := make(chan string, 1)
	tool.SetSpeaker(func(_ context.Context, text string) error {
		spoken <- text
		return nil
	})
	file := "demo.jsonl"
	writeRunScriptTestFile(t, scriptsDir, file, strings.Join([]string{
		`{"wait":500}`,
		`{"tts":"短句"}`,
		`{"call":{"tool":"screenshot","input":{}}}`,
	}, "\n"))

	out, err := tool.Call(context.Background(), `{"file":`+quoteJSON(file)+`}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	var result runScriptResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, out)
	}
	if !result.OK || result.StepsRun != 3 {
		t.Fatalf("result = %#v, output=%s", result, out)
	}
	if len(waits) != 1 || waits[0] != 500*time.Millisecond {
		t.Fatalf("waits = %v, want [500ms]", waits)
	}
	select {
	case got := <-spoken:
		if got != "短句" {
			t.Fatalf("spoken text = %q, want 短句", got)
		}
	case <-time.After(time.Second):
		t.Fatal("tts speaker was not called")
	}
	if len(calledTool.inputs) != 1 || calledTool.inputs[0] != `{}` {
		t.Fatalf("tool inputs = %#v", calledTool.inputs)
	}
	if result.Steps[1].Text != "短句" || result.Steps[1].Output != "queued" {
		t.Fatalf("tts result = %#v, want text and queued output", result.Steps[1])
	}
}

func TestRunScriptPassesStringEncodedCallInputUnchanged(t *testing.T) {
	calledTool := &stubTool{name: "keyboard_text", output: "ok"}
	scriptsDir := t.TempDir()
	tool := NewRunScriptTool(scriptsDir, func(name string) (langtools.Tool, bool) {
		if name == calledTool.name {
			return calledTool, true
		}
		return nil, false
	})
	file := "demo.jsonl"
	writeRunScriptTestFile(t, scriptsDir, file, `{"call":{"tool":"keyboard_text","input":"{\"text\":\"demo\"}"}}`)

	out, err := tool.Call(context.Background(), `{"file":`+quoteJSON(file)+`}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	var result runScriptResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, out)
	}
	if !result.OK || result.StepsRun != 1 {
		t.Fatalf("result = %#v, output=%s", result, out)
	}
	if len(calledTool.inputs) != 1 || calledTool.inputs[0] != `{"text":"demo"}` {
		t.Fatalf("tool inputs = %#v, want preformatted JSON string unchanged", calledTool.inputs)
	}
}

func TestRunScriptTTSDoesNotBlockFollowingSteps(t *testing.T) {
	calledTool := &stubTool{name: "screenshot", output: `{"ok":true}`}
	scriptsDir := t.TempDir()
	tool := NewRunScriptTool(scriptsDir, func(name string) (langtools.Tool, bool) {
		if name == calledTool.name {
			return calledTool, true
		}
		return nil, false
	})
	started := make(chan string, 1)
	release := make(chan struct{})
	tool.SetSpeaker(func(_ context.Context, text string) error {
		started <- text
		<-release
		return nil
	})
	file := "demo.jsonl"
	writeRunScriptTestFile(t, scriptsDir, file, strings.Join([]string{
		`{"type":"tts","text":"异步播放"}`,
		`{"type":"call","tool":"screenshot","input":{}}`,
	}, "\n"))

	done := make(chan string, 1)
	go func() {
		out, err := tool.Call(context.Background(), `{"file":`+quoteJSON(file)+`}`)
		if err != nil {
			done <- "error: " + err.Error()
			return
		}
		done <- out
	}()

	var out string
	select {
	case out = <-done:
	case <-time.After(500 * time.Millisecond):
		close(release)
		t.Fatal("run_script blocked waiting for tts playback")
	}
	close(release)

	select {
	case got := <-started:
		if got != "异步播放" {
			t.Fatalf("speaker text = %q, want 异步播放", got)
		}
	case <-time.After(time.Second):
		t.Fatal("tts speaker was not started")
	}

	var result runScriptResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, out)
	}
	if !result.OK || result.StepsRun != 2 {
		t.Fatalf("result = %#v, output=%s", result, out)
	}
	if len(calledTool.inputs) != 1 {
		t.Fatalf("following call did not run, inputs=%#v", calledTool.inputs)
	}
	if len(result.Steps) < 1 || result.Steps[0].Output != "queued" {
		t.Fatalf("tts step output = %#v, want queued", result.Steps)
	}
}

func TestRunScriptScrubsScreenshotOutputPreview(t *testing.T) {
	calledTool := &stubTool{
		name:   "touch_gesture",
		output: `{"width":10,"height":20,"format":"jpeg","size":30,"data":"base64-image","action_output":"ok"}`,
	}
	scriptsDir := t.TempDir()
	tool := NewRunScriptTool(scriptsDir, func(name string) (langtools.Tool, bool) {
		if name == calledTool.name {
			return calledTool, true
		}
		return nil, false
	})
	file := "demo.jsonl"
	writeRunScriptTestFile(t, scriptsDir, file, `{"type":"call","tool":"touch_gesture","input":{"type":"tap","point":{"x":500,"y":500}}}`)

	out, err := tool.Call(context.Background(), `{"file":`+quoteJSON(file)+`}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if strings.Contains(out, "base64-image") || strings.Contains(out, `"data"`) {
		t.Fatalf("run_script output should scrub screenshot data: %s", out)
	}
	var result runScriptResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, out)
	}
	if len(result.Steps) != 1 || !strings.Contains(result.Steps[0].Output, `"action_output":"ok"`) {
		t.Fatalf("run_script output should keep compact action output: %#v", result.Steps)
	}
}

func TestRunScriptRejectsSelfCall(t *testing.T) {
	var tool *RunScriptTool
	scriptsDir := t.TempDir()
	tool = NewRunScriptTool(scriptsDir, func(name string) (langtools.Tool, bool) {
		if name == "run_script" {
			return tool, true
		}
		return nil, false
	})
	file := "demo.jsonl"
	writeRunScriptTestFile(t, scriptsDir, file, `{"type":"call","tool":"run_script","input":{}}`)

	out, err := tool.Call(context.Background(), `{"file":`+quoteJSON(file)+`}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if !strings.Contains(out, "cannot call itself") {
		t.Fatalf("output = %s, want self-call rejection", out)
	}
}

func TestRunScriptRejectsLongWaitWithoutSleeping(t *testing.T) {
	tool := NewRunScriptTool(t.TempDir(), func(string) (langtools.Tool, bool) { return nil, false })
	var slept bool
	tool.sleeper = func(context.Context, time.Duration) error {
		slept = true
		return nil
	}

	result := tool.executeStep(context.Background(), runScriptStep{
		Line: 1,
		Type: "wait",
		Wait: 31 * time.Second,
	})

	if result.OK || !strings.Contains(result.Error, "wait duration must be <=") {
		t.Fatalf("result = %#v, want max wait rejection", result)
	}
	if slept {
		t.Fatal("sleeper was called for over-limit wait")
	}
}

func TestRunScriptRejectsPathLikeFileName(t *testing.T) {
	tool := NewRunScriptTool(t.TempDir(), func(string) (langtools.Tool, bool) { return nil, false })
	for _, file := range []string{"../demo.jsonl", "nested/demo.jsonl", "/tmp/demo.jsonl", `nested\demo.jsonl`} {
		out, err := tool.Call(context.Background(), `{"file":`+quoteJSON(file)+`}`)
		if err != nil {
			t.Fatalf("Call error for %q: %v", file, err)
		}
		if !strings.Contains(out, "script file must be a file name under scripts/") {
			t.Fatalf("output for %q = %s, want file-name rejection", file, out)
		}
	}
}

func TestRunScriptToolSetUsesConfigScriptsDir(t *testing.T) {
	configDir := t.TempDir()
	scriptsDir := filepath.Join(configDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeRunScriptTestFile(t, scriptsDir, "wait.jsonl", `{"type":"wait","ms":1}`)

	tools := NewBuiltinToolSetFromConfig(Config{Model: ModelConfig{Provider: "fake"}, ConfigDir: configDir}, ProxyConfig{}, nil)
	tool, ok := tools.Get("run_script")
	if !ok {
		t.Fatal("run_script tool missing")
	}
	out, err := tool.Call(context.Background(), `{"file":"wait.jsonl"}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if !strings.Contains(out, `"ok":true`) || !strings.Contains(out, `"type":"wait"`) {
		t.Fatalf("output = %s, want wait result from config scripts dir", out)
	}
}

func TestRunScriptToolSetRejectsNonScriptCallableTools(t *testing.T) {
	scriptsDir := t.TempDir()
	writeRunScriptTestFile(t, scriptsDir, "demo.jsonl", `{"type":"call","tool":"shell","input":{"command":"echo denied"}}`)
	tools := NewBuiltinToolSet(
		HIDConfig{},
		AudioConfig{},
		SearchConfig{},
		ProxyConfig{},
		WithRunScriptScriptsDir(scriptsDir),
	)
	tool, ok := tools.Get("run_script")
	if !ok {
		t.Fatal("run_script tool missing")
	}
	out, err := tool.Call(context.Background(), `{"file":"demo.jsonl"}`)
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	var result runScriptResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid result JSON: %v\n%s", err, out)
	}
	if result.OK || !strings.Contains(result.Error, `tool "shell" is not available`) {
		t.Fatalf("result = %#v, output=%s", result, out)
	}
}

func writeRunScriptTestFile(t *testing.T, dir, file, content string) string {
	t.Helper()
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func quoteJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
