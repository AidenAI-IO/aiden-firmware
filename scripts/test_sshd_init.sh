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
ROOT_SSH_DIR="$ROOT_HOME/.ssh"
PERSISTENT_HOST_DIR="$ROOT_SSH_DIR/host"
LEGACY_HOST_DIR="$TMP_DIR/userdata/ssh/host"
SSHD_RUNNING="$TMP_DIR/sshd.running"
SSHD_ARGS_LOG="$TMP_DIR/sshd.args"
LOCK_FILE="$TMP_DIR/sshd.lock"
REAL_CP=$(command -v cp)

mkdir -p "$BIN_DIR" "$ETC_SSH_DIR" "$ROOT_SSH_DIR" "$LEGACY_HOST_DIR"

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

run_sshd() {
	action="$1"
	shift
	env \
		BOOT_CONF="$TMP_DIR/missing.conf" \
		SSHD_BIN="$BIN_DIR/sshd" \
		SSH_KEYGEN_BIN="$BIN_DIR/ssh-keygen" \
		PIDOF_BIN="$BIN_DIR/pidof" \
		KILLALL_BIN="$BIN_DIR/killall" \
		ETC_SSH_DIR="$ETC_SSH_DIR" \
		ROOT_HOME="$ROOT_HOME" \
		ROOT_SSH_DIR="$ROOT_SSH_DIR" \
		PERSISTENT_HOST_DIR="$PERSISTENT_HOST_DIR" \
		LEGACY_PERSISTENT_HOST_DIR="$LEGACY_HOST_DIR" \
		SSHD_RUNNING="$SSHD_RUNNING" \
		SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
		LOCK_FILE="$LOCK_FILE" \
		PATH="$BIN_DIR:$PATH" \
		REAL_CP="$REAL_CP" \
		"$@" \
		"$SCRIPT" "$action"
}

echo "legacy private ed25519" > "$LEGACY_HOST_DIR/ssh_host_ed25519_key"
echo "legacy public ed25519" > "$LEGACY_HOST_DIR/ssh_host_ed25519_key.pub"
echo "legacy host cert ed25519" > "$LEGACY_HOST_DIR/ssh_host_ed25519_key-cert.pub"

run_sshd start >/dev/null

if [ "$(cat "$PERSISTENT_HOST_DIR/ssh_host_ed25519_key")" != "legacy private ed25519" ]; then
	echo "legacy host key was not migrated into /root" >&2
	exit 1
fi

if [ "$(readlink "$ETC_SSH_DIR/ssh_host_ed25519_key")" != "$PERSISTENT_HOST_DIR/ssh_host_ed25519_key" ]; then
	echo "host key was not linked back into /etc/ssh" >&2
	exit 1
fi

if [ ! -s "$PERSISTENT_HOST_DIR/ssh_host_rsa_key" ] || [ ! -s "$PERSISTENT_HOST_DIR/ssh_host_ecdsa_key" ]; then
	echo "missing host keys were not generated under /root/.ssh/host" >&2
	exit 1
fi

if ! grep -q "HostCertificate=$PERSISTENT_HOST_DIR/ssh_host_ed25519_key-cert.pub" "$SSHD_ARGS_LOG"; then
	echo "sshd was not started with persisted host certificate" >&2
	cat "$SSHD_ARGS_LOG" >&2
	exit 1
fi

if [ ! -f "$LOCK_FILE" ] || [ ! -f "$SSHD_RUNNING" ]; then
	echo "sshd did not start cleanly" >&2
	exit 1
fi

run_sshd stop >/dev/null

if [ -f "$LOCK_FILE" ] || [ -f "$SSHD_RUNNING" ]; then
	echo "sshd stop did not clean up state" >&2
	exit 1
fi

