#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly CANONICALIZER=${REPO_ROOT}/scripts/debian-stage3/canonicalize-bsp.py
readonly CRC_TABLE_SOURCE=${REPO_ROOT}/pico-sdk/sysdrv/source/uboot/u-boot/tools/rockchip/boot_merger.c
readonly TEST_ROOT=$(mktemp -d)
trap 'rm -rf "${TEST_ROOT}"' EXIT

python3 - "${TEST_ROOT}/fit.img" "${TEST_ROOT}/loader.bin" "${CRC_TABLE_SOURCE}" <<'PY'
import pathlib
import re
import struct
import sys

fit_path = pathlib.Path(sys.argv[1])
loader_path = pathlib.Path(sys.argv[2])
crc_source = pathlib.Path(sys.argv[3]).read_text(encoding="utf-8")

fit = bytearray(128)
struct.pack_into(">10I", fit, 0, 0xD00DFEED, 128, 72, 96, 40, 17, 16, 0, 16, 24)
struct.pack_into(">QQ", fit, 40, 0x7F1234500000, 128)
fit_path.write_bytes(fit)

loader = bytearray(128)
loader[:4] = b"LDR "
struct.pack_into("<H", loader, 4, 102)
struct.pack_into("<HBBBBB", loader, 14, 2026, 8, 17, 22, 45, 1)

table_match = re.search(
    r"gTable_Crc32\s*\[256\]\s*=\s*\{(.*?)\};", crc_source, re.DOTALL
)
table = [int(value, 16) for value in re.findall(r"0x[0-9a-fA-F]+", table_match.group(1))]
assert len(table) == 256

def crc32(data):
    crc = 0
    for byte in data:
        crc = ((crc << 8) & 0xFFFFFFFF) ^ table[((crc >> 24) ^ byte) & 0xFF]
    return crc

struct.pack_into("<I", loader, len(loader) - 4, crc32(loader[:-4]))
loader_path.write_bytes(loader)
PY

"${CANONICALIZER}" \
    --source-date-epoch 1767360516 \
    --fit "${TEST_ROOT}/fit.img" \
    --crc-table-source "${CRC_TABLE_SOURCE}" \
    --loader "${TEST_ROOT}/loader.bin"
cp "${TEST_ROOT}/fit.img" "${TEST_ROOT}/fit.pass1"
cp "${TEST_ROOT}/loader.bin" "${TEST_ROOT}/loader.pass1"
"${CANONICALIZER}" \
    --check \
    --source-date-epoch 1767360516 \
    --fit "${TEST_ROOT}/fit.img" \
    --crc-table-source "${CRC_TABLE_SOURCE}" \
    --loader "${TEST_ROOT}/loader.bin"
"${CANONICALIZER}" \
    --source-date-epoch 1767360516 \
    --fit "${TEST_ROOT}/fit.img" \
    --crc-table-source "${CRC_TABLE_SOURCE}" \
    --loader "${TEST_ROOT}/loader.bin"
cmp "${TEST_ROOT}/fit.pass1" "${TEST_ROOT}/fit.img"
cmp "${TEST_ROOT}/loader.pass1" "${TEST_ROOT}/loader.bin"

python3 - "${TEST_ROOT}/fit.img" "${TEST_ROOT}/loader.bin" <<'PY'
import datetime
import pathlib
import struct
import sys

fit = pathlib.Path(sys.argv[1]).read_bytes()
loader = pathlib.Path(sys.argv[2]).read_bytes()
assert struct.unpack_from(">QQ", fit, 40) == (0, 0)
timestamp = datetime.datetime.fromtimestamp(1767360516, datetime.timezone.utc)
assert loader[14:21] == struct.pack(
    "<HBBBBB", timestamp.year, timestamp.month, timestamp.day,
    timestamp.hour, timestamp.minute, timestamp.second
)
PY

cp "${TEST_ROOT}/loader.bin" "${TEST_ROOT}/bad-loader.bin"
printf '\001' | dd of="${TEST_ROOT}/bad-loader.bin" bs=1 seek=32 conv=notrunc status=none
if "${CANONICALIZER}" \
    --check \
    --source-date-epoch 1767360516 \
    --crc-table-source "${CRC_TABLE_SOURCE}" \
    --loader "${TEST_ROOT}/bad-loader.bin" >/dev/null 2>&1; then
    echo "BSP canonicalizer accepted an invalid loader CRC" >&2
    exit 1
fi

echo "Debian BSP canonicalizer checks passed"
