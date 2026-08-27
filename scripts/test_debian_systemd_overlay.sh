#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly OVERLAY=${REPO_ROOT}/overlay-debian
readonly UNIT_DIR=${OVERLAY}/etc/systemd/system
readonly INIT_MAP=${REPO_ROOT}/scripts/debian/init-script-map.tsv
readonly ENV_MAP=${REPO_ROOT}/scripts/debian/environment-service-map.tsv
readonly TEST_ROOT=$(mktemp -d)
trap 'rm -rf "${TEST_ROOT}"' EXIT

fail() {
    echo "Debian systemd overlay test failure: $*" >&2
    exit 1
}

[ -d "${UNIT_DIR}" ] || fail "overlay-debian systemd unit directory is missing"

grep -qx 'JobRunningTimeoutSec=5s' \
    "${UNIT_DIR}/dev-rtc0.device.d/20-aiden-optional.conf"
grep -qx 'JobRunningTimeoutSec=30s' \
    "${UNIT_DIR}/sys-subsystem-net-devices-wlan0.device.d/20-aiden-optional.conf"
grep -qx 'RuntimeDirectory=sshd' \
    "${UNIT_DIR}/aiden-ssh-identity.service"
grep -qx 'RuntimeDirectoryMode=0755' \
    "${UNIT_DIR}/aiden-ssh-identity.service"
grep -qx 'TimeoutStartSec=180s' \
    "${UNIT_DIR}/aiden-ssh-identity.service"
grep -qx 'for type in ed25519 ecdsa rsa; do' \
    "${OVERLAY}/usr/lib/aiden/aiden-ssh-identity"
grep -q 'runtime_dir=/run/sshd' \
    "${OVERLAY}/usr/lib/aiden/aiden-ssh-identity"
grep -q 'mkdir -p .*"${runtime_dir}"' \
    "${OVERLAY}/usr/lib/aiden/aiden-ssh-identity"
grep -q 'chmod 0755 "${runtime_dir}"' \
    "${OVERLAY}/usr/lib/aiden/aiden-ssh-identity"
grep -qx 'ConditionPathExists=/dev/rtc0' \
    "${UNIT_DIR}/aiden-rtc.service"
if grep -Eq '^(Wants|Requires|After)=.*dev-rtc0\.device' \
    "${UNIT_DIR}/aiden-rtc.service"; then
    fail "optional RTC service still waits for dev-rtc0.device"
fi

while IFS= read -r script; do
    [ -x "${script}" ] || fail "helper is not executable: ${script#${REPO_ROOT}/}"
    sh -n "${script}"
done < <(find "${OVERLAY}/usr/lib/aiden" -maxdepth 1 -type f | LC_ALL=C sort)

awk -F '\t' '$2 == "migrate" {print $3}' "${INIT_MAP}" \
    | tr ',' '\n' | LC_ALL=C sort -u \
    | while IFS= read -r unit; do
        case "${unit}" in
            oem.mount|aiden-*.service)
                [ -f "${UNIT_DIR}/${unit}" ] \
                    || fail "mapped native unit is missing: ${unit}"
                ;;
        esac
    done

while IFS=$'\t' read -r init_script unit environment_file invalid_policy; do
    [ "${init_script}" = init_script ] && continue
    unit_path=${UNIT_DIR}/${unit}
    [ -f "${unit_path}" ] || fail "environment consumer unit is missing: ${unit}"
    grep -q 'aiden-environment.service' "${unit_path}" \
        || fail "${unit} does not depend on aiden-environment.service"
    grep -qx "EnvironmentFile=${environment_file}" "${unit_path}" \
        || fail "${unit} does not consume ${environment_file}"
    [ -n "${invalid_policy}" ] || fail "${unit} has no invalid-environment policy"
done <"${ENV_MAP}"

if rg -n 'aiden-env-run|/etc/init\.d/' "${UNIT_DIR}"; then
    fail "Debian units must not use the Buildroot environment wrapper or SysV scripts"
fi
if rg -n '\b(ifconfig|dhcpcd|dhclient|udhcpc|killall)\b' \
    "${OVERLAY}/usr" "${UNIT_DIR}"; then
    fail "Debian runtime helpers contain a retired network control command"
