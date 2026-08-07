package ble

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/godbus/dbus/v5"
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

func TestDisconnectedDeviceClearsSubscriberStatus(t *testing.T) {
	service := NewService(1)
	service.status.update(func(status *RuntimeStatus) {
		status.WakeSubscriber = true
		status.ANCSSubscribed = true
	})
	backend := newBlueZBackend(service, "Aiden", 0)

	backend.updateDeviceStatus(
		dbus.ObjectPath("/org/bluez/hci0/dev_00_11_22_33_44_55"),
		map[string]dbus.Variant{
			"Name":      dbus.MakeVariant("iPhone"),
			"Address":   dbus.MakeVariant("00:11:22:33:44:55"),
			"Connected": dbus.MakeVariant(false),
		},
		1,
	)

	status := service.Status()
	if !status.Paired || status.Connected || status.WakeSubscriber || status.ANCSSubscribed {
		t.Fatalf("unexpected disconnected status: %#v", status)
	}
}
