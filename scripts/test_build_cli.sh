#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

test_dir="$(mktemp -d "${TMPDIR:-/tmp}/aiden-build-cli-test.XXXXXX")"
cleanup() {
  rm -rf "$test_dir"
}
trap cleanup EXIT

fixture_repo="$test_dir/repo"
fake_bin="$test_dir/bin"
docker_args_log="$test_dir/docker-args.log"
cleanup_log="$test_dir/cleanup.log"
provision_log="$test_dir/provision.log"
test_aiden_go_root=
test_sudo_exit_code=0
test_ota_public_key_path=
mkdir -p "$fixture_repo/scripts" "$fake_bin"
cp "$ROOT_DIR/build.sh" "$fixture_repo/build.sh"
if [ -d "$ROOT_DIR/scripts/build" ]; then
  cp -R "$ROOT_DIR/scripts/build" "$fixture_repo/scripts/build"
fi

go_cache="$test_dir/go-cache"
go_root="$go_cache/go1.26.0.linux-amd64"
resolved_go_root="$go_root"

cat > "$fake_bin/docker" <<'SH'
#!/bin/sh
: "${FAKE_DOCKER_ARGS_LOG:?}"
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$FAKE_DOCKER_ARGS_LOG"
done
if [ -n "${FAKE_DOCKER_WAIT_FILE:-}" ]; then
  : "${FAKE_DOCKER_SIGNAL_LOG:?}"
  : "${FAKE_DOCKER_PID_FILE:?}"
  printf '%s\n' "$$" > "$FAKE_DOCKER_PID_FILE"
  : > "$FAKE_DOCKER_WAIT_FILE"
  trap 'printf "TERM\n" > "$FAKE_DOCKER_SIGNAL_LOG"; exit 143' TERM
  while :; do sleep 1; done
fi
exit "${FAKE_DOCKER_EXIT_CODE:-0}"
SH
cat > "$fake_bin/id" <<'SH'
#!/bin/sh
case "${1:-}" in
  -u) printf '%s\n' 1234 ;;
  -g) printf '%s\n' 5678 ;;
  *) exit 2 ;;
esac
SH
cat > "$fake_bin/uname" <<'SH'
#!/bin/sh
printf '%s\n' Linux
SH
cat > "$fake_bin/sudo" <<'SH'
#!/bin/sh
: "${FAKE_CLEANUP_LOG:?}"
printf 'sudo' >> "$FAKE_CLEANUP_LOG"
printf ' %s' "$@" >> "$FAKE_CLEANUP_LOG"
printf '\n' >> "$FAKE_CLEANUP_LOG"
exit "${FAKE_SUDO_EXIT_CODE:-0}"
SH
cat > "$fake_bin/chmod" <<'SH'
#!/bin/sh
: "${FAKE_CLEANUP_LOG:?}"
if [ "${1:-}" = -R ]; then
  printf 'chmod' >> "$FAKE_CLEANUP_LOG"
  printf ' %s' "$@" >> "$FAKE_CLEANUP_LOG"
  printf '\n' >> "$FAKE_CLEANUP_LOG"
fi
exec /bin/chmod "$@"
SH
cat > "$fake_bin/curl" <<'SH'
#!/bin/sh
: "${FAKE_PROVISION_LOG:?}"
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then
    output=$2
    shift 2
  else
    shift
  fi
done
[ -n "$output" ] || exit 2
printf 'download\n' >> "$FAKE_PROVISION_LOG"
sleep "${FAKE_CURL_DELAY:-0}"
: > "$output"
SH
cat > "$fake_bin/sha256sum" <<'SH'
#!/bin/sh
if grep -q '^corrupt$' "$1"; then
  printf '%s  %s\n' deadbeef "$1"
  exit 0
fi
printf '%s  %s\n' \
  aac1b08a0fb0c4e0a7c1555beb7b59180b05dfc5a3d62e40e9de90cd42f88235 "$1"
SH
cat > "$fake_bin/tar" <<'SH'
#!/bin/sh
extract_dir=
while [ "$#" -gt 0 ]; do
  if [ "$1" = -C ]; then
    extract_dir=$2
    shift 2
  else
    shift
  fi
