package ble

import (
	"fmt"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

const consumerControlReportID byte = 1

var consumerControlReportMap = []byte{
	0x05, 0x0c, // Usage Page (Consumer)
	0x09, 0x01, // Usage (Consumer Control)
	0xa1, 0x01, // Collection (Application)
	0x85, consumerControlReportID, // Report ID
	0x15, 0x00, // Logical Minimum (0)
	0x26, 0xff, 0x03, // Logical Maximum (1023)
	0x19, 0x00, // Usage Minimum (Unassigned)
	0x2a, 0xff, 0x03, // Usage Maximum (1023)
	0x75, 0x10, // Report Size (16)
	0x95, 0x01, // Report Count (1)
	0x81, 0x00, // Input (Data, Array, Absolute)
	0xc0, // End Collection
}

func hidInformationValue() []byte {
	// HID 1.11, no country code, NormallyConnectable set. Remote wake is not
	// advertised because the service never emits Consumer Control input.
	return []byte{0x11, 0x01, 0x00, 0x02}
}

func hidReportMapValue() []byte {
	return append([]byte(nil), consumerControlReportMap...)
}

func hidReportReferenceValue() []byte {
	// Report Type 1 is an Input Report.
	return []byte{consumerControlReportID, 0x01}
}

func readValueAtOffset(value []byte, options map[string]dbus.Variant) ([]byte, *dbus.Error) {
	offset := uint16(0)
	if variant, ok := options["offset"]; ok {
		parsed, ok := variant.Value().(uint16)
		if !ok {
			return nil, dbus.NewError("org.bluez.Error.InvalidArguments", []any{"offset must be uint16"})
		}
		offset = parsed
	}
	if int(offset) > len(value) {
		return nil, dbus.NewError("org.bluez.Error.InvalidOffset", []any{fmt.Sprintf("offset %d exceeds value length %d", offset, len(value))})
	}
	return append([]byte(nil), value[offset:]...), nil
}

type staticReadCharacteristic struct {
	value []byte
}

func (c *staticReadCharacteristic) ReadValue(options map[string]dbus.Variant) ([]byte, *dbus.Error) {
	return readValueAtOffset(c.value, options)
}

type hidControlPointCharacteristic struct{}

func (c *hidControlPointCharacteristic) WriteValue(value []byte, _ map[string]dbus.Variant) *dbus.Error {
	if len(value) != 1 {
		return dbus.NewError("org.bluez.Error.InvalidValueLength", []any{"HID Control Point expects one byte"})
	}
	if value[0] != 0 && value[0] != 1 {
		return dbus.NewError("org.bluez.Error.NotSupported", []any{"unsupported HID Control Point command"})
	}
	return nil
}

type hidReportCharacteristic struct {
	properties *prop.Properties
}

func (c *hidReportCharacteristic) ReadValue(options map[string]dbus.Variant) ([]byte, *dbus.Error) {
	value, _ := c.properties.GetMust(blueZGattCharInterface, "Value").([]byte)
	return readValueAtOffset(value, options)
}

func (c *hidReportCharacteristic) StartNotify() *dbus.Error {
	if notifying, _ := c.properties.GetMust(blueZGattCharInterface, "Notifying").(bool); notifying {
		return dbus.NewError("org.bluez.Error.InProgress", []any{"notifications already enabled"})
	}
	c.properties.SetMust(blueZGattCharInterface, "Notifying", true)
	return nil
}

func (c *hidReportCharacteristic) StopNotify() *dbus.Error {
	c.properties.SetMust(blueZGattCharInterface, "Notifying", false)
	return nil
}

type staticReadDescriptor struct {
	value []byte
}

func (d *staticReadDescriptor) ReadValue(options map[string]dbus.Variant) ([]byte, *dbus.Error) {
	return readValueAtOffset(d.value, options)
}
