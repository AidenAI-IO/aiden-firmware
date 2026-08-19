package mnk

import (
	"bytes"
	"context"
	"sync"
	"testing"
)

type layoutCaptureDevice struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (d *layoutCaptureDevice) Write(b []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.buf.Write(b)
	return err
}

func (d *layoutCaptureDevice) Close() {}

func (d *layoutCaptureDevice) bytes() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]byte(nil), d.buf.Bytes()...)
}

func TestHIDProviderKeypressUsesAZERTYLayout(t *testing.T) {
	keyboard := &layoutCaptureDevice{}
	provider := NewHIDProvider(nil, keyboard, nil, nil, false, "azerty", nil)

	if err := provider.Keypress(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("Keypress: %v", err)
	}
	got := keyboard.bytes()
	if len(got) < 8 {
		t.Fatalf("wrote %d bytes, want at least 8", len(got))
	}
	if got[0] != 0 || got[2] != 0x14 {
		t.Fatalf("press report = %v, want modifier 0 and AZERTY a usage 0x14", got[:8])
	}
}

func TestHIDProviderKeypressUsesQWERTYLayoutByDefault(t *testing.T) {
	keyboard := &layoutCaptureDevice{}
	provider := NewHIDProvider(nil, keyboard, nil, nil, false, "qwerty", nil)

	if err := provider.Keypress(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("Keypress: %v", err)
	}
	got := keyboard.bytes()
	if got[0] != 0 || got[2] != 0x04 {
		t.Fatalf("press report = %v, want modifier 0 and QWERTY a usage 0x04", got[:8])
	}
}
