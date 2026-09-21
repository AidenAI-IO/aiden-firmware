#!/usr/bin/env python3
"""Build the explicitly owned configuration payload; never sweep in the overlay."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts/release"))
import contract

CATALOG = ROOT / "scripts/debian-system/config-package.json"


def files():
    catalog = json.loads(CATALOG.read_text())
    if catalog["format"] != 1 or not catalog["files"]:
        raise ValueError("Invalid configuration package catalog")
    for path, mode in catalog["files"].items():
        if (Path(path).is_absolute() or ".." in Path(path).parts
                or not path.startswith(("etc/", "usr/lib/aiden/"))
                or mode not in ("0644", "0440", "0755")):
            raise ValueError(f"Invalid package path/mode: {path}")
    return catalog["files"]


def build(output):
    manifest = contract.package_manifest("aiden-system-config")
    version = manifest["business_release"] + "-" + manifest["package_revision"]
    output.mkdir(parents=True, exist_ok=True)
    root = output / "system-config-root"
    if root.exists():
        shutil.rmtree(root)
    control = root / "DEBIAN"
    control.mkdir(parents=True)
    conffiles = []
    for path, mode in files().items():
        source = ROOT / "overlay-debian" / path
        if source.is_symlink() or not source.is_file():
            raise ValueError(f"Missing regular configuration file: {path}")
        destination = root / path
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, destination)
        destination.chmod(int(mode, 8))
        if path.startswith("etc/"):
            conffiles.append("/" + path)
    (control / "conffiles").write_text("\n".join(sorted(conffiles)) + "\n")
    (control / "control").write_text(
        f"Package: aiden-system-config\nVersion: {version}\nArchitecture: all\n"
        "Section: admin\nPriority: optional\nMaintainer: Aiden AI <firmware@aiden.ai>\n"
        "Pre-Depends: python3-minimal\nDepends: systemd, sudo\n"
        f"Breaks: aiden-business (<< {version}), aiden-business (>> {version})\n"
        "Description: Aiden managed service and terminal configuration\n"
        " Platform-bound configuration paired with the same Aiden business release.\n")
    document = root / "usr/share/doc/aiden-system-config/release-manifest.json"
    document.parent.mkdir(parents=True)
    document.write_text(json.dumps(manifest, indent=2) + "\n")
    subprocess.run(["bash", str(ROOT / "scripts/debian-package/write-maintainer-scripts.sh"),
                    str(control), "aiden-system-config"], check=True)
    package = output / f"aiden-system-config_{version}_all.deb"
    subprocess.run(["dpkg-deb", "--build", "--root-owner-group", str(root), str(package)], check=True)
    shutil.copyfile(package, output / "aiden-system-config.deb")
    return package


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("build", "exclude", "audit"))
    parser.add_argument("destination", type=Path)
    args = parser.parse_args()
    if args.action == "build":
        print(build(args.destination))
    elif args.action == "exclude":
        args.destination.write_text("".join("/" + p + "\n" for p in files()))
    else:
        for path, mode in files().items():
            installed = args.destination / path
            if installed.is_symlink() or not installed.is_file():
                raise ValueError(f"Configuration package file missing: {path}")
            stat = installed.stat()
            if (stat.st_uid, stat.st_gid, stat.st_mode & 0o777) != (0, 0, int(mode, 8)):
                raise ValueError(f"Configuration package file ownership/mode invalid: {path}")
            owner = subprocess.check_output(["dpkg-query", "--root=" + str(args.destination),
                                             "-S", "/" + path], text=True)
            if owner.strip() != "aiden-system-config: /" + path:
                raise ValueError(f"Wrong configuration file owner: {path}")
            if installed.read_bytes() != (ROOT / "overlay-debian" / path).read_bytes():
                raise ValueError(f"Configuration differs from source: {path}")


if __name__ == "__main__":
    main()
