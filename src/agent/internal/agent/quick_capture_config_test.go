package agent

import "testing"

func TestQuickCaptureConfigRejectsLegacyWakeupPins(t *testing.T) {
	for _, pin := range []int{32, 33} {
		if err := (QuickCaptureConfig{GPIOPin: pin}).Validate(); err == nil {
			t.Fatalf("GPIOPin %d Validate() succeeded, want an error", pin)
		}
	}
}

func TestQuickCaptureConfigAcceptsVerifiedGPIO3(t *testing.T) {
	if err := (QuickCaptureConfig{GPIOPin: 3}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestQuickCaptureConfigAcceptsDisabledGPIO(t *testing.T) {
	if err := (QuickCaptureConfig{GPIOPin: 0}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestQuickCaptureConfigRejectsUnsupportedGPIOPins(t *testing.T) {
	for _, pin := range []int{-1, 1, 31, 34, 151} {
		if err := (QuickCaptureConfig{GPIOPin: pin}).Validate(); err == nil {
			t.Errorf("GPIOPin %d Validate() succeeded, want an error", pin)
		}
	}
}
