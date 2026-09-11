#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly AGGREGATOR=${REPO_ROOT}/overlay-debian/usr/lib/aiden/aiden-ota-health-aggregate
readonly TEST_ROOT=$(mktemp -d)
trap 'rm -rf "${TEST_ROOT}"' EXIT

fail() {
    echo "Debian OTA health aggregator test failure: $*" >&2
    exit 1
}

mkdir -p "${TEST_ROOT}/bin"
printf '{}\n' >"${TEST_ROOT}/pending_boot.json"
printf 'SAFE_ENV=1\n' >"${TEST_ROOT}/system.env"
cat >"${TEST_ROOT}/aiden_boot.conf" <<'EOF'
ENABLE_AGENT=0
ENABLE_AUDIO_SERVICE=0
ENABLE_BLE_SERVICE=0
ENABLE_CONFIG_WEB=0
ENABLE_FRAME_SERVICE=0
ENABLE_USB_DHCP=0
ENABLE_USB_HID=0
ENABLE_WIFIDRV=0
ENABLE_WLAN_GUARD=0
ENABLE_BLUETOOTH_HCI=0
EOF

cat >"${TEST_ROOT}/bin/systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "${*: -1}" >>"${SYSTEMCTL_LOG}"
if [ "${FAIL_SERVICE:-}" = "${*: -1}" ]; then
    exit 1
fi
EOF
cat >"${TEST_ROOT}/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
exit "${CURL_STATUS:-0}"
EOF
cat >"${TEST_ROOT}/bin/ota" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >"${OTA_LOG}"
EOF
chmod +x "${TEST_ROOT}/bin/systemctl" "${TEST_ROOT}/bin/curl" "${TEST_ROOT}/bin/ota"

run_aggregator() {
    SYSTEMCTL_LOG=${TEST_ROOT}/systemctl.log \
    OTA_LOG=${TEST_ROOT}/ota.log \
    OTA_BIN=${TEST_ROOT}/bin/ota \
    OTA_CONFIG=${TEST_ROOT}/debian-config.json \
    PENDING_PATH=${TEST_ROOT}/pending_boot.json \
    BOOT_CONFIG=${TEST_ROOT}/aiden_boot.conf \
    ENVIRONMENT_PATH=${TEST_ROOT}/system.env \
    SYSTEMCTL_BIN=${TEST_ROOT}/bin/systemctl \
    CURL_BIN=${TEST_ROOT}/bin/curl \
    SLEEP_BIN=/bin/true \
    WAIT_SECONDS=0 \
        "${AGGREGATOR}"
}

run_aggregator
grep -qx -- '--config' "${TEST_ROOT}/ota.log"
grep -qx "${TEST_ROOT}/debian-config.json" "${TEST_ROOT}/ota.log"
grep -qx 'mark-health' "${TEST_ROOT}/ota.log"
for service in \
    oem.mount userdata.mount userdata-ota.mount \
    aiden-userdata-migrate.service aiden-environment.service \
    systemd-networkd.service; do
    grep -qx "${service}" "${TEST_ROOT}/systemctl.log" \
        || fail "core service was not checked: ${service}"
done

rm -f "${TEST_ROOT}/ota.log"
if FAIL_SERVICE=systemd-networkd.service run_aggregator >/dev/null 2>&1; then
    fail "aggregator accepted an inactive local network stack"
fi
[ ! -e "${TEST_ROOT}/ota.log" ] || fail "marker command ran after a failed core check"

printf 'ENABLE_AGENT=1\n' >>"${TEST_ROOT}/aiden_boot.conf"
rm -f "${TEST_ROOT}/ota.log"
if CURL_STATUS=1 run_aggregator >/dev/null 2>&1; then
    fail "aggregator accepted an unresponsive local Agent runtime"
fi
[ ! -e "${TEST_ROOT}/ota.log" ] || fail "marker command ran after failed Agent probe"

echo "Debian OTA health aggregator tests passed"
