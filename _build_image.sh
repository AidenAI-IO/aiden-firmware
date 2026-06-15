#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OVERLAY="$SCRIPT_DIR/overlay"
PICO_SDK="$SCRIPT_DIR/pico-sdk"
DEST_OVERLAY="$PICO_SDK/project/cfg/BoardConfig_IPC/overlay/overlay-luckfox-buildroot-aiden"

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

AIDEN_GENERATED_BINARIES=(
    abctl
    agent
    audio_service
    audio_service_cli
    audio_stream
    config_web
    cpu_vad
    example_audio_capture
    example_audio_play
    example_camera_capture
    example_usb_hid
    example_wakeup
    frame_service
    frame_service_cli
    hello
    image_process
    ota
    rknn_vad
    trigger
)

clean_generated_binaries() {
    local bin_dir="$1"
    local binary

    mkdir -p "$bin_dir"
    for binary in "${AIDEN_GENERATED_BINARIES[@]}"; do
        rm -f "$bin_dir/$binary"
    done
}

partition_size_bytes() {
    local name="$1"
    local entry size suffix number

    IFS=',' read -ra entries <<< "$RK_PARTITION_CMD_IN_ENV"
    for entry in "${entries[@]}"; do
        case "$entry" in
            *"($name)")
                size="${entry%%(*}"
                size="${size%%@*}"
                suffix="${size: -1}"
                number="${size%?}"
                case "$suffix" in
                    K|k) echo $((number * 1024)); return 0 ;;
                    M|m) echo $((number * 1024 * 1024)); return 0 ;;
                    G|g) echo $((number * 1024 * 1024 * 1024)); return 0 ;;
                    T|t) echo $((number * 1024 * 1024 * 1024 * 1024)); return 0 ;;
                    P|p) echo $((number * 1024 * 1024 * 1024 * 1024 * 1024)); return 0 ;;
                    E|e) echo $((number * 1024 * 1024 * 1024 * 1024 * 1024 * 1024)); return 0 ;;
                    -) return 1 ;;
                    *) echo "$size"; return 0 ;;
                esac
                ;;
        esac
    done
    return 1
}

partition_image_size_bytes() {
    local image_name="$1"

    partition_size_bytes "$image_name" && return 0
    partition_size_bytes "${image_name}_a" && return 0
    partition_size_bytes "${image_name}_b" && return 0
    return 1
}

partition_fs_type() {
    local name="$1"
    local entry part_name part_fs_type

    IFS=',' read -ra entries <<< "$RK_PARTITION_FS_TYPE_CFG"
    for entry in "${entries[@]}"; do
        part_name="${entry%%@*}"
        part_fs_type="${entry##*@}"
        if [ "${part_name%_[ab]}" = "${name%_[ab]}" ]; then
            echo "$part_fs_type"
            return 0
        fi
    done
    return 1
}

strip_release_files() {
    local target_dir="$1"
    local strip_tool toolchain_cross toolchain_bin

    if [ "${RK_BUILD_VERSION_TYPE:-}" = "DEBUG" ]; then
        return 0
    fi
    if [ "${LF_TARGET_ROOTFS:-}" != "buildroot" ] && [ "${LF_TARGET_ROOTFS:-}" != "busybox" ]; then
        return 0
    fi
    toolchain_cross="${RK_PROJECT_TOOLCHAIN_CROSS:-${RK_TOOLCHAIN_CROSS:-}}"
    if [ -z "$toolchain_cross" ]; then
        echo "  ⚠ Warning: RK_TOOLCHAIN_CROSS is unset; skipping release strip for $target_dir" >&2
        return 0
    fi
    toolchain_bin="$PICO_SDK/tools/linux/toolchain/$toolchain_cross/bin"
    if [ -x "$toolchain_bin/${toolchain_cross}-strip" ]; then
        strip_tool="$toolchain_bin/${toolchain_cross}-strip"
    elif command -v "${toolchain_cross}-strip" >/dev/null 2>&1; then
        strip_tool="${toolchain_cross}-strip"
    else
        echo "  ⚠ Warning: ${toolchain_cross}-strip not found; skipping release strip for $target_dir" >&2
        return 0
    fi

    find "$target_dir" \( -name "lib*.la" -o -name "lib*.a" \) -exec rm -rf {} +
    find "$target_dir" -type d -name pkgconfig -exec rm -rf {} +
    find "$target_dir" -type f \( -perm /111 -o -name '*.so*' \) \
        -not \( -name 'libpthread*.so*' -o -name 'ld-*.so*' -o -name '*.ko' \) -print0 |
        xargs -0 "$strip_tool" 2>/dev/null || true
    find "$target_dir" -type f -name '*.ko' -print0 |
        xargs -0 "$strip_tool" --strip-debug 2>/dev/null || true
}

