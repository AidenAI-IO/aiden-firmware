#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT_DIR/overlay/etc/init.d/S50sshd"

if [ ! -f "$SCRIPT" ]; then
    echo "missing $SCRIPT" >&2
    exit 1
fi

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

BIN_DIR="$TMP_DIR/bin"
ETC_SSH_DIR="$TMP_DIR/etc/ssh"
ROOT_HOME="$TMP_DIR/root"
PERSISTENT_SSH_DIR="$TMP_DIR/userdata/ssh"
SSHD_RUNNING="$TMP_DIR/sshd.running"
SSHD_ARGS_LOG="$TMP_DIR/sshd.args"
LOCK_FILE="$TMP_DIR/sshd.lock"
REAL_CP=$(command -v cp)

mkdir -p "$BIN_DIR" "$ETC_SSH_DIR" "$ROOT_HOME/.ssh"

cat > "$BIN_DIR/ssh-keygen" <<'EOF'
#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do
    if [ "$1" = "-f" ]; then
        shift
        out="${1:-}"
    fi
    shift || break
done
[ -n "$out" ] || exit 1
if [ -n "${FAIL_KEYGEN_FOR:-}" ]; then
    case "$out" in
        *"$FAIL_KEYGEN_FOR"*) exit 42 ;;
    esac
fi
if [ -n "${EMPTY_KEYGEN_FOR:-}" ]; then
    case "$out" in
        *"$EMPTY_KEYGEN_FOR"*) : > "$out"; exit 42 ;;
    esac
fi
echo "generated private $out" > "$out"
echo "generated public $out" > "$out.pub"
EOF
chmod +x "$BIN_DIR/ssh-keygen"

cat > "$BIN_DIR/sshd" <<'EOF'
#!/bin/sh
echo "$*" > "$SSHD_ARGS_LOG"
touch "$SSHD_RUNNING"
EOF
chmod +x "$BIN_DIR/sshd"

cat > "$BIN_DIR/pidof" <<'EOF'
#!/bin/sh
[ "${1:-}" = "sshd" ] && [ -f "$SSHD_RUNNING" ]
EOF
chmod +x "$BIN_DIR/pidof"

cat > "$BIN_DIR/killall" <<'EOF'
#!/bin/sh
[ "${1:-}" = "sshd" ] && rm -f "$SSHD_RUNNING"
EOF
chmod +x "$BIN_DIR/killall"

cat > "$BIN_DIR/cp" <<'EOF'
#!/bin/sh
if [ -n "${FAIL_CP_FOR:-}" ]; then
    for arg in "$@"; do
        case "$arg" in
            *"$FAIL_CP_FOR"*) exit 43 ;;
        esac
    done
fi
exec "$REAL_CP" "$@"
EOF
chmod +x "$BIN_DIR/cp"

# run_sshd action etc-dir root-home persistent-dir [extra env assignments...]
# Invokes the init script with the standard mocked environment. Extra env vars
# (FAIL_KEYGEN_FOR, EMPTY_KEYGEN_FOR, FAIL_CP_FOR, ...) are passed through
# verbatim so individual cases can opt into failure injection.
run_sshd() {
    action="$1"
    etc_dir="$2"
    root_home="$3"
    persistent_dir="$4"
    shift 4
    env \
        BOOT_CONF="$TMP_DIR/missing.conf" \
        SSHD_BIN="$BIN_DIR/sshd" \
        SSH_KEYGEN_BIN="$BIN_DIR/ssh-keygen" \
        PIDOF_BIN="$BIN_DIR/pidof" \
        KILLALL_BIN="$BIN_DIR/killall" \
        ETC_SSH_DIR="$etc_dir" \
        ROOT_HOME="$root_home" \
        PERSISTENT_SSH_DIR="$persistent_dir" \
        SSHD_RUNNING="$SSHD_RUNNING" \
        SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
        LOCK_FILE="$LOCK_FILE" \
        PATH="$BIN_DIR:$PATH" \
        REAL_CP="$REAL_CP" \
        "$@" \
        "$SCRIPT" "$action"
}

