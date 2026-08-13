# Configure managed Python package environment for interactive login shells.
#
# The Agent installs runtime Python packages under /userdata to keep them out of
# the A/B rootfs slots. Those packages persist across reboot and OTA while the
# Python version remains compatible.
#
# This file exports AIDEN_PYTHON_USERBASE so both Agent-driven installs and
# manual user installs share the same package environment. TMPDIR is not set
# globally to avoid affecting other services; the Agent's shell tool injects it
# command-scoped when needed.
#
# Goals:
#
#   - Agent and user shell use the same Python package environment.
#   - Packages persist across reboot and rootfs OTA.
#   - StorageMonitor can reclaim abandoned temporary data.
#
# Non-goals:
#
#   - Setting TMPDIR globally (would increase storage wear for all services).
#   - Changing PYTHONPATH or modifying sys.path.
#   - Setting PIP_USER or other pip flags globally (those are command-scoped).

AIDEN_PYTHON_VERSION=$(/usr/bin/python3 -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")' 2>/dev/null)

if [ -n "$AIDEN_PYTHON_VERSION" ]; then
    AIDEN_PYTHON_ROOT="/userdata/agent/python"
    AIDEN_PYTHON_USERBASE="$AIDEN_PYTHON_ROOT/py$AIDEN_PYTHON_VERSION"

    export AIDEN_PYTHON_USERBASE

    # Create user base directory if missing. The temporary directory is created
    # on demand by the Agent when running shell commands.
    mkdir -p "$AIDEN_PYTHON_USERBASE" 2>/dev/null || true

    unset AIDEN_PYTHON_ROOT
fi

unset AIDEN_PYTHON_VERSION
