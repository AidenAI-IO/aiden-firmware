#!/bin/sh
# S55aiden_usb_dhcp and S56config_web used to sleep a flat second after
# spawning their daemon. rcS is sequential, so that was dead time on the path
# to http://192.168.42.1. Both now poll for readiness instead; these tests
# check the poll is both fast and still a real check.
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DHCP_SCRIPT="$ROOT_DIR/overlay/etc/init.d/S55aiden_usb_dhcp"
WEB_SCRIPT="$ROOT_DIR/overlay/etc/init.d/S56config_web"

for path in "$DHCP_SCRIPT" "$WEB_SCRIPT"; do
    [ -x "$path" ] || { echo "missing executable: $path" >&2; exit 1; }
done

# Neither start path may reintroduce a flat "sleep 1".
for path in "$DHCP_SCRIPT" "$WEB_SCRIPT"; do
    if awk '/^start\(\)/ { in_start = 1 } in_start && /^\tsleep 1$|^    sleep 1$/ { found = 1 } /^}/ { in_start = 0 } END { exit found ? 0 : 1 }' "$path"; then
        echo "$path still sleeps a flat second in start()" >&2
        exit 1
    fi
done

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

fail() { echo "FAIL: $*" >&2; exit 1; }

# --- S56config_web: readiness is "the port is listening" -------------------
# /proc/net/tcp fixture: field 2 is local_address (HEX_IP:HEX_PORT), field 4 is
# the state, 0A = LISTEN.
TCP_LISTENING="$TMP_DIR/tcp_listening"
TCP_EMPTY="$TMP_DIR/tcp_empty"
TCP_OTHER_PORT="$TMP_DIR/tcp_other"
header='  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode'
printf '%s\n   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1\n' "$header" > "$TCP_LISTENING"
printf '%s\n' "$header" > "$TCP_EMPTY"
printf '%s\n   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1\n' "$header" > "$TCP_OTHER_PORT"

# Sourcing an init script runs its trailing case dispatch, which would start a
# real service. Strip the dispatch and keep the function definitions so the
# helpers can be exercised in isolation.
functions_only() {
    sed '/^case "${1:-}" in/,$d' "$1"
}

WEB_FUNCS="$TMP_DIR/config_web_funcs.sh"
DHCP_FUNCS="$TMP_DIR/usb_dhcp_funcs.sh"
functions_only "$WEB_SCRIPT" > "$WEB_FUNCS"
functions_only "$DHCP_SCRIPT" > "$DHCP_FUNCS"
grep -q 'port_is_listening' "$WEB_FUNCS" || fail "could not isolate config_web helpers"
grep -q 'wait_until_running' "$DHCP_FUNCS" || fail "could not isolate usb_dhcp helpers"

probe_port() {
    PORT="$1" PROC_NET_TCP="$2" sh -c '
        . "$1"
        port_is_listening
    ' _ "$WEB_FUNCS" 2>/dev/null
}

probe_port 80 "$TCP_LISTENING" || fail "port 80 LISTEN not detected"
probe_port 80 "$TCP_EMPTY" && fail "reported listening on an empty table"
probe_port 80 "$TCP_OTHER_PORT" && fail "port 80 matched a socket on port 8080"
probe_port 8080 "$TCP_OTHER_PORT" || fail "port 8080 (0x1F90) not detected"
probe_port 80 "$TMP_DIR/no-such-file" && fail "reported listening with no /proc/net/tcp"

# --- S56config_web: the exec race must not be read as a failed start -------
# Regression: aiden-env-run is a shell that execs into config_web, so for the
# first few milliseconds /proc/<pid>/exe still points at the shell and
# is_running() is legitimately false. Treating that as failure made
# S56config_web report rc=1 and delete its pidfile while the portal was in fact
# coming up fine.
LIVE_PID_FILE="$TMP_DIR/live.pid"
echo $$ > "$LIVE_PID_FILE"

sh -c '
        . "$1"
        PID_FILE="$2"
        STARTUP_TIMEOUT_TICKS=40
        polls=0
        # Not config_web yet, and no socket, for the first few polls.
        is_running() { polls=$((polls + 1)); [ "$polls" -ge 5 ]; }
        port_is_listening() { [ "$polls" -ge 5 ]; }
        wait_until_ready
    ' _ "$WEB_FUNCS" "$LIVE_PID_FILE" 2>/dev/null \
    || fail "wait_until_ready gave up during the launcher exec window"

# A pid that is genuinely gone must still fail fast.
DEAD_PID_FILE="$TMP_DIR/dead.pid"
echo 999999 > "$DEAD_PID_FILE"
start_s=$(date +%s)
if sh -c '
        . "$1"
        PID_FILE="$2"
        STARTUP_TIMEOUT_TICKS=40
        wait_until_ready
    ' _ "$WEB_FUNCS" "$DEAD_PID_FILE" 2>/dev/null; then
    fail "wait_until_ready succeeded for a dead pid"
fi
elapsed=$(( $(date +%s) - start_s ))
[ "$elapsed" -le 2 ] || fail "dead pid took ${elapsed}s to detect; must fail fast"

# A live process that never opens the port must still be reported as started
# rather than torn down.
sh -c '
        . "$1"
        PID_FILE="$2"
        STARTUP_TIMEOUT_TICKS=3
        is_running() { [ -n "$1" ]; }
        port_is_listening() { return 1; }
        wait_until_ready
    ' _ "$WEB_FUNCS" "$LIVE_PID_FILE" 2>/dev/null \
    || fail "a live process with no socket must fall back to the liveness check"

# --- S55aiden_usb_dhcp: poll must bail out on a dead daemon ---------------
# wait_until_running must not spin for its whole budget when the pid is gone;
# with a stale pid file it should fail fast.
PID_FILE="$TMP_DIR/dhcp.pid"
echo 999999 > "$PID_FILE"

# PID_FILE is assigned unconditionally at the top of the script, so it has to be
# overridden after sourcing rather than through the environment.
start_s=$(date +%s)
if sh -c '
        . "$1"
        PID_FILE="$2"
        STARTUP_TIMEOUT_TICKS=4
        wait_until_running
    ' _ "$DHCP_FUNCS" "$PID_FILE" 2>/dev/null; then
    fail "wait_until_running succeeded for a dead pid"
fi
elapsed=$(( $(date +%s) - start_s ))
[ "$elapsed" -le 6 ] || fail "poll took ${elapsed}s; budget should be bounded"

# The whole point: a ready daemon is detected without burning a full second.
# Point the poll at this very shell's pid via a stub is_running override.
READY_PID_FILE="$TMP_DIR/ready.pid"
echo $$ > "$READY_PID_FILE"
start_s=$(date +%s)
sh -c '
        . "$1"
        PID_FILE="$2"
        is_running() { [ -n "$1" ]; }
        wait_until_running
    ' _ "$DHCP_FUNCS" "$READY_PID_FILE" 2>/dev/null || fail "wait_until_running missed a live daemon"
elapsed=$(( $(date +%s) - start_s ))
[ "$elapsed" -le 1 ] || fail "ready daemon took ${elapsed}s to detect; poll is too slow"

echo "PASS: service startup polling"
