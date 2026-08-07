package ble

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
)

func TestConsumerControlReportMapDoesNotExposeKeyboard(t *testing.T) {
	reportMap := hidReportMapValue()
	if !bytes.Contains(reportMap, []byte{0x05, 0x0c}) {
		t.Fatal("Consumer Usage Page is missing")
	}
	if bytes.Contains(reportMap, []byte{0x05, 0x07}) {
		t.Fatal("minimal HOGP must not expose a keyboard Usage Page")
	}
	if got := hidInformationValue(); len(got) != 4 || got[3]&0x02 == 0 {
		t.Fatalf("HID Information must declare NormallyConnectable: %v", got)
	}
	if got := hidReportReferenceValue(); !bytes.Equal(got, []byte{consumerControlReportID, 0x01}) {
		t.Fatalf("unexpected Report Reference: %v", got)
	}
}

func TestReadValueAtOffset(t *testing.T) {
	value, dbusErr := readValueAtOffset([]byte{1, 2, 3}, map[string]dbus.Variant{
		"offset": dbus.MakeVariant(uint16(1)),
	})
	if dbusErr != nil || !bytes.Equal(value, []byte{2, 3}) {
		t.Fatalf("unexpected offset read value=%v error=%v", value, dbusErr)
	}
	if _, dbusErr := readValueAtOffset([]byte{1}, map[string]dbus.Variant{
		"offset": dbus.MakeVariant(uint16(2)),
	}); dbusErr == nil || dbusErr.Name != "org.bluez.Error.InvalidOffset" {
		t.Fatalf("expected InvalidOffset, got %v", dbusErr)
	}
}

func TestStableDeviceNameUsesAdapterSuffix(t *testing.T) {
	if got := stableDeviceName("Aiden", "AA:BB:CC:DD:12:34"); got != "Aiden-1234" {
		t.Fatalf("unexpected stable name %q", got)
	}
	got := stableDeviceName(strings.Repeat("x", 40), "AA:BB:CC:DD:12:34")
	if len(got) != 29 || !strings.HasSuffix(got, "-1234") {
		t.Fatalf("stable name must fit legacy advertising: %q", got)
	}
	got = stableDeviceName(strings.Repeat("你", 20), "AA:BB:CC:DD:12:34")
	if len(got) > 29 || !utf8.ValidString(got) || !strings.HasSuffix(got, "-1234") {
		t.Fatalf("stable name must truncate on a UTF-8 boundary: %q", got)
	}
	got = stableDeviceName(strings.Repeat("x", 40)+"-1234", "AA:BB:CC:DD:12:34")
	if len(got) != 29 || strings.Count(got, "-1234") != 1 {
		t.Fatalf("stable name must truncate an existing suffix exactly once: %q", got)
	}
}

func TestSelectBondedDevicePrefersTrustedBond(t *testing.T) {
	paired := dbus.ObjectPath("/org/bluez/hci0/dev_02")
	trusted := dbus.ObjectPath("/org/bluez/hci0/dev_01")
	objects := managedObjects{
		paired: {
			blueZDeviceInterface: {
				"Paired":  dbus.MakeVariant(true),
				"Trusted": dbus.MakeVariant(false),
			},
		},
		trusted: {
			blueZDeviceInterface: {
				"Paired":  dbus.MakeVariant(true),
				"Trusted": dbus.MakeVariant(true),
			},
		},
	}
	selected, count := selectBondedDevice(objects)
	if selected != trusted || count != 2 {
		t.Fatalf("unexpected trusted selection path=%s count=%d", selected, count)
	}
}

func TestSelectBondedDevicePrefersConnectedBond(t *testing.T) {
	connected := dbus.ObjectPath("/org/bluez/hci0/dev_02")
	trusted := dbus.ObjectPath("/org/bluez/hci0/dev_01")
	objects := managedObjects{
		connected: {
			blueZDeviceInterface: {
				"Connected": dbus.MakeVariant(true),
				"Paired":    dbus.MakeVariant(true),
				"Trusted":   dbus.MakeVariant(false),
			},
		},
		trusted: {
			blueZDeviceInterface: {
				"Connected": dbus.MakeVariant(false),
				"Paired":    dbus.MakeVariant(true),
				"Trusted":   dbus.MakeVariant(true),
			},
		},
	}
	selected, count := selectBondedDevice(objects)
	if selected != connected || count != 2 {
		t.Fatalf("unexpected connected selection path=%s count=%d", selected, count)
	}
}