find_ext4_debugfs() {
    local candidate

    if command -v debugfs >/dev/null 2>&1; then
        command -v debugfs
        return 0
    fi

    for candidate in \
        "$PICO_SDK/sysdrv/tools/pc/e2fsprogs/debugfs" \
        "$PICO_SDK/sysdrv/tools/pc/e2fsprogs/bin/debugfs"; do
        if [ -x "$candidate" ]; then
            echo "$candidate"
            return 0
        fi
    done

    return 1
}

sha256_file() {
    local path="$1"

    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$path" | awk '{print $1}'
        return 0
    fi
    if command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$path" | awk '{print $1}'
        return 0
    fi

    echo "  ✗ Error: sha256sum or shasum is required to verify rebuilt image contents" >&2
    exit 1
}

file_size_bytes() {
    local path="$1"

    if stat -c '%s' "$path" >/dev/null 2>&1; then
        stat -c '%s' "$path"
        return 0
    fi
    wc -c < "$path" | tr -d ' '
}

file_allocated_bytes() {
    local path="$1"
    local blocks block_size

    if read -r blocks block_size < <(stat -c '%b %B' "$path" 2>/dev/null); then
        echo $((blocks * block_size))
        return 0
    fi
    echo unknown
}

log_binary_fingerprint() {
    local stage="$1"
    local binary="$2"
    local path="$3"
    local size allocated sha

    if [ ! -f "$path" ]; then
        echo "  binary-fingerprint stage=$stage name=$binary missing"
        return 0
    fi

    size="$(file_size_bytes "$path")"
    allocated="$(file_allocated_bytes "$path")"
    sha="$(sha256_file "$path")"
    echo "  binary-fingerprint stage=$stage name=$binary size=$size allocated=$allocated sha256=$sha"

    if command -v file >/dev/null 2>&1; then
        echo "    file: $(file -b "$path")"
    fi
    case "$binary" in
        abctl | agent | ota)
            if command -v go >/dev/null 2>&1; then
                go version -m "$path" 2>/dev/null | sed 's/^/    go-version-m: /' || true
            fi
            ;;
    esac
}

log_generated_binaries_in_dir() {
    local stage="$1"
    local bin_dir="$2"
    local binary

    echo "  → Binary fingerprints ($stage)"
    for binary in "${AIDEN_GENERATED_BINARIES[@]}"; do
        log_binary_fingerprint "$stage" "$binary" "$bin_dir/$binary"
    done
}

verify_ext4_image_file_matches() {
    local image_path="$1"
    local staged_root="$2"
    local rel_path="${3#/}"
    local staged_file="$staged_root/$rel_path"
    local debugfs_tool dump_dir dumped_file dump_log staged_sha image_sha

    if [ ! -f "$staged_file" ]; then
        echo "  ✗ Error: missing staged file for image verification: $staged_file" >&2
        exit 1
    fi
    if [ ! -s "$image_path" ]; then
        echo "  ✗ Error: missing image for content verification: $image_path" >&2
        exit 1
    fi

    debugfs_tool="$(find_ext4_debugfs)" || {
        echo "  ✗ Error: debugfs is required to verify rebuilt ext4 image contents" >&2
        exit 1
    }

    dump_dir="$(mktemp -d)"
    dumped_file="$dump_dir/dumped"
    dump_log="$dump_dir/debugfs.log"
    if ! "$debugfs_tool" -R "dump /$rel_path $dumped_file" "$image_path" >"$dump_log" 2>&1; then
        echo "  ✗ Error: failed to dump /$rel_path from $image_path" >&2
        sed -n '1,20p' "$dump_log" >&2
        rm -rf "$dump_dir"
        exit 1
    fi
    if [ ! -f "$dumped_file" ]; then
        echo "  ✗ Error: debugfs did not dump /$rel_path from $image_path" >&2
        sed -n '1,20p' "$dump_log" >&2
        rm -rf "$dump_dir"
        exit 1
    fi

    staged_sha="$(sha256_file "$staged_file")"
    image_sha="$(sha256_file "$dumped_file")"
    rm -rf "$dump_dir"

    if [ "$staged_sha" != "$image_sha" ]; then
        echo "  ✗ Error: rebuilt image content mismatch for /$rel_path" >&2
        echo "    staged: $staged_sha  $staged_file" >&2
        echo "    image:  $image_sha  $image_path:/$rel_path" >&2
        "$debugfs_tool" -R "stat /$rel_path" "$image_path" 2>&1 | sed -n '1,80p' >&2 || true
        exit 1
    fi
    echo "  image-file-verified rel=/$rel_path sha256=$image_sha"
}