fi
if rg -n '(^|[[:space:]])\.[[:space:]]+/userdata|source[[:space:]]+/userdata' \
    "${OVERLAY}/usr" "${UNIT_DIR}"; then
    fail "Debian runtime directly sources untrusted userdata"
fi

grep -qx 'What=/run/aiden/oem-device' "${UNIT_DIR}/oem.mount"
grep -qx 'What=/dev/mmcblk0p11' "${UNIT_DIR}/userdata.mount"
grep -qx 'What=/dev/mmcblk0p12' "${UNIT_DIR}/userdata-ota.mount"
grep -q 'aiden.slot_suffix' "${OVERLAY}/usr/lib/aiden/aiden-slot-resolve"
grep -q 'Root slot.*disagrees' "${OVERLAY}/usr/lib/aiden/aiden-slot-resolve"
grep -q 'rootfs${AIDEN_SLOT_SUFFIX}' "${OVERLAY}/usr/lib/aiden/aiden-rootfs-grow"
grep -q '^mount -o remount,rw /$' "${OVERLAY}/usr/lib/aiden/aiden-rootfs-grow"
grep -q 'if \[ ! -e "${marker}" \]; then' \
    "${OVERLAY}/usr/lib/aiden/aiden-rootfs-grow"
if grep -q '^ConditionPathExists=!/var/lib/aiden/rootfs-grown$' \
    "${UNIT_DIR}/aiden-rootfs-grow.service"; then
    fail "rootfs-grow must remount the root filesystem on every boot"
fi
grep -q 'systemd-timesyncd.service' \
    "${UNIT_DIR}/aiden-rootfs-grow.service"
grep -q 'mkdir -p /userdata/agent/log' \
    "${OVERLAY}/usr/lib/aiden/aiden-userdata-migrate"

grep -qx 'DHCP=yes' "${OVERLAY}/etc/systemd/network/20-wlan0.network"
grep -qx 'Address=192.168.42.1/24' "${OVERLAY}/etc/systemd/network/30-usb0.network"
grep -qx 'ConfigureWithoutCarrier=yes' "${OVERLAY}/etc/systemd/network/30-usb0.network"
grep -qx 'RequiredForOnline=no' "${OVERLAY}/etc/systemd/network/30-usb0.network"
if rg -n 'networkctl reconfigure (usb0|"?\$\{?interface\}?")' \
    "${REPO_ROOT}/overlay/etc/init.d/S49usbhid" \
    "${OVERLAY}/usr/lib/aiden/aiden-usb-ecm-watchdog" \
    "${OVERLAY}/usr/lib/aiden/aiden-wait-interface-ip" \
    "${REPO_ROOT}/overlay/oem/usr/bin/aiden-dynamic-keyboard"; then
    fail "USB helpers must not ask networkd to replace an address they just configured"
fi
grep -q '/userdata/debian/wifi/wpa_supplicant-wlan0.conf' \
    "${UNIT_DIR}/wpa_supplicant@wlan0.service.d/20-aiden.conf"
grep -q -- '--wifi-backend=systemd-networkd' \
    "${UNIT_DIR}/aiden-config-web.service"
grep -q 'AIDEN_USB_COMPOSITE_REFRESH_COMMAND=/usr/lib/aiden/aiden-usb-ecm-watchdog' \
    "${UNIT_DIR}/aiden-agent.service"
grep -q 'watchdog=running pid=\$pid supervisor=systemd' \
    "${OVERLAY}/usr/lib/aiden/aiden-agent-control"
grep -q 'systemctl show aiden-agent.service -p MainPID --value' \
    "${OVERLAY}/usr/lib/aiden/aiden-agent-control"
grep -q 'aiden-boot-timeline-init.service' "${UNIT_DIR}/aiden-agent.service"
grep -qx 'StartLimitIntervalSec=0' "${UNIT_DIR}/aiden-frame.service"
grep -qx 'Restart=on-failure' "${UNIT_DIR}/aiden-frame.service"
grep -qx 'RestartSec=2s' "${UNIT_DIR}/aiden-frame.service"
grep -qx 'ExecStart=/usr/lib/aiden/aiden-boot-timeline init-systemd' \
    "${UNIT_DIR}/aiden-boot-timeline-init.service"
grep -qx 'ExecStart=/usr/lib/aiden/aiden-boot-timeline finalize-systemd' \
    "${UNIT_DIR}/aiden-boot-timeline.service"
