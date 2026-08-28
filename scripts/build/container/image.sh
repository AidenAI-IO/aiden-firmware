#!/usr/bin/env bash
set -euo pipefail

CONTAINER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$CONTAINER_DIR/../../.." && pwd)"

if [ "${AIDEN_BUILD_CONTEXT:-}" != container ]; then
    echo "Run this task through ./build.sh image." >&2
    exit 2
fi

OVERLAY="$REPO_ROOT/overlay"
PICO_SDK="$REPO_ROOT/pico-sdk"
DEST_OVERLAY="$PICO_SDK/project/cfg/BoardConfig_IPC/overlay/overlay-luckfox-buildroot-aiden"
ROOTFS_CLI_MANAGED_STATE="${DEST_OVERLAY}.aiden-rootfs-cli-tools.list"
ROOTFS_CLI_TOOL_CATALOG="$REPO_ROOT/scripts/rootfs_cli_tools.catalog"
ROOTFS_CLI_CATALOG_LIB="$REPO_ROOT/scripts/rootfs_cli_tool_catalog.sh"
ROOTFS_CLI_BUILD_DIR="$REPO_ROOT/build/rootfs-cli-tools"
ROOTFS_CLI_CACHE_DIR="$REPO_ROOT/.cache/rootfs-cli-tools"
AIDEN_BUILD_BIN_DIR="${AIDEN_BUILD_BIN_DIR:-$REPO_ROOT/build/bin}"
GENERATED_BINARY_MANIFEST="$REPO_ROOT/build/aiden-generated-binaries.sha256"

# shellcheck source=/dev/null
source "$ROOTFS_CLI_CATALOG_LIB"
# shellcheck source=lib/generated_binaries.sh
source "$CONTAINER_DIR/lib/generated_binaries.sh"
# shellcheck source=lib/ext4_images.sh
source "$CONTAINER_DIR/lib/ext4_images.sh"

ROOTFS_CLI_NAME_POLICY_RECORDS="$(rootfs_cli_catalog_name_policy_records "$ROOTFS_CLI_TOOL_CATALOG")" || exit 1
ROOTFS_CLI_PRESERVE_TOOLS=()
while IFS='|' read -r tool strip_policy; do
    if [ "$strip_policy" = "preserve" ]; then
        ROOTFS_CLI_PRESERVE_TOOLS+=("$tool")
    fi
done <<< "$ROOTFS_CLI_NAME_POLICY_RECORDS"

if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
    # Keep image metadata stable without relying on every package treating epoch 0
    # as truthy. Callers can still set SOURCE_DATE_EPOCH explicitly, including 0.
    SOURCE_DATE_EPOCH="${AIDEN_REPRODUCIBLE_IMAGE_EPOCH:-1}"
fi
if ! [[ "$SOURCE_DATE_EPOCH" =~ ^[0-9]+$ ]]; then
    echo "SOURCE_DATE_EPOCH must be an unsigned Unix timestamp: $SOURCE_DATE_EPOCH" >&2
    exit 1
fi
export SOURCE_DATE_EPOCH

require_rknnmrt_version() {
    local runtime="$1"
    local version major minor

    version="$(strings "$runtime" | sed -n 's/^librknnmrt version: \([0-9][0-9.]*\).*/\1/p' | head -n 1)"
    if [ -z "$version" ]; then
        echo "  ✗ Error: cannot read RKNN runtime version from $runtime" >&2
        exit 1
    fi
    major="${version%%.*}"
    minor="${version#*.}"
    minor="${minor%%.*}"
    if [ "$major" -lt 2 ] || { [ "$major" -eq 2 ] && [ "$minor" -lt 3 ]; }; then
        echo "  ✗ Error: RKNN runtime $version is too old for silero_vad_rv1106.rknn; use librknnmrt >= 2.3.x" >&2
        exit 1
    fi
}

clean_managed_staging_paths() {
    local base="$1"
    shift
    local rel_path

    mkdir -p "$base"
    for rel_path in "$@"; do
        rm -rf "$base/$rel_path"
    done
}

run_pico_sdk_build() {
    (cd "$PICO_SDK" && ./build.sh "$@")
}

run_pico_sdk_project_build() {
    (cd "$PICO_SDK/project" && ./build.sh "$@")
}

echo "=== Aiden Firmware - Image Builder ==="
echo ""

