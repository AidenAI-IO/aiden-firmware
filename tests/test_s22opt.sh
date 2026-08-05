#!/bin/sh
# Host tests for overlay/etc/init.d/S22opt (the /opt bind mount for opkg).
#
# The script takes every path and every external binary from the environment,
# so the whole thing runs on a dev host with no board and no root: mount and
# umount are replaced by stubs that edit a fake /proc/self/mountinfo, which is
# the only thing S22opt reads to decide what is mounted where.
#
# The cases that matter are the failure ones. V7 (a failed bind must never let
# a package land in the rootfs) and V9 (a mount from the wrong source must be
# an error, not a success) are the two guards the whole design rests on; see
# docs/07-operations/opkg-package-management.md §6.2 and §8.
#
# The board runs busybox ash, which is stricter than the host /bin/sh. Set SH to
# re-run the same cases under a closer stand-in, e.g. "SH=dash sh
# tests/test_s22opt.sh".

set -u

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
S22OPT="$SCRIPT_DIR/../overlay/etc/init.d/S22opt"
SH=${SH:-sh}

# A bind of /userdata/opt shows up in mountinfo as the /userdata *device* with
# an in-filesystem root of /opt -- never as the string "/userdata/opt". Getting
# that wrong is exactly the mistake the source check has to survive.
USERDATA_DEV=8:6
OTHER_DEV=8:7

failures=0
tmproot=$(mktemp -d "${TMPDIR:-/tmp}/s22opt-test.XXXXXX") || exit 1
trap 'rm -rf "$tmproot"' EXIT INT TERM

fail() {
	echo "  FAIL: $*" >&2
	failures=$((failures + 1))
}

pass() {
	echo "  ok: $*"
}

check_status() {
	# check_status <expected> <actual> <description>
	if [ "$1" = "$2" ]; then
		pass "$3"
	else
		fail "$3 (expected exit $1, got $2)"
	fi
}

check_contains() {
	# check_contains <file> <pattern> <description>
	if grep -q "$2" "$1" 2>/dev/null; then
		pass "$3"
	else
		fail "$3 (no match for '$2' in $1)"
		sed 's/^/        /' "$1" >&2 2>/dev/null || true
	fi
}

check_absent() {
	# check_absent <path> <description>
	if [ -e "$1" ]; then
		fail "$2 ($1 exists)"
	else
		pass "$2"
	fi
}

check_present() {
	# check_present <path> <description>
	if [ -e "$1" ]; then
		pass "$2"
	else
		fail "$2 ($1 is missing)"
	fi
}

# Build a fresh sandbox: fake mountinfo, mount/umount stubs, empty dirs.
#
# Stub knobs, set by each case before running S22opt:
#   $case_dir/fail-bind    exists -> "mount -o bind" fails
#   $case_dir/fail-tmpfs   exists -> "mount -t tmpfs" fails
#   $case_dir/fail-umount  exists -> umount fails
#   $case_dir/bind-dev     bind mount lands on this device  (default $USERDATA_DEV)
#   $case_dir/bind-root    bind mount lands on this fs root (default /opt)
new_case() {
	case_name=$1
	case_dir=$tmproot/$case_name
	mkdir -p "$case_dir/bin" "$case_dir/opt" "$case_dir/userdata/opt" "$case_dir/run"

	MOUNTINFO=$case_dir/mountinfo
	MOUNTLOG=$case_dir/mount.log
	OUTPUT=$case_dir/output
	: > "$MOUNTLOG"

	# /userdata is mounted from fstab before any S script runs.
	printf '25 1 %s / %s rw,relatime - ext4 /dev/mmcblk0p6 rw\n' \
		"$USERDATA_DEV" "$case_dir/userdata" > "$MOUNTINFO"

	cat > "$case_dir/bin/mount" <<'STUB'
#!/bin/sh
printf '%s\n' "mount $*" >> "$MOUNTLOG"
target=
fstype=
bind=0
while [ "$#" -gt 0 ]; do
	case $1 in
		-t) fstype=$2; shift 2 ;;
		-o) case ,$2, in *,bind,*) bind=1 ;; esac; shift 2 ;;
		*) target=$1; shift ;;
	esac
