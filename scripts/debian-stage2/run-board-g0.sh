#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly STAGE2_OUTPUT=${DEBIAN_STAGE2_OUTPUT_DIR:-${REPO_ROOT}/output/debian-stage2}
readonly BUNDLE_DIR=${DEBIAN_STAGE2_G0_OUTPUT_DIR:-${STAGE2_OUTPUT}/board-g0}
readonly ARCHIVE=${BUNDLE_DIR}/debian-stage2-g0.tar.gz
readonly BOARD_TARGET=${AIDEN_G0_BOARD_TARGET:-aiden@192.168.76.153}
readonly REMOTE_DIR=${AIDEN_G0_REMOTE_DIR:-/home/aiden/debian-stage2-g0}
readonly LOCAL_RESULTS=${AIDEN_G0_LOCAL_RESULTS_DIR:-${BUNDLE_DIR}/board-results}
readonly SSH_BIN=${SSH_BIN:-ssh}
readonly SCP_BIN=${SCP_BIN:-scp}
readonly SSH_PORT=${AIDEN_G0_SSH_PORT:-22}
readonly -a SSH_OPTIONS=(-p "${SSH_PORT}" -o ConnectTimeout=8 -o ServerAliveInterval=10)
readonly -a SCP_OPTIONS=(-P "${SSH_PORT}" -o ConnectTimeout=8)

