#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

exec "$ROOT_DIR/scripts/start_docker_sandbox.sh" --build