done
if [ "$bind" = 1 ]; then
	[ -e "$CASE_DIR/fail-bind" ] && exit 1
	dev=$(cat "$CASE_DIR/bind-dev" 2>/dev/null || echo "$STUB_USERDATA_DEV")
	root=$(cat "$CASE_DIR/bind-root" 2>/dev/null || echo /opt)
	printf '90 25 %s %s %s rw,relatime - ext4 /dev/mmcblk0p6 rw\n' \
		"$dev" "$root" "$target" >> "$MOUNTINFO"
	exit 0
fi
if [ "$fstype" = tmpfs ]; then
	[ -e "$CASE_DIR/fail-tmpfs" ] && exit 1
	printf '91 25 0:42 / %s ro,relatime - tmpfs tmpfs ro\n' "$target" >> "$MOUNTINFO"
	exit 0
fi
exit 1
STUB

	cat > "$case_dir/bin/umount" <<'STUB'
#!/bin/sh
printf '%s\n' "umount $*" >> "$MOUNTLOG"
[ -e "$CASE_DIR/fail-umount" ] && exit 1
awk -v target="$1" '$5 != target' "$MOUNTINFO" > "$MOUNTINFO.new" &&
	mv "$MOUNTINFO.new" "$MOUNTINFO"
STUB

	chmod +x "$case_dir/bin/mount" "$case_dir/bin/umount"
}

run_s22opt() {
	CASE_DIR=$case_dir \
	MOUNTINFO=$MOUNTINFO \
	MOUNTLOG=$MOUNTLOG \
	STUB_USERDATA_DEV=$USERDATA_DEV \
	MOUNTINFO_PATH=$MOUNTINFO \
	OPT_DIR=$case_dir/opt \
	USERDATA_DIR=$case_dir/userdata \
	PERSISTENT_OPT=$case_dir/userdata/opt \
	USERFEEDS_FILE=$case_dir/opt/etc/opkg/userfeeds.conf \
	LOCK_DIR=$case_dir/run/lock \
	MOUNT_BIN=$case_dir/bin/mount \
	UMOUNT_BIN=$case_dir/bin/umount \
		"$SH" "$S22OPT" "$@" > "$OUTPUT" 2>&1
}

# Append a mountinfo entry for $case_dir/opt directly, bypassing the stub, to
# set up a pre-existing mount state.
preset_opt_mount() {
	# preset_opt_mount <dev> <fs-root> [fstype] [options]
	printf '90 25 %s %s %s %s - %s /dev/mmcblk0p6 rw\n' \
		"$1" "$2" "$case_dir/opt" "${4:-rw,relatime}" "${3:-ext4}" >> "$MOUNTINFO"
}

echo "=== S22opt tests ==="

echo "Test 1: a clean boot binds /userdata/opt and lays out the opkg tree"
new_case clean-boot
run_s22opt start
check_status 0 $? "start succeeds"
check_contains "$MOUNTLOG" "mount -o bind $case_dir/userdata/opt $case_dir/opt" \
	"binds the persistent root onto /opt"
check_present "$case_dir/opt/etc/opkg/userfeeds.conf" "creates the user feed file"
check_present "$case_dir/opt/var/lib/opkg/lists" "creates lists_dir"
check_present "$case_dir/opt/var/lib/opkg/info" "creates info_dir"
check_present "$case_dir/opt/var/cache/opkg" "creates cache_dir"
check_present "$case_dir/opt/tmp" "creates tmp_dir"
check_present "$case_dir/run/lock" "creates the lock directory"
run_s22opt status
check_status 0 $? "status reports bound"
check_contains "$OUTPUT" "opt=bound" "status names the source"

