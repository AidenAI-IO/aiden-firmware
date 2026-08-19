#!/usr/bin/env python3
"""Normalize nondeterministic timestamps in a constrained Stage 3 ext4 image."""

import argparse
import os
import struct


SUPERBLOCK_OFFSET = 1024
SUPERBLOCK_SIZE = 1024
EXT4_FEATURE_COMPAT_SPARSE_SUPER2 = 0x200
EXT4_FEATURE_INCOMPAT_64BIT = 0x80
EXT4_FEATURE_RO_COMPAT_SPARSE_SUPER = 0x1
EXT4_FEATURE_RO_COMPAT_METADATA_CSUM = 0x400


def read_u16(data, offset):
    return struct.unpack_from("<H", data, offset)[0]


def read_u32(data, offset):
    return struct.unpack_from("<I", data, offset)[0]


def write_u32(handle, offset, value):
    handle.seek(offset)
    handle.write(struct.pack("<I", value))


def ceil_div(numerator, denominator):
    return (numerator + denominator - 1) // denominator


def has_sparse_super(group):
    if group in (0, 1):
        return True
    for base in (3, 5, 7):
        value = group
        while value > 1 and value % base == 0:
            value //= base
        if value == 1:
            return True
    return False


def canonicalize(path, epoch):
    with open(path, "r+b", buffering=0) as handle:
        handle.seek(SUPERBLOCK_OFFSET)
        superblock = handle.read(SUPERBLOCK_SIZE)
        if len(superblock) != SUPERBLOCK_SIZE or read_u16(superblock, 0x38) != 0xEF53:
            raise ValueError("not an ext filesystem")

        compat = read_u32(superblock, 0x5C)
        incompat = read_u32(superblock, 0x60)
        ro_compat = read_u32(superblock, 0x64)
        if compat & EXT4_FEATURE_COMPAT_SPARSE_SUPER2:
            raise ValueError("sparse_super2 ext filesystems are not supported")
        if incompat & EXT4_FEATURE_INCOMPAT_64BIT:
            raise ValueError("64bit ext filesystems are not supported")
        if not ro_compat & EXT4_FEATURE_RO_COMPAT_SPARSE_SUPER:
            raise ValueError("the production sparse_super layout is required")
        if ro_compat & EXT4_FEATURE_RO_COMPAT_METADATA_CSUM:
            raise ValueError("metadata_csum ext filesystems are not supported")

        block_size = 1024 << read_u32(superblock, 0x18)
        blocks_count = read_u32(superblock, 0x04)
        first_data_block = read_u32(superblock, 0x14)
        blocks_per_group = read_u32(superblock, 0x20)
        inodes_count = read_u32(superblock, 0x00)
        inodes_per_group = read_u32(superblock, 0x28)
        inode_size = read_u16(superblock, 0x58)
        if not all((blocks_count, blocks_per_group, inodes_count, inodes_per_group)):
            raise ValueError("invalid ext filesystem geometry")
        if inode_size < 128 or inode_size > block_size or inode_size % 4:
            raise ValueError("unsupported inode size")

        block_groups = max(
            ceil_div(blocks_count - first_data_block, blocks_per_group),
            ceil_div(inodes_count, inodes_per_group),
        )
        descriptor_table = (first_data_block + 1) * block_size

        for group in range(block_groups):
            descriptor_offset = descriptor_table + group * 32
            handle.seek(descriptor_offset + 4)
            descriptor = handle.read(8)
            if len(descriptor) != 8:
                raise ValueError(f"short descriptor for block group {group}")
            inode_bitmap_block, inode_table_block = struct.unpack("<II", descriptor)
            if not inode_bitmap_block or not inode_table_block:
                raise ValueError(f"invalid inode metadata for block group {group}")

            handle.seek(inode_bitmap_block * block_size)
            inode_bitmap = handle.read(block_size)
            if len(inode_bitmap) != block_size:
                raise ValueError(f"short inode bitmap for block group {group}")

            group_inodes = min(inodes_per_group, inodes_count - group * inodes_per_group)
            for index in range(group_inodes):
                if not inode_bitmap[index // 8] & (1 << (index % 8)):
                    continue
                inode_offset = inode_table_block * block_size + index * inode_size
                for field_offset in (0x08, 0x0C, 0x10):
                    write_u32(handle, inode_offset + field_offset, epoch)

                if inode_size <= 128:
                    continue
                handle.seek(inode_offset + 0x80)
                extra_isize_data = handle.read(2)
                if len(extra_isize_data) != 2:
                    raise ValueError(f"short inode {group * inodes_per_group + index + 1}")
                extra_isize = struct.unpack("<H", extra_isize_data)[0]
                for required_size, field_offset in (
                    (8, 0x84),
                    (12, 0x88),
                    (16, 0x8C),
                    (24, 0x94),
                ):
                    if extra_isize >= required_size:
                        write_u32(handle, inode_offset + field_offset, 0)
                if extra_isize >= 20:
                    write_u32(handle, inode_offset + 0x90, epoch)

        superblock_offsets = [SUPERBLOCK_OFFSET]
        for group in range(1, block_groups):
            if not has_sparse_super(group):
                continue
            offset = (first_data_block + group * blocks_per_group) * block_size
            handle.seek(offset + 0x38)
            if handle.read(2) != struct.pack("<H", 0xEF53):
                raise ValueError(f"missing backup superblock in block group {group}")
            superblock_offsets.append(offset)

        for offset in superblock_offsets:
            write_u32(handle, offset + 0x2C, epoch)
            write_u32(handle, offset + 0x30, epoch)
            write_u32(handle, offset + 0x40, epoch)

        handle.flush()
        os.fsync(handle.fileno())


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("image")
    parser.add_argument("epoch", type=int)
    args = parser.parse_args()
    if args.epoch < 0 or args.epoch > 0xFFFFFFFF:
        parser.error("epoch must fit in an ext4 32-bit timestamp")
    canonicalize(args.image, args.epoch)


if __name__ == "__main__":
    main()
