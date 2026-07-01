package agent

import (
	"context"
	"testing"
	"time"
)

func testNoWaitSleep(context.Context, time.Duration) error {
	return nil
}

func newFastTextInputEngine(hw textInputHardwareDeps, vision textInputVision) *textInputEngine {
	return newTextInputEngineWithSleep(hw, vision, testNoWaitSleep)
}

func skipHIDSleeps(t *testing.T) {
	t.Helper()
	original := sleepMs
	sleepMs = func(int) {}
	t.Cleanup(func() { sleepMs = original })
}

func skipQuickActionDelays(t *testing.T) {
	t.Helper()
	original := sleepQuickActionDelay
	sleepQuickActionDelay = func(context.Context, int) error { return nil }
	t.Cleanup(func() { sleepQuickActionDelay = original })
}
