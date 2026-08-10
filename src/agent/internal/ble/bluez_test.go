package ble

import (
	"reflect"
	"testing"
)

func TestWakeCharacteristicFlagsKeepPairingProbeEncrypted(t *testing.T) {
	want := []string{"encrypt-read", "notify"}
	if got := wakeCharacteristicFlags(); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected Wake characteristic flags: got %v want %v", got, want)
	}
}
