#!/usr/bin/env python3
"""Validate the shared protocol fixtures without network access or secrets."""

from __future__ import annotations

import base64
import json
from pathlib import Path
import struct


ROOT = Path(__file__).resolve().parent


def load_json(name: str) -> dict:
    with (ROOT / name).open(encoding="utf-8") as stream:
        value = json.load(stream)
    if not isinstance(value, dict):
        raise AssertionError(f"{name} is not a JSON object")
    return value


def check_uds() -> None:
    request = load_json("uds-health-request.json")
    assert request == {"type": "request", "method": "health"}

    encoded = (ROOT / "uds-frame-response.bin").read_bytes()
    prefix_size = 4 + 8
    assert len(encoded) >= prefix_size
    header_size, payload_size = struct.unpack_from("<IQ", encoded)
    header_start = prefix_size
    payload_start = header_start + header_size
    assert payload_start + payload_size == len(encoded)
    header = json.loads(encoded[header_start:payload_start].decode("utf-8"))
    assert header == {"type": "response", "method": "latest_frame", "seq": "7"}
    assert encoded[payload_start:] == bytes((0, 1, 2, 3))


def check_config() -> None:
    config = load_json("config-wire.json")
    assert config["model"] == {"provider": "openai", "model": "gpt-4"}
    assert config["search"] == {"provider": "duckduckgo"}
    assert config["device"] == {"device_type": "iOS"}
    assert config["agent"] == {}


def check_manifest() -> None:
    manifest = load_json("ota-manifest.json")
    assert manifest["schema_version"] == 2
    assert manifest["channel"] == "stable"
    assert [part["name"] for part in manifest["parts"]] == ["boot", "rootfs"]
    assert manifest["parts"][0]["asset_a"]["name"] == "boot_a.img"
    assert manifest["parts"][0]["asset_b"]["name"] == "boot_b.img"
    assert manifest["parts"][1]["asset"]["name"] == "rootfs.img"
    for part in manifest["parts"]:
        assets = [part.get("asset"), part.get("asset_a"), part.get("asset_b")]
        for asset in assets:
            if asset is None:
                continue
            assert asset["size"] > 0
            assert len(asset["sha256"]) == 64
            int(asset["sha256"], 16)
    signature = base64.b64decode(manifest["signature"]["value"], validate=True)
    assert len(signature) == 64


def main() -> None:
    check_uds()
    check_config()
    check_manifest()
    print("shared contract fixtures passed")


if __name__ == "__main__":
    main()
