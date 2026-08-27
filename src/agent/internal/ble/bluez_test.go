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

func TestWakeReasonCodeIncludesLocalLiveActivityRefresh(t *testing.T) {
	if got := wakeReasonCode("live_activity"); got != 4 {
		t.Fatalf("wakeReasonCode(live_activity) = %d, want 4", got)
	}
}

func TestWakeReasonCodeIncludesPlannedUSBReenumeration(t *testing.T) {
	if got := wakeReasonCode("usb_reenumeration"); got != 5 {
		t.Fatalf("wakeReasonCode(usb_reenumeration) = %d, want 5", got)
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

func TestObjectManagerSignalMatchCoversRootPath(t *testing.T) {
	// Regression test: BlueZ exports its ObjectManager at "/" and emits
	// InterfacesAdded/InterfacesRemoved from that object, carrying the added
	// object's path in argument 0. Subscribing with path_namespace=/org/bluez
	// matches neither "/" nor its own emissions, so the bus silently drops every
	// one of those signals.
	//
	// The visible symptom is narrow but severe: a phone paired AFTER ble_service
	// starts is announced only via InterfacesAdded, as are the ANCS
	// GattService1/GattCharacteristic1 objects iOS exposes once bonded. Losing
	// them means the reconciler never runs while those objects exist and no
	// notifications are ingested until the service restarts and re-reads the
	// whole object tree via a direct GetManagedObjects call.
	var spec *signalMatchSpec
	for _, candidate := range signalMatchSpecs() {
		if candidate.name == "object_manager" {
			spec = &candidate
			break
		}
	}
	if spec == nil {
		t.Fatal("no object_manager signal subscription is registered")
	}

	// MatchOption keeps its fields private and formatMatchOptions is unexported,
	// so assert by comparing against independently constructed options.
	forbidden := dbus.WithMatchPathNamespace(dbus.ObjectPath("/org/bluez"))
	wantPath := dbus.WithMatchObjectPath(blueZRootPath)
	sawRootPath := false
	for _, option := range spec.options {
		if option == forbidden {
			t.Error("ObjectManager subscription filters on path_namespace=/org/bluez, " +
				"which excludes the root path the signals are emitted from")
		}
		if option == wantPath {
			sawRootPath = true
		}
	}
	if !sawRootPath {
		t.Errorf("ObjectManager subscription does not cover the root path %q", blueZRootPath)
	}
}

func TestPropertiesSignalMatchStaysScopedToBlueZObjects(t *testing.T) {
	// The Properties subscription is correct as-is and must not be widened while
	// fixing the ObjectManager one: PropertiesChanged is emitted from the object
	// that changed, so /org/bluez is the right namespace there.
	var spec *signalMatchSpec
	for _, candidate := range signalMatchSpecs() {
		if candidate.name == "properties" {
			spec = &candidate
			break
		}
	}
	if spec == nil {
		t.Fatal("no properties signal subscription is registered")
	}
	want := dbus.WithMatchPathNamespace(dbus.ObjectPath("/org/bluez"))
	for _, option := range spec.options {
		if option == want {
			return
		}
	}
	t.Error("properties subscription lost its /org/bluez path namespace")
}
