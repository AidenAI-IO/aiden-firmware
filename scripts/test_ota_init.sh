#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT_DIR/overlay/etc/init.d/S54ota"

if [ ! -f "$SCRIPT" ]; then
    echo "missing $SCRIPT" >&2
    exit 1
fi

TMP_DIR=$(mktemp -d)
trap 'if [ -f "$TMP_DIR/watchdog.pid" ]; then kill "$(cat "$TMP_DIR/watchdog.pid")" 2>/dev/null || true; fi; if [ -f "$TMP_DIR/ota.pid" ]; then kill "$(cat "$TMP_DIR/ota.pid")" 2>/dev/null || true; fi; rm -rf "$TMP_DIR"' EXIT INT TERM

OTA_BIN="$TMP_DIR/ota"
LOG_PATH="$TMP_DIR/ota.log"
PID_FILE="$TMP_DIR/ota.pid"
WATCHDOG_PID_FILE="$TMP_DIR/watchdog.pid"
USERDATA_DIR="$TMP_DIR/userdata"
RUN_DIR="$TMP_DIR/run"

cat > "$OTA_BIN" <<'EOF'
#!/bin/sh
echo "$@" >> "$OTA_DAEMON_LOG"
while :; do sleep 1; done
EOF
chmod +x "$OTA_BIN"

mkdir -p "$USERDATA_DIR" "$RUN_DIR"

OTA_BIN="$OTA_BIN" \
ENV_RUN_BIN="$ROOT_DIR/overlay/oem/usr/bin/aiden-env-run" \
LOG_PATH="$LOG_PATH" \
PID_FILE="$PID_FILE" \
WATCHDOG_PID_FILE="$WATCHDOG_PID_FILE" \
USERDATA_DIR="$USERDATA_DIR" \
OTA_DAEMON_LOG="$TMP_DIR/daemon.args" \
SLEEP_BIN=":" \
"$SCRIPT" start >/dev/null

deadline=$(( $(date +%s) + 5 ))
while [ ! -s "$PID_FILE" ] || [ ! -s "$WATCHDOG_PID_FILE" ] || [ ! -s "$TMP_DIR/daemon.args" ]; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "ota daemon did not start promptly without network carrier" >&2
        [ -f "$LOG_PATH" ] && cat "$LOG_PATH" >&2
        exit 1
    fi
    sleep 1
done

if ! grep -qx 'daemon' "$TMP_DIR/daemon.args"; then
    echo "ota daemon was not started with daemon subcommand" >&2
    cat "$TMP_DIR/daemon.args" >&2
    exit 1
fi

if ! grep -q 'starting' "$LOG_PATH"; then
    echo "ota startup log missing" >&2
    cat "$LOG_PATH" >&2
    exit 1
fi

service_pid=$(cat "$PID_FILE")
watchdog_pid=$(cat "$WATCHDOG_PID_FILE")

OTA_BIN="$OTA_BIN" \
ENV_RUN_BIN="$ROOT_DIR/overlay/oem/usr/bin/aiden-env-run" \
LOG_PATH="$LOG_PATH" \
PID_FILE="$PID_FILE" \
WATCHDOG_PID_FILE="$WATCHDOG_PID_FILE" \
USERDATA_DIR="$USERDATA_DIR" \
"$SCRIPT" stop >/dev/null

kill "$service_pid" "$watchdog_pid" 2>/dev/null || true
echo "S54ota tests passed"
