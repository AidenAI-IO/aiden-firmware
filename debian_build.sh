#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
REPO_ROOT=${SCRIPT_DIR}
readonly REPO_ROOT
readonly STAGE2_OUTPUT=${REPO_ROOT}/output/debian-stage2
readonly STAGE3_OUTPUT=${REPO_ROOT}/output/debian-stage3
readonly FINAL_OUTPUT=${REPO_ROOT}/output/debian/image
readonly DEFAULT_AGENT_CONFIG=/home/miaomiao/dev/luckfox/config/agent.toml
readonly DEFAULT_OTA_PRIVATE_KEY=${REPO_ROOT}/key/id_25519.pem
readonly DEFAULT_OTA_PUBLIC_KEY=${REPO_ROOT}/key/id_25519.pub.pem
readonly DEFAULT_GO_ROOT=${REPO_ROOT}/.toolchains/go1.26.0.linux-amd64
readonly GO_VERSION=1.26.0
readonly GO_DIST=linux-amd64
readonly GO_TARBALL=go${GO_VERSION}.${GO_DIST}.tar.gz
readonly GO_TARBALL_SHA256=aac1b08a0fb0c4e0a7c1555beb7b59180b05dfc5a3d62e40e9de90cd42f88235
readonly DEFAULT_SOURCE_DATE_EPOCH=1767360516
readonly EXPECTED_PICO_SDK_COMMIT=a290a4345685e3c711d86ed78a39579e1e735328
readonly CLEANUP_IMAGE=debian:trixie-slim@sha256:3a39a0592364683e6bab97937b72cad5a8fa6dcbbee90edb3bb48c7f8e94f258
readonly -a MANIFEST_IMAGE_ASSETS=(boot_a.img boot_b.img oem.img rootfs.img)
readonly -a RELEASE_IMAGE_ASSETS=(boot_a.img boot_b.img oem.img rootfs.img update.img)
readonly -a LOCAL_RELEASE_OUTPUT_ASSETS=(
    boot_a.img.tar.gz
    boot_b.img.tar.gz
    manifest.json
    oem.img.tar.gz
    rootfs.img.tar.gz
    update.img.tar.gz
)
TEMPORARY_DIR=

usage() {
    cat <<'EOF'
Usage: ./debian_build.sh

Build the complete Debian armhf factory firmware from clean Stage 2/Stage 3
outputs, create a locally signed OTA manifest/config, audit the image set, and
write the local firmware/OTA artifacts to output/debian/image:

  update.img
  boot_a.img.tar.gz
  boot_b.img.tar.gz
  manifest.json
  oem.img.tar.gz
  rootfs.img.tar.gz
  update.img.tar.gz

The directly flashable image remains output/debian/image/update.img.

Environment overrides:
  RK_JOBS                   Requested parallel jobs (default: 24, capped at
                            the number of online CPUs).
  AGENT_CONFIG_PATH         External agent.toml (default:
                            /home/miaomiao/dev/luckfox/config/agent.toml).
  OTA_PRIVATE_KEY_PATH      Ed25519 private PEM (default: key/id_25519.pem).
  OTA_PUBLIC_KEY_PATH       Ed25519 public PEM (default: key/id_25519.pub.pem).
  DEBIAN_STAGE2_GO_ROOT     Go 1.26.0 linux/amd64 toolchain (default:
                            .toolchains/go1.26.0.linux-amd64).
  OTA_REPO                  Local factory config repository label (default:
                            AidenAI-IO/aiden-firmware).
  OTA_CHANNEL               Local manifest channel (default: local).
  OTA_BUILD_VERSION         Local manifest version (default: timestamp + Git SHA).
  OTA_BUILD_TIME            RFC3339 manifest time (default: current UTC time).
  SOURCE_DATE_EPOCH         Reproducible archive timestamp (default: 1767360516).

The build removes only these generated directories before starting:
  output/debian-stage2
  output/debian-stage3
  output/debian

It does not publish a GitHub Release and does not copy the external agent.toml
into the source tree.
EOF
}

die() {
    printf 'debian_build.sh: %s\n' "$*" >&2
    exit 1
}

