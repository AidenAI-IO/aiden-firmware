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
GENERATED_BINARY_MANIFEST="$SCRIPT_DIR/build/aiden-generated-binaries.sha256"

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

    if stat -c 'mode=%A uid=%u gid=%g inode=%i links=%h mtime=%y ctime=%z' "$path" >/dev/null 2>&1; then
        echo "    stat: $(stat -c 'mode=%A uid=%u gid=%g inode=%i links=%h mtime=%y ctime=%z' "$path")"
    fi
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

manifest_sha_for_binary() {
    local manifest="$1"
    local binary="$2"

    awk -v binary="$binary" '$2 == binary { print $1; found = 1; exit } END { if (!found) exit 1 }' "$manifest"
}

write_generated_binary_manifest() {
    local bin_dir="$1"
    local manifest="$2"
    local binary path sha tmp_manifest

    mkdir -p "$(dirname "$manifest")"
    tmp_manifest="$(mktemp "${manifest}.tmp.XXXXXX")"
    for binary in "${AIDEN_GENERATED_BINARIES[@]}"; do
        path="$bin_dir/$binary"
        if [ ! -f "$path" ]; then
            rm -f "$tmp_manifest"
            echo "  ✗ Error: missing generated binary for manifest: $path" >&2
            exit 1
        fi
        sha="$(sha256_file "$path")"
        printf '%s  %s\n' "$sha" "$binary" >> "$tmp_manifest"
    done
    mv "$tmp_manifest" "$manifest"
    echo "  ✓ Generated binary manifest written: $manifest"
}

log_binary_storage_details() {
    local label="$1"
    local path="$2"

    if [ ! -e "$path" ]; then
        return 0
    fi
    if command -v filefrag >/dev/null 2>&1; then
        filefrag -v "$path" 2>/dev/null | sed "s/^/    filefrag-$label: /" || true
    fi
}

log_binary_diff_summary() {
    local expected_path="$1"
    local actual_path="$2"

    if [ ! -f "$expected_path" ] || [ ! -f "$actual_path" ] || ! command -v python3 >/dev/null 2>&1; then
        return 0
    fi

    python3 - "$expected_path" "$actual_path" <<'PY' || true
from pathlib import Path
import sys

expected = Path(sys.argv[1]).read_bytes()
actual = Path(sys.argv[2]).read_bytes()
limit = min(len(expected), len(actual))
diffs = [i for i in range(limit) if expected[i] != actual[i]]
if len(expected) != len(actual):
    diffs.extend(range(limit, max(len(expected), len(actual))))
if not diffs:
    print("    diff-summary: files are byte-identical")
    raise SystemExit

first = diffs[0]
last = diffs[-1]
print(f"    diff-summary: count={len(diffs)} first=0x{first:x} last=0x{last:x} expected_size={len(expected)} actual_size={len(actual)}")

page_size = 4096
start_page = first // page_size
end_page = last // page_size
zeroed_pages = []
for page in range(start_page, end_page + 1):
    start = page * page_size
    end = min(start + page_size, limit)
    if start >= end:
        continue
    expected_nonzero = sum(1 for byte in expected[start:end] if byte != 0)
    actual_nonzero = sum(1 for byte in actual[start:end] if byte != 0)
    if expected_nonzero > 0 and actual_nonzero == 0:
        zeroed_pages.append((start, end - 1))

if zeroed_pages:
    ranges = []
    range_start, range_end = zeroed_pages[0]
    for start, end in zeroed_pages[1:]:
        if start == range_end + 1:
            range_end = end
        else:
            ranges.append((range_start, range_end))
            range_start, range_end = start, end
    ranges.append((range_start, range_end))
    rendered = " ".join(f"0x{start:x}-0x{end:x}" for start, end in ranges[:8])
    suffix = " ..." if len(ranges) > 8 else ""
    print(f"    diff-summary: zeroed-page-ranges={rendered}{suffix}")
PY
}

