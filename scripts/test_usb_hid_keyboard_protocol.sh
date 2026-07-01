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

awk -v bind="echo \"\$UDC\" > \"\$GADGET_DIR/UDC\"" '
    /^start\(\)[[:space:]]*\{/ { in_start=1; next }
    in_start && /^\}/ { exit }
    !in_start { next }
    index($0, bind) { after_bind=1; step=0; next }
    after_bind && $0 ~ /^[[:space:]]*(#.*)?$/ { next }
    after_bind {
        step++
        if (step == 1 && $0 !~ /^[[:space:]]*sleep[[:space:]]+1[[:space:]]*$/) {
            exit
        }
        if (step == 2) {
            found=($0 ~ /^[[:space:]]*reenumerate_composite[[:space:]]*$/)
            exit
        }
    }
    END { exit(found ? 0 : 1) }
' "$INIT_SCRIPT" ||
    fail "S49usbhid must re-enumerate the composite gadget immediately after initial bind"

grep -Fq 'write_text_file(function_path + "/protocol", "1");' "$EXAMPLE_SRC" ||
    fail "example_usb_hid setup must keep keyboard HID boot protocol=1"

grep -Fq 'write_text_file(function_path + "/subclass", "1");' "$EXAMPLE_SRC" ||
    fail "example_usb_hid setup must keep keyboard HID boot subclass=1"

echo "usb HID keyboard protocol checks passed"