grep -q 'networkctl reconfigure' "${OVERLAY}/usr/lib/aiden/aiden-wlan-guard"
if grep -qE 'dhcpcd|dhclient' "${OVERLAY}/usr/lib/aiden/aiden-wlan-guard"; then
    fail "Wi-Fi guard takes DHCP ownership from networkd"
fi

grep -Fqx 'SUBSYSTEM=="misc", KERNEL=="rknpu", GROUP="video", MODE="0660"' \
    "${OVERLAY}/etc/udev/rules.d/70-aiden-rknpu.rules"
grep -Fqx 'SUBSYSTEM=="mpi", GROUP="video", MODE="0660"' \
    "${OVERLAY}/etc/udev/rules.d/70-aiden-rknpu.rules"
grep -Fqx 'SUBSYSTEM=="rk_dma_heap", KERNEL=="rk-dma-heap-cma", GROUP="video", MODE="0660", SYMLINK+="dma_heap/system"' \
    "${OVERLAY}/etc/udev/rules.d/70-aiden-rknpu.rules"
grep -Fq 'set_video_access /dev/rknpu' \
    "${OVERLAY}/usr/lib/aiden/aiden-media-modules"
grep -Fq 'for node in /dev/mpi/*' \
    "${OVERLAY}/usr/lib/aiden/aiden-media-modules"
grep -Fq 'rk_heap_node=/dev/rk_dma_heap/rk-dma-heap-cma' \
    "${OVERLAY}/usr/lib/aiden/aiden-media-modules"
grep -Fq 'ln -s ../rk_dma_heap/rk-dma-heap-cma /dev/dma_heap/system' \
    "${OVERLAY}/usr/lib/aiden/aiden-media-modules"

watchdog=${OVERLAY}/usr/lib/aiden/aiden-usb-ecm-watchdog
watchdog_root=${TEST_ROOT}/usb-watchdog
mkdir -p "${watchdog_root}"
printf '%s\n' ffb00000.usb >"${watchdog_root}/UDC"
printf '%s\n' configured >"${watchdog_root}/state"
AIDEN_USB_UDC_FILE="${watchdog_root}/UDC" \
AIDEN_USB_UDC_STATE_FILE="${watchdog_root}/state" \
AIDEN_USB_NET_CLASS_PATH="${watchdog_root}/no-usb0" \
AIDEN_USB_LOCK_DIR="${watchdog_root}/lock" \
AIDEN_USB_GRACE_FILE="${watchdog_root}/grace" \
AIDEN_USB_REFRESH_STATE_FILE="${watchdog_root}/refresh.state" \
    "${watchdog}" refresh
grep -qx 'last_refresh_reason=manual refresh' "${watchdog_root}/refresh.state"
grep -qx 'last_refresh_result=ok' "${watchdog_root}/refresh.state"
grep -qx 'ffb00000.usb' "${watchdog_root}/UDC"

# A composite reset drops the HID session together with ECM, so an unanswered
# ARP burst alone must never be enough to tear a working session down.
grep -Fq 'command -v arping >/dev/null 2>&1 || return 2' "${watchdog}" \
    || fail "a missing arping must not be able to justify a composite reset"
grep -Fq 'carrier_up()' "${watchdog}" \
    || fail "watchdog must treat a carrier-less usb0 as idle rather than stalled"
grep -Fq 'read_rx_packets()' "${watchdog}" \
    || fail "watchdog must sample usb0 RX counters as a positive liveness signal"
if grep -Eq '^threshold=\$\{USB_ECM_FAIL_THRESHOLD:-[1-5]\}$' "${watchdog}"; then
    fail "ECM stall confirmation must span more than a few probe intervals"
fi