ETC_MIGRATION_DIR="$TMP_DIR/etc-migration"
ETC_MIGRATION_ETC="$ETC_MIGRATION_DIR/etc/ssh"
ETC_MIGRATION_ROOT="$ETC_MIGRATION_DIR/root"
ETC_MIGRATION_ROOT_SSH="$ETC_MIGRATION_ROOT/.ssh"
ETC_MIGRATION_HOST="$ETC_MIGRATION_ROOT_SSH/host"
ETC_MIGRATION_LEGACY="$ETC_MIGRATION_DIR/userdata/ssh/host"
mkdir -p "$ETC_MIGRATION_ETC" "$ETC_MIGRATION_ROOT_SSH" "$ETC_MIGRATION_LEGACY"
echo "etc private ecdsa" > "$ETC_MIGRATION_ETC/ssh_host_ecdsa_key"
echo "etc public ecdsa" > "$ETC_MIGRATION_ETC/ssh_host_ecdsa_key.pub"

env \
	BOOT_CONF="$TMP_DIR/missing.conf" \
	SSHD_BIN="$BIN_DIR/sshd" \
	SSH_KEYGEN_BIN="$BIN_DIR/ssh-keygen" \
	PIDOF_BIN="$BIN_DIR/pidof" \
	KILLALL_BIN="$BIN_DIR/killall" \
	ETC_SSH_DIR="$ETC_MIGRATION_ETC" \
	ROOT_HOME="$ETC_MIGRATION_ROOT" \
	ROOT_SSH_DIR="$ETC_MIGRATION_ROOT_SSH" \
	PERSISTENT_HOST_DIR="$ETC_MIGRATION_HOST" \
	LEGACY_PERSISTENT_HOST_DIR="$ETC_MIGRATION_LEGACY" \
	SSHD_RUNNING="$SSHD_RUNNING" \
	SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
	LOCK_FILE="$LOCK_FILE" \
	PATH="$BIN_DIR:$PATH" \
	REAL_CP="$REAL_CP" \
	"$SCRIPT" start >/dev/null

if [ "$(cat "$ETC_MIGRATION_HOST/ssh_host_ecdsa_key")" != "etc private ecdsa" ]; then
	echo "existing /etc/ssh host key was not persisted into /root" >&2
	exit 1
fi

env \
	BOOT_CONF="$TMP_DIR/missing.conf" \
	SSHD_BIN="$BIN_DIR/sshd" \
	SSH_KEYGEN_BIN="$BIN_DIR/ssh-keygen" \
	PIDOF_BIN="$BIN_DIR/pidof" \
	KILLALL_BIN="$BIN_DIR/killall" \
	ETC_SSH_DIR="$ETC_MIGRATION_ETC" \
	ROOT_HOME="$ETC_MIGRATION_ROOT" \
	ROOT_SSH_DIR="$ETC_MIGRATION_ROOT_SSH" \
	PERSISTENT_HOST_DIR="$ETC_MIGRATION_HOST" \
	LEGACY_PERSISTENT_HOST_DIR="$ETC_MIGRATION_LEGACY" \
	SSHD_RUNNING="$SSHD_RUNNING" \
	SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
	LOCK_FILE="$LOCK_FILE" \
	PATH="$BIN_DIR:$PATH" \
	REAL_CP="$REAL_CP" \
	"$SCRIPT" stop >/dev/null

FAIL_DIR="$TMP_DIR/failcase"
FAIL_ETC_SSH_DIR="$FAIL_DIR/etc/ssh"
FAIL_ROOT_HOME="$FAIL_DIR/root"
FAIL_ROOT_SSH="$FAIL_ROOT_HOME/.ssh"
FAIL_HOST_DIR="$FAIL_ROOT_SSH/host"
FAIL_LEGACY_DIR="$FAIL_DIR/userdata/ssh/host"
mkdir -p "$FAIL_ETC_SSH_DIR" "$FAIL_ROOT_SSH" "$FAIL_LEGACY_DIR"
echo "existing ed25519" > "$FAIL_ETC_SSH_DIR/ssh_host_ed25519_key"

