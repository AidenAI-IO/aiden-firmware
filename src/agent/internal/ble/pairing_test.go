package ble

import (
	"bytes"
	"reflect"
	"testing"
)

func TestAdvertisedServiceUUIDs(t *testing.T) {
	want := []string{WakeServiceUUID}
	if got := advertisedServiceUUIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("advertisedServiceUUIDs() = %#v, want %#v", got, want)
	}
}

func TestBoardIdentityUsesFullAdapterAddress(t *testing.T) {
	text, identity, err := boardIdentityFromAdapterAddress("AA:BB:CC:DD:12:34")
	if err != nil {
		t.Fatal(err)
	}
	if text != "aabbccdd1234" || !bytes.Equal(identity, []byte{0xaa, 0xbb, 0xcc, 0xdd, 0x12, 0x34}) {
		t.Fatalf("unexpected board identity text=%q bytes=%x", text, identity)
	}
	manufacturerData := advertisedManufacturerData(identity)
	variant, ok := manufacturerData[AidenManufacturerID]
	if !ok || !bytes.Equal(variant.Value().([]byte), identity) {
		t.Fatalf("advertisement does not carry board identity: %#v", manufacturerData)
	}
}

func TestBoardIdentityRejectsInvalidAdapterAddress(t *testing.T) {
	if _, _, err := boardIdentityFromAdapterAddress("12:34"); err == nil {
		t.Fatal("short adapter address was accepted")
	}
}
