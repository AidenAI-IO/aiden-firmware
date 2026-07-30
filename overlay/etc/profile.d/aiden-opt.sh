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

OPT_HOME=/opt

case ":$PATH:" in
	*":$OPT_HOME/bin:"*) ;;
	*) PATH="$PATH:$OPT_HOME/bin:$OPT_HOME/sbin" ;;
esac

export PATH

unset OPT_HOME