done
[ -n "$extract_dir" ] || exit 2
mkdir -p "$extract_dir/go/bin"
printf 'go1.26.0\n' > "$extract_dir/go/VERSION"
cat > "$extract_dir/go/bin/go" <<'GO'
#!/bin/sh
printf '%s\n' 'go version go1.26.0 linux/amd64'
GO
/bin/chmod +x "$extract_dir/go/bin/go"
if [ "${FAKE_TAR_SIGNAL_PARENT:-0}" = 1 ]; then
  kill -TERM "$PPID"
fi
SH
chmod +x "$fake_bin/docker" "$fake_bin/id" "$fake_bin/uname" \
  "$fake_bin/sudo" "$fake_bin/chmod" "$fake_bin/curl" \
  "$fake_bin/sha256sum" "$fake_bin/tar"
: > "$provision_log"

fail() {
  echo "$*" >&2
  exit 1
}

assert_arg() {
  local expected=$1
  grep -Fxq -- "$expected" "$docker_args_log" || \
    fail "missing Docker argument: $expected"
}

assert_no_arg() {
  local unexpected=$1
  if grep -Fxq -- "$unexpected" "$docker_args_log"; then
    fail "unexpected Docker argument: $unexpected"
  fi
}

assert_arg_sequence() {
  local expected_file="$test_dir/expected-args"
  printf '%s\n' "$@" > "$expected_file"
  awk '
    NR == FNR { expected[++count] = $0; next }
    {
      if ($0 == expected[matched + 1]) {
        matched++
        if (matched == count) {
          found = 1
          exit
        }
      } else {
        matched = ($0 == expected[1]) ? 1 : 0
      }
    }
    END { exit(found ? 0 : 1) }
  ' "$expected_file" "$docker_args_log" || \
    fail "missing contiguous Docker argument sequence: $*"
}

assert_user() {
  local expected=$1
  if grep -Eq -- "^(--user|-u)=${expected}$" "$docker_args_log"; then
    return 0
  fi
  awk -v expected="$expected" '
    previous == "-u" || previous == "--user" {
      if ($0 == expected) found = 1
    }
    { previous = $0 }
    END { exit(found ? 0 : 1) }
  ' "$docker_args_log" || fail "missing Docker user: $expected"
}

assert_task() {
  local task=$1
  grep -Eq -- "^(\./|/home/)?scripts/build/container/${task}\.sh$" \
    "$docker_args_log" || fail "missing container task: $task"
}

assert_no_task() {
  local task=$1
  if grep -Eq -- "^(\./|/home/)?scripts/build/container/${task}\.sh$" \
    "$docker_args_log"; then
    fail "unexpected container task: $task"
  fi
}

run_build() {
  local docker_exit_code=$1
  shift
  : > "$docker_args_log"
  : > "$cleanup_log"

  set +e
  (
    cd "$fixture_repo"
    unset all_proxy no_proxy HTTP_PROXY ALL_PROXY NO_PROXY \
      GOPRIVATE GONOSUMDB GONOPROXY
    export PATH="$fake_bin:/usr/bin:/bin"
    export AIDEN_GO_TOOLCHAIN_CACHE="$go_cache"
    if [ -n "$test_aiden_go_root" ]; then
      export AIDEN_GO_ROOT="$test_aiden_go_root"
    else
      unset AIDEN_GO_ROOT
    fi
    if [ -n "$test_ota_public_key_path" ]; then
      export OTA_PUBLIC_KEY_PATH="$test_ota_public_key_path"
    else
      unset OTA_PUBLIC_KEY_PATH
    fi
    export FAKE_DOCKER_ARGS_LOG="$docker_args_log"
    export FAKE_DOCKER_EXIT_CODE="$docker_exit_code"
    export FAKE_CLEANUP_LOG="$cleanup_log"
    export FAKE_SUDO_EXIT_CODE="$test_sudo_exit_code"
    export FAKE_PROVISION_LOG="$provision_log"
    export http_proxy="http://lower-proxy.example:8080"
    export HTTPS_PROXY="http://upper-proxy.example:8443"
    export GOPROXY="https://go-proxy.example"
    export GOSUMDB="off"
    ./build.sh "$@"
  ) > "$test_dir/build-output.log" 2>&1
  build_status=$?
  set -e
}