# Step 1: Build applications with the verified Go toolchain supplied by the container runner.
echo "[1/6] Building applications..."
"$CONTAINER_DIR/binaries.sh"
log_generated_binaries_in_dir "build-bin" "$AIDEN_BUILD_BIN_DIR"
write_generated_binary_manifest "$AIDEN_BUILD_BIN_DIR" "$GENERATED_BINARY_MANIFEST"
check_generated_binaries_against_manifest "build-bin" "$AIDEN_BUILD_BIN_DIR" "$GENERATED_BINARY_MANIFEST" "$AIDEN_BUILD_BIN_DIR"

# Step 2: Prepare overlay directories.
echo "[2/6] Preparing overlay directories..."
mkdir -p "$OVERLAY/oem/usr/bin" "$OVERLAY/oem/usr/lib" "$OVERLAY/oem/etc"
sync_generated_binaries_from_source "$AIDEN_BUILD_BIN_DIR" "$OVERLAY/oem/usr/bin"
echo "  ✓ Binaries copied to overlay/oem/usr/bin"
repair_generated_binaries_from_manifest "overlay-oem-usr-bin" "$AIDEN_BUILD_BIN_DIR" "$OVERLAY/oem/usr/bin" "$GENERATED_BINARY_MANIFEST"

AGENT_TOOLS_DEST="$OVERLAY/userdata/agent_tools"
mkdir -p "$AGENT_TOOLS_DEST"
cp "$REPO_ROOT/scripts/generate_agent_files_report.py" "$AGENT_TOOLS_DEST/"
cp "$REPO_ROOT/scripts/agent_files_template.html" "$AGENT_TOOLS_DEST/"
cp "$REPO_ROOT/scripts/view_agent_files.sh" "$AGENT_TOOLS_DEST/"
chmod +x "$AGENT_TOOLS_DEST/generate_agent_files_report.py" "$AGENT_TOOLS_DEST/view_agent_files.sh"
echo "  ✓ Agent files report tools staged to overlay/userdata/agent_tools"

RKNNMRT_OVERLAY="$OVERLAY/oem/usr/lib/librknnmrt.so"
RKNNMRT_SOURCE="$PICO_SDK/media/iva/iva/librockiva/rockiva-rv1106-Linux/lib/librknnmrt.so"
if [ -f "$RKNNMRT_OVERLAY" ]; then
    echo "  ✓ RKNN runtime already present in overlay/oem/usr/lib"
else
    if [ ! -f "$RKNNMRT_SOURCE" ]; then
        echo "  ✗ Error: RKNN runtime not found: $RKNNMRT_SOURCE"
        exit 1
    fi
    cp "$RKNNMRT_SOURCE" "$RKNNMRT_OVERLAY"
    echo "  ✓ RKNN runtime copied to overlay/oem/usr/lib"
fi
require_rknnmrt_version "$RKNNMRT_OVERLAY"

KEY_SOURCE="${OTA_PUBLIC_KEY_PATH:-}"
if [ -n "$KEY_SOURCE" ]; then
    if [ ! -f "$KEY_SOURCE" ]; then
        echo "  ✗ Error: OTA_PUBLIC_KEY_PATH does not exist: $KEY_SOURCE"
        exit 1
    fi
elif [ -f "$REPO_ROOT/keys/ota_pubkey.pem" ]; then
    if grep -Eiq '^[[:space:]]*(#|-----).*(dev|test|placeholder)' "$REPO_ROOT/keys/ota_pubkey.pem"; then
        echo "  ✗ Error: keys/ota_pubkey.pem is marked dev/test/placeholder; refusing production image"
        exit 1
    fi
    KEY_SOURCE="$REPO_ROOT/keys/ota_pubkey.pem"
else
    echo "  ✗ Error: set OTA_PUBLIC_KEY_PATH to a production Ed25519 public key or commit keys/ota_pubkey.pem"
    exit 1
fi

"$REPO_ROOT/scripts/validate_ota_pubkey.sh" "$KEY_SOURCE"
cp "$KEY_SOURCE" "$OVERLAY/oem/etc/ota_pubkey.pem"
echo "  ✓ OTA public key copied to overlay/oem/etc/ota_pubkey.pem"

# Step 3: Sync rootfs overlay assets.
echo "[3/6] Syncing rootfs overlay assets..."
if [ ! -d "$DEST_OVERLAY" ]; then
    echo "  ✗ Error: destination directory not found at $DEST_OVERLAY"
    exit 1
fi

"$REPO_ROOT/scripts/clean_rootfs_overlay_staging.sh" \
    --catalog "$ROOTFS_CLI_TOOL_CATALOG" \
    --managed-state "$ROOTFS_CLI_MANAGED_STATE" \
    --dest-overlay "$DEST_OVERLAY"

