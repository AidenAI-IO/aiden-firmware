#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
resolver_script="$repo_root/scripts/resolve_reusable_rootfs_asset.sh"
if [ ! -x "$resolver_script" ]; then
  echo "resolve_reusable_rootfs_asset.sh is missing or not executable: $resolver_script" >&2
  echo "Check repo_root detection and script permissions." >&2
  exit 1
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

assets_dir="$tmp_dir/assets"
fake_bin="$tmp_dir/bin"
state_dir="$tmp_dir/state"
mkdir -p "$assets_dir" "$fake_bin" "$state_dir"

printf 'boot a\n' > "$assets_dir/boot_a.img"
printf 'boot b\n' > "$assets_dir/boot_b.img"
printf 'oem\n' > "$assets_dir/oem.img"
printf 'same rootfs payload\n' > "$assets_dir/rootfs.img"
printf 'update\n' > "$assets_dir/update.img"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

output_value() {
  local key="$1"
  local file="$2"
  awk -v key="$key" '
    index($0, key "=") == 1 {
      sub("^[^=]*=", "")
      print
    }
  ' "$file" | tail -n 1
}

cat > "$fake_bin/gh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

state_dir="${FAKE_GH_STATE_DIR:?}"
printf '%s\n' "$*" >> "$state_dir/calls"

if [ "${1:-}" = "--version" ]; then
  echo "gh version 2.0.0"
  exit 0
fi

if [ "${1:-}" = "api" ]; then
  shift
  endpoint=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -H)
        shift 2
        ;;
      *)
        endpoint="$1"
        shift
        ;;
    esac
  done

  case "$endpoint" in
    repos/owner/repo/releases\?per_page=20)
      cat "$state_dir/releases.json"
      ;;
    https://api.github.com/repos/owner/repo/releases/assets/manifest)
      cat "$state_dir/previous_manifest.json"
      ;;
    https://api.github.com/repos/owner/repo/releases/assets/manifest-dev)
      cat "$state_dir/previous_manifest_dev.json"
      ;;
    https://api.github.com/repos/owner/repo/releases/assets/manifest-stable)
      cat "$state_dir/previous_manifest_stable.json"
      ;;
    *)
      echo "unexpected gh api endpoint: $endpoint" >&2
      exit 2
      ;;
  esac
  exit 0
fi

if [ "${1:-}" != "release" ]; then
  echo "unexpected gh command: $*" >&2
  exit 2
fi

case "${2:-}" in
  view)
    if [ -f "$state_dir/release-exists" ]; then
      case " $* " in
        *" --json assets "*)
          if [ -f "$state_dir/remote-assets" ]; then
            jq_expr=""
            previous=""
            for arg in "$@"; do
              if [ "$previous" = "--jq" ]; then
                jq_expr="$arg"
                break
              fi
              previous="$arg"
            done
            case "$jq_expr" in
              '.assets[].name')
                cut -f1 "$state_dir/remote-assets"
                ;;
              *)
                cat "$state_dir/remote-assets"
                ;;
            esac
          fi
          exit 0
          ;;
      esac
      echo "release exists"
      exit 0
    fi
    echo "release not found" >&2
    exit 1
    ;;
  create)
    touch "$state_dir/release-exists"
    printf 'create:%s\n' "${3:-}" >> "$state_dir/events"
    exit 0
    ;;
  edit)
    printf 'publish:%s\n' "${3:-}" >> "$state_dir/events"
    exit 0
    ;;
  upload)
    asset="${4:-}"
    name="${asset##*/}"
    size="$(wc -c < "$asset" | tr -d '[:space:]')"
    printf 'upload:%s\n' "$name" >> "$state_dir/events"
    {
      if [ -f "$state_dir/remote-assets" ]; then
        awk -F '\t' -v name="$name" '$1 != name' "$state_dir/remote-assets"
      fi
      printf '%s\t%s\n' "$name" "$size"
    } | sort -u > "$state_dir/remote-assets.tmp"
    mv "$state_dir/remote-assets.tmp" "$state_dir/remote-assets"
    exit 0
    ;;
  *)
    echo "unexpected gh release command: $*" >&2
    exit 2
    ;;
esac
SH
chmod +x "$fake_bin/gh"

current_rootfs_sha="$(sha256_of "$assets_dir/rootfs.img")"
previous_manifest_url="https://downloads.example.test/releases/v-prev/rootfs.img"
previous_release_rootfs_url="https://github.example.test/owner/repo/releases/download/v-prev/rootfs.img"

jq -n \
  --arg manifest_api_url "https://api.github.com/repos/owner/repo/releases/assets/manifest" \
  --arg rootfs_url "$previous_release_rootfs_url" \
  '[{draft:false,tag_name:"v-prev",assets:[
    {name:"manifest.json",url:$manifest_api_url,browser_download_url:"https://github.example.test/owner/repo/releases/download/v-prev/manifest.json"},
    {name:"rootfs.img",url:"https://api.github.com/repos/owner/repo/releases/assets/rootfs",browser_download_url:$rootfs_url}
  ]}]' > "$state_dir/releases.json"

