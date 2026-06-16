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
PENDING_TMP_DIR=
trap 'rm -rf "$TMP_DIR" "$LATE_TMP_DIR" "$MOUNT_TMP_DIR" "$PENDING_TMP_DIR"' EXIT INT TERM

OTA_BIN="$TMP_DIR/ota"
LOG_PATH="$TMP_DIR/ota.log"
USERDATA_DIR="$TMP_DIR/userdata"
LOCK_DIR="$TMP_DIR/ota-health.lock"

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
LOCK_DIR="$LOCK_DIR" \
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

status_output=$(
    LOG_PATH="$LOG_PATH" \
    LOCK_DIR="$LOCK_DIR" \
    "$SCRIPT" status
)
if ! printf '%s\n' "$status_output" | grep -q '^ota=idle$'; then
    echo "ota status must report idle after the one-shot worker exits" >&2
    printf '%s\n' "$status_output" >&2
    exit 1
fi

PENDING_TMP_DIR=$(mktemp -d)
PENDING_OTA_BIN="$PENDING_TMP_DIR/missing-ota"
PENDING_LOG_PATH="$PENDING_TMP_DIR/ota.log"
PENDING_USERDATA_DIR="$PENDING_TMP_DIR/userdata"
PENDING_LOCK_DIR="$PENDING_TMP_DIR/ota-health.lock"
PENDING_SLEEP_BIN="$PENDING_TMP_DIR/sleep"
PENDING_SLEEP_LOG="$PENDING_TMP_DIR/sleep.ppids"
mkdir -p "$PENDING_USERDATA_DIR"
cat > "$PENDING_SLEEP_BIN" <<'EOF'
#!/bin/sh
echo "$PPID" >> "$PENDING_SLEEP_LOG"
sleep 1
EOF
chmod +x "$PENDING_SLEEP_BIN"

OTA_BIN="$PENDING_OTA_BIN" \
ENV_RUN_BIN="$ROOT_DIR/overlay/oem/usr/bin/aiden-env-run" \
LOG_PATH="$PENDING_LOG_PATH" \
USERDATA_DIR="$PENDING_USERDATA_DIR" \
USERDATA_REQUIRE_MOUNT=0 \
LOCK_DIR="$PENDING_LOCK_DIR" \
SLEEP_BIN="$PENDING_SLEEP_BIN" \
WAIT_TIMEOUT=5 \
PENDING_SLEEP_LOG="$PENDING_SLEEP_LOG" \
"$SCRIPT" start >/dev/null

deadline=$(( $(date +%s) + 5 ))
while [ ! -s "$PENDING_SLEEP_LOG" ]; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "pending ota health worker did not enter wait loop" >&2
        [ -f "$PENDING_LOG_PATH" ] && cat "$PENDING_LOG_PATH" >&2
        exit 1
    fi
    sleep 1
done

OTA_BIN="$PENDING_OTA_BIN" \
ENV_RUN_BIN="$ROOT_DIR/overlay/oem/usr/bin/aiden-env-run" \
LOG_PATH="$PENDING_LOG_PATH" \
USERDATA_DIR="$PENDING_USERDATA_DIR" \
USERDATA_REQUIRE_MOUNT=0 \
LOCK_DIR="$PENDING_LOCK_DIR" \
SLEEP_BIN="$PENDING_SLEEP_BIN" \
WAIT_TIMEOUT=5 \
PENDING_SLEEP_LOG="$PENDING_SLEEP_LOG" \
"$SCRIPT" start >/dev/null

sleep 1
pending_worker_count=$(sort -u "$PENDING_SLEEP_LOG" | wc -l | tr -d ' ')
if [ "$pending_worker_count" != "1" ]; then
    echo "repeated ota start launched $pending_worker_count pending health workers" >&2
    cat "$PENDING_SLEEP_LOG" >&2
    exit 1
fi

pending_status=$(
    LOG_PATH="$PENDING_LOG_PATH" \
    LOCK_DIR="$PENDING_LOCK_DIR" \
    "$SCRIPT" status
)
if ! printf '%s\n' "$pending_status" | grep -q '^ota=running$'; then
    echo "ota status must report the running pending health worker" >&2
    printf '%s\n' "$pending_status" >&2
    exit 1
fi

MOUNT_TMP_DIR=$(mktemp -d)
MOUNT_OTA_BIN="$MOUNT_TMP_DIR/ota"
MOUNT_LOG_PATH="$MOUNT_TMP_DIR/ota.log"
MOUNT_USERDATA_DIR="$MOUNT_TMP_DIR/userdata"
MOUNT_MOUNTS_PATH="$MOUNT_TMP_DIR/mounts"
MOUNT_LOCK_DIR="$MOUNT_TMP_DIR/ota-health.lock"
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
LOCK_DIR="$MOUNT_LOCK_DIR" \
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

LATE_TMP_DIR=$(mktemp -d)
LATE_OTA_BIN="$LATE_TMP_DIR/ota"
LATE_LOG_PATH="$LATE_TMP_DIR/ota.log"
LATE_USERDATA_DIR="$LATE_TMP_DIR/userdata"
LATE_LOCK_DIR="$LATE_TMP_DIR/ota-health.lock"
mkdir -p "$LATE_USERDATA_DIR"

OTA_BIN="$LATE_OTA_BIN" \
ENV_RUN_BIN="$ROOT_DIR/overlay/oem/usr/bin/aiden-env-run" \
LOG_PATH="$LATE_LOG_PATH" \
USERDATA_DIR="$LATE_USERDATA_DIR" \
USERDATA_REQUIRE_MOUNT=0 \
LOCK_DIR="$LATE_LOCK_DIR" \
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