verify_oem_generated_binaries_in_image() {
    local image_path="$1"
    local staged_root="$2"
    local binary missing verified

    missing=0
    verified=0
    for binary in "${AIDEN_GENERATED_BINARIES[@]}"; do
        if [ ! -f "$staged_root/usr/bin/$binary" ]; then
            echo "  ✗ Error: missing generated OEM binary in staging: $staged_root/usr/bin/$binary" >&2
            missing=1
            continue
        fi
        verify_ext4_image_file_matches "$image_path" "$staged_root" "usr/bin/$binary"
        verified=$((verified + 1))
    done

    if [ "$missing" -ne 0 ]; then
        exit 1
    fi
    echo "  ✓ Verified $verified generated OEM binaries in $(basename "$image_path")"
}

rebuild_ext4_image() {
    local name="$1"
    local src_dir="$2"
    local image_path="$RK_PROJECT_OUTPUT_IMAGE/${name}.img"
    local size_bytes fs_type

    if [ ! -d "$src_dir" ] || [ -z "$(ls -A "$src_dir" 2>/dev/null)" ]; then
        echo "  ✗ Error: missing staged content for ${name}.img: $src_dir" >&2
        exit 1
    fi

    fs_type="$(partition_fs_type "$name")" || {
        echo "  ✗ Error: filesystem type for ${name}.img not found in RK_PARTITION_FS_TYPE_CFG" >&2
        exit 1
    }
    if [ "$fs_type" != "ext4" ]; then
        echo "  ✗ Error: direct rebuild only supports ext4 ${name}.img, got $fs_type" >&2
        exit 1
    fi

    size_bytes="$(partition_image_size_bytes "$name")" || {
        echo "  ✗ Error: partition size for ${name}.img not found in RK_PARTITION_CMD_IN_ENV" >&2
        exit 1
    }

    if [ "$name" = "oem" ]; then
        log_generated_binaries_in_dir "sdk-oem-before-strip" "$src_dir/usr/bin"
    fi
    strip_release_files "$src_dir"
    if [ "$name" = "oem" ]; then
        log_generated_binaries_in_dir "sdk-oem-after-strip" "$src_dir/usr/bin"
    fi
    chown -hR 0:0 "$src_dir"
    "$PICO_SDK/sysdrv/tools/pc/e2fsprogs/mkfs_ext4.sh" "$src_dir" "$image_path" "$size_bytes"
    if [ ! -s "$image_path" ]; then
        echo "  ✗ Error: missing rebuilt image: $image_path" >&2
        exit 1
    fi
}

echo "=== Aiden Hardware Demo - Image Builder ==="
echo ""

# Step 1: 编译应用程序. _build.sh requires a verified Go in PATH and disables
# automatic Go toolchain downloads.
echo "[1/6] Building applications..."
cd "$SCRIPT_DIR"
./_build.sh
log_generated_binaries_in_dir "build-bin" "$SCRIPT_DIR/build/bin"

# Step 2: 准备 overlay 目录
echo "[2/6] Preparing overlay directories..."
mkdir -p "$OVERLAY/oem/usr/bin" "$OVERLAY/oem/usr/lib" "$OVERLAY/oem/etc"
clean_generated_binaries "$OVERLAY/oem/usr/bin"
rsync -a "$SCRIPT_DIR/build/bin/" "$OVERLAY/oem/usr/bin/"
echo "  ✓ Binaries copied to overlay/oem/usr/bin"
log_generated_binaries_in_dir "overlay-oem-usr-bin" "$OVERLAY/oem/usr/bin"

BENCHMARK_SRC="$SCRIPT_DIR/benchmark"
BENCHMARK_DEST="$OVERLAY/userdata/agent/benchmark"
BENCHMARK_RSYNC_EXCLUDES=(--exclude '__pycache__/' --exclude '*.pyc' --exclude '.DS_Store' --exclude '._*')
if [ ! -d "$BENCHMARK_SRC/runner" ] || [ ! -d "$BENCHMARK_SRC/suites" ]; then
    echo "  ✗ Error: benchmark runner or suites missing under $BENCHMARK_SRC" >&2
    exit 1