log() {
    printf '\n[%s] %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"
}

cleanup() {
    [ -z "${TEMPORARY_DIR}" ] || rm -rf "${TEMPORARY_DIR}"
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "required command is missing: $1"
}

positive_integer() {
    case "$1" in
        '' | *[!0-9]* | 0) return 1 ;;
        *) return 0 ;;
    esac
}

choose_job_count() {
    local requested=$1
    local available=$2
    positive_integer "${requested}" || die "RK_JOBS must be a positive integer: ${requested}"
    positive_integer "${available}" || die "invalid online CPU count: ${available}"
    if [ "${requested}" -gt "${available}" ]; then
        printf '%s\n' "${available}"
    else
        printf '%s\n' "${requested}"
    fi
}

online_cpu_count() {
    local count
    count=$(getconf _NPROCESSORS_ONLN 2>/dev/null || true)
    if ! positive_integer "${count}" && command -v nproc >/dev/null 2>&1; then
        count=$(nproc)
    fi
    positive_integer "${count}" || die "unable to determine the number of online CPUs"
    printf '%s\n' "${count}"
}

sha256_file() {
    sha256sum "$1" | awk '{print $1}'
}

ensure_go_toolchain() {
    local go_root=$1
    local version_file=${go_root}/VERSION
    local go_binary=${go_root}/bin/go
    local toolchain_cache=${REPO_ROOT}/.toolchains
    local tarball_path=${toolchain_cache}/${GO_TARBALL}
    local downloaded=${tarball_path}.tmp.$$
    local extract_dir=${toolchain_cache}/.go${GO_VERSION}.${GO_DIST}.$$

    if [ -x "${go_binary}" ] && [ -f "${version_file}" ] &&
        grep -qx "go${GO_VERSION}" "${version_file}"; then
        [ "$("${go_binary}" env GOVERSION)" = "go${GO_VERSION}" ] \
            || die "Go VERSION file and executable disagree in ${go_root}"
        [ "$("${go_binary}" env GOHOSTOS)" = linux ] \
            || die "Go toolchain must target a linux build host: ${go_root}"
        [ "$("${go_binary}" env GOHOSTARCH)" = amd64 ] \
            || die "Go toolchain must target an amd64 build host: ${go_root}"
        return
    fi

    if [ "${go_root}" != "${DEFAULT_GO_ROOT}" ]; then
        die "custom DEBIAN_STAGE2_GO_ROOT is not a valid Go 1.26.0 linux/amd64 toolchain: ${go_root}"
    fi

    mkdir -p "${toolchain_cache}"
    if [ ! -f "${tarball_path}" ]; then
        log "Downloading pinned Go ${GO_VERSION} toolchain"
        rm -f "${downloaded}"
        curl -fL --retry 3 --connect-timeout 20 \
            -o "${downloaded}" "https://go.dev/dl/${GO_TARBALL}"
        [ "$(sha256_file "${downloaded}")" = "${GO_TARBALL_SHA256}" ] \
            || die "downloaded Go toolchain checksum mismatch"
        mv "${downloaded}" "${tarball_path}"
    fi
    [ "$(sha256_file "${tarball_path}")" = "${GO_TARBALL_SHA256}" ] \
        || die "cached Go toolchain checksum mismatch: ${tarball_path}"

    rm -rf "${extract_dir}" "${go_root}"
    mkdir -p "${extract_dir}"
    tar -C "${extract_dir}" -xzf "${tarball_path}"
    mv "${extract_dir}/go" "${go_root}"
    rmdir "${extract_dir}"

    [ "$("${go_root}/bin/go" env GOVERSION)" = "go${GO_VERSION}" ] \
        || die "installed Go toolchain has the wrong version"
}

