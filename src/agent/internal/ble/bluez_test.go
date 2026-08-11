package ble

import (
	"reflect"
	"testing"
	"time"
)

func TestWakeCharacteristicFlagsKeepPairingProbeEncrypted(t *testing.T) {
	want := []string{"encrypt-read", "notify"}
	if got := wakeCharacteristicFlags(); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected Wake characteristic flags: got %v want %v", got, want)
	}
}

func TestWakeStopGracePeriodRejectsPreviousConnectionCleanup(t *testing.T) {
	started := time.Unix(100, 0)
	if !shouldIgnoreWakeStop(true, started, started.Add(time.Second)) {
		t.Fatal("recent StopNotify on a connected replacement link was not ignored")
	}
	if shouldIgnoreWakeStop(true, started, started.Add(3*time.Second)) {
		t.Fatal("old StopNotify was ignored after the grace period")
	}
	if shouldIgnoreWakeStop(false, started, started.Add(time.Second)) {
		t.Fatal("StopNotify was ignored after the physical link disconnected")
	}
}

func TestANCSControlPointUsesWriteRequest(t *testing.T) {
	options := ancsControlPointWriteOptions()
	writeType, ok := options["type"]
	if !ok {
		t.Fatal("ANCS Control Point write type is missing")
	}
	if got, ok := writeType.Value().(string); !ok || got != "request" {
		t.Fatalf("unexpected ANCS Control Point write type: %#v", writeType.Value())
	}
}