log_generated_binary_mismatch() {
    local stage="$1"
    local binary="$2"
    local expected_sha="$3"
    local actual_sha="$4"
    local expected_path="$5"
    local actual_path="$6"

    echo "  ✗ Generated binary mismatch stage=$stage name=$binary expected_sha256=$expected_sha actual_sha256=$actual_sha path=$actual_path" >&2
    log_binary_fingerprint "$stage-expected" "$binary" "$expected_path" >&2 || true
    log_binary_fingerprint "$stage-actual" "$binary" "$actual_path" >&2 || true
    log_binary_diff_summary "$expected_path" "$actual_path" >&2 || true
    log_binary_storage_details "expected" "$expected_path" >&2 || true
    log_binary_storage_details "actual" "$actual_path" >&2 || true
}

check_generated_binaries_against_manifest() {
    local stage="$1"
    local bin_dir="$2"
    local manifest="$3"
    local expected_bin_dir="${4:-}"
    local binary expected_sha actual_sha path expected_path mismatch checked

    if [ ! -f "$manifest" ]; then
        echo "  ✗ Error: missing generated binary manifest for $stage: $manifest" >&2
        exit 1
    fi

    mismatch=0
    checked=0
    for binary in "${AIDEN_GENERATED_BINARIES[@]}"; do
        expected_sha="$(manifest_sha_for_binary "$manifest" "$binary")" || {
            echo "  ✗ Error: manifest missing generated binary: $binary" >&2
            exit 1
        }
        path="$bin_dir/$binary"
        expected_path="${expected_bin_dir:-$bin_dir}/$binary"
        if [ ! -f "$path" ]; then
            echo "  ✗ Generated binary missing stage=$stage name=$binary path=$path expected_sha256=$expected_sha" >&2
            mismatch=1
            continue
        fi
        actual_sha="$(sha256_file "$path")"
        if [ "$actual_sha" != "$expected_sha" ]; then
            mismatch=1
            log_generated_binary_mismatch "$stage" "$binary" "$expected_sha" "$actual_sha" "$expected_path" "$path"
            continue
        fi
        checked=$((checked + 1))
    done

    if [ "$mismatch" -eq 0 ]; then
        echo "  ✓ Generated binary manifest check passed stage=$stage count=$checked"
        return 0
    fi
    return 1
}

sync_generated_binaries_from_source() {
    local source_bin_dir="$1"
    local dest_bin_dir="$2"
    local binary source_path

    mkdir -p "$dest_bin_dir"
    clean_generated_binaries "$dest_bin_dir"
    for binary in "${AIDEN_GENERATED_BINARIES[@]}"; do
        source_path="$source_bin_dir/$binary"
        if [ ! -f "$source_path" ]; then
            echo "  ✗ Error: missing generated binary source: $source_path" >&2
            exit 1
        fi
        rsync -a "$source_path" "$dest_bin_dir/$binary"
    done
}

