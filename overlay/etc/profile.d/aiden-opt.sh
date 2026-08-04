# Put opkg-installed tools on the path for interactive login shells.
#
# See docs/07-operations/opkg-package-management.md §6.7. Two deliberate
# asymmetries:
#
#   Appended, never prepended. storage_manager.go invokes mount, umount, fsck,
#   sync and mkfs.* by bare name, so a package that happens to ship one of those
#   names must not be able to shadow the system tool -- doing so breaks SD card
#   handling, and because /userdata survives OTA and rollback, a bad package
#   would survive them too. System tools stay first; packages only fill gaps.
#
#   No /opt/lib in LD_LIBRARY_PATH. Entware binaries carry
#   PT_INTERP=/opt/lib/ld-linux.so.3 and DT_RUNPATH=/opt/lib, so they resolve
#   their own runtime. Adding it gains exactly nothing and risks shadowing a
#   system library that shares a SONAME.
#
# Core services are not touched at all: they start from absolute paths and do
# not inherit a login shell's environment.

# Each directory is probed on its own: something else may already have put
# /opt/bin on PATH (a user's own profile, or /userdata/system/env, which
# aiden-env.sh sources), and a single probe would then skip /opt/sbin forever --
# or append a duplicate when only /opt/sbin was present.

for aiden_opt_dir in /opt/bin /opt/sbin; do
	case ":$PATH:" in
		*":$aiden_opt_dir:"*) ;;
		*) PATH="$PATH:$aiden_opt_dir" ;;
	esac
done

export PATH

unset aiden_opt_dir

# Let ncurses programs find Entware's terminfo database.
#
# The rootfs ships ~18 terminfo entries (ansi, dumb, linux, putty*, screen,
# vt100/vt220, xterm*). Anything else -- tmux-256color is the one people hit
# first -- is simply absent, and every ncurses program then exits with
# "Error opening terminal: $TERM". Entware's terminfo package carries a full
# database, but installs it under /opt/share/terminfo where nothing looks.
#
# Entware's own answer is a postinst that appends "export TERMINFO=..." to
# /opt/etc/profile. That file is an Entware convention this system does not
# follow: nothing sources it, so the export never runs. We deliberately do NOT
# source it either -- it is writable by any package's postinst, and sourcing it
# into every login shell would let a package silently redefine the environment,
# including undoing the PATH ordering above. Handling the one setting that
# actually matters is the smaller blast radius.
#
# TERMINFO_DIRS, not TERMINFO: TERMINFO names a *single* directory and would
# hide the rootfs database outright. TERMINFO_DIRS is a search list, so both
# survive. The rootfs comes first for the same reason /opt is appended to PATH
# and not prepended -- an installed package fills gaps, it does not redefine
# terminals that already work.
#
# The two directories are read from the environment purely so this file stays
# testable on a host that has no /opt (same approach as S22opt); nothing sets
# them in production. They grant no capability a user does not already have by
# setting TERMINFO_DIRS directly.
aiden_opt_terminfo=${AIDEN_OPT_TERMINFO:-/opt/share/terminfo}
aiden_sys_terminfo=${AIDEN_SYS_TERMINFO:-/usr/share/terminfo}

if [ -d "$aiden_opt_terminfo" ]; then
	case ":${TERMINFO_DIRS-}:" in
		*":$aiden_opt_terminfo:"*) ;;
		*)
			TERMINFO_DIRS="${TERMINFO_DIRS:-$aiden_sys_terminfo}:$aiden_opt_terminfo"
			export TERMINFO_DIRS
			;;
	esac
fi

unset aiden_opt_terminfo aiden_sys_terminfo