set +e
env \
	BOOT_CONF="$TMP_DIR/missing.conf" \
	SSHD_BIN="$BIN_DIR/sshd" \
	SSH_KEYGEN_BIN="$BIN_DIR/ssh-keygen" \
	PIDOF_BIN="$BIN_DIR/pidof" \
	KILLALL_BIN="$BIN_DIR/killall" \
	ETC_SSH_DIR="$FAIL_ETC_SSH_DIR" \
	ROOT_HOME="$FAIL_ROOT_HOME" \
	ROOT_SSH_DIR="$FAIL_ROOT_SSH" \
	PERSISTENT_HOST_DIR="$FAIL_HOST_DIR" \
	LEGACY_PERSISTENT_HOST_DIR="$FAIL_LEGACY_DIR" \
	SSHD_RUNNING="$SSHD_RUNNING" \
	SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
	LOCK_FILE="$LOCK_FILE" \
	PATH="$BIN_DIR:$PATH" \
	REAL_CP="$REAL_CP" \
	FAIL_KEYGEN_FOR="ssh_host_rsa_key" \
	"$SCRIPT" start >/dev/null 2>"$TMP_DIR/failcase.err"
status="$?"
set -e

if [ "$status" -eq 0 ]; then
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

EMPTY_PERSISTENT_DIR="$TMP_DIR/empty-persistent"
EMPTY_ETC_SSH_DIR="$EMPTY_PERSISTENT_DIR/etc/ssh"
EMPTY_ROOT_HOME="$EMPTY_PERSISTENT_DIR/root"
EMPTY_ROOT_SSH="$EMPTY_ROOT_HOME/.ssh"
EMPTY_HOST_DIR="$EMPTY_ROOT_SSH/host"
EMPTY_LEGACY_DIR="$EMPTY_PERSISTENT_DIR/userdata/ssh/host"
mkdir -p "$EMPTY_ETC_SSH_DIR" "$EMPTY_ROOT_SSH" "$EMPTY_HOST_DIR" "$EMPTY_LEGACY_DIR"
for type in rsa ecdsa ed25519; do
	: > "$EMPTY_HOST_DIR/ssh_host_${type}_key"
done

env \
	BOOT_CONF="$TMP_DIR/missing.conf" \
	SSHD_BIN="$BIN_DIR/sshd" \
	SSH_KEYGEN_BIN="$BIN_DIR/ssh-keygen" \
	PIDOF_BIN="$BIN_DIR/pidof" \
	KILLALL_BIN="$BIN_DIR/killall" \
	ETC_SSH_DIR="$EMPTY_ETC_SSH_DIR" \
	ROOT_HOME="$EMPTY_ROOT_HOME" \
	ROOT_SSH_DIR="$EMPTY_ROOT_SSH" \
	PERSISTENT_HOST_DIR="$EMPTY_HOST_DIR" \
	LEGACY_PERSISTENT_HOST_DIR="$EMPTY_LEGACY_DIR" \
	SSHD_RUNNING="$SSHD_RUNNING" \
	SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
	LOCK_FILE="$LOCK_FILE" \
	PATH="$BIN_DIR:$PATH" \
	REAL_CP="$REAL_CP" \
	"$SCRIPT" start >/dev/null

for type in rsa ecdsa ed25519; do
	if [ ! -s "$EMPTY_HOST_DIR/ssh_host_${type}_key" ]; then
		echo "empty persistent $type host key was not regenerated" >&2
		exit 1
	fi
done

env \
	BOOT_CONF="$TMP_DIR/missing.conf" \
	SSHD_BIN="$BIN_DIR/sshd" \
	SSH_KEYGEN_BIN="$BIN_DIR/ssh-keygen" \
	PIDOF_BIN="$BIN_DIR/pidof" \
	KILLALL_BIN="$BIN_DIR/killall" \
	ETC_SSH_DIR="$EMPTY_ETC_SSH_DIR" \
	ROOT_HOME="$EMPTY_ROOT_HOME" \
	ROOT_SSH_DIR="$EMPTY_ROOT_SSH" \
	PERSISTENT_HOST_DIR="$EMPTY_HOST_DIR" \
	LEGACY_PERSISTENT_HOST_DIR="$EMPTY_LEGACY_DIR" \
	SSHD_RUNNING="$SSHD_RUNNING" \
	SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
	LOCK_FILE="$LOCK_FILE" \
	PATH="$BIN_DIR:$PATH" \
	REAL_CP="$REAL_CP" \
	"$SCRIPT" stop >/dev/null