"$REPO_ROOT/scripts/build_rootfs_cli_tools.sh" \
    --catalog "$ROOTFS_CLI_TOOL_CATALOG" \
    --output-dir "$ROOTFS_CLI_BUILD_DIR" \
    --cache-dir "$ROOTFS_CLI_CACHE_DIR"
"$REPO_ROOT/scripts/stage_rootfs_cli_tools.sh" \
    --catalog "$ROOTFS_CLI_TOOL_CATALOG" \
    --managed-state "$ROOTFS_CLI_MANAGED_STATE" \
    --source-dir "$ROOTFS_CLI_BUILD_DIR" \
    --dest-overlay "$DEST_OVERLAY"

# Sync only the etc directory into the Buildroot overlay.
if [ -d "$OVERLAY/etc" ]; then
    mkdir -p "$DEST_OVERLAY/etc"
    rsync -a --delete "$OVERLAY/etc/" "$DEST_OVERLAY/etc/"
    echo "  ✓ etc directory synced"
fi

# Step 4: Run pico-sdk build stages and base firmware packaging.
echo "[4/6] Running pico-sdk build stages..."
repair_generated_binaries_from_manifest "overlay-before-sdk-sysdrv" "$AIDEN_BUILD_BIN_DIR" "$OVERLAY/oem/usr/bin" "$GENERATED_BINARY_MANIFEST"
run_pico_sdk_build sysdrv "$@"
repair_generated_binaries_from_manifest "overlay-after-sdk-sysdrv" "$AIDEN_BUILD_BIN_DIR" "$OVERLAY/oem/usr/bin" "$GENERATED_BINARY_MANIFEST"
run_pico_sdk_build media "$@"
repair_generated_binaries_from_manifest "overlay-after-sdk-media" "$AIDEN_BUILD_BIN_DIR" "$OVERLAY/oem/usr/bin" "$GENERATED_BINARY_MANIFEST"
run_pico_sdk_build app "$@"
repair_generated_binaries_from_manifest "overlay-after-sdk-app" "$AIDEN_BUILD_BIN_DIR" "$OVERLAY/oem/usr/bin" "$GENERATED_BINARY_MANIFEST"

# Step 5: Resolve output paths and inject OEM/userdata content.
echo "[5/6] Injecting oem and userdata content..."

# Source board config to get paths
if [ -f "$PICO_SDK/.BoardConfig.mk" ]; then
    source "$PICO_SDK/.BoardConfig.mk"
fi
if [ -z "${RK_PARTITION_FS_TYPE_CFG:-}" ] || [ -z "${RK_PARTITION_CMD_IN_ENV:-}" ]; then
    echo "  ✗ Error: .BoardConfig.mk or both RK_PARTITION_FS_TYPE_CFG and RK_PARTITION_CMD_IN_ENV are required" >&2
    exit 1
fi

# Set default project values.
: ${RK_CHIP:=rv1106}
SDK_ROOT_DIR="$PICO_SDK"
RK_PROJECT_OUTPUT="${SDK_ROOT_DIR}/output/out"
RK_PROJECT_OUTPUT_IMAGE="${SDK_ROOT_DIR}/output/image"
case "${RK_PROJECT_TOOLCHAIN_CROSS:-${RK_TOOLCHAIN_CROSS:-}}" in
    *-uclibc*) RK_LIBC_TPYE=uclibc ;;
    *) RK_LIBC_TPYE=glibc ;;
esac
RK_PROJECT_PACKAGE_ROOTFS_DIR="${RK_PROJECT_OUTPUT}/rootfs_${RK_LIBC_TPYE}_${RK_CHIP}"
RK_PROJECT_PACKAGE_OEM_DIR="${RK_PROJECT_OUTPUT}/oem"
RK_PROJECT_PACKAGE_USERDATA_DIR="${RK_PROJECT_OUTPUT}/userdata"
RK_PROJECT_PACKAGE_OTA_DIR="${RK_PROJECT_OUTPUT}/ota"
CONFIG_WEB_SRC="$REPO_ROOT/src/config_web/web"
CONFIG_WEB_DEST="$RK_PROJECT_PACKAGE_OEM_DIR/usr/share/aiden/config-web"

echo "  → Running base firmware packaging..."
firmware_log="$(mktemp)"
firmware_status=0
if run_pico_sdk_project_build firmware "$@" > "$firmware_log" 2>&1; then
    :
else
    firmware_status=$?
