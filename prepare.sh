#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_DIR="$SCRIPT_DIR"
PICO_SDK_DIR="$WORKSPACE_DIR/pico-sdk"
OTA_CI_DIR="$WORKSPACE_DIR/.ota-ci"
PUBLIC_KEY_HOST_PATH="$OTA_CI_DIR/ota_ed25519_public_key.pem"
PUBLIC_KEY_CONTAINER_PATH="/home/.ota-ci/ota_ed25519_public_key.pem"
ENV_FILE="$OTA_CI_DIR/env.sh"
GO_MOD_PATH="$WORKSPACE_DIR/src/agent/go.mod"
TEMP_PRIVATE_KEY=""
PRINT_ENV=0
FREE_DISK_SPACE=0
PRIVATE_KEY_ARG=""
REF_NAME_OVERRIDE=""
REF_NAME=""
RELEASE_NAME=""
TAG_NAME=""
BUILD_TIME=""
CHANNEL=""

usage() {
    cat >&2 <<'EOF'
Usage:
  ./prepare.sh [--print-env] [--free-disk-space] [--ref-name <name>] [ota_ed25519_private_key.pem]

This script mirrors the local equivalents of the GitHub Actions steps that run
before `./build_image.sh`.

Input:
  - pass an Ed25519 private key PEM path as the final argument, or
  - set OTA_ED25519_PRIVATE_KEY to the full PEM contents, matching CI.

Output:
  - .ota-ci/ota_ed25519_public_key.pem
  - .ota-ci/env.sh

Options:
  --print-env        print the generated export lines to stdout
  --free-disk-space  run the hosted-runner disk cleanup commands from CI
  --ref-name <name>  override the branch/ref name used for release channel generation
EOF
}

cleanup() {
    if [ -n "$TEMP_PRIVATE_KEY" ] && [ -f "$TEMP_PRIVATE_KEY" ]; then
        rm -f "$TEMP_PRIVATE_KEY"
    fi
}

fail() {
    echo "$*" >&2
    exit 1
}

log_step() {
    echo "[prepare] $*" >&2
}

trap cleanup EXIT

while [ "$#" -gt 0 ]; do
    case "$1" in
        -h|--help)
            usage
            exit 0
            ;;
        --print-env)
            PRINT_ENV=1
            ;;
        --free-disk-space)
            FREE_DISK_SPACE=1
            ;;
        --ref-name)
            shift
            [ "$#" -gt 0 ] || fail "missing value for --ref-name"
            REF_NAME_OVERRIDE="$1"
            ;;
        -*)
            usage
            fail "unknown option: $1"
            ;;
        *)
            if [ -n "$PRIVATE_KEY_ARG" ]; then
                usage
                fail "prepare.sh accepts at most one private key path argument"
            fi
            PRIVATE_KEY_ARG="$1"
            ;;
    esac
    shift
done

if [ -n "$PRIVATE_KEY_ARG" ] && [ -n "${OTA_ED25519_PRIVATE_KEY:-}" ]; then
    fail "provide either OTA_ED25519_PRIVATE_KEY or a private key path, not both"
fi

reclaim_workspace_ownership() {
    local owner mismatched_path

    owner="$(id -u):$(id -g)"
    mismatched_path="$(find "$WORKSPACE_DIR" \( ! -uid "$(id -u)" -o ! -gid "$(id -g)" \) -print -quit 2>/dev/null || true)"
    if [ -n "$mismatched_path" ]; then
        log_step "Reclaiming workspace ownership for local checkout"
        if command -v sudo >/dev/null 2>&1; then
            sudo chown -R "$owner" "$WORKSPACE_DIR"
        elif [ "$(id -u)" -eq 0 ]; then
            chown -R "$owner" "$WORKSPACE_DIR"
        else
            echo "Warning: skipping workspace ownership reclaim because sudo is unavailable and the current user is not root" >&2
        fi
    else
        log_step "Workspace ownership already matches $(id -u):$(id -g)"
    fi

    if [ -d "$WORKSPACE_DIR/build/.cache/go-mod" ]; then
        chmod -R u+w "$WORKSPACE_DIR/build/.cache/go-mod" || \
            echo "Warning: skipping stale Go module cache unlock because chmod failed" >&2
    fi
}

