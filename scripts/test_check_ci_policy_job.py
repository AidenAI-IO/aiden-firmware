#!/usr/bin/env python3
"""Focused tests for CI sparse-checkout policy parsing."""

import unittest

from check_ci_policy_job import sparse_checkout_paths_before_policy


class SparseCheckoutPathsBeforePolicyTest(unittest.TestCase):
    def test_collects_only_sparse_checkout_arguments(self) -> None:
        job = {
            "steps": [
                {
                    "run": """
echo /path-mentioned-outside-the-command
git -C "$dir" sparse-checkout set --no-cone \\
  /sysdrv/Makefile \\
  /sysdrv/tools/board/buildroot/luckfox_pico_defconfig
bash scripts/test_reproducible_rootfs_policy.sh
"""
                }
            ]
        }

        self.assertEqual(
            sparse_checkout_paths_before_policy(job),
            [
                {
                    "/sysdrv/Makefile",
                    "/sysdrv/tools/board/buildroot/luckfox_pico_defconfig",
                }
            ],
        )

    def test_echo_only_path_does_not_satisfy_sparse_checkout(self) -> None:
        job = {
            "steps": [
                {
                    "run": """
echo /sysdrv/tools/board/buildroot/python-charset-normalizer-aiden/
git sparse-checkout set --no-cone /sysdrv/Makefile
bash scripts/test_reproducible_rootfs_policy.sh
"""
                }
            ]
        }

        self.assertNotIn(
            "/sysdrv/tools/board/buildroot/python-charset-normalizer-aiden/",
            sparse_checkout_paths_before_policy(job)[0],
        )

    def test_stops_at_shell_command_separator(self) -> None:
        job = {
            "steps": [
                {
                    "run": (
                        "git sparse-checkout set --no-cone /sysdrv/Makefile; "
                        "echo /not-a-sparse-path\n"
                        "bash scripts/test_reproducible_rootfs_policy.sh"
                    )
                }
            ]
        }

        self.assertEqual(
            sparse_checkout_paths_before_policy(job), [{"/sysdrv/Makefile"}]
        )

    def test_tracks_sparse_checkout_before_policy_on_same_line(self) -> None:
        job = {
            "steps": [
                {
                    "run": (
                        "git sparse-checkout set --no-cone /sysdrv/Makefile && "
                        "bash scripts/test_reproducible_rootfs_policy.sh"
                    )
                }
            ]
        }

        self.assertEqual(
            sparse_checkout_paths_before_policy(job), [{"/sysdrv/Makefile"}]
        )

    def test_echoed_sparse_checkout_command_is_not_executable_input(self) -> None:
        job = {
            "steps": [
                {
                    "run": (
                        "echo git sparse-checkout set "
                        "/sysdrv/tools/board/buildroot/luckfox_pico_w_defconfig\n"
                        "bash scripts/test_reproducible_rootfs_policy.sh"
                    )
                }
            ]
        }

        self.assertEqual(sparse_checkout_paths_before_policy(job), [set()])

    def test_sparse_checkout_after_policy_does_not_satisfy_run(self) -> None:
        job = {
            "steps": [
                {
                    "run": """
bash scripts/test_reproducible_rootfs_policy.sh
git sparse-checkout set --no-cone /added-too-late
"""
                }
            ]
        }

        self.assertEqual(sparse_checkout_paths_before_policy(job), [set()])

    def test_later_sparse_checkout_replaces_paths_before_policy(self) -> None:
        job = {
            "steps": [
                {
                    "run": """
git sparse-checkout set --no-cone /required /other
git sparse-checkout set --no-cone /other
bash scripts/test_reproducible_rootfs_policy.sh
"""
                }
            ]
        }

        self.assertEqual(sparse_checkout_paths_before_policy(job), [{"/other"}])


if __name__ == "__main__":
    unittest.main()
