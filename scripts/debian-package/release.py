#!/usr/bin/env python3
"""Stage and verify standalone business releases before GitHub publication."""

import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile


def command(*args):
    return subprocess.check_output(args, text=True).strip()


def sha256(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def check_source(root, apps):
    commit = command("git", "-C", str(root), "rev-parse", "HEAD")
    if command("git", "-C", str(root), "status", "--porcelain"):
        raise ValueError("Commit source and SDK changes before building a release")
    metadata = apps / "apps/metadata"
    if (metadata / "hardware-demo-commit.txt").read_text().strip() != commit:
        raise ValueError("Application binaries are from another commit; run release.sh build")
    for name in ("hardware-demo-status.txt", "pico-sdk-status.txt"):
        if (metadata / name).read_text().strip():
            raise ValueError("Application binaries were built from a dirty source tree")
    sdk_commit = command("git", "-C", str(root / "pico-sdk"), "rev-parse", "HEAD")
    if (metadata / "pico-sdk-commit.txt").read_text().strip() != sdk_commit:
        raise ValueError("Application SDK commit does not match the checkout")
    if "status=pass" not in (apps / "apps-audit/summary.txt").read_text().splitlines():
        raise ValueError("Application audit has not passed")
    return commit, sdk_commit


def package_manifest(package, name="aiden-business"):
    data = subprocess.check_output(["dpkg-deb", "--fsys-tarfile", str(package)])
    with tarfile.open(fileobj=io.BytesIO(data)) as archive:
        return archive.extractfile(f"./usr/share/doc/{name}/release-manifest.json").read()


def verify_pair(business_package, config_package, version):
    business_manifest = json.loads(package_manifest(business_package))
    config_manifest = json.loads(package_manifest(config_package, "aiden-system-config"))
    if (business_manifest.get("package_set") != 2
            or business_manifest.get("package") != "aiden-business"
            or business_manifest.get("paired_package") != {"name": "aiden-system-config", "version": version}):
        raise ValueError("Business package does not declare the matching configuration package")
    expected = {**business_manifest, "package": "aiden-system-config", "architecture": "all",
                "paired_package": {"name": "aiden-business", "version": version}}
    if config_manifest != expected:
        raise ValueError("Configuration/business package manifests do not match")
    for field, value in (("Package", "aiden-system-config"), ("Architecture", "all"), ("Version", version)):
        if command("dpkg-deb", "--field", str(config_package), field) != value:
            raise ValueError(f"Configuration package {field} mismatch")
    dependencies = command("dpkg-deb", "--field", str(business_package), "Depends").split(",")
    if f"aiden-system-config (= {version})" not in [d.strip() for d in dependencies]:
        raise ValueError("Business package must depend on the exact configuration version")
    breaks = command("dpkg-deb", "--field", str(config_package), "Breaks")
    if {d.strip() for d in breaks.split(",")} != {f"aiden-business (<< {version})", f"aiden-business (>> {version})"}:
        raise ValueError("Configuration package must reject mismatched business versions")
    return config_manifest


def stage(root, apps, output):
    commit, sdk_commit = check_source(root, apps)
    version = os.environ["AIDEN_BUSINESS_VERSION"] + "-" + os.environ["AIDEN_BUSINESS_REVISION"]
    package_name = f"aiden-business_{version}_armhf.deb"
    release = output / "release"
    if release.exists():
        shutil.rmtree(release)
    release.mkdir(parents=True)
    package = release / package_name
    shutil.copyfile(output / package_name, package)
    (release / "release-manifest.json").write_bytes(package_manifest(package))
    compatibility = json.loads(package_manifest(package))["required_platform_contract"]
    paired = json.loads(package_manifest(package)).get("package_set", 1) == 2
    if paired:
        config_name = f"aiden-system-config_{version}_all.deb"
        shutil.copyfile(output / config_name, release / config_name)
        verify_pair(package, release / config_name, version)
        (release / "system-config-manifest.json").write_bytes(package_manifest(release / config_name, "aiden-system-config"))
    metadata = {
        "format": 1, "package": "aiden-business", "version": version, "architecture": "armhf",
        "tag": f"business-v{version}", "source_commit": commit, "sdk_commit": sdk_commit,
        "package_file": package_name, "package_sha256": sha256(package),
        "package_set": 2 if paired else 1,
    }
    (release / "build-metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    (release / "RELEASE-NOTES.md").write_text(
        f"Aiden business package {version} for armhf.\n\n"
        f"Source: `{commit}`\n\n"
        f"Requires Debian platform contract >={compatibility['min']} and <{compatibility['max_exclusive']}. "
        "Contains the paired application and configuration packages; no boot, rootfs or partition changes.\n\n"
        "Verify SHA256SUMS before installing with apt. Package maintainer scripts "
        "stop and restore previously running Aiden services; then run the OTA self-check. "
        "See docs/08-ota/debian-package.md for the installation procedure.\n\n"
        "This GitHub prerelease distributes .deb files; it is not an APT repository "
        "or a signed firmware OTA release.\n"
    )
    assets = sorted(release.iterdir())
    (release / "SHA256SUMS").write_text("".join(f"{sha256(p)}  {p.name}\n" for p in assets))
    verify(release)
    print(f"Release assets: {release}")


def verify(release):
    metadata = json.loads((release / "build-metadata.json").read_text())
    version = metadata["version"]
    if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-[1-9][0-9]*", version):
        raise ValueError("Invalid package version")
    for field in ("source_commit", "sdk_commit"):
        if not re.fullmatch(r"[0-9a-f]{40}", metadata[field]):
            raise ValueError(f"Invalid {field}")
    name = f"aiden-business_{version}_armhf.deb"
    if metadata["package_file"] != name or metadata["tag"] != f"business-v{version}":
        raise ValueError("Package filename or release tag does not match its version")
    if metadata["package"] != "aiden-business" or metadata["architecture"] != "armhf":
        raise ValueError("Wrong package or architecture")
    expected_assets = {name, "release-manifest.json", "build-metadata.json", "RELEASE-NOTES.md", "SHA256SUMS"}
    paired = metadata.get("package_set", 1) == 2
    if paired:
        expected_assets |= {f"aiden-system-config_{version}_all.deb", "system-config-manifest.json"}
    if {p.name for p in release.iterdir()} != expected_assets:
        raise ValueError("Release directory has missing or unexpected files")
    for name in expected_assets:
        if (release / name).is_symlink() or not (release / name).is_file():
            raise ValueError("Release assets must be regular files")
    expected_sums = "".join(f"{sha256(release / n)}  {n}\n" for n in sorted(expected_assets - {"SHA256SUMS"}))
    if (release / "SHA256SUMS").read_text() != expected_sums:
        raise ValueError("Release checksum verification failed")
    package = release / metadata["package_file"]
    if sha256(package) != metadata["package_sha256"]:
        raise ValueError("Package checksum does not match build metadata")
    for field, expected in (("Package", "aiden-business"), ("Version", version), ("Architecture", "armhf")):
        if command("dpkg-deb", "--field", str(package), field) != expected:
            raise ValueError(f"Package {field} mismatch")
    manifest_bytes = (release / "release-manifest.json").read_bytes()
    if package_manifest(package) != manifest_bytes:
        raise ValueError("Published manifest does not match the package")
    manifest = json.loads(manifest_bytes)
    if f"{manifest['business_release']}-{manifest['package_revision']}" != version:
        raise ValueError("Business manifest version mismatch")
    if paired:
        config_package = release / f"aiden-system-config_{version}_all.deb"
        verify_pair(package, config_package, version)
        if package_manifest(config_package, "aiden-system-config") != (release / "system-config-manifest.json").read_bytes():
            raise ValueError("Configuration manifest differs from release asset")
    elif manifest.get("package_set", 1) != 1:
        raise ValueError("Paired package release is missing its configuration package")
    return metadata


def main():
    action, *args = sys.argv[1:]
    if action == "check-source" and len(args) == 2:
        check_source(*map(Path, args))
    elif action == "stage" and len(args) == 3:
        stage(*map(Path, args))
    elif action == "verify" and len(args) in (1, 2):
        metadata = verify(Path(args[0]))
        print(metadata[args[1]] if len(args) == 2 else "Release checks passed")
    else:
        raise ValueError("Usage: release.py check-source ROOT APPS | stage ROOT APPS OUTPUT | verify RELEASE [FIELD]")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(f"Business release: {error}")
