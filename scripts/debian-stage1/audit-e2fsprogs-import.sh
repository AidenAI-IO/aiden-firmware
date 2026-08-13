#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 3 ]; then
    echo "Usage: $0 <sdk-dir> <report> <work-dir>" >&2
    exit 2
fi

readonly SDK_DIR=$1
readonly REPORT=$2
readonly WORK_DIR=$3
readonly TOOL_DIR=${SDK_DIR}/sysdrv/tools/pc/e2fsprogs
readonly MKE2FS=${TOOL_DIR}/mke2fs
readonly E2FSCK=${TOOL_DIR}/e2fsck
readonly SOURCE_DIR=${WORK_DIR}/source
readonly MOUNT_DIR=${WORK_DIR}/mount
readonly IMAGE=${WORK_DIR}/import.ext4

mounted=false
cleanup() {
    if ${mounted} && mountpoint -q "${MOUNT_DIR}"; then
        umount "${MOUNT_DIR}"
    fi
    rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

for command in getfacl getfattr getcap setfacl setfattr setcap; do
    command -v "${command}" >/dev/null 2>&1 || {
        echo "Required import-audit command not found: ${command}" >&2
        exit 1
    }
done
for tool in "${MKE2FS}" "${E2FSCK}"; do
    test -x "${tool}" || {
        echo "SDK e2fsprogs tool is missing: ${tool}" >&2
        exit 1
    }
done

rm -rf "${WORK_DIR}"
mkdir -p "${SOURCE_DIR}/setgid-dir" "${MOUNT_DIR}" "$(dirname "${REPORT}")"
printf 'owner\n' >"${SOURCE_DIR}/owner-file"
printf 'setuid\n' >"${SOURCE_DIR}/setuid-file"
printf 'xattr\n' >"${SOURCE_DIR}/xattr-file"
printf 'acl\n' >"${SOURCE_DIR}/acl-file"
printf '#!/bin/sh\nexit 0\n' >"${SOURCE_DIR}/capability-file"
printf 'hardlink\n' >"${SOURCE_DIR}/hardlink-a"
ln "${SOURCE_DIR}/hardlink-a" "${SOURCE_DIR}/hardlink-b"
ln -s owner-file "${SOURCE_DIR}/owner-link"

chown 1234:2345 "${SOURCE_DIR}/owner-file"
chmod 4750 "${SOURCE_DIR}/setuid-file"
chmod 2770 "${SOURCE_DIR}/setgid-dir"
chmod 0755 "${SOURCE_DIR}/capability-file"
setfattr -n user.luckfox-stage1 -v xattr-preserved "${SOURCE_DIR}/xattr-file"
setfacl -m u:1234:r-- "${SOURCE_DIR}/acl-file"
setcap cap_net_bind_service=ep "${SOURCE_DIR}/capability-file"

truncate -s 32M "${IMAGE}"
"${MKE2FS}" -F -t ext4 -L import-audit -m 0 \
    -E lazy_itable_init=0,lazy_journal_init=0 \
    -O '^64bit,^huge_file,^metadata_csum,^dir_index,^quota' \
    -d "${SOURCE_DIR}" "${IMAGE}" >/dev/null
"${E2FSCK}" -fn "${IMAGE}" >/dev/null

mount -o loop,ro "${IMAGE}" "${MOUNT_DIR}"
mounted=true

declare -a results=()
record() {
    local name=$1
    local expected=$2
    local actual=$3
    local status=FAIL
    if [ "${actual}" = "${expected}" ]; then
        status=PASS
    fi
    results+=("${name}\t${status}\t${expected}\t${actual}")
}

record content owner "$(tr -d '\n' <"${MOUNT_DIR}/owner-file")"
record uid_gid 1234:2345 "$(stat -c '%u:%g' "${MOUNT_DIR}/owner-file")"
record setuid_mode 4750 "$(stat -c '%a' "${MOUNT_DIR}/setuid-file")"
record setgid_mode 2770 "$(stat -c '%a' "${MOUNT_DIR}/setgid-dir")"
record user_xattr xattr-preserved \
    "$(getfattr --only-values -n user.luckfox-stage1 "${MOUNT_DIR}/xattr-file" 2>/dev/null || true)"
record posix_acl user:1234:r-- \
    "$(getfacl -cp "${MOUNT_DIR}/acl-file" 2>/dev/null | sed -n 's/^user:1234:/user:1234:/p' || true)"
record capability cap_net_bind_service=ep \
    "$(getcap -n "${MOUNT_DIR}/capability-file" 2>/dev/null | awk '{print $NF}' || true)"
record hardlink_inode \
    "$(stat -c '%i' "${MOUNT_DIR}/hardlink-a")" \
    "$(stat -c '%i' "${MOUNT_DIR}/hardlink-b")"
record hardlink_count 2 "$(stat -c '%h' "${MOUNT_DIR}/hardlink-a")"
record symlink_target owner-file "$(readlink "${MOUNT_DIR}/owner-link")"

failures=0
for result in "${results[@]}"; do
    if [[ "${result}" == *$'\tFAIL\t'* ]]; then
        failures=$((failures + 1))
    fi
done

{
    echo "Luckfox SDK e2fsprogs 1.43.9 import audit"
    echo
    printf 'mke2fs_version\t%s\n' "$("${MKE2FS}" -V 2>&1 | head -1)"
    printf 'mke2fs_sha256\t%s\n' "$(sha256sum "${MKE2FS}" | awk '{print $1}')"
    printf 'e2fsck_version\t%s\n' "$("${E2FSCK}" -V 2>&1 | head -1)"
    printf 'e2fsck_sha256\t%s\n' "$(sha256sum "${E2FSCK}" | awk '{print $1}')"
    printf 'test_image_sha256\t%s\n' "$(sha256sum "${IMAGE}" | awk '{print $1}')"
    echo
    printf 'property\tstatus\texpected\tactual\n'
    printf '%b\n' "${results[@]}"
    echo
    if [ "${failures}" -eq 0 ]; then
        echo 'sdk_mke2fs_import_result=complete-for-tested-properties'
    else
        echo 'sdk_mke2fs_import_result=incomplete-for-tested-properties'
    fi
    echo 'stage1_selected_importer=privileged-mount-plus-rsync-aHAX-numeric-ids'
    echo 'stage1_reason=the selected importer is independently audited and does not depend on SDK mke2fs -d preserving every Debian attribute'
} >"${REPORT}"
