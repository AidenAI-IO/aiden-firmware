#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BOOTSTRAP="$ROOT_DIR/overlay/etc/init.d/S50ios_hid_session"

fail() {
	echo "$*" >&2
	exit 1
}

sh -n "$BOOTSTRAP" || fail "S50ios_hid_session has invalid shell syntax"
[ -x "$BOOTSTRAP" ] || fail "S50ios_hid_session must be executable"

grep -Fq 'CONTROL=/oem/usr/bin/aiden-dynamic-keyboard' "$BOOTSTRAP" ||
	fail "iOS session bootstrap must use the shared profile controller"
grep -Fq '[ "$POINTER_MODE" = absolute ]' "$BOOTSTRAP" ||
	fail "iOS session bootstrap must be limited to absolute pointer mode"
grep -Fq 'wait_for_configuration' "$BOOTSTRAP" ||
	fail "iOS session bootstrap must wait for the safe S49 UDC bind"
grep -Fq '"$CONTROL" cycle' "$BOOTSTRAP" ||
	fail "iOS session bootstrap must run pointer-free -> normal profile refresh"
grep -Fq ': > "$READY_FILE"' "$BOOTSTRAP" ||
	fail "successful iOS session bootstrap must publish readiness"
grep -Fq 'start) refresh_session 0 ;;' "$BOOTSTRAP" ||
	fail "boot startup may skip cleanly when no USB host is attached"
grep -Fq 'refresh) refresh_session 1 ;;' "$BOOTSTRAP" ||
	fail "explicit refresh must fail when no configured USB host is available"

echo "iOS HID session bootstrap checks passed"