validate_key_pair() {
    local private_key=$1
    local public_key=$2
    local work_dir=$3
    local private_public_der=${work_dir}/private-public.der
    local public_der=${work_dir}/public.der

    [ -f "${private_key}" ] || die "OTA private PEM is missing: ${private_key}"
    [ -f "${public_key}" ] || die "OTA public PEM is missing: ${public_key}"
    openssl pkey -in "${private_key}" -pubout -outform DER \
        -out "${private_public_der}" >/dev/null 2>&1 \
        || die "invalid Ed25519 private PEM: ${private_key}"
    openssl pkey -pubin -in "${public_key}" -outform DER \
        -out "${public_der}" >/dev/null 2>&1 \
        || die "invalid Ed25519 public PEM: ${public_key}"
    cmp -s "${private_public_der}" "${public_der}" \
        || die "OTA public and private PEM files do not form a key pair"
    "${REPO_ROOT}/scripts/validate_ota_pubkey.sh" "${public_key}"
}

validate_pico_sdk() {
    git -C "${REPO_ROOT}" submodule update --init -- pico-sdk
    [ -e "${REPO_ROOT}/pico-sdk/.git" ] || die "pico-sdk submodule is unavailable"
    [ -z "$(git -C "${REPO_ROOT}/pico-sdk" status --porcelain)" ] \
        || die "pico-sdk must be clean before the Debian build"
    local actual_commit
    actual_commit=$(git -C "${REPO_ROOT}/pico-sdk" rev-parse HEAD)
    [ "${actual_commit}" = "${EXPECTED_PICO_SDK_COMMIT}" ] || {
        printf 'expected pico-sdk: %s\nactual pico-sdk:   %s\n' \
            "${EXPECTED_PICO_SDK_COMMIT}" "${actual_commit}" >&2
        die "pico-sdk commit does not match the Stage 3 build contract"
    }
}

clean_generated_outputs() {
    local target resolved_target
    log "Removing previous Debian Stage 2/Stage 3/final outputs"
    mkdir -p "${REPO_ROOT}/output"
    for target in "${STAGE2_OUTPUT}" "${STAGE3_OUTPUT}" "${REPO_ROOT}/output/debian"; do
        [ ! -L "${target}" ] || die "refusing to clean a symlinked output directory: ${target}"
        mkdir -p "${target}"
        resolved_target=$(readlink -f "${target}")
        case "${resolved_target}" in
            "${REPO_ROOT}/output/"*) ;;
            *) die "refusing to clean an output outside the repository: ${resolved_target}" ;;
        esac
        docker run --rm \
            -e "HOST_UID=$(id -u)" \
            -e "HOST_GID=$(id -g)" \
            -v "${resolved_target}:/target" \
            "${CLEANUP_IMAGE}" \
            sh -eu -c '
                find /target -mindepth 1 -delete
                chown "${HOST_UID}:${HOST_GID}" /target
                chmod 0755 /target
            '
    done
}

compress_release_assets() {
    local image_dir=$1
    local assets=$2
    local archive_epoch=$3

    [ -d "${image_dir}" ] || die "release image directory is missing: ${image_dir}"
    case "${archive_epoch}" in
        '' | *[!0-9]*) die "SOURCE_DATE_EPOCH must be an unsigned Unix timestamp: ${archive_epoch}" ;;
    esac
    SOURCE_DATE_EPOCH="${archive_epoch}" \
        "${REPO_ROOT}/scripts/compress_release_images.sh" \
        --image-dir "${image_dir}" \
        --assets "${assets}" \
        --output /dev/null
}

install_local_release_artifacts() {
    local image_dir=$1
    local output_dir=$2
    local archive_epoch=$3
    local artifact

    compress_release_assets \
        "${image_dir}" "${RELEASE_IMAGE_ASSETS[*]}" "${archive_epoch}"
    mkdir -p "${output_dir}"
    [ -s "${image_dir}/update.img" ] || die "Stage 3 did not produce update.img"
    install -m 0644 "${image_dir}/update.img" "${output_dir}/update.img"
    for artifact in "${LOCAL_RELEASE_OUTPUT_ASSETS[@]}"; do
        [ -s "${image_dir}/${artifact}" ] \
            || die "local release artifact is missing: ${image_dir}/${artifact}"
        install -m 0644 "${image_dir}/${artifact}" "${output_dir}/${artifact}"
    done
    (
        cd "${output_dir}"
        sha256sum update.img >update.img.sha256
    )
}

