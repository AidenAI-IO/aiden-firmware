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

grep -Fq 'echo 0x05ac > "$GADGET_DIR/idVendor"' "$INIT_SCRIPT" ||
    fail "S49usbhid Apple compatibility POC must advertise Apple VID 05ac"

grep -Fq 'echo 0x0267 > "$GADGET_DIR/idProduct"' "$INIT_SCRIPT" ||
    fail "S49usbhid Apple compatibility POC must advertise Magic Keyboard PID 0267"

grep -Fq 'echo "Magic Keyboard" > "$GADGET_DIR/strings/0x409/product"' "$INIT_SCRIPT" ||
    fail "S49usbhid Apple compatibility POC must advertise the Magic Keyboard product"

grep -Fq 'echo 0 > "$GADGET_DIR/functions/hid.usb0/protocol"' "$INIT_SCRIPT" ||
    fail "S49usbhid Magic Keyboard interface must use report protocol"

grep -Fq 'echo 0 > "$GADGET_DIR/functions/hid.usb0/subclass"' "$INIT_SCRIPT" ||
    fail "S49usbhid Magic Keyboard interface must use the captured non-boot subclass"

grep -Fq 'echo 65 > "$GADGET_DIR/functions/hid.usb0/report_length"' "$INIT_SCRIPT" ||
    fail "S49usbhid Magic Keyboard interface must cover report ID 0x3f"

grep -Fq 'echo full-speed > "$GADGET_DIR/max_speed"' "$INIT_SCRIPT" ||
    fail "S49usbhid pure Magic Keyboard profile must force USB full-speed"

grep -Fq 'echo 0x00 > "$GADGET_DIR/bDeviceClass"' "$INIT_SCRIPT" ||
    fail "S49usbhid pure Magic Keyboard profile must use per-interface device class"

grep -Fq 'mkdir -p "$GADGET_DIR/functions/hid.usb1"' "$INIT_SCRIPT" ||
    fail "S49usbhid must expose the first Apple vendor-defined HID interface"

grep -Fq 'mkdir -p "$GADGET_DIR/functions/hid.usb2"' "$INIT_SCRIPT" ||
    fail "S49usbhid must expose the second Apple vendor-defined HID interface"

if grep -Fq 'mkdir -p "$GADGET_DIR/functions/ecm.usb0"' "$INIT_SCRIPT"; then
    fail "S49usbhid pure Magic Keyboard profile must not create ECM"
fi

awk '
    /ln -s .*functions\/hid\.usb1.*configs\/c\.1\/hid\.usb1/ { vendor0=NR }
    /ln -s .*functions\/hid\.usb2.*configs\/c\.1\/hid\.usb2/ { vendor1=NR }
    /ln -s .*functions\/hid\.usb0.*configs\/c\.1\/hid\.usb0/ { keyboard=NR }
    END { exit(vendor0 && vendor1 && keyboard && vendor0 < vendor1 && vendor1 < keyboard ? 0 : 1) }
' "$INIT_SCRIPT" ||
    fail "S49usbhid must link Apple vendor interfaces before the keyboard interface"

[ "$(grep -Fc 'echo "$UDC" > "$GADGET_DIR/UDC"' "$INIT_SCRIPT")" -eq 1 ] ||
    fail "S49usbhid pure profile must bind the UDC exactly once"

grep -Fq 'write_text_file(function_path + "/protocol", "1");' "$EXAMPLE_SRC" ||
    fail "example_usb_hid setup must keep keyboard HID boot protocol=1"

grep -Fq 'write_text_file(function_path + "/subclass", "1");' "$EXAMPLE_SRC" ||
    fail "example_usb_hid setup must keep keyboard HID boot subclass=1"

echo "usb HID keyboard protocol checks passed"
