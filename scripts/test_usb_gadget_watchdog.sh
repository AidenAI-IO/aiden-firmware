#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
WATCHDOG="$ROOT_DIR/overlay/etc/init.d/S60usb_ecm_watchdog"

fail() {
    echo "$*" >&2
    exit 1
}

sh -n "$WATCHDOG" || fail "S60usb_ecm_watchdog has invalid shell syntax"

grep -Fq 'STATE_FILE=' "$WATCHDOG" ||
    fail "watchdog must persist last refresh diagnostics"

grep -Fq 'log_snapshot()' "$WATCHDOG" ||
    fail "watchdog must expose USB/HID/ECM state snapshots"

grep -Fq 'write_refresh_state()' "$WATCHDOG" ||
    fail "watchdog must write structured refresh state"

grep -Fq 'reset_composite()' "$WATCHDOG" ||
    fail "watchdog must recover by resetting the composite gadget"

grep -Fq 'GADGET_SWITCH_LOCK_DIR=/run/aiden_dynamic_keyboard.lock' "$WATCHDOG" ||
    fail "watchdog and iOS keyboard isolation must share the gadget switch lock"

grep -Fq 'PLANNED_SWITCH_GRACE_FILE=/run/aiden_dynamic_keyboard.grace' "$WATCHDOG" ||
    fail "watchdog and iOS keyboard isolation must share the planned switch grace deadline"

grep -Fq 'planned_switch_grace_active()' "$WATCHDOG" ||
    fail "watchdog must recognize planned profile switch grace windows"

grep -Fq 'planned profile switch grace active; clearing ECM failure count' "$WATCHDOG" ||
    fail "watchdog must clear stale ECM failures during planned profile switches"

grep -Fq 'ECM probe interrupted by planned profile switch; ignoring failure' "$WATCHDOG" ||
    fail "watchdog must ignore probes interrupted by a newly started profile switch"

grep -Fq 'USB composite reset skipped during planned profile switch grace' "$WATCHDOG" ||
    fail "all refresh paths must recheck planned switch grace before resetting"

grep -Fq '[ "$reason" = "manual refresh" ] && return 0' "$WATCHDOG" ||
    fail "HID open recovery must retry normally when refresh is skipped during grace"

grep -Fq 'return 2' "$WATCHDOG" ||
    fail "automatic recovery must distinguish a grace skip from a failed reset"

grep -Fq 'if [ "$reset_result" -eq 2 ]' "$WATCHDOG" ||
    fail "a grace-skipped automatic reset must not start the recovery cooldown"

grep -Fq 'acquire_gadget_switch_lock || return 1' "$WATCHDOG" ||
    fail "watchdog must defer resets while the iOS keyboard profile is switching"

grep -Fq 'release_gadget_switch_lock' "$WATCHDOG" ||
    fail "watchdog must release the shared gadget switch lock after reset"

grep -Fq 'reset_composite "ECM stall"' "$WATCHDOG" ||
    fail "watchdog_main must reset the composite gadget on ECM stalls"

grep -Fq 'announce_usb_reenumeration' "$WATCHDOG" ||
    fail "watchdog must announce a planned composite reset before ECM disappears"

grep -Fq '/api/bluetooth/wake/usb-reenumeration' "$WATCHDOG" ||
    fail "watchdog must use the loopback-only BLE wake endpoint"

grep -Fq 'UDC state changed:' "$WATCHDOG" ||
    fail "watchdog_main must track host UDC state transitions"

grep -Fq 'watchdog started while UDC already configured; leaving HID session intact' "$WATCHDOG" ||
    fail "watchdog_main must preserve an already configured HID session on startup"

grep -Fq 'UDC configured transition observed; leaving HID session intact' "$WATCHDOG" ||
    fail "watchdog_main must preserve healthy host-configured HID transitions"

if grep -Fq 'configured_refresh_pending=1' "$WATCHDOG" ||
    grep -Fq 'HOST_CONFIG_REFRESH_DELAY=' "$WATCHDOG" ||
    grep -Fq 'reset_composite "host configured"' "$WATCHDOG"; then
    fail "watchdog must not automatically refresh the composite gadget after healthy host configuration"
fi

grep -Fq 'refresh) reset_composite "manual refresh" ;;' "$WATCHDOG" ||
    fail "watchdog must expose a manual refresh command for stale HID sessions"

grep -Fq 'snapshot) log_snapshot "manual snapshot"; cat "$STATE_FILE" 2>/dev/null || true ;;' "$WATCHDOG" ||
    fail "watchdog must expose a non-refreshing snapshot command for incident capture"

grep -Fq 'run_with_error_capture sh -c' "$WATCHDOG" ||
    fail "watchdog must capture composite recovery stderr"

grep -Fq 'printf "\n" > "$1"' "$WATCHDOG" ||
    fail "watchdog must unbind the UDC during composite recovery"

grep -Fq 'printf "%s\n" "$1" > "$2"' "$WATCHDOG" ||
    fail "watchdog must rebind the UDC during composite recovery"

grep -Fq 'run_with_error_capture ifconfig usb0' "$WATCHDOG" ||
    fail "watchdog must capture usb0 restore errors"

grep -Fq 'CAPTURED_ERROR' "$WATCHDOG" ||
    fail "watchdog must include captured command errors in structured logs"

grep -Fq 'UDC did not reach configured state after composite reset' "$WATCHDOG" ||
    fail "watchdog reset must fail when the gadget never reaches configured"

grep -Fq 'ifconfig usb0 "$USB0_ADDR" netmask "$USB0_NETMASK" up' "$WATCHDOG" ||
    fail "watchdog must restore usb0 after composite recovery"

grep -Fq 'log_snapshot "before reset ($reason)"' "$WATCHDOG" ||
    fail "watchdog must log state before composite recovery"

grep -Fq 'log_snapshot "after reset ($reason)"' "$WATCHDOG" ||
    fail "watchdog must log state after composite recovery"

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