fi
if [ "$firmware_status" -ne 0 ]; then
    echo "  ✗ Error: base firmware packaging failed (exit $firmware_status)" >&2
    cat "$firmware_log" >&2
    rm -f "$firmware_log"
    exit "$firmware_status"
fi
grep -E "(oem|userdata|update)" "$firmware_log" || true
rm -f "$firmware_log"
echo "  ✓ Base images packaged"
repair_generated_binaries_from_manifest "overlay-after-sdk-firmware" "$AIDEN_BUILD_BIN_DIR" "$OVERLAY/oem/usr/bin" "$GENERATED_BINARY_MANIFEST"

# Copy OEM content.
if [ -d "$OVERLAY/oem" ]; then
    echo "  → Copying oem content..."
    clean_managed_staging_paths "$RK_PROJECT_PACKAGE_OEM_DIR" \
        "etc/ota_pubkey.pem" \
        "usr/ko/insmod_wifi.sh" \
        "usr/lib/librknnmrt.so" \
        "usr/model" \
        "usr/share/aiden"
    clean_generated_binaries "$RK_PROJECT_PACKAGE_OEM_DIR/usr/bin"
    rsync -a "$OVERLAY/oem/" "$RK_PROJECT_PACKAGE_OEM_DIR/"
    # Keep generated binaries anchored to build/bin; overlay/oem/usr/bin is only an intermediate staging copy.
    repair_generated_binaries_from_manifest "sdk-oem-usr-bin" "$AIDEN_BUILD_BIN_DIR" "$RK_PROJECT_PACKAGE_OEM_DIR/usr/bin" "$GENERATED_BINARY_MANIFEST"
    echo "  ✓ OEM content copied"
    log_generated_binaries_in_dir "sdk-oem-usr-bin" "$RK_PROJECT_PACKAGE_OEM_DIR/usr/bin"
fi

if [ ! -d "$CONFIG_WEB_SRC" ]; then
    echo "  ✗ Error: config web source directory not found: $CONFIG_WEB_SRC" >&2
    exit 1
fi
for entry_page in index.html llm-logs.html; do
    if [ ! -f "$CONFIG_WEB_SRC/$entry_page" ]; then
        echo "  ✗ Error: config web entry page not found: $CONFIG_WEB_SRC/$entry_page" >&2
        exit 1
    fi
done
mkdir -p "$CONFIG_WEB_DEST"
rsync -a --delete "$CONFIG_WEB_SRC/" "$CONFIG_WEB_DEST/"
echo "  ✓ config web assets synced to OEM staging"

QUICK_ACTIONS_SRC="$REPO_ROOT/src/agent/internal/agent/quick_actions.json"
QUICK_ACTIONS_DEST="$RK_PROJECT_PACKAGE_OEM_DIR/usr/share/aiden/quick_actions.json"
if [ -f "$QUICK_ACTIONS_SRC" ]; then
    mkdir -p "$(dirname "$QUICK_ACTIONS_DEST")"
    cp "$QUICK_ACTIONS_SRC" "$QUICK_ACTIONS_DEST"
    echo "  ✓ quick actions mapping synced to OEM staging"
else
    echo "  ⚠ Warning: $QUICK_ACTIONS_SRC not found; skipping quick actions" >&2
fi

# Bundled agent skills use src/agent/config/skills as the single source and
# ship with the agent in the OEM partition.
SKILLS_SRC="$REPO_ROOT/src/agent/config/skills"
SKILLS_DEST="$RK_PROJECT_PACKAGE_OEM_DIR/usr/share/aiden/skills"
if [ -d "$SKILLS_SRC" ]; then
    mkdir -p "$SKILLS_DEST"
    rsync -a --delete "$SKILLS_SRC/" "$SKILLS_DEST/"
    skill_count=$(find "$SKILLS_DEST" -mindepth 2 -maxdepth 2 -type f -name SKILL.md | wc -l | tr -d ' ')
    if [ "$skill_count" -lt 1 ]; then
        echo "  ✗ Error: no SKILL.md staged in $SKILLS_DEST" >&2
        exit 1
    fi
    echo "  ✓ bundled skills synced to OEM staging ($skill_count skill(s))"
else
    echo "  ⚠ Warning: $SKILLS_SRC not found; skipping bundled skills" >&2
fi

# VAD models live in /oem/usr/model so OTA updates can replace them with the
# oem partition. Remove stale copies from reused userdata package directories.
rm -rf "$RK_PROJECT_PACKAGE_USERDATA_DIR/agent/model"

