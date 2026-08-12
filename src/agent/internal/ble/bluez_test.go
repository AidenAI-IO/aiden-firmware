package ble

import (
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
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

func TestANCSRescanDoesNotWaitForWakeSubscriber(t *testing.T) {
	service := NewService(4)
	service.consumer.SetControlPointWriter(func([]byte) error { return nil })
	source := make([]byte, 8)
	binary.LittleEndian.PutUint32(source[4:], 42)
	if err := service.consumer.HandleNotificationSource(source); err != nil {
		t.Fatal(err)
	}

	backend := newBlueZBackend(service, "Aiden", 0)
	backend.ancs = ancsPaths{
		device:             dbus.ObjectPath("/org/bluez/hci0/dev_phone"),
		notificationSource: dbus.ObjectPath("/org/bluez/hci0/dev_phone/service/notification"),
		controlPoint:       dbus.ObjectPath("/org/bluez/hci0/dev_phone/service/control"),
		dataSource:         dbus.ObjectPath("/org/bluez/hci0/dev_phone/service/data"),
	}
	if err := backend.rescanANCS(managedObjects{}, backend.ancs.device); err != nil {
		t.Fatal(err)
	}

	page := service.EventsSince(0, 10, "")
	if len(page.Events) != 1 {
		t.Fatalf("events = %#v, want one pending ANCS event", page.Events)
	}
	if got := page.Events[0].MetadataError; got != "ANCS device disconnected" {
		t.Fatalf("metadata error = %q; ANCS must be reconciled independently of Wake subscription", got)
	}
}
