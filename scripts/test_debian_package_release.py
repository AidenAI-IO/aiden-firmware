#!/usr/bin/env python3
"""Exercise real .deb metadata and the standalone release validation boundary."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("business_release", ROOT / "scripts/debian-package/release.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "repo"
        self.root.mkdir()
        self.apps = self.root / "apps"
        self.output = self.root / "output"
        self.output.mkdir()
        self.git("init", "-q")
        (self.root / ".gitignore").write_text("apps/\noutput/\npico-sdk/\n")
        self.git("add", ".gitignore")
        self.git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "test: fixture")
        self.commit = self.git("rev-parse", "HEAD")
        (self.root / "pico-sdk").mkdir()
        metadata = self.apps / "apps/metadata"
        metadata.mkdir(parents=True)
        for name, data in {
            "hardware-demo-commit.txt": self.commit,
            "pico-sdk-commit.txt": self.commit,
            "hardware-demo-status.txt": "",
            "pico-sdk-status.txt": "",
        }.items():
            (metadata / name).write_text(data)
        (self.apps / "apps-audit").mkdir()
        (self.apps / "apps-audit/summary.txt").write_text("status=pass\n")
        pkg = self.output / "fixture"
        (pkg / "DEBIAN").mkdir(parents=True)
        (pkg / "DEBIAN/control").write_text(
            "Package: aiden-business\nVersion: 0.0.1-1\nArchitecture: armhf\n"
            "Maintainer: Test <test@example.invalid>\nDescription: Test package\n"
        )
        manifest = pkg / "usr/share/doc/aiden-business/release-manifest.json"
        manifest.parent.mkdir(parents=True)
        manifest.write_text(json.dumps({"business_release": "0.0.1", "package_revision": "1"}))
        subprocess.run(["dpkg-deb", "--build", "--root-owner-group", str(pkg),
                        str(self.output / "aiden-business_0.0.1-1_armhf.deb")], check=True, stdout=subprocess.DEVNULL)
        with patch.dict(os.environ, AIDEN_BUSINESS_VERSION="0.0.1", AIDEN_BUSINESS_REVISION="1"):
            release.stage(self.root, self.apps, self.output)
        self.assets = self.output / "release"

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.root), *args], text=True).strip()

    def test_valid_release(self):
        self.assertEqual(release.verify(self.assets)["tag"], "business-v0.0.1-1")

    def test_corrupted_asset_is_rejected(self):
        with (self.assets / "aiden-business_0.0.1-1_armhf.deb").open("ab") as f:
            f.write(b"corrupt")
        with self.assertRaisesRegex(ValueError, "checksum"):
            release.verify(self.assets)

    def test_unexpected_firmware_asset_is_rejected(self):
        (self.assets / "manifest.json").write_text("{}")
        with self.assertRaisesRegex(ValueError, "unexpected"):
            release.verify(self.assets)

    def test_wrong_package_metadata_is_rejected(self):
        metadata_path = self.assets / "build-metadata.json"
        metadata = json.loads(metadata_path.read_text())
        metadata["architecture"] = "amd64"
        metadata_path.write_text(json.dumps(metadata))
        with self.assertRaisesRegex(ValueError, "architecture"):
            release.verify(self.assets)

    def test_stale_binary_is_rejected(self):
        (self.apps / "apps/metadata/hardware-demo-commit.txt").write_text("0" * 40)
        with self.assertRaisesRegex(ValueError, "another commit"):
            release.check_source(self.root, self.apps)

    def test_dirty_source_is_rejected(self):
        (self.root / "uncommitted").write_text("dirty")
        with self.assertRaisesRegex(ValueError, "Commit source"):
            release.check_source(self.root, self.apps)

    def test_published_release_is_not_overwritten(self):
        mock = self.root / "mock-bin"
        mock.mkdir()
        gh = mock / "gh"
        gh.write_text('#!/bin/sh\nif [ "$1 $2" = "release view" ]; then echo false; exit 0; fi\necho unexpected mutation >&2\nexit 99\n')
        gh.chmod(0o755)
        result = subprocess.run([str(ROOT / "scripts/debian-package/release.sh"), "publish"],
                                env={**os.environ, "PATH": str(mock) + os.pathsep + os.environ["PATH"],
                                     "DEBIAN_PACKAGE_OUTPUT_DIR": str(self.output), "GH_REPO": "test/test"},
                                text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("already published", result.stderr)
        self.assertNotIn("unexpected mutation", result.stderr)

    def publish_with_mock(self, *, draft=False, tag_commit=None, corrupt=False):
        mock = self.root / "mock-publish"
        mock.mkdir()
        if tag_commit:
            (mock / "tag").write_text(tag_commit)
        gh = mock / "gh"
        gh.write_text('''#!/usr/bin/env python3
import json, os, shutil, sys
from pathlib import Path
args = sys.argv[1:]
root = Path(os.environ["MOCK_DIR"])
with (root / "calls").open("a") as log:
    log.write(json.dumps(args) + "\\n")
if args[:2] == ["release", "view"]:
    if os.environ["MOCK_DRAFT"] == "1":
        print("true")
    else:
        sys.exit(1)
elif args[:3] == ["api", "--method", "POST"]:
    (root / "tag").write_text(os.environ["MOCK_COMMIT"])
elif args[0] == "api":
    if not (root / "tag").exists():
        sys.exit(1)
    print((root / "tag").read_text())
elif args[:2] == ["release", "download"]:
    dest = Path(args[args.index("--dir") + 1])
    shutil.copytree(os.environ["MOCK_ASSETS"], dest, dirs_exist_ok=True)
    if os.environ["MOCK_CORRUPT"] == "1":
        (dest / "RELEASE-NOTES.md").write_text("corrupted download")
elif args[:2] not in (["release", "create"], ["release", "upload"], ["release", "edit"]):
    sys.exit(99)
''')
        gh.chmod(0o755)
        result = subprocess.run([str(ROOT / "scripts/debian-package/release.sh"), "publish"],
                                env={**os.environ, "PATH": str(mock) + os.pathsep + os.environ["PATH"],
                                     "DEBIAN_PACKAGE_OUTPUT_DIR": str(self.output), "GH_REPO": "test/test",
                                     "MOCK_DIR": str(mock), "MOCK_ASSETS": str(self.assets),
                                     "MOCK_COMMIT": self.commit, "MOCK_DRAFT": str(int(draft)),
                                     "MOCK_CORRUPT": str(int(corrupt))}, text=True, capture_output=True)
        calls = [json.loads(line) for line in (mock / "calls").read_text().splitlines()]
        return result, calls

    def test_new_release_verifies_download_before_publication(self):
        result, calls = self.publish_with_mock()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(["api", "--method", "POST"], [call[:3] for call in calls])
        self.assertEqual(calls[-2][:2], ["release", "download"])
        self.assertEqual(calls[-1][:2], ["release", "edit"])
        for flag in ("--draft=false", "--prerelease", "--latest=false"):
            self.assertIn(flag, calls[-1])

    def test_existing_draft_can_be_retried(self):
        result, calls = self.publish_with_mock(draft=True, tag_commit=self.commit)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(["release", "create"], [call[:2] for call in calls])
        self.assertEqual(calls[-1][:2], ["release", "edit"])

    def test_conflicting_tag_is_rejected_before_upload(self):
        result, calls = self.publish_with_mock(tag_commit="0" * 40)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not point", result.stderr)
        self.assertNotIn(["release", "upload"], [call[:2] for call in calls])

    def test_corrupted_download_is_not_published(self):
        result, calls = self.publish_with_mock(corrupt=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum", result.stderr)
        self.assertNotIn(["release", "edit"], [call[:2] for call in calls])


class VersionTests(unittest.TestCase):
    def test_defaults(self):
        env = {k: v for k, v in os.environ.items() if k not in ("AIDEN_BUSINESS_VERSION", "AIDEN_BUSINESS_REVISION")}
        result = subprocess.check_output(["bash", "-c", 'source "$1"; echo "$AIDEN_BUSINESS_VERSION-$AIDEN_BUSINESS_REVISION"',
                                          "bash", str(ROOT / "scripts/debian-package/version.sh")], env=env, text=True)
        self.assertEqual(result.strip(), "0.0.1-1")

    def test_unsafe_version_is_rejected(self):
        for version in ("../escape", "1.0.0\nInjected: value", "01.0.0", "1.0.0;echo bad"):
            result = subprocess.run(["bash", "-c", 'source "$1"', "bash", str(ROOT / "scripts/debian-package/version.sh")],
                                    env={**os.environ, "AIDEN_BUSINESS_VERSION": version}, capture_output=True)
            self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    if not shutil.which("dpkg-deb"):
        raise SystemExit("Run release tests on Linux with dpkg-deb installed")
    unittest.main()
