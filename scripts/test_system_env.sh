#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ENV_RUN="$ROOT_DIR/overlay/oem/usr/bin/aiden-env-run"
PROFILE_SNIPPET="$ROOT_DIR/overlay/etc/profile.d/aiden-env.sh"

if [ ! -x "$ENV_RUN" ]; then
    echo "missing executable aiden-env-run" >&2
    exit 1
fi

if [ ! -f "$PROFILE_SNIPPET" ]; then
    echo "missing SSH/login profile snippet" >&2
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

ENV_FILE="$TMP_DIR/system.env"
cat > "$ENV_FILE" <<'EOF'
AIDEN_TEST_VALUE=from-system-env
HTTP_PROXY=http://proxy.example:18080
EOF

output=$(
    AIDEN_SYSTEM_ENV="$ENV_FILE" "$ENV_RUN" sh -c 'printf "%s|%s|%s|%s" "$AIDEN_TEST_VALUE" "$HTTP_PROXY" "$NO_PROXY" "$no_proxy"'
)

expected_no_proxy='localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16'
if [ "$output" != "from-system-env|http://proxy.example:18080|$expected_no_proxy|$expected_no_proxy" ]; then
    echo "aiden-env-run did not apply system env and default no_proxy" >&2
    echo "got: $output" >&2
    exit 1
fi

ERREXIT_ENV_FILE="$TMP_DIR/errexit-system.env"
cat > "$ERREXIT_ENV_FILE" <<'EOF'
AIDEN_TEST_VALUE=before-false
false
AIDEN_TEST_AFTER_FALSE=after-false
EOF

errexit_output=$(
    AIDEN_SYSTEM_ENV="$ERREXIT_ENV_FILE" "$ENV_RUN" sh -c 'printf "%s|%s" "$AIDEN_TEST_VALUE" "$AIDEN_TEST_AFTER_FALSE"'
)

if [ "$errexit_output" != "before-false|after-false" ]; then
    echo "aiden-env-run did not isolate errexit while sourcing system env" >&2
    echo "got: $errexit_output" >&2
    exit 1
fi

profile_output=$(
    AIDEN_SYSTEM_ENV="$ENV_FILE" sh -c '. "$1"; printf "%s|%s" "$AIDEN_TEST_VALUE" "$NO_PROXY"' sh "$PROFILE_SNIPPET"
)

if [ "$profile_output" != "from-system-env|$expected_no_proxy" ]; then
    echo "profile snippet did not apply system env" >&2
    echo "got: $profile_output" >&2
    exit 1
fi

echo "system env tests passed"
