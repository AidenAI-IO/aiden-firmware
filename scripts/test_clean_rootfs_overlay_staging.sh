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
mkdir -p "$dest_overlay/etc/init.d"
mkdir -p "$dest_overlay/usr/share/aiden/skills/planner"
mkdir -p "$dest_overlay/usr/share/aiden/skills/device-operator"
mkdir -p "$dest_overlay/usr/share/aiden"

printf 'service\n' > "$dest_overlay/etc/init.d/S53agent"
printf 'mapping\n' > "$dest_overlay/usr/share/aiden/app_mapping.json"
printf 'planner\n' > "$dest_overlay/usr/share/aiden/skills/planner/SKILL.md"
printf 'operator\n' > "$dest_overlay/usr/share/aiden/skills/device-operator/SKILL.md"

"$CLEAN_SCRIPT" --dest-overlay "$dest_overlay"

if [ -e "$dest_overlay/usr/share/aiden/skills" ]; then
    echo "cleanup script must remove legacy rootfs bundled skills staging" >&2
    exit 1
fi

if [ "$(cat "$dest_overlay/usr/share/aiden/app_mapping.json")" != "mapping" ]; then
    echo "cleanup script must preserve rootfs app mapping staging" >&2
    exit 1
fi

if [ "$(cat "$dest_overlay/etc/init.d/S53agent")" != "service" ]; then
    echo "cleanup script must preserve rootfs etc staging" >&2
    exit 1
fi

if "$CLEAN_SCRIPT" --dest-overlay "$tmpdir/missing" 2>"$tmpdir/missing.err"; then
    echo "cleanup script must fail for a missing destination overlay" >&2
    exit 1
fi
if ! grep -q 'missing destination rootfs overlay' "$tmpdir/missing.err"; then
    echo "cleanup script must explain missing destination overlay errors" >&2
    exit 1
fi

echo "rootfs overlay cleanup tests passed"