usage() {
    cat <<'EOF'
Usage: scripts/debian-stage2/run-board-g0.sh ACTION

Actions:
  preflight      Read-only board reachability, OS, storage, service, module, and device checks.
  deploy         Copy and atomically install the proprietary G0 bundle in the user directory.
  loader         Verify the installed bundle and dynamic loader closure.
  module-state   Read-only media/RKNN module and device snapshot.
  load-modules   Temporarily load the Stage 3 media/RGA/RKNN module sequence with sudo.
  rknn           Run the profiled RKNN self-test and fixed-frame benchmark.
  camera         Run a short profiled camera capture.
  audio-capture  Run a short profiled audio capture.
  audio-play     Play the most recent captured PCM sample.
  stress         Run the gated two-hour RKNN/camera/audio concurrent stress test.
  snapshot       Record a board resource snapshot.
  fetch          Copy all board G0 result records back to the host.

Required explicit gates:
  AIDEN_G0_ALLOW_PROPRIETARY_TRANSFER=1  for deploy
  AIDEN_G0_ALLOW_MODULE_LOAD=1           for load-modules
  AIDEN_G0_ALLOW_AUDIO_PLAYBACK=1        for audio-play
  AIDEN_G0_ALLOW_STRESS=1                for stress

Connection/configuration:
  AIDEN_G0_BOARD_TARGET       SSH target (default: aiden@192.168.76.153).
  AIDEN_G0_SSH_PORT           SSH port (default: 22).
  AIDEN_G0_REMOTE_DIR         Must be /home/*/debian-stage2-g0 or /tmp/debian-stage2-g0.
  AIDEN_G0_RKNN_FRAMES        Benchmark frames passed to the remote runner.
  AIDEN_G0_CAMERA_FRAMES      Camera frames passed to the remote runner.
  AIDEN_G0_AUDIO_SECONDS      Audio duration passed to the remote runner.
  AIDEN_G0_STRESS_SECONDS     Concurrent stress duration (default: 7200).
  AIDEN_G0_STRESS_RKNN_FRAMES
                              RKNN frames per stress iteration (default: 1000).
  AIDEN_G0_STRESS_SAMPLE_SECONDS
                              Resource sample interval (default: 2).

This script has no all action. Payload transfer, kernel module loading, and
audio playback are separate operator decisions and cannot be triggered by a
single aggregate command.
EOF
}

fail() {
    echo "Stage 2 board G0 host failure: $*" >&2
    exit 1
}

validate_configuration() {
    local remote_user=
    case "${BOARD_TARGET}" in
        *[!A-Za-z0-9_.@:-]* | '') fail "unsafe AIDEN_G0_BOARD_TARGET" ;;
    esac
    if [[ "${REMOTE_DIR}" =~ ^/home/([A-Za-z0-9._-]+)/debian-stage2-g0$ ]]; then
        remote_user=${BASH_REMATCH[1]}
        [ "${remote_user}" != . ] && [ "${remote_user}" != .. ] ||
            fail "AIDEN_G0_REMOTE_DIR contains an unsafe home component"
    elif [ "${REMOTE_DIR}" != /tmp/debian-stage2-g0 ]; then
        fail "AIDEN_G0_REMOTE_DIR is outside the approved non-system locations"
    fi
    case "${SSH_PORT}" in
        '' | *[!0-9]*) fail "AIDEN_G0_SSH_PORT must be numeric" ;;
    esac
}

run_preflight() {
    "${SSH_BIN}" "${SSH_OPTIONS[@]}" "${BOARD_TARGET}" sh -s -- "${REMOTE_DIR}" <<'EOF'
set -eu
remote_dir=$1
printf '[identity]\n'
id
uname -a
cat /etc/debian_version 2>/dev/null || true
printf '\n[storage]\n'
df -h / /home 2>/dev/null || df -h /
printf '\n[memory]\n'
grep -E '^(MemTotal|MemAvailable|SwapTotal|SwapFree|CmaTotal|CmaAllocated|CmaReleased|CmaFree):' \
    /proc/meminfo
printf '\n[failed-units]\n'
systemctl --failed --no-legend 2>/dev/null || true
printf '\n[modules]\n'
lsmod 2>/dev/null || true
printf '\n[devices]\n'
find /dev -maxdepth 2 \( -name 'rknpu*' -o -name 'video*' -o -name 'media*' \
    -o -name 'v4l-subdev*' -o -name 'snd' -o -path '/dev/mpi/*' \
    -o -path '/dev/dma_heap/*' -o -path '/dev/rk_dma_heap/*' \) \
    -printf '%p %m %u:%g -> %l\n' 2>/dev/null | sort
printf '\n[remote-payload]\n'
if [ -d "${remote_dir}" ]; then
    du -sh "${remote_dir}"
    find "${remote_dir}" -maxdepth 2 -printf '%y %m %u:%g %s %p -> %l\n' | sort
else
    printf 'absent\n'
fi
EOF
}

run_deploy() {
    local expected remote_archive
    [ "${AIDEN_G0_ALLOW_PROPRIETARY_TRANSFER:-0}" = 1 ] ||
        fail "deploy requires AIDEN_G0_ALLOW_PROPRIETARY_TRANSFER=1"
    "${SCRIPT_DIR}/prepare-board-g0.sh" verify
    expected=$(awk '{print $1}' "${ARCHIVE}.sha256")
    remote_archive=${REMOTE_DIR}.tar.gz
    "${SCP_BIN}" "${SCP_OPTIONS[@]}" "${ARCHIVE}" \
        "${BOARD_TARGET}:${remote_archive}"
    "${SSH_BIN}" "${SSH_OPTIONS[@]}" "${BOARD_TARGET}" sh -s -- \
        "${remote_archive}" "${REMOTE_DIR}" "${expected}" <<'EOF'
set -eu
archive=$1
remote_dir=$2
expected=$3
actual=$(sha256sum "${archive}" | awk '{print $1}')
[ "${actual}" = "${expected}" ] || {
    echo "Transferred bundle checksum mismatch" >&2
    exit 1
}
parent=${remote_dir%/*}
base=${remote_dir##*/}
staging=${parent}/.${base}.new.$$
mkdir -p "${staging}"
tar -xzf "${archive}" -C "${staging}"
payload=${staging}/debian-stage2-g0
(cd "${payload}" && sha256sum -c MANIFEST.sha256)
[ "$(readlink "${payload}/lib/librga.so")" = librga.so.2 ]
[ "$(readlink "${payload}/lib/librga.so.2")" = librga.so.2.1.0 ]
if [ -e "${remote_dir}" ]; then
    backup=${remote_dir}.previous.$(date -u +%Y%m%dT%H%M%SZ)
    mv "${remote_dir}" "${backup}"
    printf 'previous_payload=%s\n' "${backup}"
