#!/usr/bin/env python3
"""Focused tests for CI sparse-checkout policy parsing."""

import unittest

from check_ci_policy_job import sparse_checkout_paths


class SparseCheckoutPathsTest(unittest.TestCase):
    def test_collects_only_sparse_checkout_arguments(self) -> None:
        job = {
            "steps": [
                {
                    "run": """
echo /path-mentioned-outside-the-command
git -C "$dir" sparse-checkout set --no-cone \\
  /sysdrv/Makefile \\
  /sysdrv/tools/board/buildroot/luckfox_pico_defconfig
"""
                }
            ]
        }

        self.assertEqual(
            sparse_checkout_paths(job),
            {
                "/sysdrv/Makefile",
                "/sysdrv/tools/board/buildroot/luckfox_pico_defconfig",
            },
        )

    def test_echo_only_path_does_not_satisfy_sparse_checkout(self) -> None:
        job = {
            "steps": [
                {
                    "run": """
echo /sysdrv/tools/board/buildroot/python-charset-normalizer-aiden/
git sparse-checkout set --no-cone /sysdrv/Makefile
"""
                }
            ]
        }

        self.assertNotIn(
            "/sysdrv/tools/board/buildroot/python-charset-normalizer-aiden/",
            sparse_checkout_paths(job),
        )

    def test_stops_at_shell_command_separator(self) -> None:
        job = {
            "steps": [
                {
                    "run": (
                        "git sparse-checkout set --no-cone /sysdrv/Makefile; "
                        "echo /not-a-sparse-path"
                    )
                }
            ]
        }

        self.assertEqual(sparse_checkout_paths(job), {"/sysdrv/Makefile"})

    def test_echoed_sparse_checkout_command_is_not_executable_input(self) -> None:
        job = {
            "steps": [
                {
                    "run": (
                        "echo git sparse-checkout set "
                        "/sysdrv/tools/board/buildroot/luckfox_pico_w_defconfig"
                    )
                }
            ]
        }

        self.assertEqual(sparse_checkout_paths(job), set())


if __name__ == "__main__":
    unittest.main()
