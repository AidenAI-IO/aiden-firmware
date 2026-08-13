#!/usr/bin/env python3
"""Check how ci.yml runs the reproducible rootfs policy check.

The policy check reads files from pico-sdk, but ci.yml deliberately does not
check out that submodule: the worktree is several GB, and a18cae5e moved the
check out of the release-script job for exactly that reason.

Running it on pull requests is still worth doing -- it previously ran only in
build.yml, which fires on schedule and workflow_dispatch, so a regression stayed
invisible until an 80-minute build failed. The compromise is a dedicated job
that sparse-fetches only the handful of files the check reads.

These invariants are structural, so they are checked against the parsed
workflow rather than by grepping the file. A grep sees a mention of
"filter=blob:none" inside a comment as satisfying the requirement, and cannot
tell which job a line belongs to.
"""

from __future__ import annotations

import sys
from pathlib import Path

POLICY_SCRIPT = "scripts/test_reproducible_rootfs_policy.sh"

# The release-script job has no SDK checkout of any kind, so the policy check
# must never be folded back into it.
RELEASE_SCRIPT_MARKER = "scripts/test_release_ci_scripts.sh"

# Markers of a full checkout. Any of these in a policy job means the job is
# paying the multi-GB cost the sparse fetch exists to avoid.
FULL_CHECKOUT_MARKERS = (
    "git submodule update",
    "git clone",
    "submodules: true",
    "submodules: recursive",
)

REQUIRED_SPARSE_MARKERS = ("filter=blob:none", "sparse-checkout")

# Every project-owned SDK input read directly by the policy script must be
# present in the sparse checkout. Otherwise a policy addition can pass locally
# against the full submodule while the pull-request job fails with a missing
# file before it evaluates the policy itself.
REQUIRED_POLICY_INPUTS = (
    "/sysdrv/Makefile",
    "/sysdrv/tools/board/buildroot/luckfox_pico_defconfig",
    "/sysdrv/tools/board/buildroot/luckfox_pico_w_defconfig",
    "/sysdrv/tools/board/buildroot/python-charset-normalizer-aiden/",
)


def fail(message: str) -> None:
    print(message, file=sys.stderr)
    raise SystemExit(1)


def workflow_triggers(workflow: dict) -> object:
    """Return ci.yml's trigger block.

    YAML 1.1 parses the bare key `on` as the boolean True, so a plain
    workflow["on"] lookup silently misses it.
    """
    for key in ("on", True):
        if key in workflow:
            return workflow[key]
    return None


def triggers_on_pull_request(triggers: object) -> bool:
    if isinstance(triggers, dict):
        return "pull_request" in triggers
    if isinstance(triggers, list):
        return "pull_request" in triggers
    return triggers == "pull_request"


def strip_comments(text: str) -> str:
    """Drop whole-line comments so a mention in prose cannot satisfy a check.

    Trailing comments are left alone: stripping them correctly needs quote
    tracking, and a marker before a trailing '#' is real shell either way.
    """
    return "\n".join(
        line for line in text.splitlines() if not line.lstrip().startswith("#")
    )


def job_shell_text(job: dict) -> str:
    """Concatenate everything in a job that can execute a command."""
    parts: list[str] = []
    steps = job.get("steps")
    if not isinstance(steps, list):
        return ""
    for step in steps:
        if not isinstance(step, dict):
            continue
        run = step.get("run")
        if isinstance(run, str):
            parts.append(run)
        uses = step.get("uses")
        if isinstance(uses, str):
            parts.append(uses)
        # actions/checkout inputs decide whether submodules come along. YAML
        # parses `true` as a bool, so normalise to the spelling used in the
        # workflow rather than Python's `True`.
        with_block = step.get("with")
        if isinstance(with_block, dict):
            for key, value in with_block.items():
                if isinstance(value, bool):
                    value = "true" if value else "false"
                parts.append(f"{key}: {value}")
    return "\n".join(parts)


def main() -> None:
    try:
        import yaml
    except ImportError:
        fail(
            "PyYAML is required to verify the CI policy job structure; "
            "install python3-yaml"
        )

    root = Path(__file__).resolve().parent.parent
    workflow_path = root / ".github/workflows/ci.yml"
    if not workflow_path.is_file():
        fail(f"missing CI workflow: {workflow_path}")

    workflow = yaml.safe_load(workflow_path.read_text())
    jobs = workflow.get("jobs") if isinstance(workflow, dict) else None
    if not isinstance(jobs, dict) or not jobs:
        fail("ci.yml declares no jobs")

    # The policy check has to reach pull requests. Asserting the trigger keeps
    # the per-job checks below meaningful: a job that never fires proves nothing.
    if not triggers_on_pull_request(workflow_triggers(workflow)):
        fail(
            "ci.yml must run on pull_request so the reproducible rootfs policy "
            "check reaches pull requests"
        )

    policy_jobs = {}
    release_jobs = set()
    for name, job in jobs.items():
        if not isinstance(job, dict):
            continue
        text = strip_comments(job_shell_text(job))
        if POLICY_SCRIPT in text:
            policy_jobs[name] = text
        if RELEASE_SCRIPT_MARKER in text:
            release_jobs.add(name)

    # An absent job is the regression this checker exists to catch, not a
    # tolerable state. Running only in build.yml is what kept the stamp
    # regression invisible until an 80-minute scheduled build failed, so a
    # missing policy job has to fail rather than print a note and pass.
    if not policy_jobs:
        fail(
            "ci.yml does not run the reproducible rootfs policy check; add a "
            f"job that runs {POLICY_SCRIPT} against a blobless sparse checkout "
            "of the pinned pico-sdk commit. Running it only in the scheduled "
            "build hides regressions until an 80-minute build fails."
        )

    for name in sorted(policy_jobs):
        text = policy_jobs[name]

        if name in release_jobs:
            fail(
                f"job '{name}' runs both the release-script checks and the "
                "submodule-dependent reproducible rootfs policy check; the "
                "policy check needs a pico-sdk checkout and must stay in its "
                "own job"
            )

        missing = [m for m in REQUIRED_SPARSE_MARKERS if m not in text]
        if missing:
            fail(
                f"job '{name}' runs the reproducible rootfs policy check "
                f"without a blobless sparse checkout (missing: "
                f"{', '.join(missing)}); fetch only the files the check reads "
                "instead of the multi-GB pico-sdk worktree"
            )

        missing_inputs = [path for path in REQUIRED_POLICY_INPUTS if path not in text]
        if missing_inputs:
            fail(
                f"job '{name}' does not sparse-fetch every SDK policy input "
                f"(missing: {', '.join(missing_inputs)}); keep the sparse "
                "checkout aligned with the files read by the policy script"
            )

        for marker in FULL_CHECKOUT_MARKERS:
            if marker in text:
                fail(
                    f"job '{name}' runs the reproducible rootfs policy check "
                    f"but also performs a full checkout ('{marker}'); that "
                    "defeats the sparse fetch and pulls the multi-GB pico-sdk "
                    "worktree"
                )

    print(
        "CI reproducible rootfs policy job structure verified "
        f"({', '.join(sorted(policy_jobs))})"
    )


if __name__ == "__main__":
    main()
