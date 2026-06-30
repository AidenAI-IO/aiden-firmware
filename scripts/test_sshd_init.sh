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
SSHD_RUNNING="$TMP_DIR/sshd.running"
SSHD_ARGS_LOG="$TMP_DIR/sshd.args"
KEYGEN_ARGS_LOG="$TMP_DIR/ssh-keygen.args"
LOCK_FILE="$TMP_DIR/sshd.lock"

mkdir -p "$BIN_DIR"

cat > "$BIN_DIR/ssh-keygen" <<'EOF'
#!/bin/sh
echo "$*" > "$KEYGEN_ARGS_LOG"
exit 0
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

run_sshd() {
	action="$1"
	env \
		BOOT_CONF="$TMP_DIR/missing.conf" \
		SSHD_BIN="$BIN_DIR/sshd" \
		SSH_KEYGEN_BIN="$BIN_DIR/ssh-keygen" \
		PIDOF_BIN="$BIN_DIR/pidof" \
		KILLALL_BIN="$BIN_DIR/killall" \
		SSHD_RUNNING="$SSHD_RUNNING" \
		SSHD_ARGS_LOG="$SSHD_ARGS_LOG" \
		KEYGEN_ARGS_LOG="$KEYGEN_ARGS_LOG" \
		LOCK_FILE="$LOCK_FILE" \
		PATH="$BIN_DIR:$PATH" \
		"$SCRIPT" "$action"
}

run_sshd start >/dev/null

if [ "$(cat "$KEYGEN_ARGS_LOG")" != "-A" ]; then
	echo "ssh-keygen was not started with -A" >&2
	cat "$KEYGEN_ARGS_LOG" >&2
	exit 1
fi

if [ -n "$(tr -d '[:space:]' < "$SSHD_ARGS_LOG")" ]; then
	echo "sshd should start without persistence-only arguments" >&2
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

echo "S50sshd init tests passed"
