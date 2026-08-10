#!/bin/sh
# S41dhcpcd must start dhcpcd detached by default (-b), because the SDK's
# blocking form stalls rcS for dhcpcd's full 30s lease timeout on a board whose
# only interface has not associated yet.
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT_DIR/overlay/etc/init.d/S41dhcpcd"
BOOT_CONF="$ROOT_DIR/overlay/etc/aiden_boot.conf"

for path in "$SCRIPT" "$BOOT_CONF"; do
    [ -f "$path" ] || { echo "missing file: $path" >&2; exit 1; }
done
[ -x "$SCRIPT" ] || { echo "S41dhcpcd must be executable" >&2; exit 1; }

grep -Eq '^DHCPCD_BACKGROUND=' "$BOOT_CONF" \
    || { echo "aiden_boot.conf must define DHCPCD_BACKGROUND" >&2; exit 1; }

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

fail() { echo "FAIL: $*" >&2; exit 1; }

mkdir -p "$TMP_DIR/bin"
cat > "$TMP_DIR/bin/start-stop-daemon" <<'STUB'
#!/bin/sh
echo "$@" >> "$SSD_LOG"
STUB
chmod +x "$TMP_DIR/bin/start-stop-daemon"

SSD_LOG="$TMP_DIR/ssd.log"
export SSD_LOG

# Point the script at a fixture dhcpcd.conf so the "config absent" guard passes.
touch "$TMP_DIR/dhcpcd.conf"
sed "s#^CONFIG=.*#CONFIG=$TMP_DIR/dhcpcd.conf#" "$SCRIPT" > "$TMP_DIR/S41dhcpcd"

run_start() {
    printf 'DHCPCD_BACKGROUND=%s\n' "$1" > "$TMP_DIR/boot.conf"
    : > "$SSD_LOG"
    PATH="$TMP_DIR/bin:$PATH" BOOT_CONF="$TMP_DIR/boot.conf" \
        sh "$TMP_DIR/S41dhcpcd" start >/dev/null
}

# Default / explicit 1: detach immediately.
run_start 1
grep -q -- '-b -f' "$SSD_LOG" || fail "expected '-b -f' with DHCPCD_BACKGROUND=1, got: $(cat "$SSD_LOG")"

# Escape hatch: 0 restores the SDK's blocking invocation.
run_start 0
if grep -q -- ' -b ' "$SSD_LOG" || grep -q -- ' -b$' "$SSD_LOG"; then
    fail "-b must not be passed when DHCPCD_BACKGROUND=0: $(cat "$SSD_LOG")"
fi
grep -q -- '-f' "$SSD_LOG" || fail "config path must still be passed: $(cat "$SSD_LOG")"

# With no boot.conf at all the default must still be the non-blocking form.
: > "$SSD_LOG"
PATH="$TMP_DIR/bin:$PATH" BOOT_CONF="$TMP_DIR/absent.conf" \
    sh "$TMP_DIR/S41dhcpcd" start >/dev/null
grep -q -- '-b -f' "$SSD_LOG" || fail "default (no boot.conf) must pass -b, got: $(cat "$SSD_LOG")"

# A missing dhcpcd.conf must remain a clean no-op, as in the SDK script.
: > "$SSD_LOG"
sed "s#^CONFIG=.*#CONFIG=$TMP_DIR/no-such.conf#" "$SCRIPT" > "$TMP_DIR/S41missing"
PATH="$TMP_DIR/bin:$PATH" sh "$TMP_DIR/S41missing" start >/dev/null \
    || fail "missing dhcpcd.conf must exit 0"
[ ! -s "$SSD_LOG" ] || fail "must not start dhcpcd without a config: $(cat "$SSD_LOG")"

echo "PASS: S41dhcpcd non-blocking start"
