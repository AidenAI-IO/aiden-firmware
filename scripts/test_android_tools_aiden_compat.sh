#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
PKG_DIR="$ROOT_DIR/pico-sdk/sysdrv/tools/board/buildroot/android-tools-aiden"
VERSION="30.0.5p1"
TARBALL="$PKG_DIR/android-tools-aiden-$VERSION.tar.xz"

if [ ! -f "$TARBALL" ]; then
    echo "android-tools-aiden source tarball is missing: $TARBALL" >&2
    exit 1
fi

WORK_DIR=$(mktemp -d)
cleanup() {
    rm -rf "$WORK_DIR"
}
trap cleanup EXIT INT TERM

tar -C "$WORK_DIR" -xf "$TARBALL"
SRC_DIR="$WORK_DIR/android-tools-aiden-$VERSION"

for patch_file in "$PKG_DIR"/*.patch; do
    patch -d "$SRC_DIR" -p1 --batch --silent < "$patch_file"
done

client_source="$SRC_DIR/vendor/core/adb/client/file_sync_client.cpp"
compression_header="$SRC_DIR/vendor/core/adb/compression_utils.h"

if grep -nF 'std::span' "$client_source" "$compression_header"; then
    echo "android-tools-aiden patches must remove std::span from adb sync/decompression code" >&2
    exit 1
fi

if ! grep -Fq 'CharSpan buffer_span' "$client_source"; then
    echo "file_sync_client.cpp must construct decoder buffers with CharSpan" >&2
    exit 1
fi

if ! grep -Fq 'using CharSpan = adb_compat::span<char>;' "$compression_header"; then
    echo "compression_utils.h must define CharSpan via the GCC 8.3 compatibility shim" >&2
    exit 1
fi

echo "android-tools-aiden compatibility patch tests passed"