NO_KEYGEN_DIR="$TMP_DIR/no-keygen"
NO_KEYGEN_ETC_SSH_DIR="$NO_KEYGEN_DIR/etc/ssh"
NO_KEYGEN_ROOT_HOME="$NO_KEYGEN_DIR/root"
NO_KEYGEN_ROOT_SSH="$NO_KEYGEN_ROOT_HOME/.ssh"
NO_KEYGEN_HOST_DIR="$NO_KEYGEN_ROOT_SSH/host"
NO_KEYGEN_LEGACY_DIR="$NO_KEYGEN_DIR/userdata/ssh/host"
mkdir -p "$NO_KEYGEN_ETC_SSH_DIR" "$NO_KEYGEN_ROOT_SSH" "$NO_KEYGEN_HOST_DIR" "$NO_KEYGEN_LEGACY_DIR"
for type in rsa ecdsa ed25519; do
	echo "provisioned $type" > "$NO_KEYGEN_HOST_DIR/ssh_host_${type}_key"
	echo "provisioned $type pub" > "$NO_KEYGEN_HOST_DIR/ssh_host_${type}_key.pub"
done

env \
	BOOT_CONF="$TMP_DIR/missing.conf" \
	SSHD_BIN="$BIN_DIR/sshd" \
	SSH_KEYGEN_BIN="$BIN_DIR/missing-ssh-keygen" \
	PIDOF_BIN="$BIN_DIR/pidof" \
	KILLALL_BIN="$BIN_DIR/killall" \
	ETC_SSH_DIR="$NO_KEYGEN_ETC_SSH_DIR" \
	ROOT_HOME="$NO_KEYGEN_ROOT_HOME" \
	ROOT_SSH_DIR="$NO_KEYGEN_ROOT_SSH" \
	PERSISTENT_HOST_DIR="$NO_KEYGEN_HOST_DIR" \
	LEGACY_PERSISTENT_HOST_DIR="$NO_KEYGEN_LEGACY_DIR" \
	SSHD_RUNNING="$SSHD_RUNNING" \
	SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
	LOCK_FILE="$LOCK_FILE" \
	PATH="$BIN_DIR:$PATH" \
	REAL_CP="$REAL_CP" \
	"$SCRIPT" start >/dev/null

if [ ! -f "$SSHD_RUNNING" ]; then
	echo "sshd did not start with provisioned host keys and missing ssh-keygen" >&2
	exit 1
fi

env \
	BOOT_CONF="$TMP_DIR/missing.conf" \
	SSHD_BIN="$BIN_DIR/sshd" \
	SSH_KEYGEN_BIN="$BIN_DIR/missing-ssh-keygen" \
	PIDOF_BIN="$BIN_DIR/pidof" \
	KILLALL_BIN="$BIN_DIR/killall" \
	ETC_SSH_DIR="$NO_KEYGEN_ETC_SSH_DIR" \
	ROOT_HOME="$NO_KEYGEN_ROOT_HOME" \
	ROOT_SSH_DIR="$NO_KEYGEN_ROOT_SSH" \
	PERSISTENT_HOST_DIR="$NO_KEYGEN_HOST_DIR" \
	LEGACY_PERSISTENT_HOST_DIR="$NO_KEYGEN_LEGACY_DIR" \
	SSHD_RUNNING="$SSHD_RUNNING" \
	SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
	LOCK_FILE="$LOCK_FILE" \
	PATH="$BIN_DIR:$PATH" \
	REAL_CP="$REAL_CP" \
	"$SCRIPT" stop >/dev/null

echo "S50sshd init tests passed"
