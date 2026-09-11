#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

fail() {
    echo "Debian-only policy failure: $*" >&2
    exit 1
}

for path in "${REPO_ROOT}/debian_build.sh" \
    "${REPO_ROOT}/scripts/debian-stage2/build-apps.sh" \
    "${REPO_ROOT}/scripts/debian-stage3/build.sh"; do
    [ -f "${path}" ] || fail "missing production path: ${path}"
done
grep -Fq 'scripts/debian-stage3/BoardConfig-EMMC-Debian13-RV1106_Luckfox_Pico_Zero-IPC.mk' \
    "${REPO_ROOT}/scripts/ota_partition_layout.sh" \
    || fail "OTA layout does not default to the Debian board configuration"
if grep -Fq 'pico-sdk/.BoardConfig.mk' \
    "${REPO_ROOT}/scripts/ota_partition_layout.sh"; then
    fail "OTA layout still defaults to the SDK Buildroot board configuration"
fi

if rg --no-ignore -n \
    '\$\{REPO_ROOT\}/overlay/|rv1106-buildroot-uclibc|scripts/build/' \
    "${REPO_ROOT}/debian_build.sh" \
    "${REPO_ROOT}/scripts/debian-stage2" \
    "${REPO_ROOT}/scripts/debian-stage3"; then
    fail "the Debian build depends on a retired repository userspace path"
fi

grep -Fq 'set(AIDEN_TARGET_PLATFORM "rv1106-debian-glibc"' \
    "${REPO_ROOT}/CMakeLists.txt" \
    || fail "CMake does not default to Debian/glibc"
if sed -n '/set_property(CACHE AIDEN_TARGET_PLATFORM PROPERTY STRINGS/,/)/p' \
    "${REPO_ROOT}/CMakeLists.txt" | rg -i 'buildroot|uclibc'; then
    fail "CMake still advertises a retired userspace platform"
fi

mapfile -d '' active_docs < <(
    find "${REPO_ROOT}/README.md" "${REPO_ROOT}/docs/README.md" \
        "${REPO_ROOT}/docs"/[0-9][0-9]-* \
        -type f -name '*.md' -print0
)
if rg --no-ignore -n -i \
    '\./build\.sh (binaries|image)|build_image\.sh|/etc/init\.d/|overlay/(etc|oem|userdata)|pico-sdk/output/image|scripts/build/|/dev/block/by-name' \
    "${active_docs[@]}"; then
    fail "active documentation publishes a retired userspace workflow"
fi

if rg --no-ignore -n -i \
    'GitHub Releases \(Recommended\)|Distribution:[[:space:]]*GitHub Releases|GitHub Actions publishes signed releases' \
    "${active_docs[@]}"; then
    fail "active documentation presents deferred GitHub publication as the current path"
fi

if rg --no-ignore -n \
    '(^|[[:space:]])gh[[:space:]]+release|create_github_release\.sh|\.github/workflows/' \
    "${REPO_ROOT}/debian_build.sh" \
    "${REPO_ROOT}/scripts/debian-stage1" \
    "${REPO_ROOT}/scripts/debian-stage2" \
    "${REPO_ROOT}/scripts/debian-stage3" \
    "${REPO_ROOT}/scripts/debian"; then
    fail "the local Debian production build invokes GitHub publication automation"
fi

echo "Debian-only production policy passed"
