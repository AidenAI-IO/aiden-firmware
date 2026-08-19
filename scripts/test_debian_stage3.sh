#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly STAGE3_DIR=${REPO_ROOT}/scripts/debian-stage3
readonly TEST_ROOT=$(mktemp -d)
trap 'rm -rf "${TEST_ROOT}"' EXIT

fail() {
    echo "Debian Stage 3 test failure: $*" >&2
    exit 1
}

bash -n \
    "${STAGE3_DIR}/build.sh" \
    "${STAGE3_DIR}/audit-bsp.sh" \
    "${STAGE3_DIR}/container-build-rootfs.sh" \
    "${STAGE3_DIR}/container-assemble-images.sh" \
    "${STAGE3_DIR}/container-install-ota-config.sh" \
    "${STAGE3_DIR}/container-audit-images.sh"
PYTHONDONTWRITEBYTECODE=1 python3 -m py_compile \
    "${STAGE3_DIR}/canonicalize-ext4.py" \
    "${STAGE3_DIR}/canonicalize-bsp.py" \
    "${STAGE3_DIR}/generate-spdx.py" \
    "${STAGE3_DIR}/validate-ota-config.py"

for script in \
    build.sh audit-bsp.sh canonicalize-bsp.py container-build-rootfs.sh container-assemble-images.sh \
    container-install-ota-config.sh container-audit-images.sh \
    canonicalize-ext4.py generate-spdx.py validate-ota-config.py; do
    [ -x "${STAGE3_DIR}/${script}" ] || fail "${script} is not executable"
done

grep -q 'snapshot.debian.org/archive/debian/20260803T000000Z' \
    "${STAGE3_DIR}/debian.sources"
grep -q 'snapshot.debian.org/archive/debian-security/20260803T000000Z' \
    "${STAGE3_DIR}/debian.sources"
grep -q '^Check-Valid-Until: no$' "${STAGE3_DIR}/debian.sources"
grep -q '^FROM debian:trixie-slim@sha256:' "${STAGE3_DIR}/Dockerfile"

for package in \
    systemd-sysv udev dbus kmod openssh-server sudo adb iproute2 iputils-arping \
    wpasupplicant bluez systemd-resolved systemd-timesyncd dnsmasq-base \
    e2fsprogs v4l-utils libdrm2; do
    grep -qx "${package}" "${STAGE3_DIR}/packages.list" \
        || fail "production package list is missing ${package}"
done
if grep -Eq '^(net-tools|dhcpcd|dhcpcd-base|isc-dhcp-client|flash-kernel|initramfs-tools)$' \
    "${STAGE3_DIR}/packages.list"; then
    fail "banned package is present in production package list"
fi
grep -q 'Pin-Priority: -1' "${STAGE3_DIR}/aiden-production.pref"

grep -Fq '1536M(rootfs_a),1536M(rootfs_b),3G(userdata),300M(ota)' \
    "${STAGE3_DIR}/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk"
grep -Fq 'RK_UBOOT_DEFCONFIG_FRAGMENT="rk-emmc.config rv1106-ab.config"' \
    "${STAGE3_DIR}/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk"
grep -Fq "RK_KERNEL_CMDLINE_EXTRA=net.ifnames\$'\\x3d'0" \
    "${STAGE3_DIR}/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk"
grep -Fq 'root=PARTLABEL=$root_label' \
    "${STAGE3_DIR}/sdk-patches/0002-append-slot-kernel-cmdline.patch"
grep -Fq 'slot_cmdline="$slot_cmdline $RK_KERNEL_CMDLINE_EXTRA"' \
    "${STAGE3_DIR}/sdk-patches/0002-append-slot-kernel-cmdline.patch"