# Drive the real watch loop against a fake sysfs to prove the liveness guard:
# ARP goes unanswered throughout, so only the RX counter decides the outcome.
run_ecm_watchdog() {
    local root=$1 seconds=$2
    mkdir -p "${root}/bin" "${root}/net/statistics"
    printf '%s\n' ffb00000.usb >"${root}/UDC"
    printf '%s\n' configured >"${root}/state"
    printf '%s\n' 1 >"${root}/net/carrier"
    printf '%s\n' '0 aa:bb:cc:dd:ee:ff 192.168.42.99 host *' >"${root}/leases"
    printf '#!/bin/sh\nexit 1\n' >"${root}/bin/arping"
    chmod 0755 "${root}/bin/arping"
    PATH="${root}/bin:${PATH}" \
    AIDEN_USB_UDC_FILE="${root}/UDC" \
    AIDEN_USB_UDC_STATE_FILE="${root}/state" \
    AIDEN_USB_NET_CLASS_PATH="${root}/net" \
    AIDEN_USB_CARRIER_FILE="${root}/net/carrier" \
    AIDEN_USB_RX_PACKETS_FILE="${root}/net/statistics/rx_packets" \
    AIDEN_USB_LEASE_FILE="${root}/leases" \
    AIDEN_USB_LOCK_DIR="${root}/lock" \
    AIDEN_USB_GRACE_FILE="${root}/grace" \
    AIDEN_USB_REFRESH_STATE_FILE="${root}/refresh.state" \
    USB_ECM_PROBE_INTERVAL=1 USB_ECM_FAIL_THRESHOLD=2 USB_ECM_COOLDOWN=1 \
        timeout "${seconds}" "${watchdog}" watch >/dev/null 2>&1 || true
}

live_root=${TEST_ROOT}/usb-watchdog-live
mkdir -p "${live_root}/net/statistics"
printf '%s\n' 100 >"${live_root}/net/statistics/rx_packets"
(
    for i in $(seq 1 12); do
        printf '%s\n' $((100 + i)) >"${live_root}/net/statistics/rx_packets"
        sleep 1
    done
) &
rx_writer=$!
run_ecm_watchdog "${live_root}" 5
kill "${rx_writer}" 2>/dev/null || true
wait "${rx_writer}" 2>/dev/null || true
grep -qx 'ffb00000.usb' "${live_root}/UDC" \
    || fail "a session still receiving traffic must stay bound to the UDC"
[ ! -f "${live_root}/refresh.state" ] \
    || fail "a session still receiving traffic must never be reset over silent ARP"

# Negative control: with RX frozen the same unanswered probes are a real stall
# and recovery must still fire, so the guard above is not simply disabling it.
stalled_root=${TEST_ROOT}/usb-watchdog-stalled
mkdir -p "${stalled_root}/net/statistics"
printf '%s\n' 100 >"${stalled_root}/net/statistics/rx_packets"
run_ecm_watchdog "${stalled_root}" 6
[ -f "${stalled_root}/refresh.state" ] \
    || fail "a genuinely stalled ECM session must still be recovered"
grep -qx 'last_refresh_reason=ECM stall' "${stalled_root}/refresh.state" \
    || fail "a stalled ECM session must be recovered with the ECM stall reason"

grep -q '/oem/usr/bin/aiden-environment' "${UNIT_DIR}/aiden-environment.service"
grep -q '/run/aiden/environment.invalid' "${UNIT_DIR}/aiden-environment.service"
grep -q 'schema_version' "${OVERLAY}/usr/lib/aiden/aiden-userdata-migrate"
grep -q 'backup_root=${state_dir}/backups' \
    "${OVERLAY}/usr/lib/aiden/aiden-userdata-migrate"
grep -q 'mark-health' "${OVERLAY}/usr/lib/aiden/aiden-ota-health-aggregate"
grep -q 'provision-identity' "${OVERLAY}/usr/lib/aiden/aiden-machine-id-provision"
grep -q 'Requires=.*aiden-machine-id.service' \
    "${UNIT_DIR}/aiden-environment.service"
grep -q 'Requires=aiden-ota-health-marker.service' \
    "${UNIT_DIR}/aiden-ota-health.service"
grep -q '/userdata/debian/ota/config.json' \
    "${UNIT_DIR}/aiden-ota-health.service"
if rg -n 'WriteHealthMarkerIfPending' "${REPO_ROOT}/src/agent/cmd/daemon"; then
    fail "Agent daemon still writes an early OTA health marker"
fi

grep -qx 'disable aiden-wetty.service' \
    "${OVERLAY}/etc/systemd/system-preset/90-aiden.preset"
grep -qx 'disable dnsmasq.service' \
    "${OVERLAY}/etc/systemd/system-preset/90-aiden.preset"