echo "old private ed25519" > "$ETC_SSH_DIR/ssh_host_ed25519_key"
echo "old public ed25519" > "$ETC_SSH_DIR/ssh_host_ed25519_key.pub"
echo "old host cert ed25519" > "$ETC_SSH_DIR/ssh_host_ed25519_key-cert.pub"
echo "old authorized key" > "$ROOT_HOME/.ssh/authorized_keys"

run_sshd start "$ETC_SSH_DIR" "$ROOT_HOME" "$PERSISTENT_SSH_DIR" >/dev/null

if [ "$(cat "$PERSISTENT_SSH_DIR/host/ssh_host_ed25519_key")" != "old private ed25519" ]; then
    echo "existing host key was not migrated to userdata" >&2
    exit 1
fi

if [ "$(cat "$PERSISTENT_SSH_DIR/root/authorized_keys")" != "old authorized key" ]; then
    echo "existing authorized_keys was not migrated to userdata" >&2
    exit 1
fi

if [ "$(readlink "$ETC_SSH_DIR/ssh_host_ed25519_key")" != "$PERSISTENT_SSH_DIR/host/ssh_host_ed25519_key" ]; then
    echo "host key was not linked back into /etc/ssh" >&2
    exit 1
fi

if [ "$(readlink "$ROOT_HOME/.ssh/authorized_keys")" != "$PERSISTENT_SSH_DIR/root/authorized_keys" ]; then
    echo "authorized_keys was not linked back into /root/.ssh" >&2
    exit 1
fi

if [ ! -s "$PERSISTENT_SSH_DIR/host/ssh_host_rsa_key" ] || [ ! -s "$PERSISTENT_SSH_DIR/host/ssh_host_ecdsa_key" ]; then
    echo "missing host keys were not generated in userdata" >&2
    exit 1
fi

if ! grep -q "HostCertificate=$PERSISTENT_SSH_DIR/host/ssh_host_ed25519_key-cert.pub" "$SSHD_ARGS_LOG"; then
    echo "sshd was not started with persistent host certificate" >&2
    cat "$SSHD_ARGS_LOG" >&2
    exit 1
fi

run_sshd stop "$ETC_SSH_DIR" "$ROOT_HOME" "$PERSISTENT_SSH_DIR" >/dev/null

if [ -f "$SSHD_RUNNING" ]; then
    echo "sshd stop did not invoke killall" >&2
    exit 1
fi

FAIL_DIR="$TMP_DIR/failcase"
FAIL_ETC_SSH_DIR="$FAIL_DIR/etc/ssh"
FAIL_ROOT_HOME="$FAIL_DIR/root"
FAIL_PERSISTENT_SSH_DIR="$FAIL_DIR/userdata/ssh"
mkdir -p "$FAIL_ETC_SSH_DIR" "$FAIL_ROOT_HOME/.ssh"
echo "existing ed25519" > "$FAIL_ETC_SSH_DIR/ssh_host_ed25519_key"

if run_sshd start "$FAIL_ETC_SSH_DIR" "$FAIL_ROOT_HOME" "$FAIL_PERSISTENT_SSH_DIR" \
    FAIL_KEYGEN_FOR="ssh_host_rsa_key" \
    >/dev/null 2>"$TMP_DIR/failcase.err"; then
    echo "sshd start succeeded despite failed host-key generation" >&2
    exit 1
fi

if ! grep -q "failed to generate SSH host key" "$TMP_DIR/failcase.err"; then
    echo "missing host-key generation failure message" >&2
    cat "$TMP_DIR/failcase.err" >&2
    exit 1
fi

if [ -f "$SSHD_RUNNING" ]; then
    echo "sshd was started despite failed host-key generation" >&2
    exit 1
fi

AUTH_FAIL_DIR="$TMP_DIR/authfail"
AUTH_FAIL_ETC_SSH_DIR="$AUTH_FAIL_DIR/etc/ssh"
AUTH_FAIL_ROOT_HOME="$AUTH_FAIL_DIR/root"
AUTH_FAIL_PERSISTENT_SSH_DIR="$AUTH_FAIL_DIR/userdata/ssh"
mkdir -p "$AUTH_FAIL_ETC_SSH_DIR" "$AUTH_FAIL_ROOT_HOME/.ssh"
echo "working authorized key" > "$AUTH_FAIL_ROOT_HOME/.ssh/authorized_keys"

