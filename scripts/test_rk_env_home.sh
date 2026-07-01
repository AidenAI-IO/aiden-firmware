#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT_DIR/overlay/etc/profile.d/RkEnv.sh"

if [ ! -f "$SCRIPT" ]; then
	echo "missing $SCRIPT" >&2
	exit 1
fi

if grep -q 'export HOME=/oem' "$SCRIPT"; then
	echo "RkEnv.sh must not override HOME to /oem" >&2
	exit 1
fi

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

OUT="$TMP_DIR/out"

env -i \
	HOME=/root \
	PATH=/bin:/usr/bin \
	LD_LIBRARY_PATH=/usr/lib \
	/bin/sh -c ". \"$SCRIPT\"; printf 'HOME=%s\nPATH=%s\nLD_LIBRARY_PATH=%s\n' \"\$HOME\" \"\$PATH\" \"\$LD_LIBRARY_PATH\"" > "$OUT"

if ! grep -q '^HOME=/root$' "$OUT"; then
	echo "RkEnv.sh changed HOME unexpectedly" >&2
	cat "$OUT" >&2
	exit 1
fi

if ! grep -q '^PATH=/bin:/usr/bin:/oem:/oem/bin:/oem/usr/bin:/oem/sbin:/oem/usr/sbin$' "$OUT"; then
	echo "RkEnv.sh did not append OEM paths as expected" >&2
	cat "$OUT" >&2
	exit 1
fi

if ! grep -q '^LD_LIBRARY_PATH=/oem/usr/lib:/oem/lib:/usr/lib$' "$OUT"; then
	echo "RkEnv.sh did not prepend OEM library paths as expected" >&2
	cat "$OUT" >&2
	exit 1
fi

echo "RkEnv home tests passed"
