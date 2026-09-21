# Embedded into each DEBIAN maintainer script by write-maintainer-scripts.sh.
# Every phase must be self-contained: preinst runs before payload unpacking,
# and dpkg may invoke either the old or the new scripts during error recovery.
set -eu

state_parent=/var/lib/aiden-business
state_dir=$state_parent/service-transition
watcher=aiden-wifi-proxy-agent-restart.path
restart_job=aiden-wifi-proxy-agent-restart.service
services='aiden-wifi-proxy.service aiden-frame.service aiden-audio.service aiden-ble.service aiden-agent.service aiden-config-web.service aiden-ttyd.service'

live_systemd() {
    [ -z "${DPKG_ROOT:-}" ] && [ "${SYSTEMD_OFFLINE:-0}" != 1 ] &&
        [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1 || return 1
    if command -v systemd-detect-virt >/dev/null 2>&1 && systemd-detect-virt --quiet --chroot; then
        return 1
    fi
}

# dpkg may configure the config dependency while the old business payload is
# still present. Keep the shared snapshot until a compatible peer is configured.
# The package currently running postinst is itself half-configured at this point.
peer_ready() {
    case "$package" in
        aiden-business) peer=aiden-system-config ;;
        aiden-system-config) peer=aiden-business ;;
    esac
    peer_state=$(dpkg-query -W -f='${Status} ${Version}' "$peer" 2>/dev/null) || return 1
    [ "$peer_state" = "install ok installed $package_version" ]
}

installed_pair_ready() {
    installed_business=$(dpkg-query -W -f='${Status} ${Version}' aiden-business 2>/dev/null) || return 1
    installed_config=$(dpkg-query -W -f='${Status} ${Version}' aiden-system-config 2>/dev/null) || return 1
    case "$installed_business" in 'install ok installed '*) ;; *) return 1 ;; esac
    [ "$installed_business" = "$installed_config" ]
}

# Use systemctl directly to also restore disabled units that the administrator
# had started manually. deb-systemd-invoke start skips these after they stop.
# Honor Debian's service policy before changing any unit in the transaction.
policy_allows() {
    action=$1
    shift
    [ -x /usr/sbin/policy-rc.d ] || return 0
    for unit in "$@"; do
        result=0
        /usr/sbin/policy-rc.d "$unit" "$action" || result=$?
        case "$result" in
            0|104) ;;
            *) echo "aiden-business: service policy denied $action ($unit, exit $result)" >&2; return 1 ;;
        esac
    done
}

restore_services() {
    [ -f "$state_dir/active" ] || return 0
    ready=0
    if [ "${1:-}" = before-unpack ]; then
        installed_pair_ready && ready=1
    else
        peer_ready && ready=1
    fi
    if [ "$ready" != 1 ]; then
        echo "$package: waiting for the matching $package_version package pair before restarting services" >&2
        return 0
    fi
    set --
    restore_watcher=0
    while IFS= read -r unit; do
        case " $services " in
            *" $unit "*) set -- "$@" "$unit" ;;
            *) if [ "$unit" = "$watcher" ]; then restore_watcher=1; fi ;;
        esac
    done < "$state_dir/active"
    policy_allows start "$@" || return 0
    if [ "$restore_watcher" -eq 1 ]; then policy_allows start "$watcher" || return 0; fi
    systemctl daemon-reload || return 1
    if [ "$#" -gt 0 ]; then systemctl start "$@" || return 1; fi
    # Enable the trigger only after all business services have started.
    if [ "$restore_watcher" -eq 1 ]; then systemctl start "$watcher" || return 1; fi
    rm -rf "$state_dir"
}

stop_services() {
    # Check both directions before stopping, so policy cannot strand a service
    # which it permits us to stop but forbids us to start again.
    policy_allows stop "$watcher" "$restart_job" $services || return 0
    policy_allows start "$watcher" $services || return 0
    if [ ! -f "$state_dir/active" ]; then
        install -d -m 0755 "$state_parent"
        staging=$(mktemp -d "$state_parent/.service-transition.XXXXXX")
        trap 'rm -rf "$staging"' EXIT
        : > "$staging/active"
        : > "$staging/loaded"
        for unit in "$watcher" "$restart_job" $services; do
            load_state=$(systemctl show --property=LoadState --value "$unit")
            [ "$load_state" != not-found ] || continue
            printf '%s\n' "$unit" >> "$staging/loaded"
            # The transient restart job must be drained, never replayed.
            [ "$unit" != "$restart_job" ] || continue
            active_state=$(systemctl show --property=ActiveState --value "$unit")
            case "$active_state" in
                active|activating|reloading) printf '%s\n' "$unit" >> "$staging/active" ;;
            esac
        done
        mv "$staging" "$state_dir"
        trap - EXIT
    fi
    # prerm and preinst can both run in one upgrade; retain the first snapshot.
    set --
    while IFS= read -r unit; do
        case "$unit" in
            "$watcher"|"$restart_job") set -- "$@" "$unit" ;;
        esac
    done < "$state_dir/loaded"
    if [ "$#" -gt 0 ] && ! systemctl stop "$@"; then
        # Restore the old pair only if both packages are still configured at
        # the same version. An earlier unpack in this transaction must not be
        # mistaken for a safe pre-upgrade state.
        restore_services before-unpack || true
        return 1
    fi
    set --
    while IFS= read -r unit; do
        case " $services " in *" $unit "*) set -- "$@" "$unit" ;; esac
    done < "$state_dir/loaded"
    if [ "$#" -gt 0 ] && ! systemctl stop "$@"; then
        restore_services before-unpack || true
        return 1
    fi
}

case "$phase:${1:-}" in
    preinst:install|preinst:upgrade|prerm:upgrade|prerm:remove|prerm:deconfigure|prerm:failed-upgrade)
        live_systemd || exit 0
        stop_services
        ;;
    postinst:configure|postinst:abort-upgrade|postinst:abort-remove|postinst:abort-deconfigure|postrm:abort-upgrade|postrm:abort-install)
        if [ "$package:$phase:${1:-}" = aiden-system-config:postinst:configure ] && [ -z "${DPKG_ROOT:-}" ]; then
            # Validate the installed conffile too: dpkg may have preserved an
            # administrator's edit instead of using the packaged version.
            visudo -c || exit 1
        fi
        live_systemd || exit 0
        restore_services
        ;;
    postrm:remove|postrm:purge)
        # Leave services stopped when removing either half of the pair; never
        # change enablement or remove user data.
        if [ -z "${DPKG_ROOT:-}" ]; then
            rm -rf "$state_dir"
            rmdir "$state_parent" 2>/dev/null || true
        fi
        ;;
esac
exit 0
