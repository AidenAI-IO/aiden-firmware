package agent

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func newTestIOSKeyboardIsolationController(events *[]string) *iosKeyboardIsolationController {
	return &iosKeyboardIsolationController{
		controlPath: "/test/control",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			*events = append(*events, args[0])
			return nil, nil
		},
	}
}

func TestIOSKeyboardIsolationWrapsModifierShortcut(t *testing.T) {
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)

	err := controller.withKeyboard(context.Background(), true, func() error {
		events = append(events, "shortcut")
		return nil
	})
	if err != nil {
		t.Fatalf("withKeyboard() error = %v", err)
	}
	if want := []string{"isolate", "shortcut", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestIOSKeyboardIsolationBatchCoalescesModifierActions(t *testing.T) {
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)

	err := controller.withBatch(context.Background(), func(batchCtx context.Context) error {
		if err := controller.withKeyboard(batchCtx, true, func() error {
			events = append(events, "first-shortcut")
			return nil
		}); err != nil {
			return err
		}
		if err := controller.withKeyboard(batchCtx, false, func() error {
			events = append(events, "plain-key")
			return nil
		}); err != nil {
			return err
		}
		return controller.withKeyboard(batchCtx, true, func() error {
			events = append(events, "second-shortcut")
			return nil
		})
	})
	if err != nil {
		t.Fatalf("withBatch() error = %v", err)
	}
	if want := []string{"isolate", "first-shortcut", "plain-key", "second-shortcut", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestIOSKeyboardIsolationNestedBatchReusesOuterProfile(t *testing.T) {
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)

	err := controller.withBatch(context.Background(), func(batchCtx context.Context) error {
		return controller.withBatch(batchCtx, func(nestedCtx context.Context) error {
			return controller.withKeyboard(nestedCtx, true, func() error {
				events = append(events, "shortcut")
				return nil
			})
		})
	})
	if err != nil {
		t.Fatalf("withBatch() error = %v", err)
	}
	if want := []string{"isolate", "shortcut", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestIOSKeyboardIsolationBatchRestoresBeforePointerAndCanReisolate(t *testing.T) {
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)

	err := controller.withBatch(context.Background(), func(batchCtx context.Context) error {
		if err := controller.withKeyboard(batchCtx, true, func() error {
			events = append(events, "first-shortcut")
			return nil
		}); err != nil {
			return err
		}
		if _, err := controller.withPointerCall(batchCtx, func(context.Context) (string, error) {
			events = append(events, "pointer")
			return "ok", nil
		}); err != nil {
			return err
		}
		return controller.withKeyboard(batchCtx, true, func() error {
			events = append(events, "second-shortcut")
			return nil
		})
	})
	if err != nil {
		t.Fatalf("withBatch() error = %v", err)
	}
	if want := []string{"isolate", "first-shortcut", "restore", "pointer", "isolate", "second-shortcut", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestIOSKeyboardIsolationLeavesPlainKeyboardInputOnNormalProfile(t *testing.T) {
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)

	err := controller.withKeyboard(context.Background(), false, func() error {
		events = append(events, "plain-input")
		return nil
	})
	if err != nil {
		t.Fatalf("withKeyboard() error = %v", err)
	}
	if want := []string{"plain-input"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestIOSKeyboardIsolationRestoresNormalProfileAfterActionFailure(t *testing.T) {
	events := []string{}
	actionErr := errors.New("write failed")
	controller := newTestIOSKeyboardIsolationController(&events)

	err := controller.withKeyboard(context.Background(), true, func() error {
		events = append(events, "shortcut")
		return actionErr
	})
	if !errors.Is(err, actionErr) {
		t.Fatalf("withKeyboard() error = %v, want %v", err, actionErr)
	}
	if want := []string{"isolate", "shortcut", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestIOSKeyboardIsolationAttemptsRestoreWhenIsolationFails(t *testing.T) {
	events := []string{}
	isolateErr := errors.New("enumeration failed")
	actionCalled := false
	controller := &iosKeyboardIsolationController{
		controlPath: "/test/control",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			if args[0] == "isolate" {
				return []byte("UDC timeout"), isolateErr
			}
			return nil, nil
		},
	}

	err := controller.withKeyboard(context.Background(), true, func() error {
		actionCalled = true
		return nil
	})
	if !errors.Is(err, isolateErr) || !strings.Contains(err.Error(), "UDC timeout") {
		t.Fatalf("withKeyboard() error = %v, want wrapped isolation error", err)
	}
	if actionCalled {
		t.Fatal("shortcut ran after isolation failed")
	}
	if want := []string{"isolate", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestIOSKeyboardIsolationCooldownsFailedRestoreBeforePointerInput(t *testing.T) {
	events := []string{}
	restoreCalls := 0
	pointerCalled := false
	controller := &iosKeyboardIsolationController{
		controlPath: "/test/control",
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			events = append(events, args[0])
			if args[0] == "restore" {
				restoreCalls++
				if restoreCalls == 1 {
					return nil, errors.New("restore failed")
				}
			}
			return nil, nil
		},
	}

	if err := controller.withKeyboard(context.Background(), true, func() error { return nil }); err == nil {
		t.Fatal("withKeyboard() error = nil, want restore failure")
	}
	if _, err := controller.withPointerCall(context.Background(), func(context.Context) (string, error) {
		pointerCalled = true
		return "ok", nil
	}); err == nil || !strings.Contains(err.Error(), "restore failed") {
		t.Fatalf("withPointerCall() error = %v, want cached restore failure", err)
	}
	if pointerCalled {
		t.Fatal("pointer action ran during restore retry cooldown")
	}
	if want := []string{"isolate", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}

	controller.lastRestoreFailure = time.Now().Add(-iosKeyboardRestoreRetryCooldown)
	if _, err := controller.withPointerCall(context.Background(), func(context.Context) (string, error) {
		pointerCalled = true
		return "ok", nil
	}); err != nil {
		t.Fatalf("withPointerCall() after cooldown error = %v", err)
	}
	if !pointerCalled {
		t.Fatal("pointer action did not run after successful restore retry")
	}
	if want := []string{"isolate", "restore", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events after cooldown = %v, want %v", events, want)
	}
}

func TestKeyboardTapIsolatesOnlyModifierChords(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)
	controller.keyboardDev = dev
	tool := &KeyboardTapTool{dev: dev, pointerMode: "absolute", iosKeyboardIsolation: controller}

	if out, err := tool.Call(context.Background(), `{"keys":["a"],"hold_ms":1}`); err != nil || out != "ok" {
		t.Fatalf("plain Call() = %q, %v", out, err)
	}
	if len(events) != 0 {
		t.Fatalf("plain key profile events = %v, want none", events)
	}

	if out, err := tool.Call(context.Background(), `{"keys":["meta","v"],"hold_ms":1}`); err != nil || out != "ok" {
		t.Fatalf("modifier Call() = %q, %v", out, err)
	}
	if want := []string{"isolate", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("modifier profile events = %v, want %v", events, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if len(data) != 16 {
		t.Fatalf("final report bytes = %d, want 16", len(data))
	}
}

func TestKeyboardTextLeavesUnmodifiedTextOnNormalProfile(t *testing.T) {
	dev, _ := newTestHIDDevice(t)
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)
	controller.keyboardDev = dev
	tool := &KeyboardTextTool{dev: dev, iosKeyboardIsolation: controller}

	out, err := tool.Call(context.Background(), `{"text":"abc123-"}`)
	if err != nil || out != "ok" {
		t.Fatalf("Call() = %q, %v", out, err)
	}
	if len(events) != 0 {
		t.Fatalf("keyboard_text profile events = %v, want none", events)
	}
}

func TestKeyboardTextIsolatesTextContainingShiftedCharacters(t *testing.T) {
	dev, path := newTestHIDDevice(t)
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)
	controller.keyboardDev = dev
	tool := &KeyboardTextTool{dev: dev, iosKeyboardIsolation: controller}

	const text = "A~!@#$%^&*()_+{}|:\"<>?"
	out, err := tool.Call(context.Background(), `{"text":"A~!@#$%^&*()_+{}|:\"<>?"}`)
	if err != nil || out != "ok" {
		t.Fatalf("Call() = %q, %v", out, err)
	}
	if want := []string{"isolate", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("keyboard_text profile events = %v, want %v", events, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if want := len(text) * 16; len(data) != want {
		t.Fatalf("keyboard_text report bytes = %d, want %d", len(data), want)
	}
	for offset := 0; offset < len(data); offset += 16 {
		if data[offset] != 0x02 {
			t.Fatalf("keyboard_text press report at offset %d has modifier 0x%02x, want Shift", offset, data[offset])
		}
	}
}

func TestKeyboardTapExtraKeysRemainOnNormalProfile(t *testing.T) {
	keyboardDev, keyboardPath := newTestHIDDevice(t)
	extraKeysDev, extraKeysPath := newTestHIDDevice(t)
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)
	controller.keyboardDev = keyboardDev
	controller.extraKeysDev = extraKeysDev
	tool := &KeyboardTapTool{
		dev:                  keyboardDev,
		androidDev:           extraKeysDev,
		pointerMode:          "absolute",
		iosKeyboardIsolation: controller,
	}

	out, err := tool.Call(context.Background(), `{"keys":["volume_up"],"hold_ms":1}`)
	if err != nil || out != "ok" {
		t.Fatalf("Call() = %q, %v", out, err)
	}
	if len(events) != 0 {
		t.Fatalf("extra-key profile events = %v, want none", events)
	}
	if data, err := os.ReadFile(keyboardPath); err != nil {
		t.Fatalf("ReadFile(keyboard) error = %v", err)
	} else if len(data) != 0 {
		t.Fatalf("standard keyboard report bytes = %d, want 0", len(data))
	}
	if data, err := os.ReadFile(extraKeysPath); err != nil {
		t.Fatalf("ReadFile(extra keys) error = %v", err)
	} else if len(data) != 4 {
		t.Fatalf("extra-key report bytes = %d, want 4", len(data))
	}
}

func TestIOSKeyboardIsolationRestoresAfterCancellation(t *testing.T) {
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)
	ctx, cancel := context.WithCancel(context.Background())

	err := controller.withKeyboard(ctx, true, func() error {
		events = append(events, "shortcut")
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("withKeyboard() error = %v, want context canceled", err)
	}
	if want := []string{"isolate", "shortcut", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestIOSKeyboardIsolationBatchRestoresAfterCancellation(t *testing.T) {
	events := []string{}
	controller := newTestIOSKeyboardIsolationController(&events)
	ctx, cancel := context.WithCancel(context.Background())

	err := controller.withBatch(ctx, func(batchCtx context.Context) error {
		if err := controller.withKeyboard(batchCtx, true, func() error {
			events = append(events, "shortcut")
			cancel()
			return nil
		}); err != nil {
			return err
		}
		return batchCtx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("withBatch() error = %v, want context canceled", err)
	}
	if want := []string{"isolate", "shortcut", "restore"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}
