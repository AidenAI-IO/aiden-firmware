package ble

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
)

var errPairingModeState = errors.New("BLE pairing mode update failed")

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

func (b *blueZBackend) refreshTrustedDevice(objects managedObjects) (dbus.ObjectPath, int, error) {
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
		if err := b.closePairingWindow(); err != nil {
			return selected, len(devices), err
		}
	}
	return selected, len(devices), nil
}

func (b *blueZBackend) StartPairing() error {
	b.wakeMu.Lock()
	closed := b.closed
	b.wakeMu.Unlock()
	if closed {
		return ErrBluetoothUnavailable
	}

	b.stateMu.Lock()
	trusted := b.trustedDevice
	b.stateMu.Unlock()
	if trusted.IsValid() {
		return ErrAlreadyPaired
	}
	return b.beginPairingWindow(time.Now())
}

func (b *blueZBackend) ForgetPairing() (int, error) {
	b.wakeMu.Lock()
	closed := b.closed
	b.wakeMu.Unlock()
	if closed || b.conn == nil || !b.adapter.IsValid() {
		return 0, ErrBluetoothUnavailable
	}
	if err := b.closePairingWindow(); err != nil {
		return 0, err
	}

	objects, err := b.getManagedObjects()
	if err != nil {
		return 0, err
	}
	devices := bondedDevices(objects)
	removed := 0
	for _, device := range devices {
		if err := callWithTimeout(
			b.conn.Object(BlueZBusName, b.adapter),
			blueZAdapterInterface+".RemoveDevice",
			device.path,
		).Err; err != nil {
			b.requestRescan()
			return removed, fmt.Errorf("remove Bluetooth bond %s: %w", device.path, err)
		}
		removed++
	}

	b.stateMu.Lock()
	b.trustedDevice = ""
	b.stateMu.Unlock()
	b.clearANCS("Bluetooth pairing removed")
	b.service.consumer.ResetConnection("Bluetooth pairing removed")
	b.service.status.update(func(status *RuntimeStatus) {
		status.BondedDeviceCount = 0
		status.TrustedDevicePath = ""
		status.TrustedDeviceName = ""
		status.TrustedDeviceAddr = ""
		status.ConnectedDevicePath = ""
		status.ConnectedDeviceName = ""
		status.ConnectedDeviceAddr = ""
		status.WakeSubscriber = false
		status.Connected = false
		status.Paired = false
		status.ServicesResolved = false
		status.ANCSSubscribed = false
	})
	b.requestRescan()
	return removed, nil
}

