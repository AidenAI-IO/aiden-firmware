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
