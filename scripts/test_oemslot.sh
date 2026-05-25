#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT_DIR/overlay/etc/init.d/S20oemslot"

if [ ! -f "$SCRIPT" ]; then
    echo "missing $SCRIPT" >&2
    exit 1
fi

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

MOUNT_LOG="$TMP_DIR/mount.log"
MOUNT_BIN="$TMP_DIR/mount"
cat > "$MOUNT_BIN" <<'EOF'
#!/bin/sh
echo "$@" >> "$MOUNT_LOG"
exit 0
EOF
chmod +x "$MOUNT_BIN"

run_case() {
    name="$1"
    cmdline="$2"
    want_status="$3"
    want_device="$4"

    : > "$MOUNT_LOG"
    cmdline_path="$TMP_DIR/cmdline-$name"
    mountpoint="$TMP_DIR/oem-$name"
    printf '%s\n' "$cmdline" > "$cmdline_path"

    set +e
    CMDLINE_PATH="$cmdline_path" MOUNT_BIN="$MOUNT_BIN" OEM_MOUNTPOINT="$mountpoint" MOUNT_LOG="$MOUNT_LOG" "$SCRIPT" start >/dev/null 2>"$TMP_DIR/err-$name"
    status="$?"
    set -e

    if [ "$want_status" = 0 ]; then
        if [ "$status" -ne 0 ]; then
            echo "$name: got exit $status, want success" >&2
            cat "$TMP_DIR/err-$name" >&2
            exit 1
        fi
        if ! grep -q -- "$want_device $mountpoint" "$MOUNT_LOG"; then
            echo "$name: mount log did not contain $want_device $mountpoint" >&2
            cat "$MOUNT_LOG" >&2
            exit 1
        fi
    else
        if [ "$status" -eq 0 ]; then
            echo "$name: got success, want failure" >&2
            exit 1
        fi
        if [ -s "$MOUNT_LOG" ]; then
            echo "$name: mount was called on failure" >&2
            cat "$MOUNT_LOG" >&2
            exit 1
        fi
    fi
}

run_case slot_a 'console=ttyS0 root=PARTLABEL=rootfs_a aiden.slot_suffix=_a quiet' 0 '/dev/block/by-name/oem_a'
run_case slot_b 'aiden.slot_suffix=_b root=PARTLABEL=rootfs_b' 0 '/dev/block/by-name/oem_b'
run_case missing 'console=ttyS0 root=PARTLABEL=rootfs_a' 1 ''
run_case invalid 'console=ttyS0 aiden.slot_suffix=_c root=PARTLABEL=rootfs_c' 1 ''

echo "S20oemslot tests passed"
