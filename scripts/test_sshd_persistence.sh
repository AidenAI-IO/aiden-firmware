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

echo "S50sshd persistence tests passed"
