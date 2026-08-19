package mnk

import (
	"context"
	"sync"
	"testing"
)

type recordingGate struct {
	mu       sync.Mutex
	events   []string
	isolated bool
}

func (g *recordingGate) WithKeyboard(ctx context.Context, isolate bool, fn func() error) error {
	_ = ctx
	g.mu.Lock()
	if isolate {
		if !g.isolated {
			g.events = append(g.events, "isolate")
			g.isolated = true
		} else {
			g.events = append(g.events, "keyboard_sticky")
		}
	} else {
		g.events = append(g.events, "keyboard_plain")
	}
	g.mu.Unlock()
	return fn()
}

func (g *recordingGate) WithExtraKeys(ctx context.Context, fn func() error) error {
	_ = ctx
	g.mu.Lock()
	g.events = append(g.events, "extra_keys")
	g.mu.Unlock()
	return fn()
}

func (g *recordingGate) WithPointer(ctx context.Context, fn func() error) error {
	_ = ctx
	g.mu.Lock()
	if g.isolated {
		g.events = append(g.events, "restore")
		g.isolated = false
	} else {
		g.events = append(g.events, "pointer")
	}
	g.mu.Unlock()
	return fn()
}

func (g *recordingGate) snapshot() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, len(g.events))
	copy(out, g.events)
	return out
}

type nopDevice struct{}

func (nopDevice) Write([]byte) error { return nil }
func (nopDevice) Close()             {}

func TestHIDProviderProfileGateStickyIsolateThenPointerRestore(t *testing.T) {
	gate := &recordingGate{}
	provider := NewHIDProvider(nopDevice{}, nopDevice{}, nil, nil, false, "qwerty", gate)

	ctx := context.Background()
	if err := provider.Keypress(ctx, []string{"ctrl", "a"}); err != nil {
		t.Fatalf("first Keypress: %v", err)
	}
	if err := provider.Keypress(ctx, []string{"ctrl", "c"}); err != nil {
		t.Fatalf("second Keypress: %v", err)
	}
	if got := gate.snapshot(); len(got) != 2 || got[0] != "isolate" || got[1] != "keyboard_sticky" {
		t.Fatalf("after modifier keypresses events = %v, want [isolate keyboard_sticky]", got)
	}

	if err := provider.Click(ctx, 500, 500, "left", 0); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if got := gate.snapshot(); len(got) != 3 || got[2] != "restore" {
		t.Fatalf("after Click events = %v, want … restore", got)
	}
}

func TestHIDProviderProfileGatePlainKeyDoesNotIsolate(t *testing.T) {
	gate := &recordingGate{}
	provider := NewHIDProvider(nopDevice{}, nopDevice{}, nil, nil, false, "qwerty", gate)

	if err := provider.Keypress(context.Background(), []string{"enter"}); err != nil {
		t.Fatalf("Keypress: %v", err)
	}
	if got := gate.snapshot(); len(got) != 1 || got[0] != "keyboard_plain" {
		t.Fatalf("events = %v, want [keyboard_plain]", got)
	}
}

func TestHIDProviderNilGatePassthrough(t *testing.T) {
	provider := NewHIDProvider(nopDevice{}, nopDevice{}, nil, nil, false, "qwerty", nil)
	if err := provider.Keypress(context.Background(), []string{"ctrl", "a"}); err != nil {
		t.Fatalf("Keypress with nil gate: %v", err)
	}
	if err := provider.Move(context.Background(), 10, 10); err != nil {
		t.Fatalf("Move with nil gate: %v", err)
	}
}
