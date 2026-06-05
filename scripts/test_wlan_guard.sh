#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GUARD_SCRIPT="$ROOT_DIR/overlay/oem/usr/bin/wlan_guard.sh"
GUARD_INIT="$ROOT_DIR/overlay/etc/init.d/S43wlan_guard"

if [ ! -x "$GUARD_SCRIPT" ]; then
    echo "missing executable wlan_guard.sh" >&2
    exit 1
fi

if [ ! -x "$GUARD_INIT" ]; then
    echo "missing executable S43wlan_guard init script" >&2
    exit 1
fi

if grep -R "wifi_opt" "$GUARD_SCRIPT" "$GUARD_INIT"; then
    echo "wlan_guard must not use legacy wifi_opt naming" >&2
    exit 1
fi

if ! grep -q 'WLAN_GUARD_IFACE:-wlan0' "$GUARD_SCRIPT"; then
    echo "wlan_guard must default to wlan0" >&2
    exit 1
fi

if ! grep -q 'ip route show default' "$GUARD_SCRIPT"; then
    echo "wlan_guard must derive gateway from the routing table" >&2
    exit 1
fi

if grep -q '192\.168\.50\.1' "$GUARD_SCRIPT"; then
    echo "wlan_guard must not hard-code a default gateway" >&2
    exit 1
fi

if ! grep -q 'wpa_cli -i "$IFACE" reassociate' "$GUARD_SCRIPT"; then
    echo "wlan_guard must reassociate wlan0 during recovery" >&2
    exit 1
fi

if ! grep -q 'iw dev "$IFACE" set power_save off' "$GUARD_SCRIPT"; then
    echo "wlan_guard must disable WLAN power save" >&2
    exit 1
fi

if ! grep -q 'GUARD_SCRIPT=/oem/usr/bin/wlan_guard.sh' "$GUARD_INIT"; then
    echo "S43wlan_guard must launch /oem/usr/bin/wlan_guard.sh" >&2
    exit 1
fi

if ! grep -q 'ENABLE_WLAN_GUARD:=1' "$GUARD_INIT"; then
    echo "S43wlan_guard must default to enabled via ENABLE_WLAN_GUARD" >&2
    exit 1
fi

echo "wlan_guard tests passed"
