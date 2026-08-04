#!/bin/sh
# Host tests for overlay/etc/profile.d/aiden-opt.sh.
#
# The script is sourced into every interactive login shell, so the properties
# that matter are ordering and idempotency: /opt must never come before the
# system directories (a package shipping "mount" or "mkfs.ext4" would otherwise
# shadow the tools storage_manager.go calls by bare name), and re-sourcing must
# not grow PATH without bound. See
# docs/07-operations/opkg-package-management.md §6.7.
#
# Set SH to re-run under a closer stand-in for the board's busybox ash, e.g.
# "SH=dash sh tests/test_aiden_opt_profile.sh".

set -u

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
PROFILE="$SCRIPT_DIR/../overlay/etc/profile.d/aiden-opt.sh"
SH=${SH:-sh}

failures=0

fail() {
	echo "  FAIL: $*" >&2
	failures=$((failures + 1))
}

pass() {
	echo "  ok: $*"
}

# Source the profile script with $1 as the starting PATH and echo the result.
resulting_path() {
	PATH_IN=$1 "$SH" -c 'PATH=$PATH_IN; . "$1"; printf "%s\n" "$PATH"' sh "$PROFILE"
}

check_path() {
	# check_path <input PATH> <expected PATH> <description>
	actual=$(resulting_path "$1")
	if [ "$actual" = "$2" ]; then
		pass "$3"
	else
		fail "$3"
		echo "        in:       $1" >&2
		echo "        expected: $2" >&2
		echo "        actual:   $actual" >&2
	fi
}

echo "=== aiden-opt.sh tests ==="

check_path \
	"/usr/bin:/bin" \
	"/usr/bin:/bin:/opt/bin:/opt/sbin" \
	"appends both directories to a clean PATH"

check_path \
	"/usr/bin:/bin:/opt/bin:/opt/sbin" \
	"/usr/bin:/bin:/opt/bin:/opt/sbin" \
	"re-sourcing is a no-op"

# The bug this guards: a single probe on /opt/bin skipped /opt/sbin entirely.
check_path \
	"/usr/bin:/opt/bin" \
	"/usr/bin:/opt/bin:/opt/sbin" \
	"adds /opt/sbin when only /opt/bin was already present"

check_path \
	"/usr/bin:/opt/sbin" \
	"/usr/bin:/opt/sbin:/opt/bin" \
	"adds /opt/bin without duplicating /opt/sbin"

# Substring traps: neither of these is /opt/bin, so both must still be added.
check_path \
	"/usr/bin:/opt/binaries" \
	"/usr/bin:/opt/binaries:/opt/bin:/opt/sbin" \
	"a directory merely starting with /opt/bin does not count as a match"

check_path \
	"/usr/bin:/usr/local/opt/bin" \
	"/usr/bin:/usr/local/opt/bin:/opt/bin:/opt/sbin" \
	"a directory merely ending in /opt/bin does not count as a match"

echo "Ordering: /opt must stay behind the system directories"
actual=$(resulting_path "/usr/sbin:/usr/bin:/sbin:/bin")
case $actual in
	/usr/sbin:/usr/bin:/sbin:/bin:*)
		pass "system directories keep their leading position"
		;;
	*)
		fail "system directories were displaced: $actual"
		;;
esac
case $actual in
	*/opt/bin*/usr/bin*)
		fail "/opt/bin was placed ahead of /usr/bin: $actual"
		;;
	*)
		pass "/opt/bin never precedes /usr/bin"
		;;
esac

echo "Hygiene: no loop variable leaks into the login shell"
leaked=$(PATH_IN=/bin "$SH" -c \
	'PATH=$PATH_IN; . "$1"; printf "%s\n" "${aiden_opt_dir-unset}"' sh "$PROFILE")
if [ "$leaked" = "unset" ]; then
	pass "aiden_opt_dir is unset afterwards"
else
	fail "aiden_opt_dir leaked as '$leaked'"
fi

echo "TERMINFO_DIRS: Entware's terminfo database must be findable (§6.7)"