remove_unusable_pico_sdk_submodule_checkout() {
    local sdk_dir sdk_git_dir

    sdk_dir="$PICO_SDK_DIR"
    sdk_git_dir="$WORKSPACE_DIR/.git/modules/pico-sdk"
    if [ ! -e "$sdk_dir" ] && [ ! -e "$sdk_git_dir" ]; then
        return 0
    fi

    if [ -d "$sdk_dir" ] && git -C "$sdk_dir" rev-parse --verify HEAD >/dev/null 2>&1; then
        return 0
    fi

    log_step "Removing unusable pico-sdk submodule checkout"
    rm -rf -- "$sdk_dir" "$sdk_git_dir"
}

repair_stale_pico_sdk_submodule_metadata() {
    local sdk_dir

    sdk_dir="$PICO_SDK_DIR"
    if [ ! -d "$sdk_dir" ]; then
        return 0
    fi

    if git -C "$sdk_dir" ls-files -s sysdrv/tools/board/ubuntu 2>/dev/null | grep -q '^160000 '; then
        log_step "Repairing stale pico-sdk submodule metadata"
        git -C "$sdk_dir" config -f .gitmodules submodule.ubuntu.path sysdrv/tools/board/ubuntu
        git -C "$sdk_dir" config -f .gitmodules submodule.ubuntu.url https://github.com/luckfox-eng33/pico_ubuntu.git
    fi
}

fetch_pico_sdk_submodule() {
    log_step "Fetching pico-sdk submodule"
    git -C "$WORKSPACE_DIR" submodule update --init --depth=1 pico-sdk >&2
    git -C "$PICO_SDK_DIR" clean -f -- .gitmodules >/dev/null
}

verify_reproducible_rootfs_policy() {
    log_step "Verifying reproducible rootfs policy"
    bash "$WORKSPACE_DIR/scripts/test_reproducible_rootfs_policy.sh" >&2
}

version_ge() {
    local current required

    current="$1"
    required="$2"
    [ "$(printf '%s\n%s\n' "$required" "$current" | sort -V | head -n 1)" = "$required" ]
}

validate_local_go_toolchain() {
    local required_go installed_go host_goroot host_goos host_goarch

    [ -f "$GO_MOD_PATH" ] || fail "missing Go version file: $GO_MOD_PATH"
    required_go="$(sed -n 's/^go //p' "$GO_MOD_PATH" | head -n 1)"
    [ -n "$required_go" ] || fail "failed to parse required Go version from $GO_MOD_PATH"

    command -v go >/dev/null 2>&1 || fail "go is required locally to match the workflow setup-go step"
    installed_go="$(go env GOVERSION 2>/dev/null || true)"
    if [ -z "$installed_go" ]; then
        installed_go="$(go version | awk '{print $3}')"
    fi
    installed_go="${installed_go#go}"
    [ -n "$installed_go" ] || fail "failed to determine installed Go version"

    if ! version_ge "$installed_go" "$required_go"; then
        fail "installed Go $installed_go is older than required Go $required_go"
    fi

    host_goroot="$(go env GOROOT)"
    host_goos="$(go env GOHOSTOS)"
    host_goarch="$(go env GOHOSTARCH)"
    [ -d "$host_goroot" ] || fail "go env GOROOT does not exist: $host_goroot"

    log_step "Using Go $installed_go from $host_goroot"
    if [ "$host_goos" != "linux" ] || [ "$host_goarch" != "amd64" ]; then
        echo "Warning: host Go toolchain is $host_goos/$host_goarch; build_image.sh will rely on Go already present in the Docker image" >&2
    fi
}

prepare_ota_public_key() {
    local private_key_path

    private_key_path=""
    if [ -n "$PRIVATE_KEY_ARG" ]; then
        private_key_path="$PRIVATE_KEY_ARG"
        [ -f "$private_key_path" ] || fail "OTA private key not found: $private_key_path"
    elif [ -n "${OTA_ED25519_PRIVATE_KEY:-}" ]; then
        mkdir -p "$OTA_CI_DIR"
        TEMP_PRIVATE_KEY="$(mktemp "$OTA_CI_DIR/ota_ed25519_private_key.XXXXXX.pem")"
        printf '%s\n' "${OTA_ED25519_PRIVATE_KEY}" > "$TEMP_PRIVATE_KEY"
        chmod 600 "$TEMP_PRIVATE_KEY"
        private_key_path="$TEMP_PRIVATE_KEY"
    else
        fail "set OTA_ED25519_PRIVATE_KEY or pass an Ed25519 private key path"
    fi

    mkdir -p "$OTA_CI_DIR"
    log_step "Preparing OTA public key"
    openssl pkey -in "$private_key_path" -pubout -out "$PUBLIC_KEY_HOST_PATH"
    "$WORKSPACE_DIR/scripts/validate_ota_pubkey.sh" "$PUBLIC_KEY_HOST_PATH"
}

