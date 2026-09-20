#!/usr/bin/env bash
set -euo pipefail

readonly PACKAGE_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PACKAGE_REPO_ROOT="$(cd "${PACKAGE_SCRIPT_DIR}/../.." && pwd)"
source "${PACKAGE_SCRIPT_DIR}/version.sh"
export DEBIAN_APPS_OUTPUT_DIR=${DEBIAN_APPS_OUTPUT_DIR:-${PACKAGE_REPO_ROOT}/output/debian-apps}
export DEBIAN_PACKAGE_OUTPUT_DIR=${DEBIAN_PACKAGE_OUTPUT_DIR:-${PACKAGE_REPO_ROOT}/output/debian-package}
[[ "${DEBIAN_APPS_OUTPUT_DIR}" = /* ]] || DEBIAN_APPS_OUTPUT_DIR=${PACKAGE_REPO_ROOT}/${DEBIAN_APPS_OUTPUT_DIR}
[[ "${DEBIAN_PACKAGE_OUTPUT_DIR}" = /* ]] || DEBIAN_PACKAGE_OUTPUT_DIR=${PACKAGE_REPO_ROOT}/${DEBIAN_PACKAGE_OUTPUT_DIR}
readonly RELEASE_DIR=${DEBIAN_PACKAGE_OUTPUT_DIR}/release

usage() {
    cat <<'EOF'
Usage: scripts/debian-package/release.sh [build|stage|publish]

build    Build/audit applications, package them and stage release assets (Linux amd64 + Docker).
stage    Package already audited apps from the current clean commit and stage release assets.
publish  Verify staged assets and publish a business-vVERSION-REVISION GitHub prerelease.

Defaults: scripts/debian-package/version.sh (0.0.1-2).
Overrides: AIDEN_BUSINESS_VERSION, AIDEN_BUSINESS_REVISION,
DEBIAN_APPS_OUTPUT_DIR, DEBIAN_PACKAGE_OUTPUT_DIR, DEBIAN_PACKAGE_BUILD_IMAGE.
Build uses the same Go/OpenCV cache variables as debian_build.sh.
Publish requires gh authentication and GH_REPO=owner/repository.
No BSP, rootfs, firmware OTA signing key or Agent credentials are needed.
EOF
}

stage_package() {
    python3 "${PACKAGE_SCRIPT_DIR}/release.py" check-source "${PACKAGE_REPO_ROOT}" "${DEBIAN_APPS_OUTPUT_DIR}"
    export DEBIAN_PACKAGE_BUILD_IMAGE=${DEBIAN_PACKAGE_BUILD_IMAGE:-aiden-debian13-armhf-builder:package}
    docker build -t "${DEBIAN_PACKAGE_BUILD_IMAGE}" -f "${PACKAGE_SCRIPT_DIR}/Dockerfile" "${PACKAGE_REPO_ROOT}"
    "${PACKAGE_SCRIPT_DIR}/build.sh"
    python3 "${PACKAGE_SCRIPT_DIR}/release.py" stage "${PACKAGE_REPO_ROOT}" "${DEBIAN_APPS_OUTPUT_DIR}" "${DEBIAN_PACKAGE_OUTPUT_DIR}"
}

build_package() {
    [ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ] || {
        echo 'Run this build on Linux amd64 (for example ssh luhaodev)' >&2
        exit 1
    }
    # Reuse the pinned toolchain installer without invoking the firmware pipeline.
    bash -c 'source "$1/debian_build.sh"; require_command docker; ensure_go_toolchain "${DEBIAN_APPS_GO_ROOT:-$DEFAULT_GO_ROOT}"; ensure_pico_sdk' bash "${PACKAGE_REPO_ROOT}"
    for action in builder opencv apps audit; do
        "${PACKAGE_REPO_ROOT}/scripts/debian-apps/build-apps.sh" "${action}"
    done
    stage_package
}

publish_package() (
    : "${GH_REPO:?Set GH_REPO=owner/repository}"
    local tag commit state tag_commit downloaded
    tag=$(python3 "${PACKAGE_SCRIPT_DIR}/release.py" verify "${RELEASE_DIR}" tag)
    commit=$(python3 "${PACKAGE_SCRIPT_DIR}/release.py" verify "${RELEASE_DIR}" source_commit)
    state=$(gh release view "${tag}" --repo "${GH_REPO}" --json isDraft --jq '.isDraft' 2>/dev/null || true)
    if [ "${state}" = false ]; then
        echo "Release ${tag} is already published; increment the package revision" >&2
        exit 1
    fi
    # GitHub may defer creating a draft release's tag until publication.
    # Materialize and verify it first so retries cannot publish another commit.
    tag_commit=$(gh api "repos/${GH_REPO}/commits/${tag}" --jq '.sha' 2>/dev/null || true)
    if [ -z "${tag_commit}" ]; then
        gh api --method POST "repos/${GH_REPO}/git/refs" \
            -f "ref=refs/tags/${tag}" -f "sha=${commit}" >/dev/null
        tag_commit=$(gh api "repos/${GH_REPO}/commits/${tag}" --jq '.sha')
    fi
    [ "${tag_commit}" = "${commit}" ] || {
        echo "Release tag ${tag} does not point to the built source commit" >&2
        exit 1
    }
    if [ "${state}" != true ]; then
        gh release create "${tag}" --repo "${GH_REPO}" --target "${commit}" \
            --title "Aiden business ${tag#business-v}" --notes-file "${RELEASE_DIR}/RELEASE-NOTES.md" \
            --draft --prerelease --latest=false
    fi
    # Existing drafts are retryable, but never overwrite a published release.
    gh release upload "${tag}" "${RELEASE_DIR}/"*.deb "${RELEASE_DIR}/RELEASE-NOTES.md" \
        "${RELEASE_DIR}/release-manifest.json" "${RELEASE_DIR}/build-metadata.json" \
        "${RELEASE_DIR}/SHA256SUMS" --repo "${GH_REPO}" --clobber
    downloaded=$(mktemp -d)
    trap 'rm -rf "${downloaded}"' EXIT
    gh release download "${tag}" --repo "${GH_REPO}" --dir "${downloaded}"
    python3 "${PACKAGE_SCRIPT_DIR}/release.py" verify "${downloaded}"
    cmp "${RELEASE_DIR}/SHA256SUMS" "${downloaded}/SHA256SUMS"
    gh release edit "${tag}" --repo "${GH_REPO}" --draft=false --prerelease --latest=false \
        --notes-file "${RELEASE_DIR}/RELEASE-NOTES.md"
)

case "${1:-build}" in
    build) build_package ;;
    stage) stage_package ;;
    publish) publish_package ;;
    -h|--help|help) usage ;;
    *) usage >&2; exit 2 ;;
esac
