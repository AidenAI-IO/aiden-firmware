package agent

import (
	"strings"
	"testing"
)

func TestQuickCaptureConfigRejectsLegacyWakeupPins(t *testing.T) {
	for _, pin := range []int{32, 33} {
		err := (QuickCaptureConfig{GPIOPin: pin}).Validate()
		if err == nil || !strings.Contains(err.Error(), "reserved for legacy wakeup") {
			t.Fatalf("GPIOPin %d Validate() error = %v", pin, err)
		}
	}
}

func TestQuickCaptureConfigAcceptsVerifiedGPIO3(t *testing.T) {
	if err := (QuickCaptureConfig{GPIOPin: 3}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