jq -n \
  --arg sha "$current_rootfs_sha" \
  --arg url "$previous_manifest_url" \
  '{schema_version:1,parts:[{name:"rootfs",asset:{name:"rootfs.img",size:20,sha256:$sha,url:$url}}]}' \
  > "$state_dir/previous_manifest.json"

outputs_file="$tmp_dir/reuse.outputs"
PATH="$fake_bin:$PATH" \
  FAKE_GH_STATE_DIR="$state_dir" \
  GH_TOKEN="test-token" \
  GITHUB_REPOSITORY="owner/repo" \
  "$resolver_script" \
    --image-dir "$assets_dir" \
    --upload-assets 'boot_a.img boot_b.img oem.img rootfs.img update.img manifest.json' \
    --output "$outputs_file"

if [ "$(output_value rootfs_reused "$outputs_file")" != "true" ]; then
  echo "rootfs asset resolver must reuse a previous release asset when sha256 matches" >&2
  exit 1
fi

rootfs_asset_url="$(output_value rootfs_asset_url "$outputs_file")"
if [ "$rootfs_asset_url" != "$previous_manifest_url" ]; then
  echo "rootfs asset resolver must prefer the previous manifest download URL, got: $rootfs_asset_url" >&2
  exit 1
fi

upload_assets="$(output_value upload_assets "$outputs_file")"
if [ "$upload_assets" != "boot_a.img boot_b.img oem.img update.img manifest.json" ]; then
  echo "rootfs asset resolver must remove rootfs.img from the upload list, got: $upload_assets" >&2
  exit 1
fi

rootfs_asset_metadata="$(output_value rootfs_asset_metadata "$outputs_file")"
if [ "$(printf '%s' "$rootfs_asset_metadata" | jq -r '.name')" != "rootfs.img" ] || \
   [ "$(printf '%s' "$rootfs_asset_metadata" | jq -r '.url')" != "$previous_manifest_url" ] || \
   [ "$(printf '%s' "$rootfs_asset_metadata" | jq -r '.sha256')" != "$current_rootfs_sha" ] || \
   [ "$(printf '%s' "$rootfs_asset_metadata" | jq -r '.image_sha256 // ""')" != "" ]; then
  echo "rootfs asset resolver must output full uncompressed rootfs asset metadata" >&2
  printf '%s\n' "$rootfs_asset_metadata" >&2
  exit 1
fi

private_key="$tmp_dir/test_key.pem"
openssl genpkey -algorithm ED25519 -out "$private_key" 2>/dev/null
manifest_output="$assets_dir/manifest.json"
"$repo_root/scripts/generate_ota_manifest.sh" \
  --version "test-version" \
  --channel "test" \
  --build-time "2026-01-01T00:00:00Z" \
  --sign-key "$private_key" \
  --image-dir "$assets_dir" \
  --asset-metadata "rootfs.img=$rootfs_asset_metadata" \
  --output "$manifest_output"

if [ "$(jq -r '.parts[] | select(.name=="rootfs") | .asset.url' "$manifest_output")" != "$previous_manifest_url" ]; then
  echo "manifest generation must embed the reused rootfs download URL" >&2
  exit 1
fi

log_file="$tmp_dir/release.log"
PATH="$fake_bin:$PATH" \
  FAKE_GH_STATE_DIR="$state_dir" \
  GH_TOKEN="test-token" \
  GITHUB_REPOSITORY="owner/repo" \
  "$repo_root/scripts/create_github_release.sh" \
    --tag-name v-test \
    --release-name "Test Release" \
    --target-commitish "$(git -C "$repo_root" rev-parse HEAD)" \
    --asset-glob "$assets_dir/*" \
    --required-assets 'boot_a.img boot_b.img oem.img rootfs.img update.img manifest.json' \
    --upload-assets "$upload_assets" \
    --retry-count 1 \
    --retry-delay-seconds 0 \
    > "$log_file" 2>&1

if grep -q '^upload:rootfs.img$' "$state_dir/events"; then
  echo "release upload must skip rootfs.img when the previous asset is reused" >&2
  exit 1
fi

if ! grep -q '^upload:manifest.json$' "$state_dir/events"; then
  echo "release upload must still publish the rewritten manifest.json" >&2
  exit 1
fi

rm -f "$outputs_file" "$state_dir/events"
previous_compressed_url="https://downloads.example.test/releases/v-prev/rootfs.img.tar.gz"
previous_compressed_sha="1111111111111111111111111111111111111111111111111111111111111111"
previous_compressed_size=12345