assert_common_container_contract() {
  assert_arg run
  assert_arg "${resolved_go_root}:/usr/local/go:ro"
  assert_arg 'http_proxy=http://lower-proxy.example:8080'
  assert_arg 'HTTPS_PROXY=http://upper-proxy.example:8443'
  assert_arg 'GOPROXY=https://go-proxy.example'
  assert_arg 'GOSUMDB=off'
  assert_no_arg 'GOPRIVATE='
  assert_no_arg 'GONOPROXY='
}

# Firmware builds use the privileged root profile, propagate Docker failures,
# and still restore ownership and cache write permission after a failed build.
# It must also provision Go from an empty shared cache without requiring an
# application build to have run first.
mkdir -p "$fixture_repo/build" "$fixture_repo/.cache/rootfs-cli-tools/go-mod"
run_build 37 firmware
[ "$build_status" -eq 37 ] || \
  fail "build.sh firmware must return Docker status 37, got $build_status"
resolved_go_root="$(cd "$go_root" && pwd)"
assert_common_container_contract
assert_arg --privileged
assert_user 0:0
assert_task firmware
assert_no_task app
grep -Eq '^sudo chown -R 1234:5678 .*build' "$cleanup_log" || \
  fail "firmware builds must restore output ownership after Docker exits"
grep -Eq '^chmod -R u\+w .*\.cache/rootfs-cli-tools/go-mod' "$cleanup_log" || \
  fail "firmware builds must restore Go module cache write permission after Docker exits"
[ "$(wc -l < "$provision_log" | tr -d ' ')" -eq 1 ] || \
  fail "firmware builds must provision the pinned Go toolchain from an empty cache"

# If sudo is unavailable or unusable, the runner must retain ownership cleanup
# by launching a short-lived root container against the same workspace mount.
test_sudo_exit_code=1
run_build 0 firmware
[ "$build_status" -eq 0 ] || fail "cleanup fallback must not change a successful firmware status"
assert_arg_sequence luckfoxtech/luckfox_pico:1.0 chown -hR 1234:5678 /home/build \
  /home/.cache/rootfs-cli-tools
test_sudo_exit_code=0

# Application builds use the caller identity and map to the application task.
# They share the provisioned toolchain rather than maintaining another cache.
run_build 0 app
[ "$build_status" -eq 0 ] || fail "build.sh app failed with status $build_status"
assert_common_container_contract
assert_user 1234:5678
assert_no_arg --privileged
assert_no_arg 0:0
assert_task app
assert_no_task firmware
[ ! -s "$cleanup_log" ] || fail "application builds must not run firmware ownership cleanup"
[ "$(wc -l < "$provision_log" | tr -d ' ')" -eq 1 ] || \
  fail "application and firmware builds must reuse the same Go toolchain cache"

# The explicit exec form preserves command argument boundaries and uses the
# requested profile instead of invoking the profile's default task.
run_build 0 exec firmware -- bash ./scripts/repack_ota_update_image.sh \
  '--label=release candidate'
[ "$build_status" -eq 0 ] || fail "build.sh exec firmware failed with status $build_status"
assert_common_container_contract
assert_arg --privileged
assert_user 0:0
assert_arg_sequence bash ./scripts/repack_ota_update_image.sh \
  '--label=release candidate'
assert_no_task firmware
assert_no_task app

# There is no implicit build target: callers must choose a stable public verb.
run_build 0
[ "$build_status" -ne 0 ] || fail "build.sh without a command must fail"
[ ! -s "$docker_args_log" ] || fail "build.sh without a command must not start Docker"

# Arbitrary container execution is intentionally limited to the firmware
# profile used by release tooling; app has no public exec form.
run_build 0 exec app -- true
[ "$build_status" -ne 0 ] || fail "build.sh exec app must not be a public command"
[ ! -s "$docker_args_log" ] || fail "build.sh exec app must not start Docker"

# AIDEN_GO_ROOT points at caller-owned state. An invalid override must fail
# without deleting or replacing that directory.
external_go_root="$test_dir/external-go"
mkdir -p "$external_go_root/bin"
printf 'keep\n' > "$external_go_root/sentinel"
printf 'go0.0.0\n' > "$external_go_root/VERSION"
test_aiden_go_root="$external_go_root"
run_build 0 app
[ "$build_status" -ne 0 ] || fail "build.sh app must reject an invalid AIDEN_GO_ROOT"
[ ! -s "$docker_args_log" ] || fail "invalid AIDEN_GO_ROOT must fail before Docker starts"
[ "$(cat "$external_go_root/sentinel")" = keep ] || \
  fail "invalid AIDEN_GO_ROOT must never replace caller-owned state"