if run_sshd start "$AUTH_FAIL_ETC_SSH_DIR" "$AUTH_FAIL_ROOT_HOME" "$AUTH_FAIL_PERSISTENT_SSH_DIR" \
    FAIL_CP_FOR="authorized_keys" \
    >/dev/null 2>"$TMP_DIR/authfail.err"; then
    echo "sshd start succeeded despite failed authorized_keys persistence" >&2
    exit 1
fi

if ! grep -q "failed to persist authorized_keys" "$TMP_DIR/authfail.err"; then
    echo "missing authorized_keys persistence failure message" >&2
    cat "$TMP_DIR/authfail.err" >&2
    exit 1
fi

if [ -L "$AUTH_FAIL_ROOT_HOME/.ssh/authorized_keys" ]; then
    echo "authorized_keys was replaced with a symlink after failed persistence" >&2
    exit 1
fi

if [ "$(cat "$AUTH_FAIL_ROOT_HOME/.ssh/authorized_keys")" != "working authorized key" ]; then
    echo "existing authorized_keys was modified after failed persistence" >&2
    exit 1
fi

if [ -f "$SSHD_RUNNING" ]; then
    echo "sshd was started despite failed authorized_keys persistence" >&2
    exit 1
fi

PARTIAL_KEYGEN_DIR="$TMP_DIR/partial-keygen"
PARTIAL_KEYGEN_ETC_SSH_DIR="$PARTIAL_KEYGEN_DIR/etc/ssh"
PARTIAL_KEYGEN_ROOT_HOME="$PARTIAL_KEYGEN_DIR/root"
PARTIAL_KEYGEN_PERSISTENT_SSH_DIR="$PARTIAL_KEYGEN_DIR/userdata/ssh"
mkdir -p "$PARTIAL_KEYGEN_ETC_SSH_DIR" "$PARTIAL_KEYGEN_ROOT_HOME/.ssh"

if run_sshd start "$PARTIAL_KEYGEN_ETC_SSH_DIR" "$PARTIAL_KEYGEN_ROOT_HOME" "$PARTIAL_KEYGEN_PERSISTENT_SSH_DIR" \
    EMPTY_KEYGEN_FOR="ssh_host_rsa_key" \
    >/dev/null 2>"$TMP_DIR/partial-keygen.err"; then
    echo "sshd start succeeded despite empty failed host-key generation" >&2
    exit 1
fi

if [ -f "$PARTIAL_KEYGEN_PERSISTENT_SSH_DIR/host/ssh_host_rsa_key" ]; then
    echo "empty failed host-key generation artifact was not removed" >&2
    exit 1
fi

if [ -f "$SSHD_RUNNING" ]; then
    echo "sshd was started despite empty failed host-key generation" >&2
    exit 1
fi

EMPTY_PERSISTENT_DIR="$TMP_DIR/empty-persistent"
EMPTY_PERSISTENT_ETC_SSH_DIR="$EMPTY_PERSISTENT_DIR/etc/ssh"
EMPTY_PERSISTENT_ROOT_HOME="$EMPTY_PERSISTENT_DIR/root"
EMPTY_PERSISTENT_SSH_DIR="$EMPTY_PERSISTENT_DIR/userdata/ssh"
EMPTY_PERSISTENT_HOST_DIR="$EMPTY_PERSISTENT_SSH_DIR/host"
mkdir -p "$EMPTY_PERSISTENT_ETC_SSH_DIR" "$EMPTY_PERSISTENT_ROOT_HOME/.ssh" "$EMPTY_PERSISTENT_HOST_DIR"
for type in rsa ecdsa ed25519; do
    : > "$EMPTY_PERSISTENT_HOST_DIR/ssh_host_${type}_key"
done

run_sshd start "$EMPTY_PERSISTENT_ETC_SSH_DIR" "$EMPTY_PERSISTENT_ROOT_HOME" "$EMPTY_PERSISTENT_SSH_DIR" >/dev/null

for type in rsa ecdsa ed25519; do
    if [ ! -s "$EMPTY_PERSISTENT_HOST_DIR/ssh_host_${type}_key" ]; then
        echo "empty persistent $type host key was not regenerated" >&2
        exit 1
    fi
