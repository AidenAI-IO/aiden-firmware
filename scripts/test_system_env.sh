#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ENV_RUN="$ROOT_DIR/overlay/oem/usr/bin/aiden-env-run"
PROFILE_SNIPPET="$ROOT_DIR/overlay/etc/profile.d/aiden-env.sh"
PYTHON_PROFILE_SNIPPET="$ROOT_DIR/overlay/etc/profile.d/aiden-python.sh"
AGENT_INIT="$ROOT_DIR/overlay/etc/init.d/S53agent"

if [ ! -x "$ENV_RUN" ]; then
    echo "missing executable aiden-env-run" >&2
    exit 1
fi

if [ ! -f "$PROFILE_SNIPPET" ]; then
    echo "missing SSH/login profile snippet" >&2
    exit 1
fi

if [ ! -f "$PYTHON_PROFILE_SNIPPET" ]; then
    echo "missing Python profile snippet" >&2
    exit 1
fi

if grep -Eq '^[[:space:]]*(mkdir|chmod)[[:space:]]' "$PYTHON_PROFILE_SNIPPET"; then
    echo "Python profile must not create or chmod host directories" >&2
    exit 1
fi

for script in S52frame_service S53audio_service S53agent S54ota S56config_web; do
    path="$ROOT_DIR/overlay/etc/init.d/$script"
    if ! grep -q 'aiden-env-run' "$path"; then
        echo "$script must launch through aiden-env-run" >&2
        exit 1
    fi
done

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

# Production aiden-env-run intentionally hard-codes the profile path so
# /userdata/system/env cannot bypass the Python policy. Rewrite only that fixed
# path in a temporary copy for this host-side test.
TEST_ENV_RUN="$TMP_DIR/aiden-env-run"
sed "s|/etc/profile.d/aiden-python.sh|$PYTHON_PROFILE_SNIPPET|g" \
    "$ENV_RUN" > "$TEST_ENV_RUN"
chmod +x "$TEST_ENV_RUN"

# Exercise S53agent's directory initializer against rewritten temporary paths,
# never the host's absolute /userdata tree.
TEST_PYTHON_USERBASE="$TMP_DIR/userdata/agent/python"
TEST_PYTHON_TMP="$TMP_DIR/userdata/tmp"
TEST_AGENT_INIT="$TMP_DIR/S53agent"
sed \
    -e "s|/userdata/agent/python|$TEST_PYTHON_USERBASE|g" \
    -e "s|/userdata/tmp|$TEST_PYTHON_TMP|g" \
    -e '/^case "${1:-}" in/,$d' \
    "$AGENT_INIT" > "$TEST_AGENT_INIT"

AIDEN_LOG_HELPER="$ROOT_DIR/overlay/oem/usr/lib/aiden-log.sh" \
    sh -c '. "$1"; prepare_managed_python_directories' sh "$TEST_AGENT_INIT"

userbase_mode=$(LC_ALL=C ls -ld "$TEST_PYTHON_USERBASE" | cut -c1-10)
tmp_mode=$(LC_ALL=C ls -ld "$TEST_PYTHON_TMP" | cut -c1-10)
if [ "$userbase_mode" != "drwxr-xr-x" ] || [ "$tmp_mode" != "drwxrwxrwt" ]; then
    echo "S53agent created managed Python directories with wrong permissions" >&2
    echo "userbase: $userbase_mode; tmp: $tmp_mode" >&2
    exit 1
fi

rmdir "$TEST_PYTHON_USERBASE"
mkdir "$TMP_DIR/python-userbase-target"
ln -s "$TMP_DIR/python-userbase-target" "$TEST_PYTHON_USERBASE"
if AIDEN_LOG_HELPER="$ROOT_DIR/overlay/oem/usr/lib/aiden-log.sh" \
    sh -c '. "$1"; prepare_managed_python_directories' sh "$TEST_AGENT_INIT"; then
    echo "S53agent accepted a symlink managed Python user base" >&2
    exit 1
fi

ENV_FILE="$TMP_DIR/system.env"
cat > "$ENV_FILE" <<'EOF'
AIDEN_TEST_VALUE=from-system-env
HTTP_PROXY=http://proxy.example:18080
AIDEN_PYTHON_PROFILE=/should/not/win
PYTHONUSERBASE=/should/not/win
PIP_USER=0
PIP_NO_CACHE_DIR=0
PIP_DISABLE_PIP_VERSION_CHECK=0
EOF

output=$(
    AIDEN_SYSTEM_ENV="$ENV_FILE" \
        "$TEST_ENV_RUN" sh -c 'printf "%s|%s|%s|%s|%s|%s|%s|%s" "$AIDEN_TEST_VALUE" "$HTTP_PROXY" "$NO_PROXY" "$no_proxy" "$PYTHONUSERBASE" "$PIP_USER" "$PIP_NO_CACHE_DIR" "$PIP_DISABLE_PIP_VERSION_CHECK"'
)

expected_no_proxy='localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16'
expected_python='/userdata/agent/python|1|1|1'
if [ "$output" != "from-system-env|http://proxy.example:18080|$expected_no_proxy|$expected_no_proxy|$expected_python" ]; then
    echo "aiden-env-run did not apply system env and fixed Python environment" >&2
    echo "got: $output" >&2
    exit 1
fi

legacy_profile_value=$(
    AIDEN_SYSTEM_ENV="$ENV_FILE" \
        "$TEST_ENV_RUN" sh -c 'printf "%s" "${AIDEN_PYTHON_PROFILE-unset}"'
)
if [ "$legacy_profile_value" != "unset" ]; then
    echo "aiden-env-run leaked the retired AIDEN_PYTHON_PROFILE override" >&2
    exit 1
fi

ERREXIT_ENV_FILE="$TMP_DIR/errexit-system.env"
cat > "$ERREXIT_ENV_FILE" <<'EOF'
AIDEN_TEST_VALUE=before-false
false
AIDEN_TEST_AFTER_FALSE=after-false
EOF

errexit_output=$(
    AIDEN_SYSTEM_ENV="$ERREXIT_ENV_FILE" \
        "$TEST_ENV_RUN" sh -c 'printf "%s|%s" "$AIDEN_TEST_VALUE" "$AIDEN_TEST_AFTER_FALSE"'
)

if [ "$errexit_output" != "before-false|after-false" ]; then
    echo "aiden-env-run did not isolate errexit while sourcing system env" >&2
    echo "got: $errexit_output" >&2
    exit 1
fi

profile_output=$(
    AIDEN_SYSTEM_ENV="$ENV_FILE" sh -c '. "$1"; . "$2"; printf "%s|%s|%s|%s|%s|%s" "$AIDEN_TEST_VALUE" "$NO_PROXY" "$PYTHONUSERBASE" "$PIP_USER" "$PIP_NO_CACHE_DIR" "$PIP_DISABLE_PIP_VERSION_CHECK"' sh "$PROFILE_SNIPPET" "$PYTHON_PROFILE_SNIPPET"
)

if [ "$profile_output" != "from-system-env|$expected_no_proxy|$expected_python" ]; then
    echo "profile snippets did not apply system env and fixed Python environment" >&2
    echo "got: $profile_output" >&2
    exit 1
fi

echo "system env tests passed"