if [ -e "${REPO_ROOT}/pico-sdk/project/build.sh" ]; then
    git -C "${REPO_ROOT}/pico-sdk" apply --check \
        "${STAGE3_DIR}/sdk-patches/0001-use-all-host-cpus.patch"
    git -C "${REPO_ROOT}/pico-sdk" apply --check \
        "${STAGE3_DIR}/sdk-patches/0002-append-slot-kernel-cmdline.patch"
    git -C "${REPO_ROOT}/pico-sdk" apply --check \
        "${STAGE3_DIR}/sdk-patches/0003-add-ab-images-action.patch"
    git -C "${REPO_ROOT}/pico-sdk" apply --check \
        "${STAGE3_DIR}/sdk-patches/0004-make-bsp-images-reproducible.patch"
fi

grep -Fq 'rsync -aHAX --numeric-ids --chown=0:0' \
    "${STAGE3_DIR}/container-build-rootfs.sh"
if grep -Eq 'chown[[:space:]]+(-[^[:space:]]+[[:space:]]+)*(-R|--recursive)' \
    "${STAGE3_DIR}/container-build-rootfs.sh"; then
    fail "rootfs builder recursively chowns Debian package files"
fi
grep -Fq 'aiden-usb-gadget' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'aiden-boot-timeline' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'aiden-boot-timeline.service' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'aiden-machine-id.service' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'useradd --uid 1000 --gid aiden --create-home' \
    "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'sudo,audio,video,dialout,plugdev,netdev aiden' \
    "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'mgLNEH35w8GS9UrV1Yi4BXg1g.CYyVIAnUAXIXmato37U4M5obgDhGY2YhpIwHd7sNCtBq/uB.5oEk8jHPNYZ.' \
    "${STAGE3_DIR}/container-build-rootfs.sh"
grep -qx 'PasswordAuthentication yes' \
    "${REPO_ROOT}/overlay-debian/etc/ssh/sshd_config.d/20-aiden.conf"
grep -qx 'PermitRootLogin prohibit-password' \
    "${REPO_ROOT}/overlay-debian/etc/ssh/sshd_config.d/20-aiden.conf"
grep -Fq 'systemctl --root="${ROOTFS_DIR}" preset-all' \
    "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'ssh.socket rsync.service' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'sbom.spdx.json' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'rootfs.tar.zst' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'hash_seed=${ROOTFS_UUID}' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq -- '-d "${ROOTFS_IMPORT_DIR}"' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'canonicalize-ext4.py' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'comparison=rsync-HAXc-numeric-ids' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'var/cache/apt/pkgcache.bin' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'var/cache/apt/srcpkgcache.bin' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'var/cache/ldconfig/aux-cache' "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'safe.directory="${REPO_ROOT}/pico-sdk"' \
    "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq 'safe.directory="${REPO_ROOT}"' \
    "${STAGE3_DIR}/container-build-rootfs.sh"
grep -Fq -- '--path-format=absolute --git-common-dir' "${STAGE3_DIR}/build.sh"
grep -Fq 'KBUILD_BUILD_USER=aiden' "${STAGE3_DIR}/build.sh"
grep -Fq './build.sh abimages' "${STAGE3_DIR}/build.sh"
grep -Fq '0004-make-bsp-images-reproducible.patch' "${STAGE3_DIR}/build.sh"
grep -Fq 'canonicalize-bsp.py' "${STAGE3_DIR}/build.sh"
grep -Fq 'audit-bsp.sh' "${STAGE3_DIR}/build.sh"
grep -Fq 'factory A/B metadata is invalid' "${STAGE3_DIR}/audit-bsp.sh"
grep -Fq 'boot_${slot}.img contains multiple root arguments' \
    "${STAGE3_DIR}/audit-bsp.sh"
grep -Fq 'bsp-artifacts.sha256' "${STAGE3_DIR}/audit-bsp.sh"
grep -Fq -- '--check' "${STAGE3_DIR}/audit-bsp.sh"

for binary in \
    abctl agent aiden-environment audio_service ble_service config_web cpu_vad \
    frame_service ota rknn_vad; do
    grep -qx "    ${binary}" "${STAGE3_DIR}/container-assemble-images.sh" \
        || fail "production OEM allowlist is missing ${binary}"