jq -n \
  --arg manifest_api_url "https://api.github.com/repos/owner/repo/releases/assets/manifest" \
  --arg rootfs_url "$previous_compressed_url" \
  '[{draft:false,tag_name:"v-prev",assets:[
    {name:"manifest.json",url:$manifest_api_url,browser_download_url:"https://github.example.test/owner/repo/releases/download/v-prev/manifest.json"},
    {name:"rootfs.img.tar.gz",url:"https://api.github.com/repos/owner/repo/releases/assets/rootfs-archive",browser_download_url:$rootfs_url}
  ]}]' > "$state_dir/releases.json"

jq -n \
  --arg archive_sha "$previous_compressed_sha" \
  --arg image_sha "$current_rootfs_sha" \
  --arg url "$previous_compressed_url" \
  --argjson size "$previous_compressed_size" \
  '{schema_version:1,parts:[{name:"rootfs",asset:{name:"rootfs.img.tar.gz",size:$size,sha256:$archive_sha,image_sha256:$image_sha,url:$url}}]}' \
  > "$state_dir/previous_manifest.json"

PATH="$fake_bin:$PATH" \
  FAKE_GH_STATE_DIR="$state_dir" \
  GH_TOKEN="test-token" \
  GITHUB_REPOSITORY="owner/repo" \
  "$resolver_script" \
    --image-dir "$assets_dir" \
    --upload-assets 'boot_a.img boot_b.img oem.img rootfs.img update.img manifest.json' \
    --output "$outputs_file"

if [ "$(output_value rootfs_reused "$outputs_file")" != "true" ]; then
  echo "rootfs asset resolver must reuse a previous compressed rootfs asset when image_sha256 matches" >&2
  exit 1
fi

compressed_metadata="$(output_value rootfs_asset_metadata "$outputs_file")"
if [ "$(printf '%s' "$compressed_metadata" | jq -r '.name')" != "rootfs.img.tar.gz" ] || \
   [ "$(printf '%s' "$compressed_metadata" | jq -r '.url')" != "$previous_compressed_url" ] || \
   [ "$(printf '%s' "$compressed_metadata" | jq -r '.sha256')" != "$previous_compressed_sha" ] || \
   [ "$(printf '%s' "$compressed_metadata" | jq -r '.image_sha256')" != "$current_rootfs_sha" ] || \
   [ "$(printf '%s' "$compressed_metadata" | jq -r '.size')" != "$previous_compressed_size" ]; then
  echo "rootfs asset resolver must output full compressed rootfs asset metadata" >&2
  printf '%s\n' "$compressed_metadata" >&2
  exit 1
fi

compressed_upload_assets="$(output_value upload_assets "$outputs_file")"
if [ "$compressed_upload_assets" != "boot_a.img boot_b.img oem.img update.img manifest.json" ]; then
  echo "rootfs asset resolver must remove rootfs.img from the upload list when reusing a compressed asset, got: $compressed_upload_assets" >&2
  exit 1
fi

"$repo_root/scripts/generate_ota_manifest.sh" \
  --version "test-version" \
  --channel "test" \
  --build-time "2026-01-01T00:00:00Z" \
  --sign-key "$private_key" \
  --image-dir "$assets_dir" \
  --asset-metadata "rootfs.img=$compressed_metadata" \
  --output "$manifest_output"

if ! jq -e \
  --arg archive_sha "$previous_compressed_sha" \
  --arg image_sha "$current_rootfs_sha" \
  --arg url "$previous_compressed_url" \
  --argjson size "$previous_compressed_size" \
  '.parts[] | select(.name=="rootfs") | .asset |
    .name == "rootfs.img.tar.gz" and
    .url == $url and
    .size == $size and
    .sha256 == $archive_sha and
    .image_sha256 == $image_sha' \
  "$manifest_output" >/dev/null; then
  echo "manifest generation must embed the complete reused compressed rootfs asset metadata" >&2
  jq '.parts[] | select(.name=="rootfs") | .asset' "$manifest_output" >&2
  exit 1
fi

rm -f "$outputs_file" "$state_dir/events"
jq -n \
  --arg archive_sha "$previous_compressed_sha" \
  --arg url "$previous_compressed_url" \
  --argjson size "$previous_compressed_size" \
  '{schema_version:1,parts:[{name:"rootfs",asset:{name:"rootfs.img.tar.gz",size:$size,sha256:$archive_sha,image_sha256:"2222222222222222222222222222222222222222222222222222222222222222",url:$url}}]}' \
  > "$state_dir/previous_manifest.json"

PATH="$fake_bin:$PATH" \
  FAKE_GH_STATE_DIR="$state_dir" \
  GH_TOKEN="test-token" \
  GITHUB_REPOSITORY="owner/repo" \
  "$resolver_script" \
    --image-dir "$assets_dir" \
    --upload-assets 'boot_a.img boot_b.img oem.img rootfs.img update.img manifest.json' \
    --output "$outputs_file"