# Copy userdata content.
if [ -d "$OVERLAY/userdata" ] && [ "$(ls -A "$OVERLAY/userdata" 2>/dev/null)" ]; then
    echo "  → Copying userdata content..."
    clean_managed_staging_paths "$RK_PROJECT_PACKAGE_USERDATA_DIR" \
        "agent/agent.toml" \
        "agent/benchmark" \
        "agent/model" \
        "agent_tools" \
        "ota" \
        "system/env" \
        "wpa_supplicant.conf"
    rsync -a "$OVERLAY/userdata/" "$RK_PROJECT_PACKAGE_USERDATA_DIR/"
    echo "  ✓ USERDATA content copied"
fi

# The dedicated OTA filesystem mounts beneath /userdata, so userdata.img must
# contain the empty mount point but no OTA state or configuration files.
mkdir -p "$RK_PROJECT_PACKAGE_USERDATA_DIR/ota"

# OTA owns the complete partition. Start each full build from an empty staging
# directory; CI seeds config.json later and repacks only ota.img + update.img.
rm -rf "$RK_PROJECT_PACKAGE_OTA_DIR"
mkdir -p "$RK_PROJECT_PACKAGE_OTA_DIR"

# Step 6: Rebuild Aiden-managed images. Do not call firmware again here:
# SDK __PACKAGE_OEM regenerates usr/ko and would overwrite the Aiden overlay.
echo "[6/6] Rebuilding Aiden-managed images..."

echo "  → Rebuilding oem.img..."
rebuild_ext4_image oem "$RK_PROJECT_PACKAGE_OEM_DIR"
verify_oem_generated_binaries_in_image "$RK_PROJECT_OUTPUT_IMAGE/oem.img" "$RK_PROJECT_PACKAGE_OEM_DIR"
verify_oem_config_web_in_image "$RK_PROJECT_OUTPUT_IMAGE/oem.img" "$RK_PROJECT_PACKAGE_OEM_DIR"

# The SDK firmware packager strips every executable in the assembled rootfs,
# including already-minimized static tools copied from the Buildroot overlay.
# Restore only catalog entries with strip_policy=preserve after that generic
# pass, then rebuild rootfs.img while excluding those entries from the second
# release strip pass. Normal-policy tools keep the SDK-stripped bytes.
echo "  → Restaging rootfs CLI tools after SDK release strip..."
"$REPO_ROOT/scripts/stage_rootfs_cli_tools.sh" \
    --catalog "$ROOTFS_CLI_TOOL_CATALOG" \
    --policy preserve \
    --source-dir "$ROOTFS_CLI_BUILD_DIR" \
    --dest-overlay "$RK_PROJECT_PACKAGE_ROOTFS_DIR"
echo "  → Rebuilding rootfs.img..."
rebuild_ext4_image rootfs "$RK_PROJECT_PACKAGE_ROOTFS_DIR"
verify_rootfs_cli_tools_in_image "$RK_PROJECT_OUTPUT_IMAGE/rootfs.img" "$DEST_OVERLAY" "$RK_PROJECT_PACKAGE_ROOTFS_DIR"

echo "  → Rebuilding userdata.img..."
rebuild_ext4_image userdata "$RK_PROJECT_PACKAGE_USERDATA_DIR"

echo "  → Rebuilding ota.img..."
rebuild_ext4_image ota "$RK_PROJECT_PACKAGE_OTA_DIR"

echo "  → Rebuilding update.img..."
run_pico_sdk_project_build updateimg "$@"
if [ ! -s "$RK_PROJECT_OUTPUT_IMAGE/update.img" ]; then
    echo "  ✗ Error: missing rebuilt image: $RK_PROJECT_OUTPUT_IMAGE/update.img" >&2
    exit 1
fi
echo "  ✓ Images rebuilt"

echo ""
echo "=== Build Complete ==="
echo "Images location: $RK_PROJECT_OUTPUT_IMAGE"
echo ""
ls -lh "$RK_PROJECT_OUTPUT_IMAGE"/*.img 2>/dev/null | awk '{print "  " $9 " (" $5 ")"}'
echo ""

missing=0
for img in misc.img boot_a.img boot_b.img oem.img rootfs.img userdata.img ota.img update.img; do
    if [ ! -s "$RK_PROJECT_OUTPUT_IMAGE/$img" ]; then
        echo "  ✗ Missing expected image: $RK_PROJECT_OUTPUT_IMAGE/$img" >&2
        missing=1
    fi
done
if [ "$missing" -ne 0 ]; then
    exit 1
fi
echo "  ✓ Expected A/B images verified"
