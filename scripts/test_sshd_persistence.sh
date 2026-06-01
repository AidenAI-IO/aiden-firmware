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

echo "old private ed25519" > "$ETC_SSH_DIR/ssh_host_ed25519_key"
echo "old public ed25519" > "$ETC_SSH_DIR/ssh_host_ed25519_key.pub"
echo "old host cert ed25519" > "$ETC_SSH_DIR/ssh_host_ed25519_key-cert.pub"
echo "old authorized key" > "$ROOT_HOME/.ssh/authorized_keys"

BOOT_CONF="$TMP_DIR/missing.conf" \
SSHD_BIN="$BIN_DIR/sshd" \
SSH_KEYGEN_BIN="$BIN_DIR/ssh-keygen" \
PIDOF_BIN="$BIN_DIR/pidof" \
KILLALL_BIN="$BIN_DIR/killall" \
ETC_SSH_DIR="$ETC_SSH_DIR" \
ROOT_HOME="$ROOT_HOME" \
PERSISTENT_SSH_DIR="$PERSISTENT_SSH_DIR" \
SSHD_RUNNING="$SSHD_RUNNING" \
SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
LOCK_FILE="$LOCK_FILE" \
"$SCRIPT" start >/dev/null

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

if [ ! -f "$PERSISTENT_SSH_DIR/host/ssh_host_rsa_key" ] || [ ! -f "$PERSISTENT_SSH_DIR/host/ssh_host_ecdsa_key" ]; then
    echo "missing host keys were not generated in userdata" >&2
    exit 1
fi

if ! grep -q "HostCertificate=$PERSISTENT_SSH_DIR/host/ssh_host_ed25519_key-cert.pub" "$SSHD_ARGS_LOG"; then
    echo "sshd was not started with persistent host certificate" >&2
    cat "$SSHD_ARGS_LOG" >&2
    exit 1
fi

BOOT_CONF="$TMP_DIR/missing.conf" \
SSHD_BIN="$BIN_DIR/sshd" \
SSH_KEYGEN_BIN="$BIN_DIR/ssh-keygen" \
PIDOF_BIN="$BIN_DIR/pidof" \
KILLALL_BIN="$BIN_DIR/killall" \
ETC_SSH_DIR="$ETC_SSH_DIR" \
ROOT_HOME="$ROOT_HOME" \
PERSISTENT_SSH_DIR="$PERSISTENT_SSH_DIR" \
SSHD_RUNNING="$SSHD_RUNNING" \
SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
LOCK_FILE="$LOCK_FILE" \
"$SCRIPT" stop >/dev/null

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

if BOOT_CONF="$TMP_DIR/missing.conf" \
SSHD_BIN="$BIN_DIR/sshd" \
SSH_KEYGEN_BIN="$BIN_DIR/ssh-keygen" \
PIDOF_BIN="$BIN_DIR/pidof" \
KILLALL_BIN="$BIN_DIR/killall" \
ETC_SSH_DIR="$FAIL_ETC_SSH_DIR" \
ROOT_HOME="$FAIL_ROOT_HOME" \
PERSISTENT_SSH_DIR="$FAIL_PERSISTENT_SSH_DIR" \
SSHD_RUNNING="$SSHD_RUNNING" \
SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
LOCK_FILE="$LOCK_FILE" \
FAIL_KEYGEN_FOR="ssh_host_rsa_key" \
"$SCRIPT" start >/dev/null 2>"$TMP_DIR/failcase.err"; then
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

if PATH="$BIN_DIR:$PATH" \
REAL_CP="$REAL_CP" \
BOOT_CONF="$TMP_DIR/missing.conf" \
SSHD_BIN="$BIN_DIR/sshd" \
SSH_KEYGEN_BIN="$BIN_DIR/ssh-keygen" \
PIDOF_BIN="$BIN_DIR/pidof" \
KILLALL_BIN="$BIN_DIR/killall" \
ETC_SSH_DIR="$AUTH_FAIL_ETC_SSH_DIR" \
ROOT_HOME="$AUTH_FAIL_ROOT_HOME" \
PERSISTENT_SSH_DIR="$AUTH_FAIL_PERSISTENT_SSH_DIR" \
SSHD_RUNNING="$SSHD_RUNNING" \
SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
LOCK_FILE="$LOCK_FILE" \
FAIL_CP_FOR="authorized_keys" \
"$SCRIPT" start >/dev/null 2>"$TMP_DIR/authfail.err"; then
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

BOOT_CONF="$TMP_DIR/missing.conf" \
SSHD_BIN="$BIN_DIR/sshd" \
SSH_KEYGEN_BIN="$BIN_DIR/missing-ssh-keygen" \
PIDOF_BIN="$BIN_DIR/pidof" \
KILLALL_BIN="$BIN_DIR/killall" \
ETC_SSH_DIR="$NO_KEYGEN_ETC_SSH_DIR" \
ROOT_HOME="$NO_KEYGEN_ROOT_HOME" \
PERSISTENT_SSH_DIR="$NO_KEYGEN_PERSISTENT_SSH_DIR" \
SSHD_RUNNING="$SSHD_RUNNING" \
SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
LOCK_FILE="$LOCK_FILE" \
"$SCRIPT" start >/dev/null

if [ ! -f "$SSHD_RUNNING" ]; then
    echo "sshd did not start with provisioned host keys and missing ssh-keygen" >&2
    exit 1
fi

BOOT_CONF="$TMP_DIR/missing.conf" \
SSHD_BIN="$BIN_DIR/sshd" \
SSH_KEYGEN_BIN="$BIN_DIR/missing-ssh-keygen" \
PIDOF_BIN="$BIN_DIR/pidof" \
KILLALL_BIN="$BIN_DIR/killall" \
ETC_SSH_DIR="$NO_KEYGEN_ETC_SSH_DIR" \
ROOT_HOME="$NO_KEYGEN_ROOT_HOME" \
PERSISTENT_SSH_DIR="$NO_KEYGEN_PERSISTENT_SSH_DIR" \
SSHD_RUNNING="$SSHD_RUNNING" \
SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
LOCK_FILE="$LOCK_FILE" \
"$SCRIPT" stop >/dev/null

echo "S50sshd persistence tests passed"
