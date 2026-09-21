#!/usr/bin/env python3
"""Generate the platform declaration and package compatibility metadata."""

import argparse
import json
import os
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[2]


def declaration():
    value = json.loads((ROOT / "overlay-debian/usr/lib/aiden/platform/contract.json").read_text())
    contract = os.environ.get("AIDEN_PLATFORM_CONTRACT") or value["platform_contract"]
    if not re.fullmatch(r"[1-9][0-9]*\.0\.0", contract):
        raise ValueError("Platform contract must be a positive MAJOR.0.0")
    value["platform_contract"] = contract
    fields = {"channel": "AIDEN_RELEASE_CHANNEL", "base_release": "AIDEN_PLATFORM_BASE",
              "system_fingerprint": "AIDEN_SYSTEM_FINGERPRINT"}
    supplied = {key: os.environ.get(env, "") for key, env in fields.items()}
    if any(supplied.values()):
        if supplied["channel"] not in ("dev", "staging", "prod"):
            raise ValueError("Invalid release channel")
        if not re.fullmatch(supplied["channel"] + r"-v[0-9]+\.[0-9]+\.[0-9]+", supplied["base_release"]):
            raise ValueError("Invalid platform base release")
        if not re.fullmatch(r"[0-9a-f]{64}", supplied["system_fingerprint"]):
            raise ValueError("Missing system fingerprint")
        value.update(supplied)
    return value


def package_manifest():
    platform = declaration()
    version = os.environ["AIDEN_BUSINESS_VERSION"]
    revision = os.environ["AIDEN_BUSINESS_REVISION"]
    if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", version):
        raise ValueError("Invalid business version")
    if not re.fullmatch(r"[1-9][0-9]*", revision):
        raise ValueError("Invalid package revision")
    major = int(platform["platform_contract"].split(".")[0])
    manifest = {
        "format": 1, "product": "aiden", "artifact_kind": "debian-package",
        "package": "aiden-business", "business_release": version, "package_revision": revision,
        "architecture": "armhf", "required_platform_contract": {
            "min": platform["platform_contract"], "max_exclusive": f"{major + 1}.0.0"},
        "config_schema": {"min_supported": 3, "target": 4}, "business_epoch": 1,
        "runtime_config": 1,
    }
    if "channel" in platform:
        manifest["platform"] = platform
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("platform", "package", "preinst-check"))
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    if args.action == "preinst-check":
        expected = declaration()
        # Embed the declaration in preinst: the new package payload is not unpacked yet.
        args.output.write_text(
            'if [ "$phase" = preinst ]; then\n'
            '  case "${1:-}" in install|upgrade)\n'
            "    python3 - <<'AIDEN_CONTRACT_CHECK'\n"
            "import json, os, sys\n"
            f"expected = {expected!r}\n"
            "path = os.environ.get('DPKG_ROOT', '') + '/usr/lib/aiden/platform/contract.json'\n"
            "try:\n"
            "    with open(path) as stream: actual = json.load(stream)\n"
            "    for key, value in expected.items():\n"
            "        if actual.get(key) != value: raise ValueError(f'{key}: expected {value}, got {actual.get(key)}')\n"
            "except (OSError, ValueError) as error:\n"
            "    sys.exit(f'aiden-business: incompatible platform; install the matching channel OTA first: {error}')\n"
            "AIDEN_CONTRACT_CHECK\n"
            '    ;;\n  esac\nfi\n'
        )
    else:
        value = declaration() if args.action == "platform" else package_manifest()
        args.output.write_text(json.dumps(value, indent=2) + "\n")


if __name__ == "__main__":
    main()