repair_generated_binaries_from_manifest() {
    local stage="$1"
    local source_bin_dir="$2"
    local dest_bin_dir="$3"
    local manifest="$4"

    log_generated_binaries_in_dir "$stage" "$dest_bin_dir"
    if check_generated_binaries_against_manifest "$stage" "$dest_bin_dir" "$manifest" "$source_bin_dir"; then
        return 0
    fi

    echo "  ⚠ Generated binary mismatch detected stage=$stage; restoring from $source_bin_dir" >&2
    if ! check_generated_binaries_against_manifest "$stage-source" "$source_bin_dir" "$manifest" "$source_bin_dir"; then
        echo "  ✗ Error: generated binary source is not trustworthy: $source_bin_dir" >&2
        exit 1
    fi

    sync_generated_binaries_from_source "$source_bin_dir" "$dest_bin_dir"
    log_generated_binaries_in_dir "$stage-after-repair" "$dest_bin_dir"
    if ! check_generated_binaries_against_manifest "$stage-after-repair" "$dest_bin_dir" "$manifest" "$source_bin_dir"; then
        echo "  ✗ Error: generated binary repair failed stage=$stage dest=$dest_bin_dir" >&2
        exit 1
    fi
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
        repair_generated_binaries_from_manifest "sdk-oem-before-strip" "$SCRIPT_DIR/build/bin" "$src_dir/usr/bin" "$GENERATED_BINARY_MANIFEST"
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
write_generated_binary_manifest "$SCRIPT_DIR/build/bin" "$GENERATED_BINARY_MANIFEST"
check_generated_binaries_against_manifest "build-bin" "$SCRIPT_DIR/build/bin" "$GENERATED_BINARY_MANIFEST" "$SCRIPT_DIR/build/bin"

# Step 2: 准备 overlay 目录
echo "[2/6] Preparing overlay directories..."
mkdir -p "$OVERLAY/oem/usr/bin" "$OVERLAY/oem/usr/lib" "$OVERLAY/oem/etc"
sync_generated_binaries_from_source "$SCRIPT_DIR/build/bin" "$OVERLAY/oem/usr/bin"
echo "  ✓ Binaries copied to overlay/oem/usr/bin"
repair_generated_binaries_from_manifest "overlay-oem-usr-bin" "$SCRIPT_DIR/build/bin" "$OVERLAY/oem/usr/bin" "$GENERATED_BINARY_MANIFEST"

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

# Step 4: Run pico-sdk build stages and base firmware packaging.
echo "[4/6] Running pico-sdk build stages..."
cd "$PICO_SDK"
repair_generated_binaries_from_manifest "overlay-before-sdk-sysdrv" "$SCRIPT_DIR/build/bin" "$OVERLAY/oem/usr/bin" "$GENERATED_BINARY_MANIFEST"
./build.sh sysdrv "$@"
repair_generated_binaries_from_manifest "overlay-after-sdk-sysdrv" "$SCRIPT_DIR/build/bin" "$OVERLAY/oem/usr/bin" "$GENERATED_BINARY_MANIFEST"
./build.sh media "$@"
repair_generated_binaries_from_manifest "overlay-after-sdk-media" "$SCRIPT_DIR/build/bin" "$OVERLAY/oem/usr/bin" "$GENERATED_BINARY_MANIFEST"
./build.sh app "$@"
repair_generated_binaries_from_manifest "overlay-after-sdk-app" "$SCRIPT_DIR/build/bin" "$OVERLAY/oem/usr/bin" "$GENERATED_BINARY_MANIFEST"

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
repair_generated_binaries_from_manifest "overlay-after-sdk-firmware" "$SCRIPT_DIR/build/bin" "$OVERLAY/oem/usr/bin" "$GENERATED_BINARY_MANIFEST"

# 复制 oem 内容
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
    repair_generated_binaries_from_manifest "sdk-oem-usr-bin" "$SCRIPT_DIR/build/bin" "$RK_PROJECT_PACKAGE_OEM_DIR/usr/bin" "$GENERATED_BINARY_MANIFEST"
    echo "  ✓ OEM content copied"
    log_generated_binaries_in_dir "sdk-oem-usr-bin" "$RK_PROJECT_PACKAGE_OEM_DIR/usr/bin"
fi

# Bundled phone-bridge app mapping: src/agent/internal/agent/app_mapping.json is
# the Go embed source and is also shipped in OEM so operations can update it
# without rebuilding the agent binary. Runtime order is configDir override,
# bundled OEM file, then embedded defaults.
APP_MAPPING_SRC="$SCRIPT_DIR/src/agent/internal/agent/app_mapping.json"
APP_MAPPING_DEST="$RK_PROJECT_PACKAGE_OEM_DIR/usr/share/aiden/app_mapping.json"
if [ -f "$APP_MAPPING_SRC" ]; then
    mkdir -p "$(dirname "$APP_MAPPING_DEST")"
    cp "$APP_MAPPING_SRC" "$APP_MAPPING_DEST"
    echo "  ✓ phone-bridge app mapping synced to OEM staging"
else
    echo "  ⚠ Warning: $APP_MAPPING_SRC not found; skipping app mapping" >&2
fi

QUICK_ACTIONS_SRC="$SCRIPT_DIR/src/agent/internal/agent/quick_actions.json"
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
