#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
INIT_SCRIPT="$ROOT_DIR/overlay/etc/init.d/S49usbhid"
CONTROL_SCRIPT="$ROOT_DIR/overlay/oem/usr/bin/aiden-dynamic-keyboard"
CONF_FILE="$ROOT_DIR/overlay/etc/aiden_dynamic_keyboard.conf"
AGENT_CONFIG="$ROOT_DIR/overlay/userdata/agent/agent.toml"

fail() {
	echo "$*" >&2
	exit 1
}

sh -n "$INIT_SCRIPT" || fail "S49usbhid has invalid shell syntax"
sh -n "$CONTROL_SCRIPT" || fail "aiden-dynamic-keyboard has invalid shell syntax"
sh -n "$CONF_FILE" || fail "aiden_dynamic_keyboard.conf has invalid shell syntax"
[ -x "$CONTROL_SCRIPT" ] || fail "aiden-dynamic-keyboard must be executable"

grep -Eq '^[[:space:]]*dynamic_keyboard[[:space:]]*=[[:space:]]*true([[:space:]]|$)' "$AGENT_CONFIG" ||
	fail "firmware agent.toml must enable the iOS dynamic keyboard profile"

grep -Fq 'dynamic_keyboard_control = "/oem/usr/bin/aiden-dynamic-keyboard"' "$AGENT_CONFIG" ||
	fail "firmware agent.toml must configure the dynamic keyboard controller"

grep -Fq 'if [ "$DYNAMIC_KEYBOARD" -ne 1 ]; then' "$INIT_SCRIPT" ||
	fail "S49usbhid must omit the keyboard link in dynamic mode"

grep -Fq 'write_dynamic_keyboard_state' "$INIT_SCRIPT" ||
	fail "S49usbhid must initialize keyboard-off state"

grep -Fq 'echo "" > "$UDC_PATH"' "$CONTROL_SCRIPT" ||
	fail "runtime switch must unbind the whole UDC"

grep -Fq 'unlink_functions' "$CONTROL_SCRIPT" ||
	fail "runtime switch must rebuild configuration links"

grep -Fq 'ln -s "$KEYBOARD_FUNC" "$CONFIG_DIR/hid.usb0"' "$CONTROL_SCRIPT" ||
	fail "keyboard-on profile must link the keyboard function"

grep -Fq "printf 'invalid\\n'" "$CONTROL_SCRIPT" ||
	fail "runtime switch must reject mixed or missing HID profile links"

grep -Fq 'link_ecm' "$CONTROL_SCRIPT" ||
	fail "both profiles must restore ECM"

grep -Fq 'POINTER_FUNC=$GADGET_DIR/functions/hid.usb1' "$CONTROL_SCRIPT" ||
	fail "keyboard-off profile must preserve the configured pointer function"

on_branch=$(awk 'index($0, "if [ \"$mode\" = on ]; then") { capture=1; next } capture && /^[[:space:]]*else$/ { exit } capture { print }' "$CONTROL_SCRIPT")
printf '%s\n' "$on_branch" | grep -Fq 'hid.usb0' ||
	fail "keyboard-on profile must link hid.usb0"
if printf '%s\n' "$on_branch" | grep -Fq 'hid.usb1'; then
	fail "keyboard-on profile must not expose the AssistiveTouch pointer"
fi

if awk '/link_ecm\(\)/,/^}/' "$CONTROL_SCRIPT" | grep -Fq 'hid.usb2'; then
	fail "dynamic iOS profiles must not expose Consumer Control during the isolation test"
fi

grep -Fq 'wait_for_configuration "$udc"' "$CONTROL_SCRIPT" ||
	fail "runtime switch must wait for host configuration"

grep -Fq '> "$DYNAMIC_KEYBOARD_STATE_FILE"' "$CONTROL_SCRIPT" ||
	fail "runtime switch must publish profile state for stale HID descriptor detection"

grep -Fq 'kill -0 "$owner"' "$CONTROL_SCRIPT" ||
	fail "runtime switch must recover a lock left by a killed controller"

off_pid=$(sed -n 's/^DYNAMIC_KEYBOARD_OFF_ID_PRODUCT=//p' "$CONF_FILE")
on_pid=$(sed -n 's/^DYNAMIC_KEYBOARD_ON_ID_PRODUCT=//p' "$CONF_FILE")
[ -n "$off_pid" ] && [ -n "$on_pid" ] && [ "$off_pid" != "$on_pid" ] ||
	fail "keyboard-off and keyboard-on must use distinct product IDs for iOS descriptor cache isolation"

grep -Fq 'Aiden Mouse ECM' "$CONF_FILE" ||
	fail "keyboard-off identity must advertise the restored mouse profile"

grep -Fq 'Aiden Keyboard ECM' "$CONF_FILE" ||
	fail "keyboard-on identity must advertise the isolated keyboard profile"

echo "dynamic keyboard checks passed"