fi
mv "${payload}" "${remote_dir}"
rmdir "${staging}"
printf 'installed_payload=%s\n' "${remote_dir}"
EOF
}

run_remote_action() {
    local action=$1
    local rknn_frames=${AIDEN_G0_RKNN_FRAMES:-1000}
    local camera_frames=${AIDEN_G0_CAMERA_FRAMES:-10}
    local audio_seconds=${AIDEN_G0_AUDIO_SECONDS:-10}
    local stress_seconds=${AIDEN_G0_STRESS_SECONDS:-7200}
    local stress_rknn_frames=${AIDEN_G0_STRESS_RKNN_FRAMES:-1000}
    local stress_sample_seconds=${AIDEN_G0_STRESS_SAMPLE_SECONDS:-2}
    for value in "${rknn_frames}" "${camera_frames}" "${audio_seconds}" \
        "${stress_seconds}" "${stress_rknn_frames}" "${stress_sample_seconds}"; do
        case "${value}" in '' | *[!0-9]*) fail "remote test counts must be numeric" ;; esac
    done
    "${SSH_BIN}" "${SSH_OPTIONS[@]}" "${BOARD_TARGET}" \
        env "AIDEN_G0_RKNN_FRAMES=${rknn_frames}" \
        "AIDEN_G0_CAMERA_FRAMES=${camera_frames}" \
        "AIDEN_G0_AUDIO_SECONDS=${audio_seconds}" \
        "AIDEN_G0_STRESS_SECONDS=${stress_seconds}" \
        "AIDEN_G0_STRESS_RKNN_FRAMES=${stress_rknn_frames}" \
        "AIDEN_G0_STRESS_SAMPLE_SECONDS=${stress_sample_seconds}" \
        "${REMOTE_DIR}/board-g0-remote.sh" "${action}"
}

run_load_modules() {
    [ "${AIDEN_G0_ALLOW_MODULE_LOAD:-0}" = 1 ] ||
        fail "load-modules requires AIDEN_G0_ALLOW_MODULE_LOAD=1"
    "${SSH_BIN}" -tt "${SSH_OPTIONS[@]}" "${BOARD_TARGET}" \
        sudo -- "${REMOTE_DIR}/board-g0-remote.sh" load-modules
}

run_audio_play() {
    [ "${AIDEN_G0_ALLOW_AUDIO_PLAYBACK:-0}" = 1 ] ||
        fail "audio-play requires AIDEN_G0_ALLOW_AUDIO_PLAYBACK=1"
    run_remote_action audio-play
}

run_stress() {
    [ "${AIDEN_G0_ALLOW_STRESS:-0}" = 1 ] ||
        fail "stress requires AIDEN_G0_ALLOW_STRESS=1"
    run_remote_action stress
}

run_fetch() {
    mkdir -p "${LOCAL_RESULTS}"
    "${SCP_BIN}" "${SCP_OPTIONS[@]}" -r \
        "${BOARD_TARGET}:${REMOTE_DIR}/results" "${LOCAL_RESULTS}/"
}

main() {
    local action=${1:-help}
    case "${action}" in
        -h | --help | help)
            usage
            return
            ;;
        preflight | deploy | loader | module-state | load-modules | rknn | camera | \
            audio-capture | audio-play | stress | snapshot | fetch) ;;
        *)
            usage >&2
            exit 2
            ;;
    esac
    validate_configuration
    case "${action}" in
        preflight) run_preflight ;;
        deploy) run_deploy ;;
        loader | module-state | rknn | camera | audio-capture | snapshot)
            run_remote_action "${action}"
            ;;
        load-modules) run_load_modules ;;
        audio-play) run_audio_play ;;
        stress) run_stress ;;
        fetch) run_fetch ;;
    esac
}

main "$@"
