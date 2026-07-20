#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
INIT_SCRIPT="$ROOT_DIR/overlay/etc/init.d/S49usbhid"
DHCP_SCRIPT="$ROOT_DIR/overlay/etc/init.d/S55aiden_usb_dhcp"

fail() {
    echo "$*" >&2
    exit 1
}

sh -n "$INIT_SCRIPT" || fail "S49usbhid has invalid shell syntax"
sh -n "$DHCP_SCRIPT" || fail "S55aiden_usb_dhcp has invalid shell syntax"

grep -Fq 'echo full-speed > "$GADGET_DIR/max_speed"' "$INIT_SCRIPT" ||
    fail "iOS control profile must keep the E11 full-speed baseline"

grep -Fq 'echo 1 > "$GADGET_DIR/functions/hid.usb0/protocol"' "$INIT_SCRIPT" ||
    fail "keyboard must use boot protocol=1"

grep -Fq 'echo 1 > "$GADGET_DIR/functions/hid.usb0/subclass"' "$INIT_SCRIPT" ||
    fail "keyboard must use boot subclass=1"

grep -Fq 'echo 8 > "$GADGET_DIR/functions/hid.usb0/report_length"' "$INIT_SCRIPT" ||
    fail "keyboard must keep the standard 8-byte report"

grep -Fq 'echo 1 > "$GADGET_DIR/functions/hid.usb0/no_out_endpoint"' "$INIT_SCRIPT" ||
    fail "keyboard must not expose an unused OUT endpoint"

grep -Fq 'echo 0 > "$GADGET_DIR/functions/hid.usb1/protocol"' "$INIT_SCRIPT" ||
    fail "touchscreen digitizer must use protocol=0"

grep -Fq 'echo 0 > "$GADGET_DIR/functions/hid.usb1/subclass"' "$INIT_SCRIPT" ||
    fail "touchscreen digitizer must use subclass=0"

grep -Fq 'echo 6 > "$GADGET_DIR/functions/hid.usb1/report_length"' "$INIT_SCRIPT" ||
    fail "touchscreen digitizer must keep the six-byte single-contact report"

grep -Fq 'echo 1 > "$GADGET_DIR/functions/hid.usb1/no_out_endpoint"' "$INIT_SCRIPT" ||
    fail "touchscreen digitizer must not expose an unused OUT endpoint"

grep -Fq "printf '\\x05\\x0d\\x09\\x04" "$INIT_SCRIPT" ||
    fail "hid.usb1 must advertise Digitizers / Touch Screen rather than Generic Desktop / Mouse"

if grep -Fq "printf '\\x05\\x01\\x09\\x02" "$INIT_SCRIPT"; then
    fail "touchscreen experiment must not advertise a Generic Desktop / Mouse collection"
fi

for forbidden in \
    'mkdir -p "$GADGET_DIR/functions/hid.usb2"' \
    'mkdir -p "$GADGET_DIR/functions/ecm.usb0"' \
    'ln -s "$GADGET_DIR/functions/ecm.usb0"'; do
    if grep -Fq "$forbidden" "$INIT_SCRIPT"; then
        fail "keyboard + pointer control contains forbidden topology: $forbidden"
    fi
done

bind_count=$(grep -Fc 'echo "$UDC" > "$GADGET_DIR/UDC"' "$INIT_SCRIPT")
[ "$bind_count" -eq 1 ] || fail "keyboard + pointer control must bind the UDC exactly once"

if grep -Fq 'reenumerate_composite' "$INIT_SCRIPT"; then
    fail "keyboard + pointer control must not re-enumerate after its initial bind"
fi

awk \
    -v keyboard_link='ln -s "$GADGET_DIR/functions/hid.usb0" "$GADGET_DIR/configs/c.1/hid.usb0"' \
    -v pointer_link='ln -s "$GADGET_DIR/functions/hid.usb1" "$GADGET_DIR/configs/c.1/hid.usb1"' \
    -v bind='echo "$UDC" > "$GADGET_DIR/UDC"' '
    index($0, keyboard_link) { keyboard=NR; next }
    index($0, pointer_link) { pointer=NR; next }
    pointer && $0 ~ /^[[:space:]]*sleep[[:space:]]+1[[:space:]]*$/ { delayed=NR; next }
    index($0, bind) { bound=NR; exit }
    END { exit(keyboard && pointer && delayed && bound && keyboard < pointer && pointer < delayed && delayed < bound ? 0 : 1) }
' "$INIT_SCRIPT" || fail "profile must link keyboard first, touchscreen second, wait one second, and bind once"

grep -Fq 'pointer_mode = "touchscreen"' "$ROOT_DIR/overlay/userdata/agent/agent.toml" ||
    fail "experimental agent configuration must emit touchscreen digitizer reports"

grep -Fq 'ECM_FUNC=/sys/kernel/config/usb_gadget/aiden_hid/functions/ecm.usb0' "$DHCP_SCRIPT" ||
    fail "USB DHCP startup must identify whether the active profile exposes ECM"

grep -Fq 'if [ ! -d "$ECM_FUNC" ]; then' "$DHCP_SCRIPT" ||
    fail "USB DHCP startup must exit immediately when the active profile omits ECM"

echo "iOS keyboard + touchscreen control checks passed"