fi
mkdir -p "$BENCHMARK_DEST/runner" "$BENCHMARK_DEST/suites"
rsync -a --delete "${BENCHMARK_RSYNC_EXCLUDES[@]}" "$BENCHMARK_SRC/runner/" "$BENCHMARK_DEST/runner/"
rsync -a --delete "${BENCHMARK_RSYNC_EXCLUDES[@]}" "$BENCHMARK_SRC/suites/" "$BENCHMARK_DEST/suites/"
if [ -f "$BENCHMARK_SRC/pyproject.toml" ]; then
    cp "$BENCHMARK_SRC/pyproject.toml" "$BENCHMARK_DEST/pyproject.toml"
else
    rm -f "$BENCHMARK_DEST/pyproject.toml"
fi
echo "  ✓ Benchmark runner and suites staged to overlay/userdata/agent/benchmark"

AGENT_TOOLS_DEST="$OVERLAY/userdata/agent_tools"
mkdir -p "$AGENT_TOOLS_DEST"
cp "$SCRIPT_DIR/scripts/generate_agent_files_report.py" "$AGENT_TOOLS_DEST/"
cp "$SCRIPT_DIR/scripts/agent_files_template.html" "$AGENT_TOOLS_DEST/"
cp "$SCRIPT_DIR/scripts/view_agent_files.sh" "$AGENT_TOOLS_DEST/"
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
elif [ -f "$SCRIPT_DIR/keys/ota_pubkey.pem" ]; then
    if grep -Eiq 'dev|test|placeholder' "$SCRIPT_DIR/keys/ota_pubkey.pem"; then
        echo "  ✗ Error: keys/ota_pubkey.pem is marked dev/test/placeholder; refusing production image"
        exit 1
    fi
    KEY_SOURCE="$SCRIPT_DIR/keys/ota_pubkey.pem"
else
    echo "  ✗ Error: set OTA_PUBLIC_KEY_PATH to a production Ed25519 public key or commit keys/ota_pubkey.pem"
    exit 1
fi

"$SCRIPT_DIR/scripts/validate_ota_pubkey.sh" "$KEY_SOURCE"
cp "$KEY_SOURCE" "$OVERLAY/oem/etc/ota_pubkey.pem"
echo "  ✓ OTA public key copied to overlay/oem/etc/ota_pubkey.pem"

# Step 3: Sync rootfs overlay assets.
echo "[3/6] Syncing rootfs overlay assets..."
if [ ! -d "$DEST_OVERLAY" ]; then
    echo "  ✗ Error: destination directory not found at $DEST_OVERLAY"
    exit 1
fi

"$SCRIPT_DIR/scripts/clean_rootfs_overlay_staging.sh" --dest-overlay "$DEST_OVERLAY"

# 只复制 etc 目录到 buildroot overlay
if [ -d "$OVERLAY/etc" ]; then
    mkdir -p "$DEST_OVERLAY/etc"
    rsync -a --delete "$OVERLAY/etc/" "$DEST_OVERLAY/etc/"
    echo "  ✓ etc directory synced"
fi

# Bundled phone-bridge app mapping: src/agent/internal/agent/app_mapping.json 是
# Go embed 的单一源，固件里同步落到 /usr/share/aiden/app_mapping.json，方便运维
# 不重新 build 二进制就能更新映射（agent 启动时优先读 configDir，再读这里，最后
# 回退到二进制内嵌副本）。
APP_MAPPING_SRC="$SCRIPT_DIR/src/agent/internal/agent/app_mapping.json"
APP_MAPPING_DEST="$DEST_OVERLAY/usr/share/aiden/app_mapping.json"
if [ -f "$APP_MAPPING_SRC" ]; then
    mkdir -p "$(dirname "$APP_MAPPING_DEST")"
    cp "$APP_MAPPING_SRC" "$APP_MAPPING_DEST"
    echo "  ✓ phone-bridge app mapping synced"
else
    echo "  ⚠ Warning: $APP_MAPPING_SRC not found; skipping app mapping" >&2
fi

QUICK_ACTIONS_SRC="$SCRIPT_DIR/src/agent/internal/agent/quick_actions.json"
QUICK_ACTIONS_DEST="$DEST_OVERLAY/usr/share/aiden/quick_actions.json"
if [ -f "$QUICK_ACTIONS_SRC" ]; then
    mkdir -p "$(dirname "$QUICK_ACTIONS_DEST")"
    cp "$QUICK_ACTIONS_SRC" "$QUICK_ACTIONS_DEST"
    echo "  ✓ quick actions mapping synced"
else
    echo "  ⚠ Warning: $QUICK_ACTIONS_SRC not found; skipping quick actions" >&2
fi

