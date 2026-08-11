package ble

import (
	"reflect"
	"testing"
)

func TestPairingCharacteristicOnlyExposesEncryptedRead(t *testing.T) {
	want := []string{"encrypt-read"}
	if got := pairingCharacteristicFlags(); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected pairing characteristic flags: got %v want %v", got, want)
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
