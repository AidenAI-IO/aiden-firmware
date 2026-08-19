#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly DEFAULT_OUTPUT_DIR=${REPO_ROOT}/output/debian-stage2
if [ -n "${DEBIAN_STAGE2_OUTPUT_DIR:-}" ]; then
    if [[ "${DEBIAN_STAGE2_OUTPUT_DIR}" = /* ]]; then
        OUTPUT_DIR=${DEBIAN_STAGE2_OUTPUT_DIR}
    else
        OUTPUT_DIR=${REPO_ROOT}/${DEBIAN_STAGE2_OUTPUT_DIR}
    fi
else
    OUTPUT_DIR=${DEFAULT_OUTPUT_DIR}
fi
readonly OUTPUT_DIR
readonly BUILD_IMAGE=${DEBIAN_STAGE2_BUILD_IMAGE:-aiden-debian13-armhf-builder:stage2}
readonly OPENCV_ARCHIVE=opencv-mobile-4.13.0.zip
readonly OPENCV_URL=https://github.com/nihui/opencv-mobile/releases/download/v35/${OPENCV_ARCHIVE}
readonly OPENCV_SHA256=9304482980b3e4ff1050a8527cdb5777fadf8c5dd9c1a8620170d23e252fb150
readonly JOBS=${RK_JOBS:-$(getconf _NPROCESSORS_ONLN)}
readonly GO_ROOT=${DEBIAN_STAGE2_GO_ROOT:-${REPO_ROOT}/.toolchains/go1.26.0.linux-amd64}
readonly GO_BUILD_CACHE=${DEBIAN_STAGE2_GO_BUILD_CACHE:-${REPO_ROOT}/.cache/debian-stage2/go-build}
readonly GO_MODULE_CACHE=${DEBIAN_STAGE2_GO_MODULE_CACHE:-${REPO_ROOT}/.cache/debian-stage2/go-mod}
readonly BUILD_EPOCH=${SOURCE_DATE_EPOCH:-1767360516}

usage() {
    cat <<'EOF'
Usage: scripts/debian-stage2/build-apps.sh [all|builder|opencv|apps|audit]

Environment:
  DEBIAN_STAGE2_OUTPUT_DIR  Output directory (defaults to output/debian-stage2).
  DEBIAN_STAGE2_BUILD_IMAGE Docker image name for the Debian armhf toolchain.
  DEBIAN_STAGE2_GO_ROOT     Pinned Go 1.26.0 linux/amd64 toolchain.
  DEBIAN_STAGE2_GO_BUILD_CACHE/DEBIAN_STAGE2_GO_MODULE_CACHE
                             Persistent writable Go caches.
  SOURCE_DATE_EPOCH         Reproducible Go build timestamp.
  RK_JOBS                   Parallel build jobs (defaults to all host CPUs).
EOF
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || {
        echo "Required command not found: $1" >&2
        exit 1
    }
}

ensure_opencv_source() {
    local cache_dir=${OUTPUT_DIR}/cache
    local archive=${cache_dir}/${OPENCV_ARCHIVE}
    local actual
    mkdir -p "${cache_dir}"
    if [ ! -f "${archive}" ]; then
        local temp=${archive}.tmp.$$
        curl -fL --retry 3 --connect-timeout 20 -o "${temp}" "${OPENCV_URL}"
        mv "${temp}" "${archive}"
    fi
    actual=$(sha256sum "${archive}" | awk '{print $1}')
    if [ "${actual}" != "${OPENCV_SHA256}" ]; then
        echo "OpenCV-Mobile source checksum mismatch" >&2
        echo "expected: ${OPENCV_SHA256}" >&2
        echo "actual:   ${actual}" >&2
        exit 1
    fi
}

run_builder() {
    docker build -t "${BUILD_IMAGE}" -f "${SCRIPT_DIR}/Dockerfile" "${REPO_ROOT}"
    docker image inspect "${BUILD_IMAGE}" --format '{{.Id}}' \
        >"${OUTPUT_DIR}/builder-image-id.txt"
}

run_container_script() {
    local script=$1
    local image_id
    shift
    image_id=$(docker image inspect "${BUILD_IMAGE}" --format '{{.Id}}')
    if [ ! -x "${GO_ROOT}/bin/go" ] || [ ! -f "${GO_ROOT}/VERSION" ] ||
        ! grep -qx 'go1.26.0' "${GO_ROOT}/VERSION"; then
        echo "Pinned Go 1.26.0 toolchain is missing: ${GO_ROOT}" >&2
        exit 1
    fi
    case "${BUILD_EPOCH}" in
    '' | *[!0-9]*)
        echo "SOURCE_DATE_EPOCH must be an unsigned Unix timestamp: ${BUILD_EPOCH}" >&2
        exit 1
        ;;
    esac
    mkdir -p "${GO_BUILD_CACHE}" "${GO_MODULE_CACHE}"
    docker run --rm \
        -u "$(id -u):$(id -g)" \
        -e "DEBIAN_STAGE2_OUTPUT_DIR=/out" \
        -e "DEBIAN_STAGE2_BUILD_IMAGE_ID=${image_id}" \
        -e "RK_JOBS=${JOBS}" \
        -e "SOURCE_DATE_EPOCH=${BUILD_EPOCH}" \
        -v "${REPO_ROOT}:/work" \
        -v "${OUTPUT_DIR}:/out" \
        -v "${GO_ROOT}:/usr/local/go:ro" \
        -v "${GO_BUILD_CACHE}:/go-build-cache" \
        -v "${GO_MODULE_CACHE}:/go-mod-cache" \
        -w /work \
        "${BUILD_IMAGE}" \
        bash "${script}" "$@"
}

run_opencv() {
    ensure_opencv_source
    run_container_script scripts/debian-stage2/container-build-opencv-mobile.sh
}

run_apps() {
    run_container_script scripts/debian-stage2/container-build-apps.sh
}

run_audit() {
    run_container_script scripts/debian-stage2/audit-apps.sh \
        /out/apps /out/apps-audit
}

main() {
    local action=${1:-all}
    case "${action}" in
    -h | --help | help)
        usage
        return
        ;;
    all | builder | opencv | apps | audit)
        ;;
    *)
        usage >&2
        exit 2
        ;;
    esac

    require_command docker
    require_command sha256sum
    mkdir -p "${OUTPUT_DIR}"

    case "${action}" in
    all)
        require_command curl
        run_builder
        run_opencv
        run_apps
        run_audit
        ;;
    builder)
        run_builder
        ;;
    opencv)
        require_command curl
        run_opencv
        ;;
    apps)
        run_apps
        ;;
    audit)
        run_audit
        ;;
    esac
}

main "$@"
