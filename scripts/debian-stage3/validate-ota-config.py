#!/usr/bin/env python3
import argparse
import datetime as dt
import hashlib
import json
import pathlib
import re
import sys


VERSION_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
FORBIDDEN_STORAGE_KEYS = {
    "storage_mount_point",
    "storage_device_path",
    "storage_filesystem",
    "reserve_size_bytes",
    "reserve_safety_margin_bytes",
}


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def require_string(config: dict, key: str) -> str:
    value = config.get(key)
    if not isinstance(value, str) or not value:
        raise ValueError(f"{key} must be a non-empty string")
    return value


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Validate a Debian Stage 3 device OTA configuration against factory images."
    )
    parser.add_argument("--config", required=True, type=pathlib.Path)
    parser.add_argument("--boot-a", required=True, type=pathlib.Path)
    parser.add_argument("--boot-b", required=True, type=pathlib.Path)
    parser.add_argument("--oem", required=True, type=pathlib.Path)
    parser.add_argument("--rootfs", required=True, type=pathlib.Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    with args.config.open(encoding="utf-8") as stream:
        config = json.load(stream)
    if not isinstance(config, dict):
        raise ValueError("OTA configuration must be a JSON object")

    forbidden = sorted(FORBIDDEN_STORAGE_KEYS.intersection(config))
    if forbidden:
        raise ValueError(f"fixed production storage keys are not configurable: {', '.join(forbidden)}")

    version = require_string(config, "factory_version")
    if not VERSION_RE.fullmatch(version):
        raise ValueError("factory_version has an invalid format")
    build_time = require_string(config, "factory_build_time")
    try:
        dt.datetime.fromisoformat(build_time.replace("Z", "+00:00"))
    except ValueError as exc:
        raise ValueError("factory_build_time is not RFC3339") from exc
    require_string(config, "repo")
    require_string(config, "channel")
    if config.get("download_safety_margin_bytes") != 16 * 1024 * 1024:
        raise ValueError("download_safety_margin_bytes must be 16777216")

    hashes = config.get("factory_partition_hashes")
    if not isinstance(hashes, dict) or set(hashes) != {"a", "b"}:
        raise ValueError("factory_partition_hashes must contain exactly slots a and b")
    expected = {
        "a": {
            "boot": sha256(args.boot_a),
            "oem": sha256(args.oem),
            "rootfs": sha256(args.rootfs),
        },
        "b": {
            "boot": sha256(args.boot_b),
            "oem": sha256(args.oem),
            "rootfs": sha256(args.rootfs),
        },
    }
    for slot in ("a", "b"):
        actual = hashes.get(slot)
        if not isinstance(actual, dict) or set(actual) != {"boot", "oem", "rootfs"}:
            raise ValueError(f"factory_partition_hashes.{slot} must contain exactly boot, oem, and rootfs")
        for part in ("boot", "oem", "rootfs"):
            if actual.get(part) != expected[slot][part]:
                raise ValueError(
                    f"factory_partition_hashes.{slot}.{part} does not match the factory image"
                )

    print(f"factory_version={version}")
    print(f"factory_build_time={build_time}")
    for slot in ("a", "b"):
        for part in ("boot", "oem", "rootfs"):
            print(f"factory_partition_hashes.{slot}.{part}={expected[slot][part]}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"validate-ota-config.py: {exc}", file=sys.stderr)
        raise SystemExit(1)
