# Put opkg-installed tools on the path for interactive login shells.
#
# Two deliberate asymmetries keep installed packages isolated from system
# tools and libraries:
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

# Existing /opt entries are removed first, then both are appended, so the result
# does not depend on what ran before this file.
#
# Merely probing "is it already there?" is not enough. profile.d is sourced in
# alphabetical order, so aiden-env.sh -- which sources /userdata/system/env with
# "set -a" -- has already run by the time we get here. A PATH=/opt/bin:$PATH in
# that file would leave /opt/bin ahead of /usr/bin, and a probe would see it
# present and leave it there, quietly breaking the ordering guarantee above.
#
# Scope, stated honestly: this only fixes login shells. Core services are
# started through aiden-env-run, which sources the same /userdata/system/env and
# is not affected by this file at all -- so this is not a security boundary, it
# is a predictable default. A user who really wants /opt first can still do it
# in ~/.profile, which runs after /etc/profile.d.
#
# Done with string surgery rather than IFS splitting because splitting silently
# drops a trailing empty PATH component, and an empty component means "the
# current directory" -- changing that behind the user's back is its own bug.
aiden_opt_path=":$PATH:"
for aiden_opt_dir in /opt/bin /opt/sbin; do
	while :; do
		case $aiden_opt_path in
			*":$aiden_opt_dir:"*)
				aiden_opt_path="${aiden_opt_path%%":$aiden_opt_dir:"*}:${aiden_opt_path#*":$aiden_opt_dir:"}"
				;;
			*)
				break
				;;
		esac
	done
done
aiden_opt_path=${aiden_opt_path#:}
aiden_opt_path=${aiden_opt_path%:}

# The :+ guard matters: without it an emptied PATH would yield a leading ":",
# which is an empty component, i.e. the current directory on PATH.
PATH="${aiden_opt_path:+$aiden_opt_path:}/opt/bin:/opt/sbin"

export PATH

unset aiden_opt_dir aiden_opt_path

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
# Set unconditionally -- deliberately NOT guarded on the directory existing.
#
# An earlier version only acted when /opt/share/terminfo was already there. That
# is wrong for the flow everybody actually uses:
#
#     ssh in            <- nothing installed yet, so the guard did nothing
#     opkg install htop <- terminfo arrives now
#     htop              <- same shell: TERMINFO_DIRS still unset -> still fails
#
# The variable is read by ncurses at program start, so it has to be right for
# shells that logged in before the package existed. Only the next login would
# have picked it up, which makes the fix useless exactly when it is needed.
#
# Naming a directory that does not exist costs nothing: ncurses skips missing
# entries in the search list (verified on the board with both orderings), and
# $HOME/.terminfo is still searched independently of this variable.
#
# The two directories are read from the environment purely so this file stays
# testable on a host that has no /opt (same approach as S22opt); nothing sets
# them in production. They grant no capability a user does not already have by
# setting TERMINFO_DIRS directly.
aiden_opt_terminfo=${AIDEN_OPT_TERMINFO:-/opt/share/terminfo}
aiden_sys_terminfo=${AIDEN_SYS_TERMINFO:-/usr/share/terminfo}

case ":${TERMINFO_DIRS-}:" in
	*":$aiden_opt_terminfo:"*) ;;
	*)
		TERMINFO_DIRS="${TERMINFO_DIRS:-$aiden_sys_terminfo}:$aiden_opt_terminfo"
		export TERMINFO_DIRS
		;;
esac

unset aiden_opt_terminfo aiden_sys_terminfo
