#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
WATCHDOG="$ROOT_DIR/overlay/etc/init.d/S60usb_ecm_watchdog"

fail() {
    echo "$*" >&2
    exit 1
}

sh -n "$WATCHDOG" || fail "S60usb_ecm_watchdog has invalid shell syntax"

grep -Fq 'HOST_CONFIG_REFRESH_DELAY=' "$WATCHDOG" ||
    fail "watchdog must delay host-configured HID session refresh"

grep -Fq 'reset_composite()' "$WATCHDOG" ||
    fail "watchdog must recover by resetting the composite gadget"

grep -Fq 'reset_composite "ECM stall"' "$WATCHDOG" ||
    fail "watchdog_main must reset the composite gadget on ECM stalls"

grep -Fq 'UDC state changed:' "$WATCHDOG" ||
    fail "watchdog_main must track host UDC state transitions"

grep -Fq 'configured_refresh_pending=1' "$WATCHDOG" ||
    fail "watchdog_main must schedule a refresh after host configuration"

grep -Fq 'reset_composite "host configured"' "$WATCHDOG" ||
    fail "watchdog_main must refresh the composite gadget after host configuration"

grep -Fq 'refresh) reset_composite "manual refresh" ;;' "$WATCHDOG" ||
    fail "watchdog must expose a manual refresh command for stale HID sessions"

grep -Fq 'echo "" > "$GADGET_UDC"' "$WATCHDOG" ||
    fail "watchdog must unbind the UDC during composite recovery"

grep -Fq 'echo "$UDC_NAME" > "$GADGET_UDC"' "$WATCHDOG" ||
    fail "watchdog must rebind the UDC during composite recovery"

grep -Fq 'UDC did not reach configured state after composite reset' "$WATCHDOG" ||
    fail "watchdog reset must fail when the gadget never reaches configured"

grep -Fq 'ifconfig usb0 "$USB0_ADDR" netmask "$USB0_NETMASK" up' "$WATCHDOG" ||
    fail "watchdog must restore usb0 after composite recovery"

grep -Fq '"$0" _watchdog </dev/null >/dev/null 2>&1 &' "$WATCHDOG" ||
    fail "watchdog must start the loop through a detached _watchdog subprocess"

grep -Fq '_watchdog) watchdog_main ;;' "$WATCHDOG" ||
    fail "watchdog must expose the internal _watchdog subprocess command"

if grep -Fq 'rm "$GADGET_CONFIG/ecm.usb0"' "$WATCHDOG" ||
    grep -Fq 'ln -s "$GADGET_ECM_FUNC"' "$WATCHDOG"; then
    fail "watchdog must not recover by only unlinking/relinking ECM"
fi

if grep -Fq '/dev/hidg' "$WATCHDOG" ||
    grep -Fq 'exec 3>"$dev"' "$WATCHDOG"; then
    fail "watchdog must not probe HID gadget nodes by opening /dev/hidg*"
fi

echo "usb gadget watchdog checks passed"
