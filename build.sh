#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONTAINER_RUNNER="$REPO_ROOT/scripts/build/run_container.sh"

usage() {
  cat >&2 <<'EOF'
Usage:
  ./build.sh app
  ./build.sh firmware [sdk-build-args...]
  ./build.sh exec firmware -- <command> [args...]
EOF
}

if [ "$#" -eq 0 ]; then
  usage
  exit 2
fi

command_name="$1"
shift

case "$command_name" in
  app)
    if [ "$#" -ne 0 ]; then
      usage
      exit 2
    fi
    exec "$CONTAINER_RUNNER" app -- ./scripts/build/container/app.sh
    ;;
  firmware)
    exec "$CONTAINER_RUNNER" firmware -- ./scripts/build/container/firmware.sh "$@"
    ;;
  exec)
    if [ "$#" -lt 3 ]; then
      usage
      exit 2
    fi
    profile="$1"
    shift
    if [ "$profile" != firmware ] || [ "$1" != -- ]; then
      usage
      exit 2
    fi
    shift
    if [ "$#" -eq 0 ]; then
      usage
      exit 2
    fi
    exec "$CONTAINER_RUNNER" "$profile" -- "$@"
    ;;
  *)
    usage
    exit 2
    ;;
esac
