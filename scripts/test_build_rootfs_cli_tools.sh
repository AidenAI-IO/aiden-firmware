#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BUILD_SCRIPT="$ROOT_DIR/scripts/build_rootfs_cli_tools.sh"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fake_bin="$tmpdir/bin"
output_dir="$tmpdir/output"
cache_dir="$tmpdir/cache"
mkdir -p "$fake_bin"

make_fake_arm_elf() {
    output="$1"
    printf '\177ELF\001\001\001\000\000\000\000\000\000\000\000\000\002\000\050\000\001\000\000\000\000\000\000\000\000\000\000\000\000\000\000\000\064\000\000\000\000\000\000\000\000\000\000\000\064\000\000\000\000\000\000\000' > "$output"
    chmod 755 "$output"
}

make_fake_arm_elf "$tmpdir/fake-arm-elf"

cat > "$fake_bin/go" <<'SH'
#!/bin/sh
set -eu

case "${1:-}" in
  version)
    echo 'go version go1.26.0 linux/amd64'
    exit 0
    ;;
  install)
    for argument in "$@"; do
      module="$argument"
    done
    ;;
  *)
    echo "unexpected go command: $*" >&2
    exit 2
    ;;
esac

case "$module" in
  github.com/mikefarah/yq/v4@v4.53.3) tool=yq ;;
  github.com/wader/fq@v0.17.0) tool=fq ;;
  *)
    echo "unexpected module: $module" >&2
    exit 2
    ;;
esac

printf '%s %s/%s/%s CGO_ENABLED=%s %s\n' "$module" "${GOOS:-}" "${GOARCH:-}" "${GOARM:-}" "${CGO_ENABLED:-}" "$*" >> "${FAKE_GO_LOG:?}"
mkdir -p "${GOPATH:?}/bin/linux_arm"
cp "${FAKE_ARM_ELF:?}" "${GOPATH:?}/bin/linux_arm/$tool"
chmod 755 "${GOPATH:?}/bin/linux_arm/$tool"
SH
chmod +x "$fake_bin/go"

cat > "$fake_bin/curl" <<'SH'
#!/bin/sh
set -eu

output=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      output="${2:-}"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
[ -n "$output" ] || exit 2

archive_root=$(mktemp -d)
archive_dir="$archive_root/ripgrep-15.2.0-armv7-unknown-linux-musleabihf"
mkdir -p "$archive_dir"
cp "${FAKE_ARM_ELF:?}" "$archive_dir/rg"
chmod 755 "$archive_dir/rg"
tar -czf "$output" -C "$archive_root" "$(basename "$archive_dir")"
rm -rf "$archive_root"
printf 'curl\n' >> "${FAKE_CURL_LOG:?}"
SH
chmod +x "$fake_bin/curl"

cat > "$fake_bin/sha256sum" <<'SH'
#!/bin/sh
set -eu

path="${1:-}"
case "$(basename "$path")" in
  ripgrep-15.2.0-armv7-unknown-linux-musleabihf.tar.gz*)
    printf '0332b481aa007969a54d5c19e793208e73405c48d38f226bdee56b9ed085cdde  %s\n' "$path"
    ;;
  *)
    /usr/bin/shasum -a 256 "$path"
    ;;
esac
SH
chmod +x "$fake_bin/sha256sum"

if [ ! -x "$BUILD_SCRIPT" ]; then
    echo "missing executable rootfs CLI build script: $BUILD_SCRIPT" >&2
    exit 1
fi

PATH="$fake_bin:$PATH" \
FAKE_ARM_ELF="$tmpdir/fake-arm-elf" \
FAKE_GO_LOG="$tmpdir/go.log" \
FAKE_CURL_LOG="$tmpdir/curl.log" \
"$BUILD_SCRIPT" --output-dir "$output_dir" --cache-dir "$cache_dir"

for tool in fq yq rg; do
    if [ ! -x "$output_dir/$tool" ]; then
        echo "build must produce executable $tool" >&2
        exit 1
    fi
    if ! grep -Eq "^[0-9a-f]{64}  $tool$" "$output_dir/manifest.sha256"; then
        echo "build manifest must contain $tool checksum" >&2
        exit 1
    fi
done

if ! grep -q 'github.com/mikefarah/yq/v4@v4.53.3 linux/arm/7 CGO_ENABLED=0' "$tmpdir/go.log"; then
    echo "yq must use its pinned version and linux/arm/v7 static build" >&2
    exit 1
fi
if ! grep -q 'github.com/wader/fq@v0.17.0 linux/arm/7 CGO_ENABLED=0' "$tmpdir/go.log"; then
    echo "fq must use its pinned version and linux/arm/v7 static build" >&2
    exit 1
fi
if [ "$(wc -l < "$tmpdir/curl.log" | tr -d ' ')" -ne 1 ]; then
    echo "ripgrep archive must be downloaded exactly once" >&2
    exit 1
fi

PATH="$fake_bin:$PATH" \
FAKE_ARM_ELF="$tmpdir/fake-arm-elf" \
FAKE_GO_LOG="$tmpdir/go.log" \
FAKE_CURL_LOG="$tmpdir/curl.log" \
"$BUILD_SCRIPT" --output-dir "$output_dir" --cache-dir "$cache_dir"

if [ "$(wc -l < "$tmpdir/curl.log" | tr -d ' ')" -ne 1 ]; then
    echo "a checksum-verified ripgrep archive must be reused from cache" >&2
    exit 1
fi

(
    cd "$tmpdir"
    PATH="$fake_bin:$PATH" \
    FAKE_ARM_ELF="$tmpdir/fake-arm-elf" \
    FAKE_GO_LOG="$tmpdir/go.log" \
    FAKE_CURL_LOG="$tmpdir/curl.log" \
    "$BUILD_SCRIPT" --output-dir relative-output --cache-dir relative-cache
)
if [ ! -x "$tmpdir/relative-output/fq" ]; then
    echo "build must accept relative output and cache directories" >&2
    exit 1
fi

echo "rootfs CLI build tests passed"