func (b *blueZBackend) markDeviceTrusted(device dbus.ObjectPath) error {
	if !device.IsValid() {
		return nil
	}
	if err := callWithTimeout(
		b.conn.Object(BlueZBusName, device),
		dbusPropertiesInterface+".Set",
		blueZDeviceInterface,
		"Trusted",
		dbus.MakeVariant(true),
	).Err; err != nil {
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

func (b *blueZBackend) beginPairingWindow(now time.Time) error {
	if b.pairingWindow <= 0 {
		return nil
	}
	b.pairingModeMu.Lock()
	defer b.pairingModeMu.Unlock()

	b.stateMu.Lock()
	b.pairingOpen = true
	b.pairingDeadline = now.Add(b.pairingWindow)
	b.pairingModeDirty = true
	deadline := b.pairingDeadline
	b.stateMu.Unlock()

	if err := b.setPairingMode(true); err != nil {
		// Reapply the complete closed state, including the advertisement. A
		// failure may have happened after the old advertisement was removed,
		// so only clearing the adapter flags could leave BLE unavailable.
		cleanupErr := b.setPairingMode(false)
		b.stateMu.Lock()
		b.pairingOpen = false
		b.pairingDeadline = time.Time{}
		b.pairingModeDirty = cleanupErr != nil
		b.stateMu.Unlock()
		resultErr := fmt.Errorf("%w: %v", errPairingModeState, err)
		b.service.status.update(func(status *RuntimeStatus) {
			status.PairingOpen = false
			status.PairingDeadline = ""
			status.LastError = resultErr.Error()
		})
		return resultErr
	}
	b.stateMu.Lock()
	b.pairingModeDirty = false
	b.stateMu.Unlock()
	b.service.status.update(func(status *RuntimeStatus) {
		status.PairingOpen = true
		status.PairingDeadline = formatDeadline(deadline)
		status.LastError = ""
	})
	return nil
}

func (b *blueZBackend) closePairingWindow() error {
	b.pairingModeMu.Lock()
	defer b.pairingModeMu.Unlock()

	b.stateMu.Lock()
	wasOpen := b.pairingOpen
	wasDirty := b.pairingModeDirty
	b.pairingOpen = false
	b.pairingDeadline = time.Time{}
	b.pairingModeDirty = wasOpen || wasDirty
	b.stateMu.Unlock()

	if !wasOpen && !wasDirty {
		return nil
	}
	// Always reapply the closed state. This also recovers from a previous
	// partial D-Bus failure that may have left one adapter property enabled.
	if err := b.setPairingMode(false); err != nil {
		resultErr := fmt.Errorf("%w: %v", errPairingModeState, err)
		b.service.status.update(func(status *RuntimeStatus) {
			status.PairingOpen = false
			status.PairingDeadline = ""
			status.LastError = resultErr.Error()
		})
		return resultErr
	}
	b.stateMu.Lock()
	b.pairingModeDirty = false
	b.stateMu.Unlock()
	b.service.status.update(func(status *RuntimeStatus) {
		status.PairingOpen = false
		status.PairingDeadline = ""
		status.LastError = ""
	})
	return nil
}

func (b *blueZBackend) expirePairingWindow(now time.Time) error {
	b.stateMu.Lock()
	expired := b.pairingOpen && !b.pairingDeadline.IsZero() && !now.Before(b.pairingDeadline)
	b.stateMu.Unlock()
	if expired {
		return b.closePairingWindow()
	}
	return nil
}

func (b *blueZBackend) setPairingMode(open bool) error {
	if b.conn == nil || !b.adapter.IsValid() {
		return ErrBluetoothUnavailable
	}
	for _, name := range []string{"Pairable", "Discoverable"} {
		if err := callWithTimeout(
			b.conn.Object(BlueZBusName, b.adapter),
			dbusPropertiesInterface+".Set",
			blueZAdapterInterface,
			name,
			dbus.MakeVariant(open),
		).Err; err != nil {
			return fmt.Errorf("set adapter %s: %w", name, err)
		}
	}
	if b.advProps != nil {
		unregisterErr := callWithTimeout(
			b.conn.Object(BlueZBusName, b.adapter),
			blueZAdvManagerInterface+".UnregisterAdvertisement",
			advertisementPath,
		).Err
		if unregisterErr != nil && !isDBusErrorNamed(unregisterErr, "org.bluez.Error.DoesNotExist") {
			b.service.status.update(func(status *RuntimeStatus) { status.Advertising = false })
			return fmt.Errorf("unregister BLE advertisement: %w", unregisterErr)
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
	const maxLocalNameBytes = 29
	baseName = strings.TrimSpace(baseName)
	if baseName == "" {
		baseName = "Aiden"
	}
	compactAddress := strings.NewReplacer(":", "", "-", "").Replace(strings.ToUpper(adapterAddress))
	if len(compactAddress) < 4 {
		return truncateUTF8(baseName, maxLocalNameBytes)
	}
	suffix := compactAddress[len(compactAddress)-4:]
	if strings.HasSuffix(strings.ToUpper(baseName), "-"+suffix) {
		baseName = baseName[:len(baseName)-len(suffix)-1]
	}
	maxBaseBytes := maxLocalNameBytes - len(suffix) - 1
	if len(baseName) > maxBaseBytes {
		baseName = truncateUTF8(baseName, maxBaseBytes)
	}
	return baseName + "-" + suffix
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func formatDeadline(deadline time.Time) string {
	if deadline.IsZero() {
		return ""
	}
	return deadline.UTC().Format(time.RFC3339Nano)
}
