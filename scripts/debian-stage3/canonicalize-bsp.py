#!/usr/bin/env python3
import argparse
import datetime
import os
import pathlib
import re
import struct
import tempfile


FDT_MAGIC = 0xD00DFEED
FDT_HEADER_SIZE = 40
ROCKCHIP_LOADER_TAG = b"LDR "
ROCKCHIP_LOADER_TIME_OFFSET = 14


def load_rockchip_crc_table(path: pathlib.Path) -> tuple[int, ...]:
    source = path.read_text(encoding="utf-8")
    match = re.search(
        r"gTable_Crc32\s*\[256\]\s*=\s*\{(.*?)\};", source, re.DOTALL
    )
    if not match:
        raise ValueError(f"Rockchip CRC table is missing: {path}")
    table = tuple(int(value, 16) for value in re.findall(r"0x[0-9a-fA-F]+", match.group(1)))
    if len(table) != 256:
        raise ValueError(f"Rockchip CRC table has {len(table)} entries: {path}")
    return table


def rockchip_crc32(data: bytes, table: tuple[int, ...]) -> int:
    crc = 0
    for byte in data:
        crc = ((crc << 8) & 0xFFFFFFFF) ^ table[((crc >> 24) ^ byte) & 0xFF]
    return crc


def write_atomic(path: pathlib.Path, data: bytes) -> None:
    mode = path.stat().st_mode & 0o7777
    temporary_name = None
    try:
        with tempfile.NamedTemporaryFile(
            dir=path.parent, prefix=f".{path.name}.", delete=False
        ) as stream:
            temporary_name = stream.name
            os.fchmod(stream.fileno(), mode)
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary_name, path)
    finally:
        if temporary_name and os.path.exists(temporary_name):
            os.unlink(temporary_name)


def canonicalize_fit(path: pathlib.Path, check: bool) -> None:
    data = bytearray(path.read_bytes())
    if len(data) < FDT_HEADER_SIZE:
        raise ValueError(f"FIT is too small: {path}")
    header = struct.unpack_from(">10I", data)
    magic, total_size, _, _, reserve_offset = header[:5]
    if magic != FDT_MAGIC:
        raise ValueError(f"FIT magic is invalid: {path}")
    if total_size > len(data) or reserve_offset + 32 > total_size:
        raise ValueError(f"FIT reservation map is out of bounds: {path}")

    address, size = struct.unpack_from(">QQ", data, reserve_offset)
    next_address, next_size = struct.unpack_from(">QQ", data, reserve_offset + 16)
    if address == 0 and size == 0:
        return
    if check:
        raise ValueError(f"FIT contains a host memory reservation: {path}")
    if size != total_size or next_address != 0 or next_size != 0:
        raise ValueError(f"FIT has an unexpected reservation map: {path}")

    struct.pack_into(">QQ", data, reserve_offset, 0, 0)
    write_atomic(path, data)


def expected_loader_time(source_date_epoch: int) -> bytes:
    timestamp = datetime.datetime.fromtimestamp(
        source_date_epoch, datetime.timezone.utc
    )
    return struct.pack(
        "<HBBBBB",
        timestamp.year,
        timestamp.month,
        timestamp.day,
        timestamp.hour,
        timestamp.minute,
        timestamp.second,
    )


def canonicalize_loader(
    path: pathlib.Path,
    source_date_epoch: int,
    crc_table: tuple[int, ...],
    check: bool,
) -> None:
    data = bytearray(path.read_bytes())
    if len(data) < ROCKCHIP_LOADER_TIME_OFFSET + 7 + 4:
        raise ValueError(f"Rockchip loader is too small: {path}")
    if data[:4] != ROCKCHIP_LOADER_TAG:
        raise ValueError(f"Rockchip loader tag is invalid: {path}")

    stored_crc = struct.unpack_from("<I", data, len(data) - 4)[0]
    calculated_crc = rockchip_crc32(data[:-4], crc_table)
    if stored_crc != calculated_crc:
        raise ValueError(f"Rockchip loader CRC is invalid: {path}")

    expected_time = expected_loader_time(source_date_epoch)
    actual_time = bytes(
        data[ROCKCHIP_LOADER_TIME_OFFSET : ROCKCHIP_LOADER_TIME_OFFSET + 7]
    )
    if check:
        if actual_time != expected_time:
            raise ValueError(f"Rockchip loader timestamp is not canonical: {path}")
        return

    data[
        ROCKCHIP_LOADER_TIME_OFFSET : ROCKCHIP_LOADER_TIME_OFFSET + 7
    ] = expected_time
    struct.pack_into(
        "<I", data, len(data) - 4, rockchip_crc32(data[:-4], crc_table)
    )
    write_atomic(path, data)


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Canonicalize host-specific metadata in Rockchip BSP images"
    )
    parser.add_argument("--source-date-epoch", required=True, type=int)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--fit", action="append", default=[])
    parser.add_argument("--loader", action="append", default=[])
    parser.add_argument("--crc-table-source", type=pathlib.Path)
    args = parser.parse_args()
    if args.source_date_epoch < 0:
        parser.error("--source-date-epoch must be non-negative")
    if not args.fit and not args.loader:
        parser.error("at least one --fit or --loader path is required")
    if args.loader and args.crc_table_source is None:
        parser.error("--crc-table-source is required with --loader")

    for value in args.fit:
        canonicalize_fit(pathlib.Path(value), args.check)
    crc_table = (
        load_rockchip_crc_table(args.crc_table_source) if args.loader else ()
    )
    for value in args.loader:
        canonicalize_loader(
            pathlib.Path(value), args.source_date_epoch, crc_table, args.check
        )


if __name__ == "__main__":
    main()
