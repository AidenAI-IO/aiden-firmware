#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

assets_dir="$tmp_dir/assets"
fake_bin="$tmp_dir/bin"
state_dir="$tmp_dir/state"
mkdir -p "$assets_dir" "$fake_bin" "$state_dir"

printf 'firmware image\n' > "$assets_dir/update.img"
printf '{"version":"v-test"}\n' > "$assets_dir/manifest.json"

cat > "$fake_bin/gh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

state_dir="${FAKE_GH_STATE_DIR:?}"
printf '%s\n' "$*" >> "$state_dir/calls"

if [ "${1:-}" = "--version" ]; then
  echo "gh version 2.0.0"
  exit 0
fi

if [ "${1:-}" != "release" ]; then
  echo "unexpected gh command: $*" >&2
  exit 2
fi

case "${2:-}" in
  view)
    if [ -f "$state_dir/release-exists" ]; then
      echo "release exists"
      exit 0
    fi
    echo "release not found" >&2
    exit 1
    ;;
  create)
    create_count_file="$state_dir/create-count"
    create_count=0
    if [ -f "$create_count_file" ]; then
      create_count="$(cat "$create_count_file")"
    fi
    create_count=$((create_count + 1))
    printf '%s\n' "$create_count" > "$create_count_file"
    case " $* " in
      *" --draft "*)
        ;;
      *)
        echo "release must be created as draft" >&2
        exit 1
        ;;
    esac
    touch "$state_dir/release-exists"
    printf 'create:%s\n' "${3:-}" >> "$state_dir/events"
    if [ "${FAKE_GH_CREATE_PARTIAL_FAILURE:-0}" = "1" ] && [ "$create_count" -eq 1 ]; then
      echo "connection reset after release creation" >&2
      exit 1
    fi
    exit 0
    ;;
  edit)
    case " $* " in
      *" --draft=false "*)
        printf 'publish:%s\n' "${3:-}" >> "$state_dir/events"
        exit 0
        ;;
      *)
        echo "unexpected release edit arguments: $*" >&2
        exit 2
        ;;
    esac
    ;;
  upload)
    asset="${4:-}"
    name="${asset##*/}"
    count_file="$state_dir/upload-$name"
    count=0
    if [ -f "$count_file" ]; then
      count="$(cat "$count_file")"
    fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$count_file"
    printf 'upload:%s:%s\n' "$name" "$count" >> "$state_dir/events"
    if [ "$name" = "update.img" ] && [ "$count" -eq 1 ]; then
      echo "write EPIPE" >&2
      exit 1
    fi
    exit 0
    ;;
  *)
    echo "unexpected gh release command: $*" >&2
    exit 2
    ;;
esac
SH
chmod +x "$fake_bin/gh"

log_file="$tmp_dir/release.log"
if ! PATH="$fake_bin:$PATH" \
  FAKE_GH_STATE_DIR="$state_dir" \
  GH_TOKEN="test-token" \
  GITHUB_REPOSITORY="owner/repo" \
    "$repo_root/scripts/create_github_release.sh" \
      --tag-name v-test \
      --release-name "Test Release" \
      --target-commitish abc123 \
      --asset-glob "$assets_dir/*" \
      --retry-count 3 \
      --retry-delay-seconds 0 \
      >"$log_file" 2>&1; then
  cat "$log_file" >&2
  exit 1
fi

if ! grep -q 'Release upload context' "$log_file"; then
  echo "release script must print upload context debug logs" >&2
  exit 1
fi

if ! grep -q 'write EPIPE' "$log_file"; then
  echo "release script must include failed gh stderr in logs" >&2
  exit 1
fi

if ! grep -q 'Retrying release asset upload.*update.img' "$log_file"; then
  echo "release script must log retry attempts for failed uploads" >&2
  exit 1
fi

if [ "$(grep -c '^upload:update.img:' "$state_dir/events")" -ne 2 ]; then
  echo "release script must retry failed update.img upload once" >&2
  exit 1
fi

if [ "$(grep -c '^upload:manifest.json:' "$state_dir/events")" -ne 1 ]; then
  echo "release script must not retry successful manifest upload" >&2
  exit 1
fi

if ! grep -q '^publish:v-test$' "$state_dir/events"; then
  echo "release script must publish the draft release after uploading assets" >&2
  exit 1
fi

last_upload_line="$(grep -n '^upload:' "$state_dir/events" | tail -n 1 | cut -d: -f1)"
publish_line="$(grep -n '^publish:v-test$' "$state_dir/events" | cut -d: -f1)"
if [ "$publish_line" -le "$last_upload_line" ]; then
  echo "release script must publish only after all assets are uploaded" >&2
  exit 1
fi

rm -f "$state_dir/events" "$state_dir/calls" "$state_dir/release-exists" "$state_dir"/upload-* "$state_dir/create-count"
if ! PATH="$fake_bin:$PATH" \
  FAKE_GH_STATE_DIR="$state_dir" \
  FAKE_GH_CREATE_PARTIAL_FAILURE=1 \
  GH_TOKEN="test-token" \
  GITHUB_REPOSITORY="owner/repo" \
    "$repo_root/scripts/create_github_release.sh" \
      --tag-name v-test \
      --release-name "Test Release" \
      --target-commitish abc123 \
      --asset-glob "$assets_dir/*" \
      --retry-count 3 \
      --retry-delay-seconds 0 \
      >"$log_file" 2>&1; then
  cat "$log_file" >&2
  exit 1
fi

if ! grep -q 'connection reset after release creation' "$log_file"; then
  echo "release script must log partial release creation failures" >&2
  exit 1
fi

if [ "$(grep -c '^create:v-test$' "$state_dir/events")" -ne 1 ]; then
  echo "release script must not retry release creation after the release already exists" >&2
  exit 1
fi

if ! grep -q '^publish:v-test$' "$state_dir/events"; then
  echo "release script must continue and publish after recovering an existing draft release" >&2
  exit 1
fi

echo "GitHub release upload retry test passed."