clean_previous_build_artifacts() {
    log_step "Cleaning previous build artifacts"
    rm -rf \
        "$PICO_SDK_DIR"/output/image/*.img \
        "$PICO_SDK_DIR"/output/image/*.img.tar.gz \
        "$PICO_SDK_DIR"/output/image/manifest.json
}

sanitize_branch_name() {
    printf '%s' "$1" | sed 's/\//-/g' | sed 's/[^a-zA-Z0-9._-]/-/g'
}

resolve_ref_name() {
    if [ -n "$REF_NAME_OVERRIDE" ]; then
        REF_NAME="$REF_NAME_OVERRIDE"
        return 0
    fi

    if [ -n "${GITHUB_REF_NAME:-}" ]; then
        REF_NAME="$GITHUB_REF_NAME"
        return 0
    fi

    if REF_NAME="$(git -C "$WORKSPACE_DIR" symbolic-ref --quiet --short HEAD 2>/dev/null)"; then
        return 0
    fi

    REF_NAME="detached-$(git -C "$WORKSPACE_DIR" rev-parse --short HEAD)"
}

generate_release_info() {
    local commit_hash timestamp branch_name

    resolve_ref_name
    commit_hash="$(git -C "$WORKSPACE_DIR" rev-parse --short HEAD)"
    timestamp="$(date -u +"%Y%m%d-%H%M%S")"
    BUILD_TIME="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
    RELEASE_NAME="${timestamp}-${commit_hash}"
    TAG_NAME="$RELEASE_NAME"

    if [ "$REF_NAME" = "main" ]; then
        CHANNEL="stable"
    else
        branch_name="$(sanitize_branch_name "$REF_NAME")"
        CHANNEL="dev-${branch_name}"
    fi

    log_step "Generated release metadata: release_name=$RELEASE_NAME channel=$CHANNEL ref_name=$REF_NAME"
}

free_hosted_runner_disk_space() {
    if [ "$FREE_DISK_SPACE" -ne 1 ]; then
        return 0
    fi

    log_step "Running hosted-runner disk cleanup"
    df -h >&2
    if command -v sudo >/dev/null 2>&1; then
        sudo rm -rf /usr/local/lib/android /usr/share/dotnet /opt/ghc /usr/local/share/boost
        sudo rm -rf /opt/hostedtoolcache/CodeQL
        sudo apt-get clean >/dev/null
    elif [ "$(id -u)" -eq 0 ]; then
        rm -rf /usr/local/lib/android /usr/share/dotnet /opt/ghc /usr/local/share/boost
        rm -rf /opt/hostedtoolcache/CodeQL
        apt-get clean >/dev/null
    else
        fail "--free-disk-space requires sudo or root"
    fi
    docker system prune -af >&2 || true
    df -h >&2
}

write_env_file() {
    cat > "$ENV_FILE" <<EOF
export OTA_PUBLIC_KEY_PATH="$PUBLIC_KEY_CONTAINER_PATH"
export GITHUB_REF_NAME="$REF_NAME"
export AIDEN_RELEASE_NAME="$RELEASE_NAME"
export AIDEN_TAG_NAME="$TAG_NAME"
export AIDEN_BUILD_TIME="$BUILD_TIME"
export AIDEN_CHANNEL="$CHANNEL"
export RELEASE_NAME="$RELEASE_NAME"
export TAG_NAME="$TAG_NAME"
export BUILD_TIME="$BUILD_TIME"
export CHANNEL="$CHANNEL"
EOF
}

log_step "Preparing local environment for build_image.sh"
reclaim_workspace_ownership
remove_unusable_pico_sdk_submodule_checkout
repair_stale_pico_sdk_submodule_metadata
log_step "Using current local checkout (skipping actions/checkout)"
fetch_pico_sdk_submodule
verify_reproducible_rootfs_policy
free_hosted_runner_disk_space
validate_local_go_toolchain
prepare_ota_public_key
clean_previous_build_artifacts
generate_release_info
write_env_file

log_step "Prepared OTA public key at $PUBLIC_KEY_HOST_PATH"
log_step "Wrote environment file $ENV_FILE"
if [ "$PRINT_ENV" -eq 1 ]; then
    cat "$ENV_FILE"
else
    echo "Run: source \"$ENV_FILE\"" >&2
fi
