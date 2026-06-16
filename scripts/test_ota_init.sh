#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT_DIR/overlay/etc/init.d/S54ota"

if [ ! -f "$SCRIPT" ]; then
    echo "missing $SCRIPT" >&2
    exit 1
fi

TMP_DIR=$(mktemp -d)
LATE_TMP_DIR=
MOUNT_TMP_DIR=
trap 'rm -rf "$TMP_DIR" "$LATE_TMP_DIR" "$MOUNT_TMP_DIR"' EXIT INT TERM

OTA_BIN="$TMP_DIR/ota"
LOG_PATH="$TMP_DIR/ota.log"
USERDATA_DIR="$TMP_DIR/userdata"

cat > "$OTA_BIN" <<'EOF'
#!/bin/sh
echo "$@" >> "$OTA_DAEMON_LOG"
exit 0
EOF
chmod +x "$OTA_BIN"

mkdir -p "$USERDATA_DIR"

OTA_BIN="$OTA_BIN" \
ENV_RUN_BIN="$ROOT_DIR/overlay/oem/usr/bin/aiden-env-run" \
LOG_PATH="$LOG_PATH" \
USERDATA_DIR="$USERDATA_DIR" \
USERDATA_REQUIRE_MOUNT=0 \
OTA_DAEMON_LOG="$TMP_DIR/daemon.args" \
SLEEP_BIN=":" \
WAIT_TIMEOUT=1 \
"$SCRIPT" start >/dev/null

deadline=$(( $(date +%s) + 5 ))
while [ ! -s "$TMP_DIR/daemon.args" ]; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "ota health did not run promptly" >&2
        [ -f "$LOG_PATH" ] && cat "$LOG_PATH" >&2
        exit 1
    fi
    sleep 1
done

if ! grep -qx 'health' "$TMP_DIR/daemon.args"; then
    echo "ota health was not processed through the health subcommand" >&2
    cat "$TMP_DIR/daemon.args" >&2
    exit 1
fi

if ! grep -q 'processing pending health' "$LOG_PATH"; then
    echo "ota health startup log missing" >&2
    cat "$LOG_PATH" >&2
    exit 1
fi

deadline=$(( $(date +%s) + 5 ))
while ! grep -q 'health processing exited with status 0' "$LOG_PATH"; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "ota health did not finish promptly" >&2
        cat "$LOG_PATH" >&2
        exit 1
    fi
    sleep 1
done

if ! grep -q 'health processing exited with status 0' "$LOG_PATH"; then
    echo "ota health completion log missing" >&2
    cat "$LOG_PATH" >&2
    exit 1
fi

status_output=$(
    LOG_PATH="$LOG_PATH" "$SCRIPT" status
)
if ! printf '%s\n' "$status_output" | grep -q '^ota=one-shot$'; then
    echo "ota status must report one-shot mode" >&2
    printf '%s\n' "$status_output" >&2
    exit 1
fi

LATE_TMP_DIR=$(mktemp -d)
LATE_OTA_BIN="$LATE_TMP_DIR/ota"
LATE_LOG_PATH="$LATE_TMP_DIR/ota.log"
LATE_USERDATA_DIR="$LATE_TMP_DIR/userdata"
mkdir -p "$LATE_USERDATA_DIR"

MOUNT_TMP_DIR=$(mktemp -d)
MOUNT_OTA_BIN="$MOUNT_TMP_DIR/ota"
MOUNT_LOG_PATH="$MOUNT_TMP_DIR/ota.log"
MOUNT_USERDATA_DIR="$MOUNT_TMP_DIR/userdata"
MOUNT_MOUNTS_PATH="$MOUNT_TMP_DIR/mounts"
cat > "$MOUNT_OTA_BIN" <<'EOF'
#!/bin/sh
echo "$@" >> "$OTA_DAEMON_LOG"
exit 0
EOF
chmod +x "$MOUNT_OTA_BIN"
mkdir -p "$MOUNT_USERDATA_DIR"
: > "$MOUNT_MOUNTS_PATH"

OTA_BIN="$MOUNT_OTA_BIN" \
ENV_RUN_BIN="$ROOT_DIR/overlay/oem/usr/bin/aiden-env-run" \
LOG_PATH="$MOUNT_LOG_PATH" \
USERDATA_DIR="$MOUNT_USERDATA_DIR" \
MOUNTS_PATH="$MOUNT_MOUNTS_PATH" \
OTA_DAEMON_LOG="$MOUNT_TMP_DIR/daemon.args" \
SLEEP_BIN=":" \
WAIT_TIMEOUT=1 \
"$SCRIPT" start >/dev/null

deadline=$(( $(date +%s) + 5 ))
while ! { [ -f "$MOUNT_LOG_PATH" ] && grep -q 'userdata mount unavailable after 1s' "$MOUNT_LOG_PATH"; }; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "ota health did not wait for mounted userdata" >&2
        [ -f "$MOUNT_LOG_PATH" ] && cat "$MOUNT_LOG_PATH" >&2
        exit 1
    fi
    sleep 1
done

if [ -e "$MOUNT_TMP_DIR/daemon.args" ]; then
    echo "ota health ran before userdata was mounted" >&2
    cat "$MOUNT_TMP_DIR/daemon.args" >&2
    exit 1
fi

OTA_BIN="$LATE_OTA_BIN" \
ENV_RUN_BIN="$ROOT_DIR/overlay/oem/usr/bin/aiden-env-run" \
LOG_PATH="$LATE_LOG_PATH" \
USERDATA_DIR="$LATE_USERDATA_DIR" \
USERDATA_REQUIRE_MOUNT=0 \
OTA_DAEMON_LOG="$LATE_TMP_DIR/daemon.args" \
SLEEP_BIN="sleep" \
WAIT_TIMEOUT=5 \
"$SCRIPT" start >/dev/null

sleep 1
if [ -e "$LATE_TMP_DIR/daemon.args" ]; then
    echo "ota health ran before ota binary became available" >&2
    cat "$LATE_TMP_DIR/daemon.args" >&2
    exit 1
fi

cat > "$LATE_OTA_BIN" <<'EOF'
#!/bin/sh
echo "$@" >> "$OTA_DAEMON_LOG"
exit 0
EOF
chmod +x "$LATE_OTA_BIN"

deadline=$(( $(date +%s) + 5 ))
while [ ! -s "$LATE_TMP_DIR/daemon.args" ]; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "ota health did not run after late ota binary appeared" >&2
        [ -f "$LATE_LOG_PATH" ] && cat "$LATE_LOG_PATH" >&2
        exit 1
    fi
    sleep 1
done

if ! grep -qx 'health' "$LATE_TMP_DIR/daemon.args"; then
    echo "late ota health was not processed through the health subcommand" >&2
    cat "$LATE_TMP_DIR/daemon.args" >&2
    exit 1
fi

deadline=$(( $(date +%s) + 5 ))
while ! { [ -f "$LATE_LOG_PATH" ] && grep -q 'health processing exited with status 0' "$LATE_LOG_PATH"; }; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "late ota health did not finish promptly" >&2
        [ -f "$LATE_LOG_PATH" ] && cat "$LATE_LOG_PATH" >&2
        exit 1
    fi
    sleep 1
done

echo "S54ota tests passed"