# Relative host overrides are resolved from the repository, and OTA keys are
# mounted at a stable absolute path inside the container.
relative_go_root="$fixture_repo/relative-go"
mkdir -p "$relative_go_root/bin" "$fixture_repo/keys"
printf 'go1.26.0\n' > "$relative_go_root/VERSION"
cat > "$relative_go_root/bin/go" <<'SH'
#!/bin/sh
printf '%s\n' 'go version go1.26.0 linux/amd64'
SH
chmod +x "$relative_go_root/bin/go"
printf 'test-key\n' > "$fixture_repo/keys/ota.pem"
relative_go_root_resolved="$(cd "$relative_go_root" && pwd)"
relative_key_resolved="$(cd "$fixture_repo/keys" && pwd)/ota.pem"
test_aiden_go_root=relative-go
test_ota_public_key_path=keys/ota.pem
run_build 0 firmware
[ "$build_status" -eq 0 ] || fail "relative host paths must be accepted from the repository root"
assert_arg "$relative_go_root_resolved:/usr/local/go:ro"
assert_arg 'OTA_PUBLIC_KEY_PATH=/run/aiden/ota_pubkey.pem'
assert_arg "$relative_key_resolved:/run/aiden/ota_pubkey.pem:ro"
test_aiden_go_root=
test_ota_public_key_path=

# CI historically passes repository paths in their container-visible /home
# form. Keep that input compatible when it is not a real host path.
test_ota_public_key_path=/home/keys/ota.pem
run_build 0 firmware
[ "$build_status" -eq 0 ] || fail "container-visible OTA key paths must map back to the repository"
assert_arg "$relative_key_resolved:/run/aiden/ota_pubkey.pem:ro"
test_ota_public_key_path=

# A corrupt cached archive must be discarded and downloaded again before
# extraction, rather than poisoning all later callers of the shared cache.
corrupt_cache="$test_dir/corrupt-go-cache"
mkdir -p "$corrupt_cache"
printf 'corrupt\n' > "$corrupt_cache/go1.26.0.linux-amd64.tar.gz"
: > "$provision_log"
set +e
(
  cd "$fixture_repo"
  PATH="$fake_bin:/usr/bin:/bin" \
    AIDEN_GO_TOOLCHAIN_CACHE="$corrupt_cache" \
    FAKE_DOCKER_ARGS_LOG="$docker_args_log" \
    FAKE_DOCKER_EXIT_CODE=0 \
    FAKE_PROVISION_LOG="$provision_log" \
    ./build.sh app
) > "$test_dir/corrupt-cache.log" 2>&1
corrupt_cache_status=$?
set -e
[ "$corrupt_cache_status" -eq 0 ] || fail "corrupt Go cache archive must be recoverable"
[ "$(wc -l < "$provision_log" | tr -d ' ')" -eq 1 ] || \
  fail "corrupt Go cache archive must be downloaded exactly once"

# An empty lock can remain if a process dies between mkdir and owner metadata.
# The next caller must reclaim it instead of waiting for the full timeout.
stale_cache="$test_dir/stale-go-cache"
stale_lock="$stale_cache/.go1.26.0.linux-amd64.lock"
mkdir -p "$stale_lock"
set +e
(
  cd "$fixture_repo"
  PATH="$fake_bin:/usr/bin:/bin" \
    AIDEN_GO_TOOLCHAIN_CACHE="$stale_cache" \
    AIDEN_GO_TOOLCHAIN_LOCK_TIMEOUT_SECONDS=1 \
    FAKE_DOCKER_ARGS_LOG="$docker_args_log" \
    FAKE_DOCKER_EXIT_CODE=0 \
    FAKE_PROVISION_LOG="$provision_log" \
    ./build.sh app
) > "$test_dir/stale-lock.log" 2>&1
stale_lock_status=$?
set -e
[ "$stale_lock_status" -eq 0 ] || fail "ownerless Go cache lock must be reclaimed"
[ ! -e "$stale_lock" ] || fail "reclaimed Go cache lock must be removed"