echo "Test 2: an existing user feed file is never truncated (§6.4)"
new_case keep-feeds
mkdir -p "$case_dir/opt/etc/opkg"
echo 'src/gz entware https://bin.entware.net/armv7sf-k3.2' \
	> "$case_dir/opt/etc/opkg/userfeeds.conf"
preset_opt_mount "$USERDATA_DEV" /opt
run_s22opt start
check_status 0 $? "start succeeds when already bound"
check_contains "$case_dir/opt/etc/opkg/userfeeds.conf" "bin.entware.net" \
	"the configured feed survives"
if [ "$(wc -l < "$case_dir/opt/etc/opkg/userfeeds.conf")" -eq 1 ]; then
	pass "the feed file is left exactly as it was"
else
	fail "the feed file was rewritten"
fi
if grep -q "mount -o bind" "$MOUNTLOG"; then
	fail "re-bound an already correct mount"
else
	pass "does not re-mount when already bound"
fi

echo "Test 3: a failed bind seals /opt read-only and leaves opkg unusable (V7)"
new_case seal
: > "$case_dir/fail-bind"
run_s22opt start
check_status 1 $? "start fails"
check_contains "$MOUNTLOG" "mount -t tmpfs -o ro,size=0 tmpfs $case_dir/opt" \
	"seals /opt with a read-only tmpfs"
check_contains "$OUTPUT" "sealed read-only" "says /opt was sealed"
# No feed file means no src line anywhere, so opkg cannot resolve any package.
# Note this is NOT because opkg refuses to start on a dangling config symlink --
# uClibc-ng's glob() silently drops it. Keeping the only src line inside /opt is
# what makes a failed mount safe, which is why the default source is seeded here
# and not shipped as a rootfs fragment (§6.5.1).
check_absent "$case_dir/opt/etc/opkg/userfeeds.conf" \
	"creates no feed file, so no source is reachable"
run_s22opt status
check_status 1 $? "status fails while sealed"
check_contains "$OUTPUT" "opt=sealed" "status reports the seal"

echo "Test 4: when even the seal cannot be applied, that is reported"
new_case seal-fails
: > "$case_dir/fail-bind"
: > "$case_dir/fail-tmpfs"
run_s22opt start
check_status 1 $? "start fails"
check_contains "$OUTPUT" "could not be sealed" "warns that /opt is unprotected"
check_absent "$case_dir/opt/etc/opkg/userfeeds.conf" "still creates no feed file"

echo "Test 5: /opt mounted from the wrong path on the right device is an error (V9)"
new_case wrong-root
preset_opt_mount "$USERDATA_DEV" /somewhere-else
run_s22opt status
check_status 1 $? "status fails"
check_contains "$OUTPUT" "opt=wrong-source" "status calls out the wrong source"
run_s22opt start
check_status 1 $? "start refuses"
check_contains "$OUTPUT" "unexpected source" "start explains why"
if grep -q "mount -o bind" "$MOUNTLOG"; then
	fail "stacked a second mount on /opt"
else
	pass "does not stack a mount on the foreign one"
fi

echo "Test 6: /opt mounted from the right path on the wrong device is an error (V9)"
new_case wrong-dev
preset_opt_mount "$OTHER_DEV" /opt
run_s22opt status
check_status 1 $? "status fails"
check_contains "$OUTPUT" "opt=wrong-source" "a same-path mount on another device is rejected"

echo "Test 7: a bind that succeeds but lands elsewhere is torn down and sealed"
new_case bind-lands-wrong
echo /elsewhere > "$case_dir/bind-root"
run_s22opt start
check_status 1 $? "start fails"
check_contains "$OUTPUT" "not bound from" "the post-mount check catches it"
check_contains "$MOUNTLOG" "umount $case_dir/opt" "releases the wrong mount"
check_contains "$MOUNTLOG" "mount -t tmpfs -o ro,size=0 tmpfs $case_dir/opt" \
	"seals /opt instead of leaving a writable foreign mount"
