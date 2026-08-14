# Configure the fixed Python package environment shared by services and login
# shells. aiden-env-run sources this file after /userdata/system/env so the
# managed paths and pip policy cannot drift between the Agent and user shells.
#
# The Agent installs runtime Python packages under /userdata to keep them out of
# the A/B rootfs slots. Those packages persist across reboot and OTA while the
# Python version remains compatible.
#
# This file exports PYTHONUSERBASE and pip configuration flags globally so both
# Agent-driven installs and manual user installs work identically without
# command-scoped environment manipulation.
#
# PYTHONUSERBASE=/userdata/agent/python uses pip's natural layout:
#   /userdata/agent/python/lib/python3.11/site-packages/
#   /userdata/agent/python/lib/python3.12/site-packages/
# Different firmware Python versions therefore keep separate import directories
# while sharing the user-base bin/ directory.

# Set PYTHONUSERBASE to the managed root without version suffix.
# pip creates version-specific directories under lib/ automatically.
PYTHONUSERBASE=/userdata/agent/python
export PYTHONUSERBASE

# Configure pip to use user-install mode and disable caching globally.
# This ensures consistent behavior for both agent and user shell installs.
export PIP_USER=1
export PIP_NO_CACHE_DIR=1
export PIP_DISABLE_PIP_VERSION_CHECK=1

# TMPDIR is deliberately not exported here: only Agent shell commands use
# /userdata/tmp automatically. S53agent prepares both managed directories at
# service startup, and the Go runtime validates them again before enabling the
# shell override.
