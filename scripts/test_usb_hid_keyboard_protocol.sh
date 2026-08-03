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

grep -Fq 'echo 1 > "$GADGET_DIR/functions/hid.usb0/protocol"' "$INIT_SCRIPT" ||
    fail "S49usbhid must keep keyboard HID boot protocol=1 for host compatibility"

grep -Fq 'echo 1 > "$GADGET_DIR/functions/hid.usb0/subclass"' "$INIT_SCRIPT" ||
    fail "S49usbhid must keep keyboard HID boot subclass=1 for host compatibility"

awk -v bind="echo \"\$UDC\" > \"\$GADGET_DIR/UDC\"" '
    /^start\(\)[[:space:]]*\{/ { in_start=1; next }
    in_start && /^\}/ { exit }
    !in_start { next }
    index($0, bind) { bind_count++ }
    END { exit(bind_count == 1 ? 0 : 1) }
' "$INIT_SCRIPT" ||
    fail "S49usbhid must bind the completed composite exactly once during startup"

if grep -Fq 'reenumerate_composite' "$INIT_SCRIPT"; then
    fail "S49usbhid must not soft-disconnect and re-enumerate the same startup identity"
fi

grep -Fq 'write_text_file(function_path + "/protocol", "1");' "$EXAMPLE_SRC" ||
    fail "example_usb_hid setup must keep keyboard HID boot protocol=1"

grep -Fq 'write_text_file(function_path + "/subclass", "1");' "$EXAMPLE_SRC" ||
    fail "example_usb_hid setup must keep keyboard HID boot subclass=1"

echo "usb HID keyboard protocol checks passed"