check_absent "$case_dir/opt/etc/opkg/userfeeds.conf" "no feed file, so opkg stays disabled"

echo "Test 7b: a wrong landing that cannot be released is reported, not ignored"
new_case bind-lands-wrong-stuck
echo /elsewhere > "$case_dir/bind-root"
: > "$case_dir/fail-umount"
run_s22opt start
check_status 1 $? "start fails"
check_contains "$OUTPUT" "cannot release the unexpected mount" "warns that /opt is unprotected"
check_absent "$case_dir/opt/etc/opkg/userfeeds.conf" "still creates no feed file"

echo "Test 8: /userdata not mounted seals /opt instead of writing to the rootfs"
new_case no-userdata
: > "$MOUNTINFO"
run_s22opt start
check_status 1 $? "start fails"
check_contains "$OUTPUT" "is not mounted" "names the missing /userdata"
check_contains "$MOUNTLOG" "mount -t tmpfs" "seals /opt anyway"

echo "Test 8b: a writable foreign mount on /opt is never mistaken for a seal"
new_case no-userdata-foreign-opt
# /userdata is gone AND something else already holds /opt, writable. Treating
# "already mounted" as "already sealed" would report protection that does not
# exist, and an opkg install would land in that foreign mount.
: > "$MOUNTINFO"
preset_opt_mount 0:99 / ext4 rw,relatime
run_s22opt start
check_status 1 $? "start fails"
check_contains "$OUTPUT" "cannot be sealed" "says the seal could not be applied"
if grep -q "mount -t tmpfs" "$MOUNTLOG"; then
	fail "stacked a tmpfs on top of a foreign mount"
else
	pass "does not stack a seal on a foreign mount"
fi
if grep -q "^umount" "$MOUNTLOG"; then
	fail "unmounted a mount it did not create"
else
	pass "does not unmount a foreign mount"
fi

echo "Test 8c: an already-sealed /opt is still recognised as sealed"
new_case no-userdata-already-sealed
: > "$MOUNTINFO"
preset_opt_mount 0:42 / tmpfs ro,relatime
run_s22opt start
check_status 1 $? "start still fails (no /userdata)"
if grep -q "mount -t tmpfs" "$MOUNTLOG"; then
	fail "re-sealed an already sealed /opt"
else
	pass "does not re-seal"
fi
check_absent "$case_dir/opt/etc/opkg/userfeeds.conf" "creates no feed file"

echo "Test 9: restart recovers from a seal once the bind can succeed"
new_case recover
preset_opt_mount 0:42 / tmpfs ro,relatime
run_s22opt restart
check_status 0 $? "restart succeeds"
check_contains "$MOUNTLOG" "umount $case_dir/opt" "releases the seal first"
check_contains "$MOUNTLOG" "mount -o bind" "then binds the persistent root"
check_present "$case_dir/opt/etc/opkg/userfeeds.conf" "the opkg tree is laid out"

echo "Test 10: a seal that cannot be released is not silently worked around"
new_case stuck-seal
preset_opt_mount 0:42 / tmpfs ro,relatime
: > "$case_dir/fail-umount"
run_s22opt restart
check_status 1 $? "restart fails"
check_contains "$OUTPUT" "cannot release" "explains the stuck seal"
if grep -q "mount -o bind" "$MOUNTLOG"; then
	fail "bound on top of the seal"
else
	pass "does not bind on top of the seal"
fi

