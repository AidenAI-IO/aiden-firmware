#!/usr/bin/env python3
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

try:
    from . import run_manifest
except ImportError:
    sys.path.insert(0, str(Path(__file__).resolve().parent))
    import run_manifest


class ManifestRunnerTest(unittest.TestCase):
    def test_manifest_has_unique_required_deterministic_suites(self):
        suites = run_manifest.load_manifest()
        names = [suite["name"] for suite in suites]
        self.assertEqual(len(names), len(set(names)))
        self.assertIn("contracts", names)
        self.assertIn("cpp-host", names)
        self.assertIn("go-unit", names)
        self.assertIn("web", names)
        for suite in suites:
            self.assertTrue(suite["paths"])
            self.assertTrue(suite["command"].strip())
            if suite["required"]:
                self.assertTrue(suite["dependencies"])

    def test_quick_profile_keeps_smoke_tests_and_full_keeps_required_suites(self):
        suites = run_manifest.load_manifest()
        quick = {suite["name"] for suite in run_manifest.select_suites(suites, profile="quick")}
        full = {suite["name"] for suite in run_manifest.select_suites(suites, profile="full")}
        self.assertEqual(quick, {
            "manifest-runner", "contracts", "cpp-host", "go-static", "go-unit",
            "web", "benchmark-python", "skillopt-python",
        })
        self.assertEqual(full, {suite["name"] for suite in suites if suite["required"]})
        self.assertIn("script-contracts", full)
        self.assertIn("docker-sandbox-contract", full)
        self.assertNotIn("production-cross-smoke", full)
        by_name = {suite["name"]: suite for suite in suites}
        self.assertIn("-R '^aiden_tests$'", by_name["cpp-host"]["quick_command"])
        self.assertIn("-run '^TestShared'", by_name["go-unit"]["quick_command"])
        self.assertIn("tests/test_suite.py", by_name["benchmark-python"]["quick_command"])

    def test_quick_profile_uses_short_command_without_changing_full(self):
        suite = {
            "name": "smoke", "class": "deterministic", "required": True,
            "paths": ["tests"], "workdir": ".", "dependencies": [],
            "timeout_seconds": 5, "command": "printf full", "quick_command": "printf quick",
        }
        with patch.object(run_manifest.subprocess, "run", return_value=SimpleNamespace(returncode=0)) as run, patch("builtins.print"):
            self.assertEqual(run_manifest.run_suite(suite, profile="quick")["status"], "passed")
            self.assertIn("printf quick", run.call_args.args[0])
            self.assertEqual(run_manifest.run_suite(suite, profile="full")["status"], "passed")
            self.assertIn("printf full", run.call_args.args[0])

    def test_explicit_suite_and_required_optional_suite_remain_selectable(self):
        suites = run_manifest.load_manifest()
        selected = run_manifest.select_suites(suites, profile="quick", requested={"script-contracts"})
        self.assertEqual([suite["name"] for suite in selected], ["script-contracts"])
        promoted = run_manifest.select_suites(
            suites, profile="quick", required_names={"production-cross-smoke"},
        )
        self.assertTrue(next(suite for suite in promoted if suite["name"] == "production-cross-smoke")["required"])
        with self.assertRaises(run_manifest.ManifestError):
            run_manifest.select_suites(suites, profile="quick", requested={"typo"})
        with self.assertRaisesRegex(run_manifest.ManifestError, "excluded by --suite"):
            run_manifest.select_suites(
                suites, profile="full", requested={"web"},
                required_names={"production-cross-smoke"},
            )
        with self.assertRaisesRegex(run_manifest.ManifestError, "excluded by --class"):
            run_manifest.select_suites(
                suites, profile="full", suite_class="docker",
                required_names={"production-cross-smoke"},
            )

    def test_explicit_optional_suite_fails_instead_of_silently_skipping(self):
        optional = {
            "name": "on-demand", "required": False, "class": "docker",
            "paths": ["tests"], "required_inputs": ["tests/missing-on-demand-fixture"],
        }
        selected = run_manifest.select_suites([optional], profile="quick", requested={"on-demand"})
        self.assertTrue(selected[0]["required"])
        result = run_manifest.run_suite(selected[0])
        self.assertEqual(result["status"], "failed")
        self.assertFalse(result["executed"])
        self.assertIn("missing required inputs", result["reason"])

    def test_optional_production_smoke_is_explicit(self):
        suites = {suite["name"]: suite for suite in run_manifest.load_manifest()}
        self.assertFalse(suites["production-cross-smoke"]["required"])
        self.assertEqual(suites["production-cross-smoke"]["class"], "production")
        self.assertTrue(suites["production-cross-smoke"]["required_inputs"])
        self.assertTrue(suites["docker-package-contract"]["requires_docker_socket"])
        self.assertTrue(suites["docker-sandbox-smoke"]["requires_host_network"])


if __name__ == "__main__":
    unittest.main()