main() {
    case "${1:-}" in
        -h | --help | help)
            usage
            return
            ;;
        '') ;;
        *)
            usage >&2
            exit 2
            ;;
    esac

    local command
    for command in awk cmp curl date docker getconf git grep id install openssl \
        python3 readlink sha256sum tar; do
        require_command "${command}"
    done
    docker info >/dev/null 2>&1 \
        || die "Docker is unavailable; ensure the daemon is running and this user can access it"

    local available_jobs selected_jobs
    available_jobs=$(online_cpu_count)
    selected_jobs=$(choose_job_count "${RK_JOBS:-24}" "${available_jobs}")
    export RK_JOBS=${selected_jobs}
    log "Using RK_JOBS=${RK_JOBS} (${available_jobs} online CPUs)"

    local agent_config=${AGENT_CONFIG_PATH:-${DEFAULT_AGENT_CONFIG}}
    local ota_private_key=${OTA_PRIVATE_KEY_PATH:-${DEFAULT_OTA_PRIVATE_KEY}}
    local ota_public_key=${OTA_PUBLIC_KEY_PATH:-${DEFAULT_OTA_PUBLIC_KEY}}
    local go_root=${DEBIAN_STAGE2_GO_ROOT:-${DEFAULT_GO_ROOT}}
    local ota_repo=${OTA_REPO:-AidenAI-IO/aiden-firmware}
    local ota_channel=${OTA_CHANNEL:-local}
    local ota_build_time=${OTA_BUILD_TIME:-$(date -u '+%Y-%m-%dT%H:%M:%SZ')}
    local stage3_build_image=${DEBIAN_STAGE3_BUILD_IMAGE:-aiden-debian13-armhf-builder:stage3}
    local release_archive_epoch=${SOURCE_DATE_EPOCH:-${DEFAULT_SOURCE_DATE_EPOCH}}
    local git_revision ota_build_version
    git_revision=$(git -C "${REPO_ROOT}" rev-parse --short=12 HEAD)
    ota_build_version=${OTA_BUILD_VERSION:-local-$(date -u '+%Y%m%d-%H%M%S')-${git_revision}}

    [ -f "${agent_config}" ] \
        || die "external Agent configuration is missing: ${agent_config}"
    [ -f "${ota_private_key}" ] || die "OTA private PEM is missing: ${ota_private_key}"
    [ -f "${ota_public_key}" ] || die "OTA public PEM is missing: ${ota_public_key}"
    [ -d "${go_root}" ] || ensure_go_toolchain "${go_root}"
    agent_config=$(readlink -f "${agent_config}")
    ota_private_key=$(readlink -f "${ota_private_key}")
    ota_public_key=$(readlink -f "${ota_public_key}")
    go_root=$(readlink -f "${go_root}")

    TEMPORARY_DIR=$(mktemp -d "${TMPDIR:-/tmp}/aiden-debian-build.XXXXXX")
    trap cleanup EXIT
    validate_key_pair "${ota_private_key}" "${ota_public_key}" "${TEMPORARY_DIR}"
    ensure_go_toolchain "${go_root}"
    validate_pico_sdk
    clean_generated_outputs

    log "Building and auditing Debian Stage 2 applications"
    DEBIAN_STAGE2_OUTPUT_DIR="${STAGE2_OUTPUT}" \
        DEBIAN_STAGE2_GO_ROOT="${go_root}" \
        "${REPO_ROOT}/scripts/debian-stage2/build-apps.sh" all
    grep -qx 'status=pass' "${STAGE2_OUTPUT}/apps-audit/summary.txt" \
        || die "Debian Stage 2 application audit did not pass"

    log "Building Debian Stage 3 rootfs builder"
    DEBIAN_STAGE3_OUTPUT_DIR="${STAGE3_OUTPUT}" \
        DEBIAN_STAGE2_OUTPUT_DIR="${STAGE2_OUTPUT}" \
        "${REPO_ROOT}/scripts/debian-stage3/build.sh" builder

    log "Building Debian Stage 3 rootfs"
    DEBIAN_STAGE3_OUTPUT_DIR="${STAGE3_OUTPUT}" \
        DEBIAN_STAGE2_OUTPUT_DIR="${STAGE2_OUTPUT}" \
        "${REPO_ROOT}/scripts/debian-stage3/build.sh" rootfs

    log "Building Debian Stage 3 BSP and A/B boot images"
    DEBIAN_STAGE3_OUTPUT_DIR="${STAGE3_OUTPUT}" \
        DEBIAN_STAGE2_OUTPUT_DIR="${STAGE2_OUTPUT}" \
        "${REPO_ROOT}/scripts/debian-stage3/build.sh" bsp

    log "Assembling Debian Stage 3 factory images"
    OTA_PUBLIC_KEY_PATH="${ota_public_key}" \
        AGENT_CONFIG_PATH="${agent_config}" \
        DEBIAN_STAGE3_OUTPUT_DIR="${STAGE3_OUTPUT}" \
        DEBIAN_STAGE2_OUTPUT_DIR="${STAGE2_OUTPUT}" \
        "${REPO_ROOT}/scripts/debian-stage3/build.sh" images

    log "Compressing OTA partition assets for the signed manifest"
    compress_release_assets \
        "${STAGE3_OUTPUT}/image" \
        "${MANIFEST_IMAGE_ASSETS[*]}" \
        "${release_archive_epoch}"

    local device_config=${STAGE3_OUTPUT}/debian-ota-config.json
    log "Generating a locally signed OTA manifest"
    docker run --rm \
        -u "$(id -u):$(id -g)" \
        -v "${REPO_ROOT}:/work:ro" \
        -v "${STAGE3_OUTPUT}:/out" \
        -v "${ota_private_key}:/run/secrets/ota_private.pem:ro" \
        -w /work \
        "${stage3_build_image}" \
        bash scripts/generate_ota_manifest.sh \
        --version "${ota_build_version}" \
        --channel "${ota_channel}" \
        --build-time "${ota_build_time}" \
        --sign-key /run/secrets/ota_private.pem \
        --image-dir /out/image \
        --output /out/image/manifest.json

    log "Generating the matching factory OTA configuration"
    docker run --rm \
        -u "$(id -u):$(id -g)" \
        -v "${REPO_ROOT}:/work:ro" \
        -v "${STAGE3_OUTPUT}:/out" \
        -w /work \
        "${stage3_build_image}" \
        bash scripts/generate_ota_device_config.sh \
        --manifest /out/image/manifest.json \
        --repo "${ota_repo}" \
        --channel "${ota_channel}" \
        --output /out/debian-ota-config.json

    log "Installing the OTA configuration and repacking update.img"
    OTA_DEVICE_CONFIG_PATH="${device_config}" \
        DEBIAN_STAGE3_OUTPUT_DIR="${STAGE3_OUTPUT}" \
        DEBIAN_STAGE2_OUTPUT_DIR="${STAGE2_OUTPUT}" \
        "${REPO_ROOT}/scripts/debian-stage3/build.sh" config

    log "Running the final Debian image audit"
    DEBIAN_STAGE3_OUTPUT_DIR="${STAGE3_OUTPUT}" \
        DEBIAN_STAGE2_OUTPUT_DIR="${STAGE2_OUTPUT}" \
        "${REPO_ROOT}/scripts/debian-stage3/build.sh" audit
    grep -qx 'Audit passed' "${STAGE3_OUTPUT}/audit-report.txt" \
        || die "final Debian image audit did not pass"

    log "Installing local firmware and OTA release artifacts"
    install_local_release_artifacts \
        "${STAGE3_OUTPUT}/image" "${FINAL_OUTPUT}" "${release_archive_epoch}"

    log "Debian firmware build completed"
    printf 'Firmware: %s\n' "${FINAL_OUTPUT}/update.img"
    printf 'SHA-256: %s\n' "$(sha256_file "${FINAL_OUTPUT}/update.img")"
    printf 'Release artifacts:\n'
    printf '  %s\n' "${LOCAL_RELEASE_OUTPUT_ASSETS[@]}"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
