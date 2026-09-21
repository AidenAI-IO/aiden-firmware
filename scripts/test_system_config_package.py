#!/usr/bin/env python3
"""Real dpkg pair upgrades, partial transactions, conffiles and recovery."""
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts/debian-package"))
import system_config
import test_debian_package_lifecycle as lifecycle
from test_debian_package_lifecycle import UNITS, RESTART, TTYD


@unittest.skipUnless(shutil.which("dpkg-deb") and shutil.which("dpkg"), "Requires Linux dpkg")
class PairTests(unittest.TestCase):
    def setUp(self):
        self.fixture = lifecycle.LifecycleTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.root = self.fixture.root
        self.device = self.root / "dpkg-root"
        self.env = dict(self.fixture.env)
        self.env.update(AIDEN_BUSINESS_REVISION="1", AIDEN_RELEASE_CHANNEL="",
                        AIDEN_PLATFORM_BASE="", AIDEN_SYSTEM_FINGERPRINT="")
        database = self.device / "var/lib/dpkg"
        database.mkdir(parents=True)
        (database / "status").write_text("\n".join(
            f"Package: {name}\nStatus: install ok installed\nArchitecture: all\nVersion: 99\nDescription: platform fixture\n"
            for name in ("python3-minimal", "systemd", "sudo")))
        query = self.fixture.bin / "dpkg-query"
        query.write_text(f'#!/bin/sh\nexec {shutil.which("dpkg-query")} --root="{self.device}" "$@"\n')
        self.real_visudo = shutil.which("visudo")
        visudo = self.fixture.bin / "visudo"
        visudo.write_text('#!/bin/sh\n[ "${MOCK_INVALID_SUDOERS:-0}" != 1 ]\n')
        visudo.chmod(0o755)
        self.fixture.set_states({unit: "inactive" for unit in UNITS})

    def package(self, version):
        output = self.root / ("build-" + version)
        env = {**self.env, "AIDEN_BUSINESS_VERSION": version}
        with patch.dict(os.environ, env):
            system_config.build(output)
        config_root = output / "system-config-root"
        business_root = output / "business-root"
        (business_root / "DEBIAN").mkdir(parents=True)
        (business_root / "DEBIAN/control").write_text(
            f"Package: aiden-business\nVersion: {version}-1\nArchitecture: all\n"
            f"Depends: aiden-system-config (= {version}-1)\n"
            "Maintainer: Test <test@example.test>\nDescription: business fixture\n")
        subprocess.run(["bash", str(ROOT / "scripts/debian-package/write-maintainer-scripts.sh"),
                        str(business_root / "DEBIAN")], env=env, check=True)
        packages = {}
        for name, root in (("aiden-business", business_root), ("aiden-system-config", config_root)):
            for phase in ("preinst", "postinst", "prerm", "postrm"):
                path = root / "DEBIAN" / phase
                # Run actual package hooks against a real isolated dpkg DB,
                # with only systemd and absolute runtime paths substituted.
                text = path.read_text().replace("set -eu\n", "set -eu\nunset DPKG_ROOT\n", 1)
                text = text.replace("/var/lib/aiden-business", str(self.root / "package-state"))
                text = text.replace("/run/systemd/system", str(self.fixture.marker))
                text = text.replace("/usr/sbin/policy-rc.d", str(self.fixture.policy))
                path.write_text(text)
            package = output / f"{name}_{version}-1_all.deb"
            subprocess.run(["dpkg-deb", "--build", "--root-owner-group", str(root), str(package)],
                           check=True, stdout=subprocess.DEVNULL)
            packages[name] = package
        return packages

    def dpkg(self, *args, success=True):
        result = subprocess.run(["dpkg", "--force-not-root", "--force-script-chrootless", "--force-confold",
                                 "--log=" + str(self.root / "dpkg.log"),
                                 "--root=" + str(self.device), *map(str, args)],
                                env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        return result

    def installed_pair(self, version="0.0.2"):
        packages = self.package(version)
        self.dpkg("--install", *packages.values())
        self.before = {unit: ("inactive" if unit in (RESTART, TTYD) else "active") for unit in UNITS}
        self.fixture.set_states(self.before)
        self.fixture.calls.unlink(missing_ok=True)
        return packages

    def assert_stopped(self):
        self.assertTrue(all(state == "inactive" for state in self.fixture.states().values()))
        self.assertTrue(self.fixture.transaction.exists())

    def test_upgrade_then_downgrade_resumes_only_after_both_configured(self):
        old = self.installed_pair()
        new = self.package("0.0.3")
        for packages in (new, old):
            self.dpkg("--auto-deconfigure", "--unpack", packages["aiden-system-config"], packages["aiden-business"])
            self.assert_stopped()
            self.dpkg("--configure", "aiden-system-config")
            self.assert_stopped()
            self.dpkg("--configure", "aiden-business")
            self.assertEqual(self.fixture.states(), self.before)
            self.assertFalse(self.fixture.transaction.exists())

    def test_interrupted_business_upgrade_is_recoverable(self):
        self.installed_pair()
        new = self.package("0.0.3")
        self.dpkg("--unpack", new["aiden-business"])
        self.dpkg("--configure", "aiden-business", success=False)
        self.assert_stopped()
        self.dpkg("--install", new["aiden-system-config"])
        self.assert_stopped()
        self.dpkg("--configure", "aiden-business")
        self.assertEqual(self.fixture.states(), self.before)

    def test_invalid_installed_sudoers_blocks_service_restart_and_retry_recovers(self):
        self.installed_pair()
        new = self.package("0.0.3")
        self.dpkg("--auto-deconfigure", "--unpack", *new.values())
        self.env["MOCK_INVALID_SUDOERS"] = "1"
        self.dpkg("--configure", "aiden-system-config", success=False)
        self.assert_stopped()
        self.env.pop("MOCK_INVALID_SUDOERS")
        self.dpkg("--configure", "--pending")
        self.assertEqual(self.fixture.states(), self.before)

    def test_payload_permissions_conffiles_and_local_edits(self):
        old = self.installed_pair()
        config = old["aiden-system-config"]
        tar = subprocess.check_output(["dpkg-deb", "--fsys-tarfile", str(config)])
        with tarfile.open(fileobj=io.BytesIO(tar)) as archive:
            for path, mode in system_config.files().items():
                member = archive.getmember("./" + path)
                self.assertEqual((member.uid, member.gid, member.mode), (0, 0, int(mode, 8)))
        control = self.root / "extracted-control"
        subprocess.run(["dpkg-deb", "-e", str(config), str(control)], check=True)
        self.assertEqual((control / "conffiles").read_text().splitlines(),
                         ["/etc/profile.d/aiden-env.sh", "/etc/sudoers.d/20-aiden-proxy"])
        if self.real_visudo:
            subprocess.run([self.real_visudo, "-cf", str(self.device / "etc/sudoers.d/20-aiden-proxy")], check=True)
        profile = self.device / "etc/profile.d/aiden-env.sh"
        profile.write_text(profile.read_text() + "\n# local administrator setting\n")
        new = self.package("0.0.3")
        self.dpkg("--auto-deconfigure", "--install", *new.values())
        self.assertIn("local administrator setting", profile.read_text())
        owners = subprocess.check_output([shutil.which("dpkg-query"), "--root=" + str(self.device),
                                           "-S", "/etc/profile.d/aiden-env.sh"], text=True)
        self.assertEqual(owners.strip(), "aiden-system-config: /etc/profile.d/aiden-env.sh")


if __name__ == "__main__":
    if shutil.which("dpkg") and os.geteuid() != 0:
        raise SystemExit("Run this integration test as root in the disposable package-builder container; dpkg must replace 0440 conffiles.")
    unittest.main()
