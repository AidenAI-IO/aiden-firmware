#!/usr/bin/env python3
"""Exercise signed indexes, contract isolation and upgrade policy with real APT."""

import contextlib
import copy
from datetime import datetime, timezone
import functools
import getpass
import hashlib
import http.server
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts/apt"))
import repository

loader = importlib.machinery.SourceFileLoader("apt_source", str(ROOT / "overlay-debian/usr/lib/aiden/aiden-apt-source"))
spec = importlib.util.spec_from_loader(loader.name, loader)
apt_source = importlib.util.module_from_spec(spec)
loader.exec_module(apt_source)
REPO = "AidenAI-IO/aiden-firmware"


def run(*args, **kwargs):
    return subprocess.run(args, text=True, capture_output=True, check=True, **kwargs)


class QuietHandler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass


@unittest.skipUnless(all(shutil.which(x) for x in ("dpkg-deb", "apt-get", "apt-cache", "gpg", "gpgv")),
                     "Requires Linux APT, dpkg-deb and GnuPG")
class RepositoryTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="aiden-apt-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.gpg = self.root / "gnupg"
        self.gpg.mkdir(mode=0o700)
        self.old_gpg = os.environ.get("GNUPGHOME")
        os.environ["GNUPGHOME"] = str(self.gpg)
        self.addCleanup(self.restore_gpg)
        run("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "",
            "--quick-generate-key", "APT Test <apt@example.test>", "ed25519", "sign", "0")
        self.key = self.root / "public.asc"
        self.key.write_text(run("gpg", "--batch", "--armor", "--export", "apt@example.test").stdout)
        self.cache = self.root / "cache"
        self.site = self.root / "site"
        self.history = []

    def restore_gpg(self):
        subprocess.run(["gpgconf", "--kill", "all"], capture_output=True)
        if self.old_gpg is None:
            os.environ.pop("GNUPGHOME", None)
        else:
            os.environ["GNUPGHOME"] = self.old_gpg

    def release(self, version, channel="dev", kind="business", contract=None, paired=False):
        previous = next((r for r in reversed(self.history) if r["channel"] == channel), None)
        commit = hashlib.sha1(version.encode()).hexdigest()
        tag = f"{channel}-v{version}"
        platform = ({"contract": contract, "base_release": tag, "source_commit": commit} if kind == "ota"
                    else copy.deepcopy(previous["platform"]))
        record = {
            "format": 1, "repo": REPO, "channel": channel, "version": version, "tag": tag, "kind": kind,
            "source_commit": commit, "source_tree": commit, "build_time": "2026-01-01T00:00:00Z",
            "platform": platform, "previous_tag": previous["tag"] if previous else None,
            "previous_commit": previous["source_commit"] if previous else None,
            "fingerprints": {"business": hashlib.sha256(version.encode()).hexdigest(),
                             "system": hashlib.sha256(platform["contract"].encode()).hexdigest()},
        }
        declaration = {"format": 1, "product": "aiden", "platform_id": "luckfox-rv1106", "architecture": "armhf",
                       "os_release": "debian-13", "libc": "glibc", "channel": channel,
                       "platform_contract": platform["contract"], "base_release": platform["base_release"],
                       "system_fingerprint": record["fingerprints"]["system"]}
        major = int(platform["contract"].split(".")[0])
        manifest = {"business_release": version, "package_revision": "1", "platform": declaration,
                    "required_platform_contract": {"min": f"{major}.0.0", "max_exclusive": f"{major+1}.0.0"}}
        if paired:
            record["package_set"] = 2
            manifest.update(package="aiden-business", architecture="armhf", package_set=2,
                            paired_package={"name": "aiden-system-config", "version": version + "-1"})
        package_root = self.root / tag
        (package_root / "DEBIAN").mkdir(parents=True)
        (package_root / "DEBIAN/control").write_text(
            f"Package: aiden-business\nVersion: {version}-1\nArchitecture: armhf\n"
            "Maintainer: Test <apt@example.test>\nDescription: business fixture\n"
            + (f"Depends: aiden-system-config (= {version}-1)\n" if paired else ""))
        document = package_root / "usr/share/doc/aiden-business/release-manifest.json"
        document.parent.mkdir(parents=True)
        document.write_text(json.dumps(manifest))
        directory = self.cache / tag
        directory.mkdir(parents=True)
        path = directory / f"aiden-business_{version}-1_armhf.deb"
        run("dpkg-deb", "--build", "--root-owner-group", str(package_root), str(path))
        record["assets"] = {path.name: {"size": path.stat().st_size, "sha256": repository.sha256(path)}}
        if paired:
            config_root = self.root / (tag + "-config")
            (config_root / "DEBIAN").mkdir(parents=True)
            (config_root / "DEBIAN/control").write_text(
                f"Package: aiden-system-config\nVersion: {version}-1\nArchitecture: all\n"
                f"Breaks: aiden-business (<< {version}-1), aiden-business (>> {version}-1)\n"
                "Maintainer: Test <apt@example.test>\nDescription: config fixture\n")
            document = config_root / "usr/share/doc/aiden-system-config/release-manifest.json"
            document.parent.mkdir(parents=True)
            document.write_text(json.dumps({**manifest, "package": "aiden-system-config", "architecture": "all",
                                           "paired_package": {"name": "aiden-business", "version": version + "-1"}}))
            path = directory / f"aiden-system-config_{version}-1_all.deb"
            run("dpkg-deb", "--build", "--root-owner-group", str(config_root), str(path))
            record["assets"][path.name] = {"size": path.stat().st_size, "sha256": repository.sha256(path)}
        self.history.append(record)
        return declaration

    def build(self, now=None):
        return repository.build_site(self.history, REPO, self.site, self.cache, self.key, now=now)

    @contextlib.contextmanager
    def serve(self):
        handler = functools.partial(QuietHandler, directory=str(self.site))
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            yield f"http://127.0.0.1:{server.server_port}"
        finally:
            server.shutdown()
            server.server_close()
            thread.join()

    def apt_fixture(self, platform, url):
        root = self.root / "device"
        declaration = root / "usr/lib/aiden/platform/contract.json"
        declaration.parent.mkdir(parents=True)
        declaration.write_text(json.dumps(platform))
        apt_source.configure(root, self.key)
        source = root / "etc/apt/sources.list.d/aiden-business.sources"
        source.write_text(source.read_text().replace(apt_source.DEFAULT_URL, url + "/apt")
                          .replace("Signed-By: /usr/", f"Signed-By: {root}/usr/"))
        for path in ("var/lib/apt/lists/partial", "var/cache/apt/archives/partial", "var/lib/dpkg", "etc/apt/apt.conf.d"):
            (root / path).mkdir(parents=True, exist_ok=True)
        (root / "var/lib/dpkg/status").write_text(
            "Package: aiden-business\nStatus: install ok installed\nArchitecture: armhf\nVersion: 0.0.2-1\nDescription: old business\n\n"
            "Package: system-fixture\nStatus: install ok installed\nArchitecture: armhf\nVersion: 1\nDescription: old system\n")
        self.apt_options = ["-o", f"Dir={root}", "-o", f"Dir::State::status={root}/var/lib/dpkg/status",
                            "-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=",
                            "-o", "Dir::Etc::sourcelist=-", "-o", "APT::Architecture=armhf",
                            "-o", "Acquire::Languages=none", "-o", "APT::Update::Error-Mode=any",
                            "-o", f"APT::Sandbox::User={getpass.getuser()}", "-o", "Debug::NoLocking=true"]
        return root

    def apt(self, program, *args, check=True):
        return subprocess.run([program, *self.apt_options, *args], capture_output=True, text=True, check=check)

    def test_contract_isolation_and_only_business_upgrades(self):
        device = self.release("0.0.2", kind="ota", contract="1.0.0")
        self.release("0.0.3")
        self.release("0.0.4", "staging", "ota", "2.0.0")
        self.release("0.0.5", kind="ota", contract="3.0.0")
        self.release("0.0.6")
        suites = self.build()
        self.assertEqual(set(suites), {"dev-c1", "staging-c2", "dev-c3"})
        self.assertEqual(suites["dev-c1"], ["dev-v0.0.2", "dev-v0.0.3"])
        with self.serve() as url:
            root = self.apt_fixture(device, url)
            system = self.site / "system"
            system.mkdir()
            payload = "Package: system-fixture\nVersion: 2\nArchitecture: armhf\nFilename: unused.deb\nSize: 1\nDescription: system update\n\n"
            (system / "Packages").write_text(payload)
            (system / "Release").write_text("Origin: Debian\nLabel: Debian\nSuite: test\nCodename: test\nArchitectures: armhf\n")
            (root / "etc/apt/sources.list.d/system.list").write_text(f"deb [trusted=yes] {url}/system ./\n")
            self.apt("apt-get", "update")
            policy = self.apt("apt-cache", "policy", "aiden-business", "system-fixture").stdout
            self.assertIn("Candidate: 0.0.3-1", policy)
            self.assertIn("Candidate: 1\n", policy)
            result = self.apt("apt-get", "--simulate", "upgrade").stdout
            self.assertIn("Inst aiden-business", result)
            self.assertNotIn("Inst system-fixture", result)
            self.assertNotIn("0.0.6", policy)
            downloaded = self.apt("apt-get", "--print-uris", "--download-only", "--assume-yes", "install", "aiden-business").stdout
            self.assertIn("pool/dev-c1/dev-v0.0.3/aiden-business_0.0.3-1_armhf.deb", downloaded)

    def test_signed_metadata_has_no_expiry(self):
        device = self.release("0.0.2", kind="ota", contract="1.0.0")
        self.build(now=datetime(2020, 1, 1, tzinfo=timezone.utc))
        for name in ("Release", "InRelease"):
            self.assertNotIn("Valid-Until:", (self.site / "apt/dists/dev-c1" / name).read_text())
        with self.serve() as url:
            self.apt_fixture(device, url)
            self.apt("apt-get", "update")
            self.assertIn("Candidate: 0.0.2-1", self.apt("apt-cache", "policy", "aiden-business").stdout)

    def test_apt_upgrade_selects_exact_pair_and_allows_paired_downgrade(self):
        device = self.release("0.0.2", kind="ota", contract="1.0.0", paired=True)
        self.release("0.0.3", paired=True)
        self.build()
        with self.serve() as url:
            root = self.apt_fixture(device, url)
            status = root / "var/lib/dpkg/status"
            status.write_text(status.read_text().replace("Description: old business", "Depends: aiden-system-config (= 0.0.2-1)\nDescription: old business")
                              + "\nPackage: aiden-system-config\nStatus: install ok installed\nArchitecture: all\nVersion: 0.0.2-1\n"
                              "Breaks: aiden-business (<< 0.0.2-1), aiden-business (>> 0.0.2-1)\nDescription: old config\n")
            self.apt("apt-get", "update")
            result = self.apt("apt", "--simulate", "upgrade").stdout
            self.assertIn("Inst aiden-system-config", result)
            self.assertIn("Inst aiden-business", result)
            self.assertNotIn("Remv ", result)
            self.assertNotIn("Inst system-fixture", result)
            status.write_text(status.read_text().replace("0.0.2-1", "0.0.3-1"))
            down = self.apt("apt-get", "--simulate", "--allow-downgrades", "install",
                            "aiden-business=0.0.2-1", "aiden-system-config=0.0.2-1").stdout
            self.assertIn("Inst aiden-business", down)
            self.assertIn("Inst aiden-system-config", down)
            # An explicit incompatible pair is rejected before installing anything.
            bad = self.apt("apt-get", "--simulate", "install", "aiden-business=0.0.3-1",
                           "aiden-system-config=0.0.2-1", check=False)
            self.assertNotEqual(bad.returncode, 0)

    def test_missing_config_asset_cannot_be_indexed_as_half_a_release(self):
        self.release("0.0.2", kind="ota", contract="1.0.0", paired=True)
        del self.history[0]["assets"]["aiden-system-config_0.0.2-1_all.deb"]
        with self.assertRaises(KeyError):
            self.build()
        self.assertFalse(self.site.exists())

    def test_tampered_indexes_are_rejected(self):
        device = self.release("0.0.2", kind="ota", contract="1.0.0")
        self.build()
        for path in (self.site / "apt/dists/dev-c1/main/binary-armhf").rglob("*"):
            if path.is_file():
                data = path.read_bytes()
                path.write_bytes(bytes([data[0] ^ 1]) + data[1:])
        with self.serve() as url:
            self.apt_fixture(device, url)
            result = self.apt("apt-get", "update", check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("mismatch", (result.stdout + result.stderr).lower())

    def test_untrusted_signature_is_rejected(self):
        device = self.release("0.0.2", kind="ota", contract="1.0.0")
        self.build()
        run("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "", "--quick-generate-key",
            "Other <other@example.test>", "ed25519", "sign", "0")
        self.key.write_text(run("gpg", "--batch", "--armor", "--export", "other@example.test").stdout)
        with self.serve() as url:
            self.apt_fixture(device, url)
            result = self.apt("apt-get", "update", check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertTrue(any(word in result.stderr for word in ("NO_PUBKEY", "Missing key", "signature")))

    def test_corrupt_published_asset_is_rejected_before_signing(self):
        self.release("0.0.2", kind="ota", contract="1.0.0")
        package = next(self.cache.rglob("*.deb"))
        package.write_bytes(package.read_bytes() + b"modified")
        with self.assertRaisesRegex(ValueError, "checksum/size"):
            self.build()
        self.assertFalse(self.site.exists())

    def test_manifest_contract_mismatch_is_rejected(self):
        self.release("0.0.2", kind="ota", contract="1.0.0")
        self.history[0]["platform"]["contract"] = "2.0.0"
        with self.assertRaisesRegex(ValueError, "manifest"):
            self.build()
        self.assertFalse(self.site.exists())

    def test_retention_preserves_old_contract_suites(self):
        self.release("0.0.2", kind="ota", contract="1.0.0")
        for version in ("0.0.3", "0.0.4", "0.0.5"):
            self.release(version)
        self.release("0.0.6", kind="ota", contract="2.0.0")
        selected = repository.select_records(self.history, REPO)
        self.assertEqual([r["version"] for r in selected["dev-c1"]], ["0.0.3", "0.0.4", "0.0.5"])
        self.assertEqual([r["version"] for r in selected["dev-c2"]], ["0.0.6"])


class ConfigurationTests(unittest.TestCase):
    def test_unmanaged_or_invalid_platform_does_not_write_sources(self):
        for platform in ({}, {"channel": "unknown"}, {"channel": "dev", "platform_contract": "0.0.0"}):
            with self.subTest(platform=platform), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                path = root / "usr/lib/aiden/platform/contract.json"
                path.parent.mkdir(parents=True)
                path.write_text(json.dumps(platform))
                with self.assertRaises(ValueError):
                    apt_source.configure(root, repository.PUBLIC_KEY)
                self.assertFalse((root / "etc/apt").exists())


if __name__ == "__main__":
    unittest.main()
