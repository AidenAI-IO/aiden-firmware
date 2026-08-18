package mnk

import (
	"context"
	"testing"
)

func TestHIDProviderHorizontalScrollWritesACPanReport(t *testing.T) {
	pointer := &layoutCaptureDevice{}
	provider := NewHIDProvider(pointer, nil, nil, nil, false, "qwerty", nil)

	if err := provider.Scroll(context.Background(), 3, 0); err != nil {
		t.Fatalf("Scroll() error = %v", err)
	}
	report := pointer.bytes()
	if len(report) != 7 {
		t.Fatalf("report length = %d, want 7", len(report))
	}
	if report[5] != 0 || report[6] != 3 {
		t.Fatalf("wheel bytes = [%d %d], want [0 3]", report[5], report[6])
	}
}

func TestHIDProviderVerticalScrollUsesSevenByteReport(t *testing.T) {
	pointer := &layoutCaptureDevice{}
	provider := NewHIDProvider(pointer, nil, nil, nil, false, "qwerty", nil)

	if err := provider.Scroll(context.Background(), 0, -3); err != nil {
		t.Fatalf("Scroll() error = %v", err)
	}
	report := pointer.bytes()
	if len(report) != 7 {
		t.Fatalf("report length = %d, want 7", len(report))
	}
	if report[5] != byte(253) || report[6] != 0 {
		t.Fatalf("wheel bytes = [%d %d], want [253 0]", report[5], report[6])
	}
}
