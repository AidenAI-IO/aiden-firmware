package ble

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

type bondedDevice struct {
	path       dbus.ObjectPath
	properties map[string]dbus.Variant
}

func bondedDevices(objects managedObjects) []bondedDevice {
	devices := make([]bondedDevice, 0)
	for path, interfaces := range objects {
		properties, ok := interfaces[blueZDeviceInterface]
		if !ok || !variantBool(properties, "Paired") {
			continue
		}
		devices = append(devices, bondedDevice{path: path, properties: properties})
	}
	sort.Slice(devices, func(i, j int) bool {
		iTrusted := variantBool(devices[i].properties, "Trusted")
		jTrusted := variantBool(devices[j].properties, "Trusted")
		if iTrusted != jTrusted {
			return iTrusted
		}
		return string(devices[i].path) < string(devices[j].path)
	})
	return devices
}

func selectBondedDevice(objects managedObjects) (dbus.ObjectPath, int) {
	devices := bondedDevices(objects)
	if len(devices) == 0 {
		return "", 0
	}
	return devices[0].path, len(devices)
}

func (b *blueZBackend) refreshTrustedDevice(objects managedObjects) (dbus.ObjectPath, int) {
	devices := bondedDevices(objects)
	b.stateMu.Lock()
	previous := b.trustedDevice
	selected := dbus.ObjectPath("")
	for _, device := range devices {
		if device.path == previous {
			selected = previous
			break
		}
	}
	if !selected.IsValid() && len(devices) > 0 {
		selected = devices[0].path
	}
	b.trustedDevice = selected
	b.stateMu.Unlock()

	if selected.IsValid() {
		if !variantBool(objects[selected][blueZDeviceInterface], "Trusted") {
			if err := b.markDeviceTrusted(selected); err != nil {
				b.service.status.update(func(status *RuntimeStatus) { status.LastError = err.Error() })
			}
		}
		b.closePairingWindow()
	} else if previous.IsValid() {
		b.beginPairingWindow(time.Now())
	}
	return selected, len(devices)
}

func (b *blueZBackend) markDeviceTrusted(device dbus.ObjectPath) error {
	if !device.IsValid() {
		return nil
	}
	if err := b.conn.Object(BlueZBusName, device).
		Call(dbusPropertiesInterface+".Set", 0, blueZDeviceInterface, "Trusted", dbus.MakeVariant(true)).Err; err != nil {
		return fmt.Errorf("trust paired Bluetooth device: %w", err)
	}
	return nil
}

func (b *blueZBackend) updateDeviceStatus(
	trusted dbus.ObjectPath,
	properties map[string]dbus.Variant,
	bondedCount int,
) {
	b.stateMu.Lock()
	pairingOpen := b.pairingOpen
	pairingDeadline := b.pairingDeadline
	b.stateMu.Unlock()

	b.service.status.update(func(status *RuntimeStatus) {
		status.BondedDeviceCount = bondedCount
		status.PairingOpen = pairingOpen
		status.PairingDeadline = formatDeadline(pairingDeadline)
		status.TrustedDevicePath = ""
		status.TrustedDeviceName = ""
		status.TrustedDeviceAddr = ""
		status.ConnectedDevicePath = ""
		status.ConnectedDeviceName = ""
		status.ConnectedDeviceAddr = ""
		status.Connected = false
		status.Paired = trusted.IsValid()
		status.ServicesResolved = false
		if !trusted.IsValid() {
			return
		}
		name := firstNonEmpty(variantString(properties, "Name"), variantString(properties, "Alias"))
		address := variantString(properties, "Address")
		status.TrustedDevicePath = string(trusted)
		status.TrustedDeviceName = name
		status.TrustedDeviceAddr = address
		status.Connected = variantBool(properties, "Connected")
		status.ServicesResolved = variantBool(properties, "ServicesResolved")
		if status.Connected {
			status.ConnectedDevicePath = string(trusted)
			status.ConnectedDeviceName = name
			status.ConnectedDeviceAddr = address
		}
	})
}

