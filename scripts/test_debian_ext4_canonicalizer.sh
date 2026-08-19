#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly CANONICALIZER=${REPO_ROOT}/scripts/debian-stage3/canonicalize-ext4.py
readonly ROOTFS_BUILDER=${REPO_ROOT}/scripts/debian-stage3/container-build-rootfs.sh
readonly TEST_ROOT=$(mktemp -d)
readonly BUILD_EPOCH=1767360516
readonly ROOTFS_UUID=1d29a2d4-5488-4bea-a648-bf133c4b53d3
readonly HASH_SEED=38c67104-9876-4b7a-92fd-4305316be322
trap 'rm -rf "${TEST_ROOT}"' EXIT

for command in mkfs.ext4 python3 sha256sum; do
    command -v "${command}" >/dev/null 2>&1 || exit 77
done

grep -Fq "tar --zstd --acls --xattrs --xattrs-include='*' --numeric-owner" \
    "${ROOTFS_BUILDER}"

make_source() {
    local path=$1
    local atime=$2
    install -d -m 0755 "${path}/directory"
    printf 'canonical ext4\n' >"${path}/directory/file"
    chmod 0640 "${path}/directory/file"
    ln "${path}/directory/file" "${path}/directory/hardlink"
    ln -s file "${path}/directory/symlink"
    touch -a -d "@${atime}" "${path}/directory/file"
    touch -m -d "@${BUILD_EPOCH}" "${path}/directory/file"
    touch -h -d "@${BUILD_EPOCH}" "${path}/directory" "${path}/directory/symlink"
}

make_image() {
    local source_dir=$1
    local image=$2
    truncate -s 96M "${image}"
    SOURCE_DATE_EPOCH=${BUILD_EPOCH} mkfs.ext4 -q -F -b 4096 -L rootfs \
        -U "${ROOTFS_UUID}" -m 1 \
        -E "lazy_itable_init=0,lazy_journal_init=0,hash_seed=${HASH_SEED},root_owner=0:0" \
        -O '^64bit,^huge_file,^metadata_csum,^dir_index,^quota' \
        -d "${source_dir}" "${image}"
    PYTHONDONTWRITEBYTECODE=1 python3 "${CANONICALIZER}" "${image}" "${BUILD_EPOCH}"
}

make_source "${TEST_ROOT}/source-a" 100
make_source "${TEST_ROOT}/source-b" 200
make_image "${TEST_ROOT}/source-a" "${TEST_ROOT}/a.ext4"
make_image "${TEST_ROOT}/source-b" "${TEST_ROOT}/b.ext4"
cmp "${TEST_ROOT}/a.ext4" "${TEST_ROOT}/b.ext4"

if command -v e2fsck >/dev/null 2>&1; then
    e2fsck -fn "${TEST_ROOT}/a.ext4" >/dev/null
fi

echo "Debian ext4 canonicalizer tests passed"
