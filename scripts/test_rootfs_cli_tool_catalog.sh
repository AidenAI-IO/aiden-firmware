#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CATALOG_LIB="$ROOT_DIR/scripts/rootfs_cli_tool_catalog.sh"
DEFAULT_CATALOG="$ROOT_DIR/scripts/rootfs_cli_tools.catalog"

if [ ! -f "$CATALOG_LIB" ]; then
    echo "missing rootfs CLI tool catalog library: $CATALOG_LIB" >&2
    exit 1
fi
if [ ! -f "$DEFAULT_CATALOG" ]; then
    echo "missing rootfs CLI tool catalog: $DEFAULT_CATALOG" >&2
    exit 1
fi

# shellcheck source=/dev/null
source "$CATALOG_LIB"

default_names="$(rootfs_cli_catalog_names "$DEFAULT_CATALOG" | paste -sd ' ' -)"
if [ "$default_names" != "fq yq rg" ]; then
    echo "default catalog must list fq, yq, and rg in install order, got: $default_names" >&2
    exit 1
fi

default_preserved="$(rootfs_cli_catalog_names "$DEFAULT_CATALOG" preserve | paste -sd ' ' -)"
if [ "$default_preserved" != "fq yq rg" ]; then
    echo "default catalog must preserve fq, yq, and rg bytes, got: $default_preserved" >&2
    exit 1
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

custom_catalog="$tmpdir/custom.catalog"
cat > "$custom_catalog" <<'EOF'
# name|version|kind|source|target|source_sha256|artifact_path|strip_policy
alpha|v1.0.0|go|example.com/alpha@v1.0.0|linux/arm/v7|-|-|preserve
beta|2.0.0|tar_gz|https://example.com/beta.tar.gz|armv7-unknown-linux-musleabihf|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa|beta-2.0.0/beta|normal
EOF

custom_names="$(rootfs_cli_catalog_names "$custom_catalog" | paste -sd ' ' -)"
if [ "$custom_names" != "alpha beta" ]; then
    echo "catalog must expose every configured tool, got: $custom_names" >&2
    exit 1
fi
custom_preserved="$(rootfs_cli_catalog_names "$custom_catalog" preserve | paste -sd ' ' -)"
if [ "$custom_preserved" != "alpha" ]; then
    echo "catalog policy filtering must return only preserved tools, got: $custom_preserved" >&2
    exit 1
fi

duplicate_catalog="$tmpdir/duplicate.catalog"
cat > "$duplicate_catalog" <<'EOF'
alpha|v1.0.0|go|example.com/alpha@v1.0.0|linux/arm/v7|-|-|preserve
alpha|v2.0.0|go|example.com/alpha@v2.0.0|linux/arm/v7|-|-|preserve
EOF
if rootfs_cli_catalog_records "$duplicate_catalog" >"$tmpdir/duplicate.out" 2>"$tmpdir/duplicate.err"; then
    echo "catalog must reject duplicate tool names" >&2
    exit 1
fi
if ! grep -q 'duplicate tool name: alpha' "$tmpdir/duplicate.err"; then
    echo "duplicate tool errors must identify the repeated name" >&2
    exit 1
fi

invalid_policy_catalog="$tmpdir/invalid-policy.catalog"
cat > "$invalid_policy_catalog" <<'EOF'
alpha|v1.0.0|go|example.com/alpha@v1.0.0|linux/arm/v7|-|-|sometimes
EOF
if rootfs_cli_catalog_records "$invalid_policy_catalog" >"$tmpdir/policy.out" 2>"$tmpdir/policy.err"; then
    echo "catalog must reject unknown strip policies" >&2
    exit 1
fi
if ! grep -q 'invalid strip policy for alpha: sometimes' "$tmpdir/policy.err"; then
    echo "strip policy errors must identify the tool and invalid value" >&2
    exit 1
fi

ambiguous_kind_catalog="$tmpdir/ambiguous-kind.catalog"
cat > "$ambiguous_kind_catalog" <<'EOF'
alpha|v1.0.0|archive|https://example.com/alpha.tar.gz|armv7-unknown-linux-musleabihf|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa|alpha/alpha|preserve
EOF
if rootfs_cli_catalog_records "$ambiguous_kind_catalog" >"$tmpdir/kind.out" 2>"$tmpdir/kind.err"; then
    echo "catalog must reject the ambiguous archive build kind" >&2
    exit 1
fi
if ! grep -q 'invalid build kind for alpha: archive' "$tmpdir/kind.err"; then
    echo "build kind errors must identify the tool and invalid value" >&2
    exit 1
fi

unsupported_target_catalog="$tmpdir/unsupported-target.catalog"
cat > "$unsupported_target_catalog" <<'EOF'
alpha|v1.0.0|tar_gz|https://example.com/alpha.tar.gz|armv6-unknown-linux-musleabihf|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa|alpha/alpha|preserve
EOF
if rootfs_cli_catalog_records "$unsupported_target_catalog" >"$tmpdir/target.out" 2>"$tmpdir/target.err"; then
    echo "catalog must reject unsupported archive targets" >&2
    exit 1
fi
if ! grep -q 'unsupported tar_gz target for alpha: armv6-unknown-linux-musleabihf' "$tmpdir/target.err"; then
    echo "target errors must identify the tool and unsupported target" >&2
    exit 1
fi

echo "rootfs CLI tool catalog tests passed"
