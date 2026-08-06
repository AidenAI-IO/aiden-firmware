package ble

import (
	"bytes"
	"strings"
	"testing"

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

func TestPairingPolicyAllowsOnlyWindowOrTrustedDevice(t *testing.T) {
	trusted := dbus.ObjectPath("/org/bluez/hci0/dev_01")
	other := dbus.ObjectPath("/org/bluez/hci0/dev_02")
	backend := &blueZBackend{pairingOpen: true}
	if !backend.deviceAllowed(other) {
		t.Fatal("first device must be allowed during pairing window")
	}
	backend.stateMu.Lock()
	backend.trustedDevice = trusted
	backend.pairingOpen = false
	backend.stateMu.Unlock()
	if !backend.deviceAllowed(trusted) || backend.deviceAllowed(other) {
		t.Fatal("only the selected trusted device must remain authorized")
	}
}