# The script reads both terminfo directories from the environment so this runs
# on a host with no /opt at all. OPT_TI is created; SYS_TI merely has to be a
# distinguishable string.
TI_TMP=$(mktemp -d "${TMPDIR:-/tmp}/aiden-terminfo.XXXXXX")
trap 'rm -rf "$TI_TMP"' EXIT
OPT_TI="$TI_TMP/opt-terminfo"
SYS_TI="$TI_TMP/sys-terminfo"
mkdir -p "$OPT_TI" "$SYS_TI"

terminfo_dirs() {
	# terminfo_dirs <incoming TERMINFO_DIRS, or the literal "unset"> [opt dir]
	AIDEN_OPT_TERMINFO=${2-$OPT_TI} AIDEN_SYS_TERMINFO=$SYS_TI TD=$1 \
		"$SH" -c '
			if [ "$TD" = unset ]; then unset TERMINFO_DIRS; else TERMINFO_DIRS=$TD; fi
			. "$1"
			printf "%s\n" "${TERMINFO_DIRS-unset}"
		' sh "$PROFILE"
}

actual=$(terminfo_dirs unset)
if [ "$actual" = "$SYS_TI:$OPT_TI" ]; then
	pass "sets a search list with the rootfs database first"
else
	fail "unexpected TERMINFO_DIRS: got '$actual', want '$SYS_TI:$OPT_TI'"
fi

actual=$(terminfo_dirs "$SYS_TI:$OPT_TI")
if [ "$actual" = "$SYS_TI:$OPT_TI" ]; then
	pass "re-sourcing does not duplicate the /opt database"
else
	fail "re-sourcing changed TERMINFO_DIRS: $actual"
fi

actual=$(terminfo_dirs "/home/me/.terminfo")
if [ "$actual" = "/home/me/.terminfo:$OPT_TI" ]; then
	pass "an existing TERMINFO_DIRS is preserved and appended to"
else
	fail "existing TERMINFO_DIRS mishandled: $actual"
fi

# No /opt database installed -> the variable must be left completely alone,
# otherwise every shell would carry a search path pointing at nothing.
actual=$(terminfo_dirs unset "$TI_TMP/does-not-exist")
if [ "$actual" = "unset" ]; then
	pass "leaves TERMINFO_DIRS unset when no /opt database is installed"
else
	fail "TERMINFO_DIRS was set to '$actual' with no /opt database"
fi

# TERMINFO names a single directory and would hide the rootfs database.
ti=$(AIDEN_OPT_TERMINFO=$OPT_TI AIDEN_SYS_TERMINFO=$SYS_TI "$SH" -c \
	'. "$1"; printf "%s\n" "${TERMINFO-unset}"' sh "$PROFILE")
if [ "$ti" = "unset" ]; then
	pass "TERMINFO itself is never set"
else
	fail "TERMINFO was set to '$ti', which would hide the rootfs database"
fi

leaked=$(AIDEN_OPT_TERMINFO=$OPT_TI AIDEN_SYS_TERMINFO=$SYS_TI "$SH" -c \
	'. "$1"; printf "%s|%s\n" "${aiden_opt_terminfo-unset}" "${aiden_sys_terminfo-unset}"' \
	sh "$PROFILE")
if [ "$leaked" = "unset|unset" ]; then
	pass "terminfo helper variables do not leak"
else
	fail "terminfo helper variables leaked: $leaked"
fi

echo "LD_LIBRARY_PATH is deliberately left alone (§6.7)"
ld=$(PATH_IN=/bin "$SH" -c \
	'PATH=$PATH_IN; . "$1"; printf "%s\n" "${LD_LIBRARY_PATH-unset}"' sh "$PROFILE")
if [ "$ld" = "unset" ]; then
	pass "no LD_LIBRARY_PATH is introduced"
else
	fail "LD_LIBRARY_PATH was set to '$ld'"
fi

echo ""
if [ "$failures" -eq 0 ]; then
	echo "=== all aiden-opt.sh tests passed ==="
	exit 0
fi
echo "=== $failures aiden-opt.sh check(s) failed ==="
exit 1