func TestPairingPolicyAllowsOnlyWindowOrTrustedDevice(t *testing.T) {
	trusted := dbus.ObjectPath("/org/bluez/hci0/dev_01")
	other := dbus.ObjectPath("/org/bluez/hci0/dev_02")
	backend := &blueZBackend{pairingOpen: true}
	if !backend.deviceAllowed(other) {
		t.Fatal("first device must be allowed during pairing window")
	}
	backend.stateMu.Lock()
	backend.trustedDevice = trusted
	backend.pairingOpen = true
	backend.stateMu.Unlock()
	if !backend.deviceAllowed(other) {
		t.Fatal("an explicit connection window must allow a phone even when an old bond exists")
	}
	backend.stateMu.Lock()
	backend.pairingOpen = false
	backend.stateMu.Unlock()
	if !backend.deviceAllowed(trusted) || backend.deviceAllowed(other) {
		t.Fatal("only the selected trusted device must remain authorized")
	}
}

func TestStartPairingDoesNotTreatBondAsConnection(t *testing.T) {
	backend := &blueZBackend{
		pairingWindow: 0,
		trustedDevice: dbus.ObjectPath("/org/bluez/hci0/dev_01"),
	}
	if err := backend.StartPairing(); err != nil {
		t.Fatalf("StartPairing() rejected an existing bond: %v", err)
	}
}

func TestPairingModeMatchesAdapterState(t *testing.T) {
	open := map[string]dbus.Variant{
		"Pairable":     dbus.MakeVariant(true),
		"Discoverable": dbus.MakeVariant(true),
	}
	if !pairingModeMatches(open, true) || pairingModeMatches(open, false) {
		t.Fatalf("open adapter properties were misclassified: %#v", open)
	}
	drifted := map[string]dbus.Variant{
		"Pairable":     dbus.MakeVariant(false),
		"Discoverable": dbus.MakeVariant(true),
	}
	if pairingModeMatches(drifted, true) {
		t.Fatalf("drifted adapter properties were accepted: %#v", drifted)
	}
}

func TestPairingAgentRejectsLegacyCredentials(t *testing.T) {
	agent := &pairingAgent{}
	if pin, err := agent.RequestPinCode("/org/bluez/hci0/dev_01"); pin != "" || err == nil || err.Name != "org.bluez.Error.Rejected" {
		t.Fatalf("legacy PIN request was not rejected: pin=%q error=%v", pin, err)
	}
	if passkey, err := agent.RequestPasskey("/org/bluez/hci0/dev_01"); passkey != 0 || err == nil || err.Name != "org.bluez.Error.Rejected" {
		t.Fatalf("legacy passkey request was not rejected: passkey=%d error=%v", passkey, err)
	}
}

func TestBlueZSignalSourceAndOwnerChangeHandling(t *testing.T) {
	backend := newBlueZBackend(NewService(4), "Aiden", time.Minute)
	backend.blueZOwner = ":1.42"
	rescanSignal := &dbus.Signal{
		Sender: ":1.99",
		Name:   dbusObjectManagerInterface + ".InterfacesAdded",
	}
	backend.handleSignal(rescanSignal)
	if len(backend.rescanRequests) != 0 {
		t.Fatal("forged non-BlueZ signal requested a rescan")
	}
	rescanSignal.Sender = backend.blueZOwner
	backend.handleSignal(rescanSignal)
	if len(backend.rescanRequests) != 1 {
		t.Fatal("signal from the BlueZ owner did not request a rescan")
	}

	backend.handleSignal(&dbus.Signal{
		Sender: dbusBusName,
		Name:   dbusBusInterface + ".NameOwnerChanged",
		Body:   []any{BlueZBusName, backend.blueZOwner, ":1.43"},
	})
	select {
	case err := <-backend.fatalErrors:
		if err == nil {
			t.Fatal("BlueZ owner change reported a nil fatal error")
		}
	default:
		t.Fatal("BlueZ owner change did not stop the backend")
	}
}

func TestDBusInProgressErrorRecognition(t *testing.T) {
	if !isDBusErrorNamed(dbus.NewError("org.bluez.Error.InProgress", nil), "org.bluez.Error.InProgress") {
		t.Fatal("pointer D-Bus error name was not recognized")
	}
	if !isDBusErrorNamed(dbus.Error{Name: "org.bluez.Error.InProgress"}, "org.bluez.Error.InProgress") {
		t.Fatal("value D-Bus error name was not recognized")
	}
}
