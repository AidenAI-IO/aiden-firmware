#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
CLEAN_SCRIPT="$ROOT_DIR/scripts/clean_rootfs_overlay_staging.sh"

if [ ! -x "$CLEAN_SCRIPT" ]; then
    echo "missing executable rootfs overlay cleanup script: $CLEAN_SCRIPT" >&2
    exit 1
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

dest_overlay="$tmpdir/dest-overlay"
catalog="$tmpdir/tools.catalog"
mkdir -p "$dest_overlay/etc/init.d"
mkdir -p "$dest_overlay/usr/share/aiden/skills/planner"
mkdir -p "$dest_overlay/usr/share/aiden/skills/device-operator"
mkdir -p "$dest_overlay/usr/share/aiden"

printf 'service\n' > "$dest_overlay/etc/init.d/S53agent"
printf 'actions\n' > "$dest_overlay/usr/share/aiden/quick_actions.json"
printf 'planner\n' > "$dest_overlay/usr/share/aiden/skills/planner/SKILL.md"
printf 'operator\n' > "$dest_overlay/usr/share/aiden/skills/device-operator/SKILL.md"
mkdir -p "$dest_overlay/usr/bin"
printf 'tool\n' > "$dest_overlay/usr/bin/fx"

cat > "$catalog" <<'EOF'
fx|v1.0.0|go|example.com/fx@v1.0.0|linux/arm/v7|-|-|normal
EOF

"$CLEAN_SCRIPT" --catalog "$catalog" --dest-overlay "$dest_overlay"

if [ -e "$dest_overlay/usr/share/aiden" ]; then
    echo "cleanup script must remove legacy rootfs bundled Aiden share staging" >&2
    exit 1
fi

if [ "$(cat "$dest_overlay/etc/init.d/S53agent")" != "service" ]; then
    echo "cleanup script must preserve rootfs etc staging" >&2
    exit 1
fi
if [ -e "$dest_overlay/usr/bin/fx" ]; then
    echo "cleanup script must remove every tool declared by the catalog" >&2
    exit 1
fi

if "$CLEAN_SCRIPT" --catalog "$catalog" --dest-overlay "$tmpdir/missing" 2>"$tmpdir/missing.err"; then
    echo "cleanup script must fail for a missing destination overlay" >&2
    exit 1
fi
if ! grep -q 'missing destination rootfs overlay' "$tmpdir/missing.err"; then
    echo "cleanup script must explain missing destination overlay errors" >&2
    exit 1
fi

echo "rootfs overlay cleanup tests passed"
