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

grep -Fq 'reenumerate_composite()' "$INIT_SCRIPT" ||
    fail "S49usbhid must define startup composite re-enumeration for iOS HID session refresh"

awk -v unbind="echo \"\" > \"\$GADGET_DIR/UDC\" 2>/dev/null" \
    -v rebind="echo \"\$UDC\" > \"\$GADGET_DIR/UDC\"" '
    /^reenumerate_composite\(\)[[:space:]]*\{/ { in_fn=1; next }
    in_fn && /^\}/ { done=1; exit }
    in_fn && index($0, unbind) { found_unbind=1 }
    in_fn && index($0, rebind) { found_rebind=1 }
    END { exit(done && found_unbind && found_rebind ? 0 : 1) }
' "$INIT_SCRIPT" ||
    fail "S49usbhid startup re-enumeration must unbind and rebind the UDC in reenumerate_composite"

grep -Fq 'if [ "$DYNAMIC_KEYBOARD" -eq 1 ]; then' "$INIT_SCRIPT" ||
    fail "S49usbhid must distinguish dynamic and persistent keyboard startup"

grep -Fq 'write_dynamic_keyboard_state' "$INIT_SCRIPT" ||
    fail "dynamic keyboard startup must bind once and record keyboard-off state"

grep -Fq 'reenumerate_composite' "$INIT_SCRIPT" ||
    fail "persistent keyboard startup must retain the existing iOS re-enumeration"

grep -Fq 'write_text_file(function_path + "/protocol", "1");' "$EXAMPLE_SRC" ||
    fail "example_usb_hid setup must keep keyboard HID boot protocol=1"

grep -Fq 'write_text_file(function_path + "/subclass", "1");' "$EXAMPLE_SRC" ||
    fail "example_usb_hid setup must keep keyboard HID boot subclass=1"

echo "usb HID keyboard protocol checks passed"