func (b *blueZBackend) beginPairingWindow(now time.Time) {
	if b.pairingWindow <= 0 {
		return
	}
	b.stateMu.Lock()
	b.pairingOpen = true
	b.pairingDeadline = now.Add(b.pairingWindow)
	deadline := b.pairingDeadline
	b.stateMu.Unlock()
	if err := b.setPairingMode(true); err != nil {
		b.service.status.update(func(status *RuntimeStatus) { status.LastError = err.Error() })
	}
	b.service.status.update(func(status *RuntimeStatus) {
		status.PairingOpen = true
		status.PairingDeadline = formatDeadline(deadline)
	})
}

func (b *blueZBackend) closePairingWindow() {
	b.stateMu.Lock()
	wasOpen := b.pairingOpen
	b.pairingOpen = false
	b.pairingDeadline = time.Time{}
	b.stateMu.Unlock()
	if wasOpen {
		if err := b.setPairingMode(false); err != nil {
			b.service.status.update(func(status *RuntimeStatus) { status.LastError = err.Error() })
		}
	}
	b.service.status.update(func(status *RuntimeStatus) {
		status.PairingOpen = false
		status.PairingDeadline = ""
	})
}

func (b *blueZBackend) expirePairingWindow(now time.Time) {
	b.stateMu.Lock()
	expired := b.pairingOpen && !b.pairingDeadline.IsZero() && !now.Before(b.pairingDeadline)
	b.stateMu.Unlock()
	if expired {
		b.closePairingWindow()
	}
}

func (b *blueZBackend) setPairingMode(open bool) error {
	if b.conn == nil || !b.adapter.IsValid() {
		return ErrBluetoothUnavailable
	}
	for _, name := range []string{"Pairable", "Discoverable"} {
		if err := b.conn.Object(BlueZBusName, b.adapter).
			Call(dbusPropertiesInterface+".Set", 0, blueZAdapterInterface, name, dbus.MakeVariant(open)).Err; err != nil {
			return fmt.Errorf("set adapter %s: %w", name, err)
		}
	}
	if b.advProps != nil {
		if err := b.conn.Object(BlueZBusName, b.adapter).
			Call(blueZAdvManagerInterface+".UnregisterAdvertisement", 0, advertisementPath).Err; err != nil {
			return fmt.Errorf("unregister BLE advertisement: %w", err)
		}
		b.advProps.SetMust(blueZAdvertisementInterface, "Discoverable", open)
		if err := b.registerAdvertisement(); err != nil {
			b.service.status.update(func(status *RuntimeStatus) { status.Advertising = false })
			return err
		}
		b.service.status.update(func(status *RuntimeStatus) { status.Advertising = true })
	}
	return nil
}

func (b *blueZBackend) deviceAllowed(device dbus.ObjectPath) bool {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	if b.trustedDevice.IsValid() {
		return device == b.trustedDevice
	}
	return b.pairingOpen
}

func stableDeviceName(baseName, adapterAddress string) string {
	baseName = strings.TrimSpace(baseName)
	if baseName == "" {
		baseName = "Aiden"
	}
	compactAddress := strings.NewReplacer(":", "", "-", "").Replace(strings.ToUpper(adapterAddress))
	if len(compactAddress) < 4 {
		return baseName
	}
	suffix := compactAddress[len(compactAddress)-4:]
	if strings.HasSuffix(strings.ToUpper(baseName), "-"+suffix) {
		return baseName
	}
	const maxLocalNameBytes = 29
	maxBaseBytes := maxLocalNameBytes - len(suffix) - 1
	if len(baseName) > maxBaseBytes {
		baseName = baseName[:maxBaseBytes]
	}
	return baseName + "-" + suffix
}

func formatDeadline(deadline time.Time) string {
	if deadline.IsZero() {
		return ""
	}
	return deadline.UTC().Format(time.RFC3339Nano)
}
