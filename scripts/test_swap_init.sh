#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT_DIR/overlay/etc/init.d/S51swap"
CONFIG="$ROOT_DIR/overlay/etc/aiden_swap.conf"

if [ ! -x "$SCRIPT" ]; then
    echo "missing executable swap init script: $SCRIPT" >&2
    exit 1
fi

for expected in \
    'ENABLE_SWAP:=1' \
    'SWAP_FILE:=/userdata/swapfile' \
    'SWAP_MOUNT_POINT:=/userdata' \
    'SWAP_REQUIRE_MOUNT:=1' \
    'SWAP_SIZE_MB:=256' \
    'SWAP_SWAPPINESS:=15' \
    'SWAP_BACKGROUND:=1'; do
    if ! grep -q "$expected" "$CONFIG"; then
        echo "swap config missing default: $expected" >&2
        exit 1
    fi
done

if sh "$SCRIPT" invalid >"/dev/null" 2>"${TMPDIR:-/tmp}/aiden-swap-usage.$$"; then
    echo "invalid swap command must fail" >&2
    rm -f "${TMPDIR:-/tmp}/aiden-swap-usage.$$"
    exit 1
fi
if ! grep -q '{start|stop|restart|reload|status}' "${TMPDIR:-/tmp}/aiden-swap-usage.$$"; then
    echo "swap usage must document reload" >&2
    cat "${TMPDIR:-/tmp}/aiden-swap-usage.$$" >&2
    rm -f "${TMPDIR:-/tmp}/aiden-swap-usage.$$"
    exit 1
fi
rm -f "${TMPDIR:-/tmp}/aiden-swap-usage.$$"

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

MOCK_BIN="$TMP_DIR/bin"
SWAP_FILE="$TMP_DIR/userdata/swapfile"
PROC_SWAPS="$TMP_DIR/proc_swaps"
MOUNTS_PATH="$TMP_DIR/mounts"
SWAPPINESS_PATH="$TMP_DIR/swappiness"
COMMAND_LOG="$TMP_DIR/commands.log"
mkdir -p "$MOCK_BIN" "$(dirname "$SWAP_FILE")"
printf 'Filename Type Size Used Priority\n' > "$PROC_SWAPS"
printf '/dev/test %s ext4 rw 0 0\n' "$(dirname "$SWAP_FILE")" > "$MOUNTS_PATH"
: > "$SWAPPINESS_PATH"
: > "$COMMAND_LOG"

cat > "$MOCK_BIN/mkswap" <<'EOF'
#!/bin/sh
printf 'mkswap %s\n' "$1" >> "$COMMAND_LOG"
EOF

cat > "$MOCK_BIN/swapon" <<'EOF'
#!/bin/sh
printf 'swapon %s\n' "$1" >> "$COMMAND_LOG"
printf '%s file 1020 0 -2\n' "$1" >> "$MOCK_PROC_SWAPS"
EOF

cat > "$MOCK_BIN/swapoff" <<'EOF'
#!/bin/sh
printf 'swapoff %s\n' "$1" >> "$COMMAND_LOG"
if [ "${MOCK_SWAPOFF_FAIL:-0}" = "1" ]; then
    exit 1
fi
tmp="${MOCK_PROC_SWAPS}.tmp"
grep -v "^$1 " "$MOCK_PROC_SWAPS" > "$tmp"
mv "$tmp" "$MOCK_PROC_SWAPS"
EOF
chmod +x "$MOCK_BIN/mkswap" "$MOCK_BIN/swapon" "$MOCK_BIN/swapoff"

# Activation is backgrounded by default (see SWAP_BACKGROUND); pin it to
# synchronous here so these assertions stay deterministic. The background path
# has its own coverage at the end of this file.
run_swap() {
    SWAP_CONFIG_FILE="$TMP_DIR/missing.conf" \
    SWAP_BACKGROUND=0 \
    ENABLE_SWAP=1 \
    SWAP_FILE="$SWAP_FILE" \
    SWAP_MOUNT_POINT="$(dirname "$SWAP_FILE")" \
    SWAP_REQUIRE_MOUNT=1 \
    SWAP_SIZE_MB=1 \
    SWAP_SWAPPINESS=15 \
    PROC_SWAPS="$PROC_SWAPS" \
    MOUNTS_PATH="$MOUNTS_PATH" \
    SWAPPINESS_PATH="$SWAPPINESS_PATH" \
    MKSWAP_BIN="$MOCK_BIN/mkswap" \
    SWAPON_BIN="$MOCK_BIN/swapon" \
    SWAPOFF_BIN="$MOCK_BIN/swapoff" \
    COMMAND_LOG="$COMMAND_LOG" \
    MOCK_PROC_SWAPS="$PROC_SWAPS" \
    sh "$SCRIPT" "$1"
}

