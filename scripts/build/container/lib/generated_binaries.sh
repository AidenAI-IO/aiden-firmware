#!/usr/bin/env bash
# Shared generated-binary integrity helpers for the firmware build.

AIDEN_GENERATED_BINARIES=(
    abctl
    agent
    ble_service
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
