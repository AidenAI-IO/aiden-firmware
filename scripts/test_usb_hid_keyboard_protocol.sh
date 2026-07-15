#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
INIT_SCRIPT="$ROOT_DIR/overlay/etc/init.d/S49usbhid"
EXAMPLE_SRC="$ROOT_DIR/src/example_usb_hid.cpp"

fail() {
    echo "$*" >&2
    exit 1
}

sh -n "$INIT_SCRIPT" || fail "S49usbhid has invalid shell syntax"

grep -Fq 'echo 0x1d6b > "$GADGET_DIR/idVendor"' "$INIT_SCRIPT" ||
    fail "S49usbhid generic control must advertise the non-Apple test VID"

grep -Fq 'echo 0x0104 > "$GADGET_DIR/idProduct"' "$INIT_SCRIPT" ||
    fail "S49usbhid generic control must advertise the generic test PID"

grep -Fq 'echo "Aiden Generic Keyboard" > "$GADGET_DIR/strings/0x409/product"' "$INIT_SCRIPT" ||
    fail "S49usbhid generic control must advertise the generic keyboard product"

grep -Fq 'echo 1 > "$GADGET_DIR/functions/hid.usb0/protocol"' "$INIT_SCRIPT" ||
    fail "S49usbhid generic keyboard must use boot protocol"

grep -Fq 'echo 1 > "$GADGET_DIR/functions/hid.usb0/subclass"' "$INIT_SCRIPT" ||
    fail "S49usbhid generic keyboard must use boot subclass"

grep -Fq 'echo 8 > "$GADGET_DIR/functions/hid.usb0/report_length"' "$INIT_SCRIPT" ||
    fail "S49usbhid generic keyboard must use an 8-byte boot report"

grep -Fq 'echo full-speed > "$GADGET_DIR/max_speed"' "$INIT_SCRIPT" ||
    fail "S49usbhid pure generic profile must force USB full-speed"

grep -Fq 'echo 0x00 > "$GADGET_DIR/bDeviceClass"' "$INIT_SCRIPT" ||
    fail "S49usbhid pure generic profile must use per-interface device class"

if grep -Fq 'mkdir -p "$GADGET_DIR/functions/hid.usb1"' "$INIT_SCRIPT" ||
   grep -Fq 'mkdir -p "$GADGET_DIR/functions/hid.usb2"' "$INIT_SCRIPT"; then
    fail "S49usbhid pure generic profile must expose only one HID function"
fi

if grep -Fq 'mkdir -p "$GADGET_DIR/functions/ecm.usb0"' "$INIT_SCRIPT"; then
    fail "S49usbhid pure generic profile must not create ECM"
fi

grep -Fq 'ln -s "$GADGET_DIR/functions/hid.usb0" "$GADGET_DIR/configs/c.1/hid.usb0"' "$INIT_SCRIPT" ||
    fail "S49usbhid pure generic profile must link the keyboard function"

[ "$(grep -Fc 'echo "$UDC" > "$GADGET_DIR/UDC"' "$INIT_SCRIPT")" -eq 1 ] ||
    fail "S49usbhid pure generic profile must bind the UDC exactly once"

grep -Fq 'write_text_file(function_path + "/protocol", "1");' "$EXAMPLE_SRC" ||
    fail "example_usb_hid setup must keep keyboard HID boot protocol=1"

grep -Fq 'write_text_file(function_path + "/subclass", "1");' "$EXAMPLE_SRC" ||
    fail "example_usb_hid setup must keep keyboard HID boot subclass=1"

echo "usb HID keyboard protocol checks passed"
