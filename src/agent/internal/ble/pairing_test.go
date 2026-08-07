package ble

import (
	"reflect"
	"testing"
)

func TestAdvertisedServiceUUIDs(t *testing.T) {
	want := []string{WakeServiceUUID}
	if got := advertisedServiceUUIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("advertisedServiceUUIDs() = %#v, want %#v", got, want)
	}
}

func TestAdvertisedAppearance(t *testing.T) {
	if got := advertisedAppearance(); got != 0 {
		t.Fatalf("pairing appearance = %#04x, want generic appearance", got)
	}
}
