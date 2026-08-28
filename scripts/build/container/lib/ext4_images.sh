#!/usr/bin/env bash
# Shared ext4 image assembly and verification helpers for the firmware build.

partition_size_bytes() {
    local name="$1"
    local entry size suffix number
    local -a entries

    IFS=',' read -ra entries <<< "${RK_PARTITION_CMD_IN_ENV:-}"
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
    local -a entries

    IFS=',' read -ra entries <<< "${RK_PARTITION_FS_TYPE_CFG:-}"
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
    local strip_tool toolchain_cross toolchain_bin tool
    local -a strip_find_args

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
    strip_find_args=(
        "$target_dir" -type f
        "(" -perm /111 -o -name '*.so*' ")"
        -not "(" -name 'libpthread*.so*' -o -name 'ld-*.so*' -o -name '*.ko' ")"
    )
    for tool in "${ROOTFS_CLI_PRESERVE_TOOLS[@]}"; do
        strip_find_args+=( -not -path "$target_dir/usr/bin/$tool" )
    done
    strip_find_args+=( -print0 )
    find "${strip_find_args[@]}" |
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

verify_oem_config_web_in_image() {
    local image_path="$1"
    local staged_root="$2"
    local rel_path verified

    verified=0
    for rel_path in \
        "usr/share/aiden/config-web/index.html" \
        "usr/share/aiden/config-web/llm-logs.html"; do
        verify_ext4_image_file_matches "$image_path" "$staged_root" "$rel_path"
        verified=$((verified + 1))
    done
    echo "  ✓ Verified $verified config web entry pages in $(basename "$image_path")"
}

verify_rootfs_cli_tools_in_image() {
    local image_path="$1"
    local original_root="$2"
    local packaged_root="$3"
    local tool strip_policy expected_root verified

    verified=0
    while IFS='|' read -r tool strip_policy; do
        expected_root="$packaged_root"
        if [ "$strip_policy" = "preserve" ]; then
            expected_root="$original_root"
        fi
        verify_ext4_image_file_matches "$image_path" "$expected_root" "usr/bin/$tool"
        verified=$((verified + 1))
    done <<< "$ROOTFS_CLI_NAME_POLICY_RECORDS"
    echo "  ✓ Verified $verified catalog CLI tools in $(basename "$image_path")"
}

rebuild_ext4_image() {
    local name="$1"
    local src_dir="$2"
    local image_path="$RK_PROJECT_OUTPUT_IMAGE/${name}.img"
    local size_bytes fs_type

    if [ ! -d "$src_dir" ]; then
        echo "  ✗ Error: missing staged content for ${name}.img: $src_dir" >&2
        exit 1
    fi
    if [ "$name" != "ota" ] && [ -z "$(ls -A "$src_dir" 2>/dev/null)" ]; then
        echo "  ✗ Error: empty staged content for ${name}.img: $src_dir" >&2
        exit 1
    fi
    if [ -z "${RK_PARTITION_FS_TYPE_CFG:-}" ] || [ -z "${RK_PARTITION_CMD_IN_ENV:-}" ]; then
        echo "  ✗ Error: .BoardConfig.mk or both RK_PARTITION_FS_TYPE_CFG and RK_PARTITION_CMD_IN_ENV are required" >&2
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
        repair_generated_binaries_from_manifest "sdk-oem-before-strip" "$AIDEN_BUILD_BIN_DIR" "$src_dir/usr/bin" "$GENERATED_BINARY_MANIFEST"
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