run_swap start >/dev/null

if [ "$(wc -c < "$SWAP_FILE" | tr -d '[:space:]')" != "1048576" ]; then
    echo "swap init did not allocate the requested size" >&2
    exit 1
fi
if stat -c '%a' "$SWAP_FILE" >/dev/null 2>&1; then
    swap_mode="$(stat -c '%a' "$SWAP_FILE")"
else
    swap_mode="$(stat -f '%Lp' "$SWAP_FILE")"
fi
if [ "$swap_mode" != "600" ]; then
    echo "swapfile permissions must be 0600" >&2
    exit 1
fi
if [ "$(cat "$SWAPPINESS_PATH")" != "15" ]; then
    echo "swap init did not configure swappiness" >&2
    exit 1
fi
if ! grep -q "^$SWAP_FILE " "$PROC_SWAPS"; then
    echo "swap init did not activate the swapfile" >&2
    exit 1
fi

commands_after_first_start="$(wc -l < "$COMMAND_LOG")"
run_swap start >/dev/null
if [ "$(wc -l < "$COMMAND_LOG")" != "$commands_after_first_start" ]; then
    echo "repeated start must not recreate or reactivate an active swapfile" >&2
    exit 1
fi

missing_swappiness_path="$TMP_DIR/missing/swappiness"
if SWAP_CONFIG_FILE="$TMP_DIR/missing.conf" \
    ENABLE_SWAP=1 \
    SWAP_FILE="$SWAP_FILE" \
    SWAP_MOUNT_POINT="$(dirname "$SWAP_FILE")" \
    SWAP_REQUIRE_MOUNT=1 \
    SWAP_SIZE_MB=1 \
    SWAP_SWAPPINESS=15 \
    PROC_SWAPS="$PROC_SWAPS" \
    MOUNTS_PATH="$MOUNTS_PATH" \
    SWAPPINESS_PATH="$missing_swappiness_path" \
    sh "$SCRIPT" start >"$TMP_DIR/swappiness.out" 2>"$TMP_DIR/swappiness.err"; then
    echo "active swap start must fail when swappiness cannot be configured" >&2
    exit 1
fi
if ! grep -q 'cannot set swappiness' "$TMP_DIR/swappiness.err"; then
    echo "active swap start did not explain swappiness failure" >&2
    cat "$TMP_DIR/swappiness.err" >&2
    exit 1
fi

commands_before_failed_restart="$(wc -l < "$COMMAND_LOG")"
if SWAP_CONFIG_FILE="$TMP_DIR/missing.conf" \
    ENABLE_SWAP=1 \
    SWAP_FILE="$SWAP_FILE" \
    SWAP_MOUNT_POINT="$(dirname "$SWAP_FILE")" \
    SWAP_REQUIRE_MOUNT=1 \
    SWAP_SIZE_MB=1 \
    SWAP_SWAPPINESS=15 \
    PROC_SWAPS="$PROC_SWAPS" \
    MOUNTS_PATH="$MOUNTS_PATH" \
    SWAPPINESS_PATH="$SWAPPINESS_PATH" \
    MKSWAP_BIN="$MOCK_BIN/mkswap" \
    SWAPON_BIN="$MOCK_BIN/swapon" \
    SWAPOFF_BIN="$MOCK_BIN/swapoff" \
    COMMAND_LOG="$COMMAND_LOG" \
    MOCK_PROC_SWAPS="$PROC_SWAPS" \
    MOCK_SWAPOFF_FAIL=1 \
    sh "$SCRIPT" restart >"$TMP_DIR/restart.out" 2>"$TMP_DIR/restart.err"; then
    echo "restart must fail when swapoff fails" >&2
    exit 1
