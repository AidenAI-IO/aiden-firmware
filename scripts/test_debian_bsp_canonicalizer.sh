#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly CANONICALIZER=${REPO_ROOT}/scripts/debian-stage3/canonicalize-bsp.py
readonly CRC_TABLE_SOURCE=${REPO_ROOT}/pico-sdk/sysdrv/source/uboot/u-boot/tools/rockchip/boot_merger.c
readonly TEST_ROOT=$(mktemp -d)
trap 'rm -rf "${TEST_ROOT}"' EXIT

python3 - \
    "${TEST_ROOT}/fit-single.img" \
    "${TEST_ROOT}/fit-multiple.img" \
    "${TEST_ROOT}/fit-invalid.img" \
    "${TEST_ROOT}/loader.bin" \
    "${CRC_TABLE_SOURCE}" <<'PY'
import pathlib
import re
import struct
import sys

single_fit_path = pathlib.Path(sys.argv[1])
multiple_fit_path = pathlib.Path(sys.argv[2])
invalid_fit_path = pathlib.Path(sys.argv[3])
loader_path = pathlib.Path(sys.argv[4])
crc_source = pathlib.Path(sys.argv[5]).read_text(encoding="utf-8")

def write_fit(path, reservations):
    total_size = 160
    reserve_offset = 40
    structure_offset = reserve_offset + 16 * (len(reservations) + 1)
    fit = bytearray(total_size)
    struct.pack_into(
        ">10I", fit, 0, 0xD00DFEED, total_size, structure_offset,
        144, reserve_offset, 17, 16, 0, 16, 24
    )
    for index, (address, size) in enumerate(reservations):
        struct.pack_into(">QQ", fit, reserve_offset + index * 16, address, size)
    path.write_bytes(fit)

write_fit(single_fit_path, [(0x7F1234500000, 160)])
write_fit(
    multiple_fit_path,
    [(0x7F1234500000, 160), (0x7F12344FF000, 160)],
)
write_fit(
    invalid_fit_path,
    [(0x7F1234500000, 160), (0x7F12344FF000, 159)],
)

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
    --fit "${TEST_ROOT}/fit-single.img" \
    --fit "${TEST_ROOT}/fit-multiple.img" \
    --crc-table-source "${CRC_TABLE_SOURCE}" \
    --loader "${TEST_ROOT}/loader.bin"
cp "${TEST_ROOT}/fit-single.img" "${TEST_ROOT}/fit-single.pass1"
cp "${TEST_ROOT}/fit-multiple.img" "${TEST_ROOT}/fit-multiple.pass1"
cp "${TEST_ROOT}/loader.bin" "${TEST_ROOT}/loader.pass1"
"${CANONICALIZER}" \
    --check \
    --source-date-epoch 1767360516 \
    --fit "${TEST_ROOT}/fit-single.img" \
    --fit "${TEST_ROOT}/fit-multiple.img" \
    --crc-table-source "${CRC_TABLE_SOURCE}" \
    --loader "${TEST_ROOT}/loader.bin"
"${CANONICALIZER}" \
    --source-date-epoch 1767360516 \
    --fit "${TEST_ROOT}/fit-single.img" \
    --fit "${TEST_ROOT}/fit-multiple.img" \
    --crc-table-source "${CRC_TABLE_SOURCE}" \
    --loader "${TEST_ROOT}/loader.bin"
cmp "${TEST_ROOT}/fit-single.pass1" "${TEST_ROOT}/fit-single.img"
cmp "${TEST_ROOT}/fit-multiple.pass1" "${TEST_ROOT}/fit-multiple.img"
cmp "${TEST_ROOT}/loader.pass1" "${TEST_ROOT}/loader.bin"

python3 - \
    "${TEST_ROOT}/fit-single.img" \
    "${TEST_ROOT}/fit-multiple.img" \
    "${TEST_ROOT}/loader.bin" <<'PY'
import datetime
import pathlib
import struct
import sys

single_fit = pathlib.Path(sys.argv[1]).read_bytes()
multiple_fit = pathlib.Path(sys.argv[2]).read_bytes()
loader = pathlib.Path(sys.argv[3]).read_bytes()
assert struct.unpack_from(">QQ", single_fit, 40) == (0, 0)
assert struct.unpack_from(">QQ", multiple_fit, 40) == (0, 0)
assert struct.unpack_from(">QQ", multiple_fit, 56) == (0, 0)
timestamp = datetime.datetime.fromtimestamp(1767360516, datetime.timezone.utc)
assert loader[14:21] == struct.pack(
    "<HBBBBB", timestamp.year, timestamp.month, timestamp.day,
    timestamp.hour, timestamp.minute, timestamp.second
)
PY

if "${CANONICALIZER}" \
    --source-date-epoch 1767360516 \
    --fit "${TEST_ROOT}/fit-invalid.img" >/dev/null 2>&1; then
    echo "BSP canonicalizer accepted an unexpected FIT reservation" >&2
    exit 1
fi

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
