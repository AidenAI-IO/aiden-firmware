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
    fail "watchdog must persist structured diagnostics"

grep -Fq 'log_snapshot()' "$WATCHDOG" ||
    fail "watchdog must expose USB/HID/ECM state snapshots"

grep -Fq 'write_watchdog_state()' "$WATCHDOG" ||
    fail "watchdog must write structured diagnostic state"

grep -Fq 'ECM stall detected; preserving HID session (diagnostic only)' "$WATCHDOG" ||
    fail "watchdog_main must preserve HID when ECM probes fail"

grep -Fq 'write_watchdog_state "ECM stall" "observed"' "$WATCHDOG" ||
    fail "watchdog_main must persist suppressed ECM reset diagnostics"

if grep -Fq 'reset_composite' "$WATCHDOG" ||
    grep -Fq 'refresh)' "$WATCHDOG"; then
    fail "watchdog must not expose legacy composite reset paths"
fi

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

grep -Fq 'write_watchdog_state "watchdog start" "monitoring"' "$WATCHDOG" ||
    fail "watchdog start must replace legacy state with current diagnostic state"

grep -Fq 'write_watchdog_state "manual snapshot" "observed"' "$WATCHDOG" ||
    fail "watchdog must expose a non-mutating snapshot command for incident capture"

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