fi
new_commands="$(tail -n "+$((commands_before_failed_restart + 1))" "$COMMAND_LOG")"
case "$new_commands" in
    "swapoff $SWAP_FILE") ;;
    *)
        echo "restart continued after swapoff failure: $new_commands" >&2
        exit 1
        ;;
esac

status_output="$(run_swap status)"
case "$status_output" in
    *"swap=active"*"size_mb=1"*"swappiness=15"*) ;;
    *)
        echo "unexpected active status: $status_output" >&2
        exit 1
        ;;
esac

run_swap stop >/dev/null
if grep -q "^$SWAP_FILE " "$PROC_SWAPS"; then
    echo "swap stop did not deactivate the swapfile" >&2
    exit 1
fi

rm -f "$SWAP_FILE"
: > "$MOUNTS_PATH"
if run_swap start >"$TMP_DIR/unmounted.out" 2>"$TMP_DIR/unmounted.err"; then
    echo "swap start must fail when the target filesystem is not mounted" >&2
    exit 1
fi
if [ -e "$SWAP_FILE" ]; then
    echo "swap start created a file on an unmounted target path" >&2
    exit 1
fi
if ! grep -q 'swap mount point is not mounted' "$TMP_DIR/unmounted.err"; then
    echo "swap start did not explain the missing mount" >&2
    cat "$TMP_DIR/unmounted.err" >&2
    exit 1
fi

# --- background activation (the default) ----------------------------------
# swapon must not block rcS: start() returns immediately and the swapfile is
# activated by a background worker.
printf '/dev/test %s ext4 rw 0 0\n' "$(dirname "$SWAP_FILE")" > "$MOUNTS_PATH"
printf 'Filename Type Size Used Priority\n' > "$PROC_SWAPS"
rm -f "$SWAP_FILE"
: > "$COMMAND_LOG"

run_swap_bg() {
    SWAP_CONFIG_FILE="$TMP_DIR/missing.conf" \
    SWAP_BACKGROUND=1 \
    ENABLE_SWAP=1 \
    SWAP_FILE="$SWAP_FILE" \
    SWAP_MOUNT_POINT="$(dirname "$SWAP_FILE")" \
    SWAP_REQUIRE_MOUNT=1 \
    SWAP_SIZE_MB=1 \
    SWAP_SWAPPINESS=15 \
    PROC_SWAPS="$PROC_SWAPS" \
    MOUNTS_PATH="$MOUNTS_PATH" \
    SWAPPINESS_PATH="$SWAPPINESS_PATH" \
    MKSWAP_BIN="$MOCK_BIN/mkswap" \
    SWAPON_BIN="$MOCK_BIN/swapon" \
    SWAPOFF_BIN="$MOCK_BIN/swapoff" \
    BOOT_TIMELINE_HELPER="$TMP_DIR/no-timeline.sh" \
    COMMAND_LOG="$COMMAND_LOG" \
    MOCK_PROC_SWAPS="$PROC_SWAPS" \
    sh "$SCRIPT" "$1"
}

bg_output="$(run_swap_bg start)"
case "$bg_output" in
    *"in background"*) ;;
    *)
        echo "background start must say so: $bg_output" >&2
        exit 1
        ;;
esac

# Swappiness is applied up front, not deferred to the worker.
if [ "$(cat "$SWAPPINESS_PATH")" != "15" ]; then
    echo "background start must still configure swappiness synchronously" >&2
    exit 1
fi

# The worker finishes shortly after; poll rather than assuming a fixed delay.
activated=0
i=0
while [ "$i" -lt 100 ]; do
    if grep -q "^$SWAP_FILE " "$PROC_SWAPS"; then
        activated=1
        break
    fi
    sleep 0.1 2>/dev/null || sleep 1
    i=$((i + 1))
done
if [ "$activated" != "1" ]; then
    echo "background worker never activated the swapfile" >&2
    cat "$COMMAND_LOG" >&2
    exit 1
fi
if [ "$(wc -c < "$SWAP_FILE" | tr -d '[:space:]')" != "1048576" ]; then
    echo "background worker did not allocate the requested size" >&2
    exit 1
fi

# A disabled background flag must remain synchronous for operators who need
# swap guaranteed before later services start.
grep -q 'SWAP_BACKGROUND' "$SCRIPT" || {
    echo "S51swap must honour SWAP_BACKGROUND" >&2
    exit 1
}

echo "swap init tests passed"
