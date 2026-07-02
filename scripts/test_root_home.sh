#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SCRIPT="$ROOT_DIR/overlay/etc/init.d/S21root_home"

if [ ! -f "$SCRIPT" ]; then
	echo "missing $SCRIPT" >&2
	exit 1
fi

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

MOUNT_LOG="$TMP_DIR/mount.log"
MOUNTS_PATH="$TMP_DIR/mounts"
MOUNT_BIN="$TMP_DIR/mount"

cat > "$MOUNT_BIN" <<'EOF'
#!/bin/sh
echo "$*" >> "$MOUNT_LOG"
if [ "$1" = "-o" ] && [ "${2:-}" = "bind" ]; then
	printf 'none %s none bind 0 0\n' "$4" >> "$MOUNTS_PATH"
fi
EOF
chmod +x "$MOUNT_BIN"

run_case() {
	name="$1"
	passwd_home="$2"
	userdata_mounted="$3"
	want_status="$4"
	want_bind="$5"

	root_home="$TMP_DIR/$name/root"
	persistent_home="$TMP_DIR/$name/userdata/userhome"
	passwd_file="$TMP_DIR/$name/passwd"
	mounts_file="$TMP_DIR/$name/mounts"

	mkdir -p "$root_home/.ssh" "$(dirname "$persistent_home")"
	printf 'root:x:0:0:root:%s:/bin/sh\n' "$passwd_home" > "$passwd_file"
	printf 'legacy authorized key\n' > "$root_home/.ssh/authorized_keys"
	: > "$MOUNT_LOG"
	: > "$mounts_file"

	if [ "$userdata_mounted" = "1" ]; then
		printf 'none %s none rw 0 0\n' "$(dirname "$persistent_home")" >> "$mounts_file"
	fi

	set +e
	ROOT_HOME="$root_home" \
	USERDATA_DIR="$(dirname "$persistent_home")" \
	PERSISTENT_HOME="$persistent_home" \
	PASSWD_FILE="$passwd_file" \
	MOUNTS_PATH="$mounts_file" \
	MOUNT_BIN="$MOUNT_BIN" \
	MOUNT_LOG="$MOUNT_LOG" \
	"$SCRIPT" start >/dev/null 2>"$TMP_DIR/$name.err"
	status="$?"
	set -e

	if [ "$want_status" = "0" ] && [ "$status" -ne 0 ]; then
		echo "$name: expected success, got $status" >&2
		cat "$TMP_DIR/$name.err" >&2
		exit 1
	fi
	if [ "$want_status" != "0" ] && [ "$status" -eq 0 ]; then
		echo "$name: expected failure, got success" >&2
		exit 1
	fi

	if ! grep -q "^root:x:0:0:root:$root_home:/bin/sh\$" "$passwd_file"; then
		echo "$name: root home was not rewritten in passwd" >&2
		cat "$passwd_file" >&2
		exit 1
	fi

	if [ "$want_bind" = "1" ]; then
		if ! grep -q -- "-o bind $persistent_home $root_home" "$MOUNT_LOG"; then
			echo "$name: bind mount was not requested" >&2
			cat "$MOUNT_LOG" >&2
			exit 1
		fi
		if [ "$(cat "$persistent_home/.ssh/authorized_keys")" != "legacy authorized key" ]; then
			echo "$name: authorized_keys was not migrated into persistent home" >&2
			exit 1
		fi
	else
		if [ -s "$MOUNT_LOG" ]; then
			echo "$name: mount should not have been called" >&2
			cat "$MOUNT_LOG" >&2
			exit 1
		fi
	fi
}

run_case mounted /oem 1 0 1
run_case already_root /root 1 0 1
run_case no_userdata /oem 0 1 0

echo "S21root_home tests passed"
