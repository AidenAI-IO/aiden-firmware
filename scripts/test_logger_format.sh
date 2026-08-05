#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
HELPER="$ROOT_DIR/overlay/oem/usr/lib/aiden-log.sh"

sh -n "$HELPER"
. "$HELPER"

message='status=1 detail="first line"
second line'
LINE_FILE="$(mktemp "${TMPDIR:-/tmp}/aiden-logger-format.XXXXXX")"
trap 'rm -f "$LINE_FILE"' 0 HUP INT TERM
aiden_log WARN Agent 'Process Supervisor' 'Process Exited' "$message" > "$LINE_FILE"

grep -Eq \
    '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z \[WARN\] \[agent\] \[process_supervisor\] process_exited message=".*"$' \
    "$LINE_FILE"

[ "$(wc -l < "$LINE_FILE" | tr -d ' ')" = "1" ]
grep -Fq 'detail=\"first line\"\nsecond line' "$LINE_FILE"

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
    grep -Eq 'aiden_log(_to_file)?[[:space:]]' "$ROOT_DIR/$script"
done

echo "logger format checks passed"
