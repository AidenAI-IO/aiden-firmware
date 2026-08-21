package mnk

import (
	"context"
	"testing"
)

func TestHIDProviderRejectsHorizontalScroll(t *testing.T) {
	pointer := &layoutCaptureDevice{}
	provider := NewHIDProvider(pointer, nil, nil, nil, false, "qwerty", nil)

	err := provider.Scroll(context.Background(), 3, 0)
	if got := AsError(err); got == nil || got.Kind != ErrInvalidArguments {
		t.Fatalf("Scroll() error = %v, want invalid arguments", err)
	}
	if report := pointer.bytes(); len(report) != 0 {
		t.Fatalf("report length = %d, want 0", len(report))
	}
}

func TestHIDProviderVerticalScrollUsesSixByteReport(t *testing.T) {
	pointer := &layoutCaptureDevice{}
	provider := NewHIDProvider(pointer, nil, nil, nil, false, "qwerty", nil)

	if err := provider.Scroll(context.Background(), 0, -3); err != nil {
		t.Fatalf("Scroll() error = %v", err)
	}
	report := pointer.bytes()
	if len(report) != 6 {
		t.Fatalf("report length = %d, want 6", len(report))
	}
	if report[5] != byte(253) {
		t.Fatalf("wheel byte = %d, want 253", report[5])
	}
}

func TestHIDProviderTouchActionsKeepsContactAcrossMoveAndWait(t *testing.T) {
	pointer := &layoutCaptureDevice{}
	provider := NewHIDProvider(pointer, nil, nil, nil, true, "qwerty", nil)
	actions := []TouchAction{
		{Type: "touch_down", Point: &Point{X: 100, Y: 800}},
		{Type: "wait", DurationMs: 0},
		{Type: "move_to", Point: &Point{X: 100, Y: 200}},
		{Type: "touch_up"},
	}
	if err := provider.TouchActions(context.Background(), actions); err != nil {
		t.Fatalf("TouchActions() error = %v", err)
	}
	data := pointer.bytes()
	if len(data) != 5*6 {
		t.Fatalf("wrote %d bytes, want five 6-byte reports", len(data))
	}
	// The press, move, and final release reports must preserve the contact bit
	// until touch_up; the repeated release reports must be zero-contact.
	for i, offset := range []int{0, 6} {
		if data[offset]&0x03 != 0x03 {
			t.Fatalf("report %d flags = %#x, want contact", i, data[offset])
		}
	}
	for offset := 12; offset < len(data); offset += 6 {
		if data[offset]&0x03 != 0 {
			t.Fatalf("release report at %d flags = %#x, want no contact", offset, data[offset])
		}
	}
}