if [ "$(output_value rootfs_reused "$outputs_file")" != "false" ]; then
  echo "rootfs asset resolver must compare compressed rootfs assets by image_sha256" >&2
  exit 1
fi

rm -f "$outputs_file" "$state_dir/events"
jq -n \
  --arg url "$previous_manifest_url" \
  '{schema_version:1,parts:[{name:"rootfs",asset:{name:"rootfs.img",size:20,sha256:"0000000000000000000000000000000000000000000000000000000000000000",url:$url}}]}' \
  > "$state_dir/previous_manifest.json"

PATH="$fake_bin:$PATH" \
  FAKE_GH_STATE_DIR="$state_dir" \
  GH_TOKEN="test-token" \
  GITHUB_REPOSITORY="owner/repo" \
  "$resolver_script" \
    --image-dir "$assets_dir" \
    --upload-assets 'boot_a.img boot_b.img oem.img rootfs.img update.img manifest.json' \
    --output "$outputs_file"

if [ "$(output_value rootfs_reused "$outputs_file")" != "false" ]; then
  echo "rootfs asset resolver must not reuse a previous asset when sha256 differs" >&2
  exit 1
fi

if [ "$(output_value rootfs_asset_url "$outputs_file")" != "" ]; then
  echo "rootfs asset resolver must leave rootfs_asset_url empty when sha256 differs" >&2
  exit 1
fi

if [ "$(output_value upload_assets "$outputs_file")" != "boot_a.img boot_b.img oem.img rootfs.img update.img manifest.json" ]; then
  echo "rootfs asset resolver must keep rootfs.img in the upload list when sha256 differs" >&2
  exit 1
fi

rm -f "$outputs_file" "$state_dir/events"
dev_rootfs_url="https://downloads.example.test/releases/dev/rootfs.img"
stable_rootfs_url="https://downloads.example.test/releases/stable/rootfs.img"

jq -n \
  '[{draft:false,prerelease:true,tag_name:"v-dev",assets:[
    {name:"manifest.json",url:"https://api.github.com/repos/owner/repo/releases/assets/manifest-dev",browser_download_url:"https://github.example.test/owner/repo/releases/download/v-dev/manifest.json"},
    {name:"rootfs.img",url:"https://api.github.com/repos/owner/repo/releases/assets/rootfs-dev",browser_download_url:"https://github.example.test/owner/repo/releases/download/v-dev/rootfs.img"}
  ]},
  {draft:false,prerelease:false,tag_name:"v-stable",assets:[
    {name:"manifest.json",url:"https://api.github.com/repos/owner/repo/releases/assets/manifest-stable",browser_download_url:"https://github.example.test/owner/repo/releases/download/v-stable/manifest.json"},
    {name:"rootfs.img",url:"https://api.github.com/repos/owner/repo/releases/assets/rootfs-stable",browser_download_url:"https://github.example.test/owner/repo/releases/download/v-stable/rootfs.img"}
  ]}]' > "$state_dir/releases.json"

jq -n \
  --arg sha "$current_rootfs_sha" \
  --arg url "$dev_rootfs_url" \
  '{schema_version:1,channel:"dev-feature",parts:[{name:"rootfs",asset:{name:"rootfs.img",size:20,sha256:$sha,url:$url}}]}' \
  > "$state_dir/previous_manifest_dev.json"

jq -n \
  --arg sha "$current_rootfs_sha" \
  --arg url "$stable_rootfs_url" \
  '{schema_version:1,channel:"stable",parts:[{name:"rootfs",asset:{name:"rootfs.img",size:20,sha256:$sha,url:$url}}]}' \
  > "$state_dir/previous_manifest_stable.json"

PATH="$fake_bin:$PATH" \
  FAKE_GH_STATE_DIR="$state_dir" \
  GH_TOKEN="test-token" \
  GITHUB_REPOSITORY="owner/repo" \
  "$resolver_script" \
    --image-dir "$assets_dir" \
    --channel stable \
    --upload-assets 'boot_a.img boot_b.img oem.img rootfs.img update.img manifest.json' \
    --output "$outputs_file"

if [ "$(output_value rootfs_reused "$outputs_file")" != "true" ]; then
  echo "stable rootfs asset resolver must keep scanning after a newer dev prerelease" >&2
  exit 1
fi

if [ "$(output_value rootfs_asset_url "$outputs_file")" != "$stable_rootfs_url" ]; then
  echo "stable rootfs asset resolver must not reuse a dev prerelease asset" >&2
  echo "got: $(output_value rootfs_asset_url "$outputs_file")" >&2
  exit 1
fi

echo "Reusable rootfs release asset test passed."
