#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
CONTROL_SCRIPT="$ROOT_DIR/overlay/oem/usr/bin/aiden-dynamic-keyboard"
CONF_FILE="$ROOT_DIR/overlay/etc/aiden_dynamic_keyboard.conf"

fail() {
	echo "$*" >&2
	exit 1
}

sh -n "$CONTROL_SCRIPT" || fail "aiden-dynamic-keyboard has invalid shell syntax"
sh -n "$CONF_FILE" || fail "aiden_dynamic_keyboard.conf has invalid shell syntax"
[ -x "$CONTROL_SCRIPT" ] || fail "aiden-dynamic-keyboard must be executable"

grep -Fq "trap release_lock EXIT" "$CONTROL_SCRIPT" ||
	fail "gadget switch lock cleanup must remain in the EXIT trap"
grep -Fq "trap 'exit 129' HUP" "$CONTROL_SCRIPT" ||
	fail "HUP must terminate before releasing the gadget switch lock"
grep -Fq "trap 'exit 130' INT" "$CONTROL_SCRIPT" ||
	fail "INT must terminate before releasing the gadget switch lock"
grep -Fq "trap 'exit 143' TERM" "$CONTROL_SCRIPT" ||
	fail "TERM must terminate before releasing the gadget switch lock"

keyboard_ecm_branch=$(awk '/link_keyboard_ecm_profile\(\)/,/^}/' "$CONTROL_SCRIPT")
printf '%s\n' "$keyboard_ecm_branch" | grep -Fq 'hid.usb0' ||
	fail "base profile must keep the standard keyboard"
printf '%s\n' "$keyboard_ecm_branch" | grep -Fq 'hid.usb2' ||
	fail "base profile must keep the Consumer Control function"
printf '%s\n' "$keyboard_ecm_branch" | grep -Fq 'ecm.usb0' ||
	fail "base profile must keep ECM"
if printf '%s\n' "$keyboard_ecm_branch" | grep -Fq 'hid.usb1'; then
	fail "base profile must omit the pointer"
fi

isolated_branch=$(awk '/link_isolated_profile\(\)/,/^}/' "$CONTROL_SCRIPT")
printf '%s\n' "$isolated_branch" | grep -Fq 'link_keyboard_ecm_profile' ||
	fail "isolated profile must use the keyboard + Consumer Control + ECM base"

current_mode_branch=$(awk '/current_mode\(\)/,/^}/' "$CONTROL_SCRIPT")
if printf '%s\n' "$current_mode_branch" | grep -Fq '[ ! -L "$CONFIG_DIR/hid.usb2" ]'; then
	fail "isolated mode detection must require Consumer Control"
fi

normal_branch=$(awk '/link_normal_profile\(\)/,/^}/' "$CONTROL_SCRIPT")
printf '%s\n' "$normal_branch" | grep -Fq 'link_keyboard_ecm_profile' ||
	fail "normal profile must restore the keyboard + Consumer Control + ECM base first"
printf '%s\n' "$normal_branch" | grep -Fq 'hid.usb1' ||
	fail "normal profile must restore the pointer last"

grep -Fq 'echo "" > "$UDC_PATH"' "$CONTROL_SCRIPT" ||
	fail "profile switching must unbind the single UDC"
grep -Fq 'detach_seconds=0.1' "$CONTROL_SCRIPT" ||
	fail "profile isolation must keep the short detach interval"
grep -Fq 'detach_seconds=$DYNAMIC_KEYBOARD_RESTORE_DETACH_SECONDS' "$CONTROL_SCRIPT" ||
	fail "normal profile restore must use the longer configured detach interval"
grep -Eq '^DYNAMIC_KEYBOARD_RESTORE_DETACH_SECONDS=0\.[1-9][0-9]*$' "$CONF_FILE" ||
	fail "normal profile restore must configure a sub-second detach interval"
grep -Eq '^DYNAMIC_KEYBOARD_WATCHDOG_GRACE_SECONDS=[1-9][0-9]*$' "$CONF_FILE" ||
	fail "planned profile switches must configure a positive watchdog grace window"
grep -Fq 'WATCHDOG_GRACE_FILE=/run/aiden_dynamic_keyboard.grace' "$CONTROL_SCRIPT" ||
	fail "profile switching must publish the shared watchdog grace deadline"
[ "$(grep -Ec '^[[:space:]]*grace_tmp=.*WATCHDOG_GRACE_FILE' "$CONTROL_SCRIPT")" -eq 1 ] ||
	fail "watchdog grace publication must use a same-directory temporary file"
grep -Fq 'mv -f "$grace_tmp" "$WATCHDOG_GRACE_FILE"' "$CONTROL_SCRIPT" ||
	fail "watchdog grace publication must atomically replace the shared deadline"
[ "$(grep -Ec '^[[:space:]]*mark_watchdog_grace$' "$CONTROL_SCRIPT")" -ge 2 ] ||
	fail "profile switching must refresh watchdog grace before and after re-enumeration"
grep -Fq 'wait_for_configuration "$udc"' "$CONTROL_SCRIPT" ||
	fail "profile switching must wait for iOS to configure the gadget"

normal_pid=$(sed -n 's/^DYNAMIC_KEYBOARD_NORMAL_ID_PRODUCT=//p' "$CONF_FILE")
isolated_pid=$(sed -n 's/^DYNAMIC_KEYBOARD_ISOLATED_ID_PRODUCT=//p' "$CONF_FILE")
[ -n "$normal_pid" ] && [ -n "$isolated_pid" ] && [ "$normal_pid" != "$isolated_pid" ] ||
	fail "normal and isolated profiles must use distinct product IDs"

echo "dynamic keyboard checks passed"
