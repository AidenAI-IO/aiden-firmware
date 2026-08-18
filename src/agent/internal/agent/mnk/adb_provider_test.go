package mnk

import (
	"context"
	"strings"
	"testing"
)

type adbTestRunner struct {
	commands [][]string
	sdk      string
}

func (r *adbTestRunner) run(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
	r.commands = append(r.commands, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	switch {
	case strings.HasSuffix(joined, "shell getprop ro.build.version.sdk"):
		return []byte(r.sdk), nil, nil
	case strings.HasSuffix(joined, "shell wm size"):
		return []byte("Physical size: 1000x1000"), nil, nil
	default:
		return nil, nil, nil
	}
}

func newTestADBProvider(t *testing.T, runner *adbTestRunner) *ADBProvider {
	t.Helper()
	t.Setenv("AIDEN_ADB_PATH", "/fake/adb")
	t.Setenv("AIDEN_ADB_SERIAL", "serial-test")
	return NewADBProvider(nil, nil, runner.run)
}

func TestADBProviderKeypressGatesKeyCombinationDuration(t *testing.T) {
	for _, test := range []struct {
		name string
		sdk  string
		want []string
		err  ErrorKind
	}{
		{name: "android11", sdk: "30", want: []string{"-s", "serial-test", "shell", "input", "keycombination", "KEYCODE_CTRL_LEFT", "KEYCODE_A"}},
		{name: "android12", sdk: "31", want: []string{"-s", "serial-test", "shell", "input", "keycombination", "-t", "50", "KEYCODE_CTRL_LEFT", "KEYCODE_A"}},
		{name: "unsupported", sdk: "29", err: ErrModuleUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &adbTestRunner{sdk: test.sdk}
			provider := newTestADBProvider(t, runner)
			err := provider.Keypress(context.Background(), []string{"ctrl", "a"})
			if test.err != "" {
				if got := AsError(err); got == nil || got.Kind != test.err {
					t.Fatalf("Keypress() error = %v, want %s", err, test.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Keypress() error = %v", err)
			}
			if len(runner.commands) != 2 {
				t.Fatalf("commands = %#v, want API query and key combination", runner.commands)
			}
			got := runner.commands[1]
			if !stringSliceEqual(got, test.want) {
				t.Fatalf("key combination = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestADBKeyboardAliasesUseKeyboardKeys(t *testing.T) {
	provider := &ADBProvider{}
	for key, want := range map[string]string{
		"esc":    "KEYCODE_ESCAPE",
		"escape": "KEYCODE_ESCAPE",
		"home":   "KEYCODE_MOVE_HOME",
	} {
		got, err := provider.resolveKeycode(key)
		if err != nil {
			t.Fatalf("resolveKeycode(%q): %v", key, err)
		}
		if got != want {
			t.Errorf("resolveKeycode(%q) = %q, want %q", key, got, want)
		}
	}
	if got, err := provider.resolveKeycode("android_home"); err != nil || got != "KEYCODE_HOME" {
		t.Errorf("resolveKeycode(android_home) = %q, %v; want KEYCODE_HOME", got, err)
	}
}

func TestADBScreenClientRejectsAmbiguousDevices(t *testing.T) {
	runner := &adbTestRunner{}
	client := NewADBScreenClient()
	provider := NewADBProvider(nil, client, func(ctx context.Context, path string, args ...string) ([]byte, []byte, error) {
		if len(args) == 1 && args[0] == "devices" {
			return []byte("List of devices attached\nfirst\tdevice\nsecond\tdevice\n"), nil, nil
		}
		return runner.run(ctx, path, args...)
	})
	t.Setenv("AIDEN_ADB_SERIAL", "")
	t.Setenv("ANDROID_SERIAL", "")
	if _, err := client.ResolveSerial(context.Background(), "/fake/adb"); err == nil || !strings.Contains(err.Error(), "multiple adb devices") {
		t.Fatalf("ResolveSerial() error = %v, want multiple-device error", err)
	}
	_ = provider
}

func TestADBProviderScrollHorizontalDirection(t *testing.T) {
	for _, test := range []struct {
		name       string
		delta      int
		wantStartX string
		wantEndX   string
	}{
		{name: "left", delta: -1, wantStartX: "400", wantEndX: "599"},
		{name: "right", delta: 1, wantStartX: "599", wantEndX: "400"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &adbTestRunner{sdk: "31"}
			provider := newTestADBProvider(t, runner)
			if err := provider.Scroll(context.Background(), test.delta, 0); err != nil {
				t.Fatalf("Scroll() error = %v", err)
			}
			if len(runner.commands) != 2 {
				t.Fatalf("commands = %#v, want wm size and swipe", runner.commands)
			}
			got := runner.commands[1]
			if len(got) < 10 || got[len(got)-6] != "swipe" {
				t.Fatalf("swipe command = %#v", got)
			}
			if got[len(got)-5] != test.wantStartX || got[len(got)-4] != "500" || got[len(got)-3] != test.wantEndX || got[len(got)-2] != "500" {
				t.Fatalf("swipe command = %#v, want horizontal direction", got)
			}
		})
	}
}

func TestADBProviderSeparatesSwipeAndDragDuration(t *testing.T) {
	for _, test := range []struct {
		name         string
		perform      func(context.Context, *ADBProvider) error
		wantDuration string
	}{
		{
			name: "swipe",
			perform: func(ctx context.Context, provider *ADBProvider) error {
				return provider.Swipe(ctx, [][2]float64{{700, 500}, {300, 500}}, ButtonLeft)
			},
			wantDuration: "300",
		},
		{
			name: "drag",
			perform: func(ctx context.Context, provider *ADBProvider) error {
				return provider.Drag(ctx, [][2]float64{{700, 500}, {300, 500}}, ButtonLeft)
			},
			wantDuration: "700",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &adbTestRunner{sdk: "31"}
			provider := newTestADBProvider(t, runner)
			if err := test.perform(context.Background(), provider); err != nil {
				t.Fatalf("gesture failed: %v", err)
			}
			if len(runner.commands) != 2 {
				t.Fatalf("commands = %#v, want wm size and swipe", runner.commands)
			}
			command := runner.commands[1]
			if got := command[len(command)-1]; got != test.wantDuration {
				t.Fatalf("duration = %s, want %s; command = %#v", got, test.wantDuration, command)
			}
		})
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