done
if grep -Eq '^    (example_|hello$|trigger$|image_process$|audio_stream$)' \
    "${STAGE3_DIR}/container-assemble-images.sh"; then
    fail "diagnostic executable leaked into the production OEM allowlist"
fi
grep -Fq 'src/agent/config/skills/' "${STAGE3_DIR}/container-assemble-images.sh"
grep -Fq 'src/config_web/web/' "${STAGE3_DIR}/container-assemble-images.sh"
grep -Fq 'AGENT_CONFIG_PATH' "${STAGE3_DIR}/build.sh"
grep -Fq '${AGENT_CONFIG_PATH}:/run/secrets/agent.toml:ro' \
    "${STAGE3_DIR}/build.sh"
grep -Fq 'apps/bin/agent" config-check --format=json' "${STAGE3_DIR}/build.sh"
grep -Fq 'install -m 0600 "${AGENT_CONFIG}" "${USERDATA_ROOT}/agent/agent.toml"' \
    "${STAGE3_DIR}/container-assemble-images.sh"
grep -Fq 'overlay/oem/usr/bin/aiden-dynamic-keyboard' \
    "${STAGE3_DIR}/container-assemble-images.sh"
grep -Fq 'kernel_drv_ko/' "${STAGE3_DIR}/container-assemble-images.sh"
grep -Fq '/userdata/debian/ota/config.json' "${STAGE3_DIR}/build.sh"
grep -Fq 'factory_partition_hashes' "${STAGE3_DIR}/validate-ota-config.py"
grep -Fq 'debian/ota/config.json' "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'Agent configuration does not match the external build input' \
    "${STAGE3_DIR}/container-audit-images.sh"

grep -Fq 'root=PARTLABEL=${root_label}' "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq "net.ifnames=0" "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'Buildroot SysV startup file leaked' "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'boot timeline helper was not installed' "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'aiden-boot-timeline.service is not enabled' "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'rootfs import attribute audit did not pass' "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'rootfs ownership or mode is invalid' "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'sudo executable ownership or mode is invalid' \
    "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'sudo group does not require password-authenticated administrator access' \
    "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'passwordless sudo policy is present' \
    "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'nondeterministic APT package cache leaked' "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'nondeterministic APT source cache leaked' "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'nondeterministic ldconfig cache leaked' "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'unresolved OEM DT_NEEDED' "${STAGE3_DIR}/container-audit-images.sh"
grep -Fq 'generic OTA image is not empty' "${STAGE3_DIR}/container-audit-images.sh"

cat >"${TEST_ROOT}/packages.tsv" <<'EOF'
package	version	architecture	source	maintainer
systemd:armhf	257.7-1	armhf	systemd	Debian systemd Maintainers <pkg-systemd-maintainers@lists.alioth.debian.org>
EOF
PYTHONDONTWRITEBYTECODE=1 "${STAGE3_DIR}/generate-spdx.py" \
    "${TEST_ROOT}/packages.tsv" "${TEST_ROOT}/sbom.json"
python3 - "${TEST_ROOT}/sbom.json" <<'PY'
import json
import sys
with open(sys.argv[1], encoding="utf-8") as stream:
    document = json.load(stream)
assert document["spdxVersion"] == "SPDX-2.3"
assert document["packages"][0]["name"] == "systemd:armhf"
assert document["packages"][0]["externalRefs"][0]["referenceType"] == "purl"
PY

help_output=${TEST_ROOT}/help-output
DEBIAN_STAGE3_OUTPUT_DIR="${help_output}" \
    "${STAGE3_DIR}/build.sh" --help >/dev/null
[ ! -e "${help_output}" ] || fail "--help created the output directory"
if "${STAGE3_DIR}/build.sh" invalid-action >/dev/null 2>&1; then
    fail "invalid build action succeeded"
fi

mkdir -p "${TEST_ROOT}/ota-config-images"
for image in boot_a.img boot_b.img oem.img rootfs.img; do
    printf '%s\n' "${image}" >"${TEST_ROOT}/ota-config-images/${image}"