# Step 4: Run pico-sdk build stages and base firmware packaging.
echo "[4/6] Running pico-sdk build stages..."
cd "$PICO_SDK"
./build.sh sysdrv "$@"
./build.sh media "$@"
./build.sh app "$@"

# Step 5: 获取输出路径并复制 oem/userdata 内容
echo "[5/6] Injecting oem and userdata content..."

# Source board config to get paths
if [ -f "$PICO_SDK/.BoardConfig.mk" ]; then
    source "$PICO_SDK/.BoardConfig.mk"
fi

# 设置默认值
: ${RK_CHIP:=rv1106}
SDK_ROOT_DIR="$PICO_SDK"
RK_PROJECT_OUTPUT="${SDK_ROOT_DIR}/output/out"
RK_PROJECT_OUTPUT_IMAGE="${SDK_ROOT_DIR}/output/image"
RK_PROJECT_PACKAGE_OEM_DIR="${RK_PROJECT_OUTPUT}/oem"
RK_PROJECT_PACKAGE_USERDATA_DIR="${RK_PROJECT_OUTPUT}/userdata"

cd "$PICO_SDK/project"
echo "  → Running base firmware packaging..."
firmware_log="$(mktemp)"
./build.sh firmware "$@" > "$firmware_log" 2>&1
grep -E "(oem|userdata|update)" "$firmware_log" || true
rm -f "$firmware_log"
echo "  ✓ Base images packaged"

# 复制 oem 内容
if [ -d "$OVERLAY/oem" ]; then
    echo "  → Copying oem content..."
    clean_managed_staging_paths "$RK_PROJECT_PACKAGE_OEM_DIR" \
        "etc/ota_pubkey.pem" \
        "usr/ko/insmod_wifi.sh" \
        "usr/lib/librknnmrt.so" \
        "usr/model"
    clean_generated_binaries "$RK_PROJECT_PACKAGE_OEM_DIR/usr/bin"
    # Keep Aiden-managed /oem/usr/bin exact when pico-sdk/output/out is reused.
    rsync -a --delete "$OVERLAY/oem/usr/bin/" "$RK_PROJECT_PACKAGE_OEM_DIR/usr/bin/"
    rsync -a "$OVERLAY/oem/" "$RK_PROJECT_PACKAGE_OEM_DIR/"
    echo "  ✓ OEM content copied"
    log_generated_binaries_in_dir "sdk-oem-usr-bin" "$RK_PROJECT_PACKAGE_OEM_DIR/usr/bin"
fi

# Bundled agent skills use src/agent/config/skills as the single source and
# ship with the agent in the OEM partition.
SKILLS_SRC="$SCRIPT_DIR/src/agent/config/skills"
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

# 复制 userdata 内容
if [ -d "$OVERLAY/userdata" ] && [ "$(ls -A "$OVERLAY/userdata" 2>/dev/null)" ]; then
    echo "  → Copying userdata content..."
    clean_managed_staging_paths "$RK_PROJECT_PACKAGE_USERDATA_DIR" \
        "agent/agent.toml" \
        "agent/benchmark" \
        "agent/model" \
        "agent_tools" \
        "system/env" \
        "wpa_supplicant.conf"
    rsync -a "$OVERLAY/userdata/" "$RK_PROJECT_PACKAGE_USERDATA_DIR/"
    echo "  ✓ USERDATA content copied"
fi

# Step 6: Rebuild Aiden-managed images. Do not call firmware again here:
# SDK __PACKAGE_OEM regenerates usr/ko and would overwrite the Aiden overlay.
echo "[6/6] Rebuilding Aiden-managed images..."
cd "$PICO_SDK/project"

echo "  → Rebuilding oem.img..."
rebuild_ext4_image oem "$RK_PROJECT_PACKAGE_OEM_DIR"
verify_oem_generated_binaries_in_image "$RK_PROJECT_OUTPUT_IMAGE/oem.img" "$RK_PROJECT_PACKAGE_OEM_DIR"

echo "  → Rebuilding userdata.img..."
rebuild_ext4_image userdata "$RK_PROJECT_PACKAGE_USERDATA_DIR"

echo "  → Rebuilding update.img..."
./build.sh updateimg "$@"
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
for img in misc.img boot_a.img boot_b.img oem.img rootfs.img userdata.img update.img; do
    if [ ! -s "$RK_PROJECT_OUTPUT_IMAGE/$img" ]; then
        echo "  ✗ Missing expected image: $RK_PROJECT_OUTPUT_IMAGE/$img" >&2
        missing=1
    fi
done
if [ "$missing" -ne 0 ]; then
    exit 1
fi
echo "  ✓ Expected A/B images verified"

cd "$SCRIPT_DIR"
