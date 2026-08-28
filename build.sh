#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONTAINER_RUNNER="$REPO_ROOT/scripts/build/run_container.sh"

usage() {
  cat >&2 <<'EOF'
Usage:
  ./build.sh binaries
  ./build.sh image [sdk-build-args...]
  ./build.sh exec image -- <command> [args...]
EOF
}

if [ "$#" -eq 0 ]; then
  usage
  exit 2
fi

command_name="$1"
shift

case "$command_name" in
  binaries)
    if [ "$#" -ne 0 ]; then
      usage
      exit 2
    fi
    exec "$CONTAINER_RUNNER" binaries -- ./scripts/build/container/binaries.sh
    ;;
  image)
    exec "$CONTAINER_RUNNER" image -- ./scripts/build/container/image.sh "$@"
    ;;
  exec)
    if [ "$#" -lt 3 ]; then
      usage
      exit 2
    fi
    profile="$1"
    shift
    if [ "$profile" != image ] || [ "$1" != -- ]; then
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
