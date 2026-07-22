package agent

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	langtools "github.com/tmc/langchaingo/tools"
)

func TestDynamicKeyboardWrapsOneAction(t *testing.T) {
	events := []string{}
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		run: func(_ context.Context, path string, args ...string) ([]byte, error) {
			events = append(events, path+":"+args[0])
			return nil, nil
		},
	}

	err := controller.withKeyboard(context.Background(), func() error {
		events = append(events, "action")
		return nil
	})
	if err != nil {
		t.Fatalf("withKeyboard() error = %v", err)
	}
	want := []string{"/test/control:on", "action", "/test/control:off"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestDynamicKeyboardSessionReusesOneEnumeration(t *testing.T) {
	events := []string{}
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			return nil, nil
		},
	}

	_, err := controller.withSessionCall(context.Background(), func(ctx context.Context) (string, error) {
		if err := controller.withKeyboard(ctx, func() error {
			events = append(events, "first")
			return nil
		}); err != nil {
			return "", err
		}
		if err := controller.withKeyboard(ctx, func() error {
			events = append(events, "second")
			return nil
		}); err != nil {
			return "", err
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("withSessionCall() error = %v", err)
	}
	if want := []string{"on", "first", "second", "off"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestDynamicKeyboardSessionSwitchesBetweenKeyboardAndPointerProfiles(t *testing.T) {
	events := []string{}
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			return nil, nil
		},
	}

	_, err := controller.withSessionCall(context.Background(), func(ctx context.Context) (string, error) {
		if err := controller.withKeyboard(ctx, func() error {
			events = append(events, "keyboard-1")
			return nil
		}); err != nil {
			return "", err
		}
		if _, err := controller.withPointerCall(ctx, func(context.Context) (string, error) {
			events = append(events, "pointer")
			return "ok", nil
		}); err != nil {
			return "", err
		}
		if err := controller.withKeyboard(ctx, func() error {
			events = append(events, "keyboard-2")
			return nil
		}); err != nil {
			return "", err
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("withSessionCall() error = %v", err)
	}
	want := []string{"on", "keyboard-1", "off", "pointer", "on", "keyboard-2", "off"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestDynamicKeyboardSessionRetriesPointerRestoreAfterSwitchFailure(t *testing.T) {
	events := []string{}
	offCalls := 0
	pointerCalled := false
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			if args[0] == "off" {
				offCalls++
				if offCalls == 1 {
					return nil, errors.New("pointer enumeration failed")
				}
			}
			return nil, nil
		},
	}

	_, err := controller.withSessionCall(context.Background(), func(ctx context.Context) (string, error) {
		if err := controller.withKeyboard(ctx, func() error { return nil }); err != nil {
			return "", err
		}
		return controller.withPointerCall(ctx, func(context.Context) (string, error) {
			pointerCalled = true
			return "ok", nil
		})
	})
	if err == nil || !strings.Contains(err.Error(), "pointer enumeration failed") {
		t.Fatalf("withSessionCall() error = %v, want pointer switch failure", err)
	}
	if pointerCalled {
		t.Fatal("pointer action ran after its profile switch failed")
	}
	if want := []string{"on", "off", "off"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if controller.mode != dynamicHIDModePointer {
		t.Fatalf("controller mode = %q, want pointer after deferred recovery", controller.mode)
	}
}

func TestDynamicKeyboardSessionReattachesWhenDeviceDisappears(t *testing.T) {
	events := []string{}
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			return nil, nil
		},
	}
	actionCalls := 0

	_, err := controller.withSessionCall(context.Background(), func(ctx context.Context) (string, error) {
		err := controller.withKeyboard(ctx, func() error {
			actionCalls++
			events = append(events, "action")
			if actionCalls == 1 {
				return os.ErrNotExist
			}
			return nil
		})
		return "", err
	})
	if err != nil {
		t.Fatalf("withSessionCall() error = %v", err)
	}
	if want := []string{"on", "action", "on", "action", "off"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestDynamicKeyboardSessionIsLazyWhenNoKeyboardActionRuns(t *testing.T) {
	events := []string{}
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			return nil, nil
		},
	}

	_, err := controller.withSessionCall(context.Background(), func(context.Context) (string, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("withSessionCall() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %v, want none", events)
	}
}

func TestDynamicKeyboardCachesRestoredPointerProfileAcrossCalls(t *testing.T) {
	events := []string{}
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			return nil, nil
		},
	}

	for range 2 {
		if _, err := controller.withPointerCall(context.Background(), func(context.Context) (string, error) {
			events = append(events, "pointer")
			return "ok", nil
		}); err != nil {
			t.Fatalf("withPointerCall() error = %v", err)
		}
	}

	if want := []string{"off", "pointer", "pointer"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestEnterTextInFieldSharesOneDynamicKeyboardSessionAcrossSubtools(t *testing.T) {
	events := []string{}
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			return nil, nil
		},
	}
	keyboardText := &dynamicKeyboardActionStub{name: "keyboard_text", controller: controller, events: &events}
	keyboardTap := &dynamicKeyboardActionStub{name: "keyboard_tap", controller: controller, events: &events}
	vision := &stubTextInputVision{analyses: []textInputScreenAnalysis{{FieldText: "hello test"}}}
	engine := newFastTextInputEngine(textInputHardwareDeps{
		mouseClick:      textInputStubTool{name: "mouse_click", out: "ok"},
		keyboardTap:     keyboardTap,
		keyboardText:    keyboardText,
		quickAction:     textInputStubTool{name: "quick_action", out: "ok"},
		screenshot:      textInputStubTool{name: "screenshot", out: `{"format":"jpeg","width":100,"height":100,"data":"abc"}`},
		dynamicKeyboard: controller,
	}, vision)
	tool := &EnterTextInFieldTool{engine: engine}
	wrapped := newDynamicKeyboardSessionTool(tool, controller)

	out, err := wrapped.Call(context.Background(), `{"text":"hello test","focus":{"x":500,"y":100}}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !strings.Contains(out, `"committed": true`) {
		t.Fatalf("Call() output = %s, want committed", out)
	}
	want := []string{"on", "keyboard_text", "keyboard_tap", "keyboard_text", "off"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

type dynamicKeyboardActionStub struct {
	name       string
	controller *dynamicKeyboardController
	events     *[]string
}

func (t *dynamicKeyboardActionStub) Name() string        { return t.name }
func (t *dynamicKeyboardActionStub) Description() string { return t.name }
func (t *dynamicKeyboardActionStub) Call(ctx context.Context, _ string) (string, error) {
	err := t.controller.withKeyboard(ctx, func() error {
		*t.events = append(*t.events, t.name)
		return nil
	})
	return "ok", err
}

func TestDynamicKeyboardTurnsOffAfterActionFailure(t *testing.T) {
	actionErr := errors.New("write failed")
	events := []string{}
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			return nil, nil
		},
	}

	err := controller.withKeyboard(context.Background(), func() error {
		return actionErr
	})
	if !errors.Is(err, actionErr) {
		t.Fatalf("withKeyboard() error = %v, want %v", err, actionErr)
	}
	if want := []string{"on", "off"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestDynamicKeyboardDoesNotRunActionWhenEnableFails(t *testing.T) {
	onErr := errors.New("enumeration failed")
	actionCalled := false
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[0] == "on" {
				return []byte("UDC timeout"), onErr
			}
			return nil, nil
		},
	}

	err := controller.withKeyboard(context.Background(), func() error {
		actionCalled = true
		return nil
	})
	if !errors.Is(err, onErr) {
		t.Fatalf("withKeyboard() error = %v, want wrapped %v", err, onErr)
	}
	if actionCalled {
		t.Fatal("action ran after keyboard enable failure")
	}
}

func TestDynamicKeyboardDisabledRunsActionDirectly(t *testing.T) {
	called := false
	var controller *dynamicKeyboardController
	if err := controller.withKeyboard(context.Background(), func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("withKeyboard() error = %v", err)
	}
	if !called {
		t.Fatal("disabled controller did not run action")
	}
}

func TestKeyboardTextUsesOneDynamicKeyboardSession(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	events := []string{}
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		keyboardDev: dev,
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			return nil, nil
		},
	}
	tool := &KeyboardTextTool{dev: dev, dynamicKeyboard: controller}

	out, err := tool.Call(context.Background(), `{"text":"ABC"}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call() output = %q, want ok", out)
	}
	if want := []string{"on", "off"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if len(data) != 3*16 {
		t.Fatalf("report bytes = %d, want %d", len(data), 3*16)
	}
}

func TestKeyboardTapUsesOneDynamicKeyboardSession(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	events := []string{}
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		keyboardDev: dev,
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			return nil, nil
		},
	}
	tool := &KeyboardTapTool{dev: dev, pointerMode: "absolute", dynamicKeyboard: controller}

	out, err := tool.Call(context.Background(), `{"keys":["ctrl","a"],"hold_ms":1}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call() output = %q, want ok", out)
	}
	if want := []string{"on", "off"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if len(data) != 16 {
		t.Fatalf("report bytes = %d, want 16", len(data))
	}
}

func TestKeyboardTapAndroidExtensionDoesNotAttachDynamicKeyboard(t *testing.T) {
	dev, keyboardPath := newTestHIDDevice(t)
	androidDev, androidPath := newTestHIDDevice(t)
	events := []string{}
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		keyboardDev: dev,
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			return nil, nil
		},
	}
	tool := &KeyboardTapTool{
		dev:             dev,
		androidDev:      androidDev,
		pointerMode:     "touchscreen",
		dynamicKeyboard: controller,
	}

	out, err := tool.Call(context.Background(), `{"keys":["KEYCODE_BACK"],"hold_ms":1}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if out != "ok" {
		t.Fatalf("Call() output = %q, want ok", out)
	}
	if len(events) != 0 {
		t.Fatalf("dynamic keyboard events = %v, want none", events)
	}
	dev.Close()
	androidDev.Close()
	if data, err := os.ReadFile(keyboardPath); err != nil {
		t.Fatalf("ReadFile(keyboard) error = %v", err)
	} else if len(data) != 0 {
		t.Fatalf("standard keyboard report bytes = %d, want 0", len(data))
	}
	if data, err := os.ReadFile(androidPath); err != nil {
		t.Fatalf("ReadFile(android) error = %v", err)
	} else if len(data) != 4 {
		t.Fatalf("Android extension report bytes = %d, want 4", len(data))
	}
}

func TestRuntimeRestoresPointerBetweenDynamicKeyboardToolCalls(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	events := []string{}
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		keyboardDev: dev,
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			return nil, nil
		},
	}
	keyboardText := &KeyboardTextTool{dev: dev, dynamicKeyboard: controller}
	toolSet := &ToolSet{
		tools: map[string]langtools.Tool{
			"keyboard_text": keyboardText,
		},
		textInputHW: &textInputHardwareDeps{dynamicKeyboard: controller},
	}
	responses := roleToolResponses("keyboard_text", `{"text":"A"}`, "unused")[:1]
	responses = append(responses,
		toolCallResponse("call_2", "keyboard_text", `{"text":"B"}`),
		contentResponse("done"),
	)
	runtime := NewRuntimeWithDeps(
		withTestConfigDir(t, Config{
			Model:       ModelConfig{Provider: "fake"},
			Instruction: "Use the requested tools.",
			HID: HIDConfig{
				DynamicKeyboard: true,
				PointerMode:     "absolute",
				InputBackend:    "hid",
			},
		}),
		&testModelResolver{model: &scriptedModel{responses: responses}},
		NewMemoryManager(""),
		toolSet,
		NewSkillIndex(),
	)

	result, err := runtime.Run(context.Background(), RunRequest{Input: "type two characters"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("Run() output = %q, want done", result.Output)
	}
	if want := []string{"on", "off", "on", "off"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("dynamic keyboard events = %v, want %v", events, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	// Each profile restore closes the test fd; its synthetic opener does not use
	// O_APPEND, so the second action overwrites the first action's two reports.
	if len(data) != 16 {
		t.Fatalf("report bytes = %d, want 16 from the final keyboard action", len(data))
	}
}

func TestDynamicKeyboardSessionRestoresPointerOnCancellation(t *testing.T) {
	events := []string{}
	controller := &dynamicKeyboardController{
		controlPath: "/test/control",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			return nil, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())

	_, err := controller.withSessionCall(ctx, func(runCtx context.Context) (string, error) {
		if err := controller.withKeyboard(runCtx, func() error {
			events = append(events, "action")
			return nil
		}); err != nil {
			return "", err
		}
		cancel()
		<-runCtx.Done()
		return "", runCtx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("withSessionCall() error = %v, want context canceled", err)
	}
	if want := []string{"on", "action", "off"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}