echo "Test 11: a persistent root outside /userdata is rejected, not assumed good"
new_case outside-userdata
PERSISTENT_OPT_OVERRIDE=$case_dir/elsewhere
preset_opt_mount "$USERDATA_DEV" /opt
CASE_DIR=$case_dir MOUNTINFO=$MOUNTINFO MOUNTLOG=$MOUNTLOG \
STUB_USERDATA_DEV=$USERDATA_DEV MOUNTINFO_PATH=$MOUNTINFO \
OPT_DIR=$case_dir/opt USERDATA_DIR=$case_dir/userdata \
PERSISTENT_OPT=$PERSISTENT_OPT_OVERRIDE \
USERFEEDS_FILE=$case_dir/opt/etc/opkg/userfeeds.conf \
LOCK_DIR=$case_dir/run/lock MOUNT_BIN=$case_dir/bin/mount \
UMOUNT_BIN=$case_dir/bin/umount \
	"$SH" "$S22OPT" status > "$OUTPUT" 2>&1
check_status 1 $? "status fails"
check_contains "$OUTPUT" "is not below" "explains the misconfiguration"

echo "Test 12: usage errors are rejected"
new_case usage
run_s22opt bogus
check_status 1 $? "an unknown subcommand fails"
check_contains "$OUTPUT" "Usage:" "prints usage"

echo "Test 13: a freshly created feed file is seeded with the default source (§6.5.1)"
new_case seed-default
run_s22opt start
check_status 0 $? "start succeeds"
UF=$case_dir/opt/etc/opkg/userfeeds.conf
check_contains "$UF" "src/gz entware https://bin.entware.net/armv7sf-k3.2" \
	"the default source is written"
# Exactly one source line: comments are fine, a second src line would mean
# opkg reports "Duplicate src declaration" (§13.1.1).
if [ "$(grep -c '^src' "$UF")" -eq 1 ]; then
	pass "exactly one src line"
else
	fail "expected 1 src line, got $(grep -c '^src' "$UF")"
fi
if [ -n "$(find "$case_dir/opt/etc/opkg" -name '.userfeeds.conf.*' 2>/dev/null)" ]; then
	fail "a temporary file was left behind"
else
	pass "no temporary file left behind"
fi

echo "Test 14: seeding happens only on creation -- a user who deletes the source keeps it deleted"
new_case respect-deletion
mkdir -p "$case_dir/opt/etc/opkg"
# The realistic shape after someone removes the seeded line: comments remain,
# no src line. The file exists, so it must not be touched.
printf '# I do not want a third-party feed\n' > "$case_dir/opt/etc/opkg/userfeeds.conf"
preset_opt_mount "$USERDATA_DEV" /opt
run_s22opt start
check_status 0 $? "start succeeds"
if grep -q '^src' "$case_dir/opt/etc/opkg/userfeeds.conf"; then
	fail "the default source was re-seeded over the user's choice"
else
	pass "no source is re-added"
fi
check_contains "$case_dir/opt/etc/opkg/userfeeds.conf" "I do not want" \
	"the user's content is untouched"

echo "Test 14b: an existing *empty* feed file is also left alone"
new_case respect-empty
mkdir -p "$case_dir/opt/etc/opkg"
: > "$case_dir/opt/etc/opkg/userfeeds.conf"
preset_opt_mount "$USERDATA_DEV" /opt
run_s22opt start
check_status 0 $? "start succeeds"
if [ -s "$case_dir/opt/etc/opkg/userfeeds.conf" ]; then
	fail "an existing empty file was seeded"
else
	pass "an existing empty file stays empty"
fi

echo "Test 15: DEFAULT_FEED= builds an image with no default source"
new_case no-default-feed
export DEFAULT_FEED=
run_s22opt start
unset DEFAULT_FEED
check_status 0 $? "start succeeds"
check_present "$case_dir/opt/etc/opkg/userfeeds.conf" "the feed file is still created"
if [ -s "$case_dir/opt/etc/opkg/userfeeds.conf" ]; then
	fail "a source was seeded despite DEFAULT_FEED being empty"
else
	pass "the file is empty"
fi

echo ""
if [ "$failures" -eq 0 ]; then
	echo "=== all S22opt tests passed ==="
	exit 0
fi
echo "=== $failures S22opt check(s) failed ==="
exit 1