# A signal during provisioning must stop the transaction after cleanup; it
# must not continue without holding the cache lock.
provision_signal_cache="$test_dir/provision-signal-cache"
: > "$docker_args_log"
set +e
(
  cd "$fixture_repo"
  PATH="$fake_bin:/usr/bin:/bin" \
    AIDEN_GO_TOOLCHAIN_CACHE="$provision_signal_cache" \
    FAKE_DOCKER_ARGS_LOG="$docker_args_log" \
    FAKE_DOCKER_EXIT_CODE=0 \
    FAKE_PROVISION_LOG="$provision_log" \
    FAKE_TAR_SIGNAL_PARENT=1 \
    ./build.sh app
) > "$test_dir/provision-signal.log" 2>&1
provision_signal_status=$?
set -e
[ "$provision_signal_status" -eq 143 ] || \
  fail "TERM during Go provisioning must return status 143, got $provision_signal_status"
[ ! -s "$docker_args_log" ] || fail "terminated provisioning must not start Docker"
[ ! -e "$provision_signal_cache/.go1.26.0.linux-amd64.lock" ] || \
  fail "terminated provisioning must release its cache lock"

# Concurrent callers share one managed toolchain cache. Provisioning must be
# serialized so one caller cannot remove the toolchain while another mounts it.
concurrent_cache="$test_dir/concurrent-go-cache"
concurrent_provision_log="$test_dir/concurrent-provision.log"
: > "$concurrent_provision_log"
run_concurrent_build() {
  (
    cd "$fixture_repo"
    PATH="$fake_bin:/usr/bin:/bin" \
      AIDEN_GO_TOOLCHAIN_CACHE="$concurrent_cache" \
      FAKE_DOCKER_ARGS_LOG="$docker_args_log" \
      FAKE_DOCKER_EXIT_CODE=0 \
      FAKE_CLEANUP_LOG="$cleanup_log" \
      FAKE_PROVISION_LOG="$concurrent_provision_log" \
      FAKE_CURL_DELAY=1 \
      ./build.sh app
  ) > "$test_dir/concurrent-$1.log" 2>&1
}
run_concurrent_build one &
concurrent_pid_one=$!
run_concurrent_build two &
concurrent_pid_two=$!
set +e
wait "$concurrent_pid_one"
concurrent_status_one=$?
wait "$concurrent_pid_two"
concurrent_status_two=$?
set -e
[ "$concurrent_status_one" -eq 0 ] && [ "$concurrent_status_two" -eq 0 ] || \
  fail "concurrent build callers must both succeed"
[ "$(wc -l < "$concurrent_provision_log" | tr -d ' ')" -eq 1 ] || \
  fail "concurrent build callers must provision the shared Go cache once"

# The runner owns the Docker client process and must forward cancellation so a
# CI signal cannot leave the container running after the wrapper exits.
signal_ready="$test_dir/docker-ready"
signal_log="$test_dir/docker-signal.log"
signal_pid_file="$test_dir/docker-pid"
(
  cd "$fixture_repo"
  export PATH="$fake_bin:/usr/bin:/bin"
  export AIDEN_GO_TOOLCHAIN_CACHE="$concurrent_cache"
  export FAKE_DOCKER_ARGS_LOG="$docker_args_log"
  export FAKE_DOCKER_EXIT_CODE=0
  export FAKE_DOCKER_WAIT_FILE="$signal_ready"
  export FAKE_DOCKER_SIGNAL_LOG="$signal_log"
  export FAKE_DOCKER_PID_FILE="$signal_pid_file"
  exec ./build.sh app
) > "$test_dir/signal.log" 2>&1 &
runner_pid=$!
for _ in $(seq 1 50); do
  [ -e "$signal_ready" ] && break
  sleep 0.1
done
[ -e "$signal_ready" ] || fail "fake Docker did not start for signal test"
kill -TERM "$runner_pid"
for _ in $(seq 1 30); do
  ! kill -0 "$runner_pid" 2>/dev/null && break
  sleep 0.1
done
if kill -0 "$runner_pid" 2>/dev/null; then
  kill -KILL "$(cat "$signal_pid_file")" 2>/dev/null || true
fi
set +e
wait "$runner_pid"
signal_status=$?
set -e
[ "$signal_status" -eq 143 ] || fail "terminated build must return status 143, got $signal_status"
[ "$(cat "$signal_log" 2>/dev/null || true)" = TERM ] || \
  fail "runner must forward TERM to the Docker client"

echo "build CLI contract tests passed"