grep -qx 'disable wpa_supplicant.service' \
    "${OVERLAY}/etc/systemd/system-preset/90-aiden.preset"
grep -qx 'enable aiden-boot-timeline.service' \
    "${OVERLAY}/etc/systemd/system-preset/90-aiden.preset"

timeline_helper=${REPO_ROOT}/overlay/etc/aiden_boot_timeline.sh
timeline_root=${TEST_ROOT}/boot-timeline
mkdir -p "${timeline_root}/archive"
printf '%s\n' '12.34 8.00' >"${timeline_root}/uptime"
cat >"${timeline_root}/systemctl" <<'EOF'
#!/bin/sh
[ "${1:-}" = show ] || exit 2
case "${2:-}" in
    aiden-agent.service)
        cat <<'PROPS'
InactiveExitTimestampMonotonic=1000000
ActiveEnterTimestampMonotonic=3500000
ExecMainStartTimestampMonotonic=1100000
ExecMainExitTimestampMonotonic=0
ActiveState=active
SubState=running
Result=success
PROPS
        ;;
    failed.service)
        cat <<'PROPS'
InactiveExitTimestampMonotonic=0
ActiveEnterTimestampMonotonic=0
ExecMainStartTimestampMonotonic=2000000
ExecMainExitTimestampMonotonic=2800000
ActiveState=failed
SubState=failed
Result=exit-code
PROPS
        ;;
    *) exit 1 ;;
esac
EOF
chmod +x "${timeline_root}/systemctl"
timeline_env=(
    "BOOT_TIMELINE_LOG=${timeline_root}/timeline.log"
    "BOOT_TIMELINE_ARCHIVE_DIR=${timeline_root}/archive"
    "BOOT_TIMELINE_ARCHIVE_STATE=${timeline_root}/archive.state"
    "BOOT_TIMELINE_LOCK_FILE=${timeline_root}/timeline.lock"
    "BOOT_TIMELINE_UPTIME_PATH=${timeline_root}/uptime"
    "BOOT_TIMELINE_SYSTEMCTL_BIN=${timeline_root}/systemctl"
    "BOOT_TIMELINE_SYSTEMD_UNITS=aiden-agent.service failed.service"
)
env "${timeline_env[@]}" "${timeline_helper}" init-systemd
env "${timeline_env[@]}" "${timeline_helper}" mark agent:listening
env "${timeline_env[@]}" "${timeline_helper}" finalize-systemd
grep -q 'systemd:begin$' "${timeline_root}/timeline.log"
grep -q '3.50 2.50 unit:active aiden-agent.service sub=running result=success$' \
    "${timeline_root}/timeline.log"
grep -q '2.80 0.80 unit:failed failed.service sub=failed result=exit-code$' \
    "${timeline_root}/timeline.log"
grep -q 'mark agent:listening$' "${timeline_root}/timeline.log"
grep -q 'mark systemd:aiden-target$' "${timeline_root}/timeline.log"
archive_path=$(cat "${timeline_root}/archive.state")
[ -f "${archive_path}" ] || fail "boot timeline archive state is dangling"
cmp "${timeline_root}/timeline.log" "${archive_path}"
env "${timeline_env[@]}" "${timeline_helper}" capture-systemd
[ "$(grep -c ' unit:' "${timeline_root}/timeline.log")" -eq 2 ] \
    || fail "systemd timeline capture is not idempotent"
awk '{ if ($1 + 0 < previous) exit 1; previous = $1 + 0 }' \
    "${timeline_root}/timeline.log" \
    || fail "systemd timeline records are not monotonic"

verify_output=${TEST_ROOT}/systemd-verify.txt
systemd-analyze verify \
    "${UNIT_DIR}"/*.service "${UNIT_DIR}"/*.mount "${UNIT_DIR}/aiden.target" \
    >"${verify_output}" 2>&1 || true
if grep -E "${UNIT_DIR}/.*(Unknown key name|Failed to parse|Missing '=')" \
    "${verify_output}"; then
    fail "systemd-analyze found an invalid overlay directive"
fi

"${REPO_ROOT}/scripts/test_debian_ota_health_aggregate.sh"
"${REPO_ROOT}/scripts/test_debian_machine_id_provision.sh"
"${REPO_ROOT}/scripts/test_debian_agent_control.sh"

echo "Debian systemd overlay tests passed"
