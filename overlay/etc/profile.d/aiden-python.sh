# Configure managed Python package environment for interactive login shells.
#
# The Agent installs runtime Python packages under /userdata to keep them out of
# the A/B rootfs slots. Those packages persist across reboot and OTA while the
# Python version remains compatible.
#
# This file exports environment variables that direct pip to the managed user
# base and a /userdata temporary directory, so both Agent-driven installs and
# manual user installs share the same package environment.
#
# Goals:
#
#   - Agent and user shell use the same Python package environment.
#   - pip downloads and extraction stay out of the small /tmp tmpfs (73 MB).
#   - Packages persist across reboot and rootfs OTA.
#   - StorageMonitor can reclaim abandoned temporary data.
#
# Non-goals:
#
#   - Changing PYTHONPATH or modifying sys.path.
#   - Setting PIP_USER, TMPDIR, or other pip flags globally (those are
#     command-scoped when needed).

AIDEN_PYTHON_VERSION=$(/usr/bin/python3 -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")' 2>/dev/null)

if [ -n "$AIDEN_PYTHON_VERSION" ]; then
    AIDEN_PYTHON_ROOT="/userdata/agent/python"
    AIDEN_PYTHON_USERBASE="$AIDEN_PYTHON_ROOT/py$AIDEN_PYTHON_VERSION"
    AIDEN_PYTHON_TMP="$AIDEN_PYTHON_ROOT/tmp"

    export AIDEN_PYTHON_USERBASE
    export AIDEN_PYTHON_TMP

    # Create directories if missing. Errors are non-fatal: the Agent will also
    # ensure these exist when it starts, and a missing directory only affects
    # immediate manual pip usage.
    mkdir -p "$AIDEN_PYTHON_USERBASE" "$AIDEN_PYTHON_TMP" 2>/dev/null || true

    # Direct Python bytecode cache to the managed temporary directory rather than
    # scattering __pycache__ directories across /userdata or /tmp.
    PYTHONPYCACHEPREFIX="$AIDEN_PYTHON_TMP/pycache"
    export PYTHONPYCACHEPREFIX
    mkdir -p "$PYTHONPYCACHEPREFIX" 2>/dev/null || true

    unset AIDEN_PYTHON_ROOT
fi

unset AIDEN_PYTHON_VERSION