done

run_sshd stop "$EMPTY_PERSISTENT_ETC_SSH_DIR" "$EMPTY_PERSISTENT_ROOT_HOME" "$EMPTY_PERSISTENT_SSH_DIR" >/dev/null

EMPTY_ETC_DIR="$TMP_DIR/empty-etc"
EMPTY_ETC_SSH_DIR="$EMPTY_ETC_DIR/etc/ssh"
EMPTY_ETC_ROOT_HOME="$EMPTY_ETC_DIR/root"
EMPTY_ETC_PERSISTENT_SSH_DIR="$EMPTY_ETC_DIR/userdata/ssh"
EMPTY_ETC_HOST_DIR="$EMPTY_ETC_PERSISTENT_SSH_DIR/host"
mkdir -p "$EMPTY_ETC_SSH_DIR" "$EMPTY_ETC_ROOT_HOME/.ssh"
for type in rsa ecdsa ed25519; do
    : > "$EMPTY_ETC_SSH_DIR/ssh_host_${type}_key"
done

run_sshd start "$EMPTY_ETC_SSH_DIR" "$EMPTY_ETC_ROOT_HOME" "$EMPTY_ETC_PERSISTENT_SSH_DIR" >/dev/null

for type in rsa ecdsa ed25519; do
    if [ ! -s "$EMPTY_ETC_HOST_DIR/ssh_host_${type}_key" ]; then
        echo "empty /etc/ssh $type host key was migrated instead of regenerated" >&2
        exit 1
    fi
    if [ "$(readlink "$EMPTY_ETC_SSH_DIR/ssh_host_${type}_key")" != "$EMPTY_ETC_HOST_DIR/ssh_host_${type}_key" ]; then
        echo "empty /etc/ssh $type host key was not replaced with symlink to persistent key" >&2
        exit 1
    fi
done

run_sshd stop "$EMPTY_ETC_SSH_DIR" "$EMPTY_ETC_ROOT_HOME" "$EMPTY_ETC_PERSISTENT_SSH_DIR" >/dev/null

NO_KEYGEN_DIR="$TMP_DIR/no-keygen"
NO_KEYGEN_ETC_SSH_DIR="$NO_KEYGEN_DIR/etc/ssh"
NO_KEYGEN_ROOT_HOME="$NO_KEYGEN_DIR/root"
NO_KEYGEN_PERSISTENT_SSH_DIR="$NO_KEYGEN_DIR/userdata/ssh"
NO_KEYGEN_HOST_DIR="$NO_KEYGEN_PERSISTENT_SSH_DIR/host"
mkdir -p "$NO_KEYGEN_ETC_SSH_DIR" "$NO_KEYGEN_ROOT_HOME/.ssh" "$NO_KEYGEN_HOST_DIR" "$NO_KEYGEN_PERSISTENT_SSH_DIR/root"
for type in rsa ecdsa ed25519; do
    echo "provisioned $type" > "$NO_KEYGEN_HOST_DIR/ssh_host_${type}_key"
    echo "provisioned $type pub" > "$NO_KEYGEN_HOST_DIR/ssh_host_${type}_key.pub"
done
echo "provisioned authorized key" > "$NO_KEYGEN_PERSISTENT_SSH_DIR/root/authorized_keys"

run_sshd start "$NO_KEYGEN_ETC_SSH_DIR" "$NO_KEYGEN_ROOT_HOME" "$NO_KEYGEN_PERSISTENT_SSH_DIR" \
    SSH_KEYGEN_BIN="$BIN_DIR/missing-ssh-keygen" \
    >/dev/null

if [ ! -f "$SSHD_RUNNING" ]; then
    echo "sshd did not start with provisioned host keys and missing ssh-keygen" >&2
    exit 1
fi

run_sshd stop "$NO_KEYGEN_ETC_SSH_DIR" "$NO_KEYGEN_ROOT_HOME" "$NO_KEYGEN_PERSISTENT_SSH_DIR" \
    SSH_KEYGEN_BIN="$BIN_DIR/missing-ssh-keygen" \
    >/dev/null

echo "S50sshd persistence tests passed"

