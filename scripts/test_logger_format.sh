#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
HELPER="$ROOT_DIR/overlay/oem/usr/lib/aiden-log.sh"

sh -n "$HELPER"
. "$HELPER"

message='status=1 detail="first line"
second line'
line="$(aiden_log WARN Agent 'Process Supervisor' 'Process Exited' "$message")"

printf '%s\n' "$line" | grep -Eq \
    '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z \[WARN\] \[agent\] \[process_supervisor\] process_exited message=".*"$'

[ "$(printf '%s\n' "$line" | wc -l | tr -d ' ')" = "1" ]
printf '%s\n' "$line" | grep -Fq 'detail=\"first line\"\nsecond line'

for script in \
    overlay/etc/init.d/S53agent \
    overlay/etc/init.d/S52frame_service \
    overlay/etc/init.d/S53audio_service \
    overlay/etc/init.d/S54ota \
    overlay/etc/init.d/S53adb_server \
    overlay/etc/init.d/S50ntp_watchdog \
    overlay/etc/init.d/S60usb_ecm_watchdog \
    overlay/oem/usr/bin/wlan_guard.sh; do
    sh -n "$ROOT_DIR/$script"
    grep -Fq 'aiden-log.sh' "$ROOT_DIR/$script"
done

echo "logger format checks passed"