done
boot_a_hash=$(sha256sum "${TEST_ROOT}/ota-config-images/boot_a.img" | awk '{print $1}')
boot_b_hash=$(sha256sum "${TEST_ROOT}/ota-config-images/boot_b.img" | awk '{print $1}')
oem_hash=$(sha256sum "${TEST_ROOT}/ota-config-images/oem.img" | awk '{print $1}')
rootfs_hash=$(sha256sum "${TEST_ROOT}/ota-config-images/rootfs.img" | awk '{print $1}')
cat >"${TEST_ROOT}/ota-config.json" <<EOF
{
  "repo": "AidenAI-IO/aiden-firmware",
  "channel": "stable",
  "download_safety_margin_bytes": 16777216,
  "factory_version": "20260817-120000-abcdef0",
  "factory_build_time": "2026-08-17T12:00:00Z",
  "factory_partition_hashes": {
    "a": {"boot": "${boot_a_hash}", "oem": "${oem_hash}", "rootfs": "${rootfs_hash}"},
    "b": {"boot": "${boot_b_hash}", "oem": "${oem_hash}", "rootfs": "${rootfs_hash}"}
  }
}
EOF
"${STAGE3_DIR}/validate-ota-config.py" \
    --config "${TEST_ROOT}/ota-config.json" \
    --boot-a "${TEST_ROOT}/ota-config-images/boot_a.img" \
    --boot-b "${TEST_ROOT}/ota-config-images/boot_b.img" \
    --oem "${TEST_ROOT}/ota-config-images/oem.img" \
    --rootfs "${TEST_ROOT}/ota-config-images/rootfs.img" \
    >"${TEST_ROOT}/ota-config-audit.txt"
grep -qx "factory_partition_hashes.b.rootfs=${rootfs_hash}" \
    "${TEST_ROOT}/ota-config-audit.txt"
sed 's/"rootfs": "[0-9a-f]*"/"rootfs": "bad"/' \
    "${TEST_ROOT}/ota-config.json" >"${TEST_ROOT}/bad-ota-config.json"
if "${STAGE3_DIR}/validate-ota-config.py" \
    --config "${TEST_ROOT}/bad-ota-config.json" \
    --boot-a "${TEST_ROOT}/ota-config-images/boot_a.img" \
    --boot-b "${TEST_ROOT}/ota-config-images/boot_b.img" \
    --oem "${TEST_ROOT}/ota-config-images/oem.img" \
    --rootfs "${TEST_ROOT}/ota-config-images/rootfs.img" >/dev/null 2>&1; then
    fail "OTA config validator accepted a mismatched rootfs hash"
fi

mkdir -p "${TEST_ROOT}/mock-bin"
cat >"${TEST_ROOT}/mock-bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\0' "$@" >>"${MOCK_DOCKER_LOG}"
if [ "${1:-}" = image ] && [ "${2:-}" = inspect ]; then
    printf 'sha256:mock-stage3-builder\n'
fi
EOF
chmod +x "${TEST_ROOT}/mock-bin/docker"
mock_output=${TEST_ROOT}/mock-output
mock_log=${TEST_ROOT}/docker-args
MOCK_DOCKER_LOG="${mock_log}" \
PATH="${TEST_ROOT}/mock-bin:${PATH}" \
DEBIAN_STAGE3_OUTPUT_DIR="${mock_output}" \
    "${STAGE3_DIR}/build.sh" rootfs
tr '\0' '\n' <"${mock_log}" >"${TEST_ROOT}/docker-args.txt"
grep -qx -- '--privileged' "${TEST_ROOT}/docker-args.txt"
grep -qx "${mock_output}:/out" "${TEST_ROOT}/docker-args.txt"
grep -qx "${REPO_ROOT}:/work:ro" "${TEST_ROOT}/docker-args.txt"
grep -qx 'scripts/debian-stage3/container-build-rootfs.sh' \
    "${TEST_ROOT}/docker-args.txt"

echo "Debian Stage 3 static checks passed"
