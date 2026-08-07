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

# A listener that exists before launch belongs to another process. start() must
# reject it without spawning config_web; otherwise the readiness poll could
# mistake the unrelated socket for the portal's listener.
PREOCCUPIED_BIN="$TMP_DIR/config_web"
PREOCCUPIED_ENV_RUN="$TMP_DIR/aiden-env-run"
PREOCCUPIED_LAUNCH_LOG="$TMP_DIR/preoccupied.launch"
printf '#!/bin/sh\nexit 0\n' > "$PREOCCUPIED_BIN"
cat > "$PREOCCUPIED_ENV_RUN" <<'EOF'
#!/bin/sh
printf 'launched\n' > "$PREOCCUPIED_LAUNCH_LOG"
exec "$@"
EOF
chmod +x "$PREOCCUPIED_BIN" "$PREOCCUPIED_ENV_RUN"

rc=0
PREOCCUPIED_LAUNCH_LOG="$PREOCCUPIED_LAUNCH_LOG" \
sh -c '
        . "$1"
        BIN="$2"
        ENV_RUN_BIN="$3"
        PID_FILE="$4"
        PROC_NET_TCP="$5"
        CONFIG_PATH="$6/agent.toml"
        SYSTEM_ENV_PATH="$6/system.env"
        PORT=80
        start
    ' _ "$WEB_FUNCS" "$PREOCCUPIED_BIN" "$PREOCCUPIED_ENV_RUN" \
        "$TMP_DIR/preoccupied.pid" "$TCP_LISTENING" "$TMP_DIR/preoccupied" \
        >/dev/null 2>&1 || rc=$?
[ "$rc" -eq 1 ] || fail "start with a preoccupied port returned status $rc, want 1"
[ ! -e "$PREOCCUPIED_LAUNCH_LOG" ] \
    || fail "config_web was launched even though its port was already occupied"

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

# A live process whose port never appears is NOT ready: reporting success there
# would record a config_web:listening milestone for a portal nobody can reach.
# Status must be exactly 1 (failed) — 2 would mean "unverified", which the
# caller treats as started, and the socket table was readable here.
rc=0
sh -c '
        . "$1"
        PID_FILE="$2"
        PROC_NET_TCP="$3"
        STARTUP_TIMEOUT_TICKS=3
        is_running() { [ -n "$1" ]; }
        port_is_listening() { return 1; }
        wait_until_ready
    ' _ "$WEB_FUNCS" "$LIVE_PID_FILE" "$TCP_EMPTY" 2>/dev/null || rc=$?
[ "$rc" -eq 1 ] \
    || fail "a live process that never listened must fail readiness (status 1), got $rc"

# ...unless the socket table itself is unreadable, in which case readiness is
# unknown rather than failed (status 2) and the caller must not tear the
# process down.
rc=0
sh -c '
        . "$1"
        PID_FILE="$2"
        PROC_NET_TCP="$3"
        STARTUP_TIMEOUT_TICKS=3
        is_running() { [ -n "$1" ]; }
        wait_until_ready
    ' _ "$WEB_FUNCS" "$LIVE_PID_FILE" "$TMP_DIR/no-such-proc-net-tcp" || rc=$?
[ "$rc" -eq 2 ] || fail "unreadable socket table must yield status 2, got $rc"

# A process that dies just after the port is observed does not own that socket:
# config_web exits on bind failure, so another process must be holding $PORT.
rc=0
sh -c '
        . "$1"
        PID_FILE="$2"
        PROC_NET_TCP="$3"
        STARTUP_TIMEOUT_TICKS=2
        # Alive when first checked, gone on the confirming re-check.
        checks=0
        is_running() { checks=$((checks + 1)); [ "$checks" -lt 2 ]; }
        wait_until_ready
    ' _ "$WEB_FUNCS" "$LIVE_PID_FILE" "$TCP_LISTENING" 2>/dev/null || rc=$?
[ "$rc" -ne 0 ] \
    || fail "a process that exited right after the port appeared must not be reported ready"

# --- Timeout must be wall-clock bounded, not tick-bounded ------------------
# Without fractional sleep each tick costs a whole second. The budget must be
# derived from the interval so the wait stays ~3s either way.
for funcs in "$WEB_FUNCS" "$DHCP_FUNCS"; do
    ticks=$(sh -c '. "$1"; echo "$STARTUP_TIMEOUT_TICKS"' _ "$funcs" 2>/dev/null)
    interval=$(sh -c '. "$1"; echo "$POLL_INTERVAL_CS"' _ "$funcs" 2>/dev/null)
    case "$ticks" in ''|*[!0-9]*) fail "$funcs: bad tick budget '$ticks'" ;; esac
    case "$interval" in ''|*[!0-9]*) fail "$funcs: bad poll interval '$interval'" ;; esac
    budget=$(( ticks * interval / 100 ))
    [ "$budget" -ge 1 ] && [ "$budget" -le 5 ] \
        || fail "$funcs: wall-clock budget ${budget}s outside 1-5s (ticks=$ticks interval=${interval}cs)"
done

# The important case is a BusyBox without FANCY_SLEEP, where each tick costs a
# whole second. Simulate it with a `sleep` that rejects fractional arguments and
# confirm the scripts shrink the tick budget instead of waiting 60 seconds.
FAKE_BIN="$TMP_DIR/fakebin"
mkdir -p "$FAKE_BIN"
cat > "$FAKE_BIN/sleep" <<'STUB'
#!/bin/sh
case "$1" in
    *.*) exit 1 ;;   # no fractional support
esac
exit 0
STUB
chmod +x "$FAKE_BIN/sleep"

for funcs in "$WEB_FUNCS" "$DHCP_FUNCS"; do
    out=$(PATH="$FAKE_BIN:$PATH" sh -c '. "$1"; echo "$POLL_INTERVAL_CS $STARTUP_TIMEOUT_TICKS"' _ "$funcs" 2>/dev/null)
    slow_interval=${out% *}
    slow_ticks=${out#* }
    [ "$slow_interval" = "100" ] \
        || fail "$funcs: expected whole-second interval without fractional sleep, got '$slow_interval'"
    slow_budget=$(( slow_ticks * slow_interval / 100 ))
    [ "$slow_budget" -le 5 ] \
        || fail "$funcs: without fractional sleep the wait balloons to ${slow_budget}s (ticks=$slow_ticks)"
done

# --- S55aiden_usb_dhcp: poll must bail out on a dead daemon ---------------
# wait_until_running must not spin for its whole budget when the pid is gone;
# with a stale pid file it should fail fast and without sleeping at all.
PID_FILE="$TMP_DIR/dhcp.pid"
echo 999999 > "$PID_FILE"
SLEEP_LOG="$TMP_DIR/short_sleep.log"
: > "$SLEEP_LOG"

# PID_FILE is assigned unconditionally at the top of the script, so it has to be
# overridden after sourcing rather than through the environment.
# Note: inside a shell function $3 is the function's own argument, so the log
# path has to be captured into a variable before short_sleep is redefined.
start_s=$(date +%s)
if sh -c '
        . "$1"
        PID_FILE="$2"
        SLEEP_LOG_PATH="$3"
        STARTUP_TIMEOUT_TICKS=40
        short_sleep() { echo slept >> "$SLEEP_LOG_PATH"; }
        wait_until_running
    ' _ "$DHCP_FUNCS" "$PID_FILE" "$SLEEP_LOG" 2>/dev/null; then
    fail "wait_until_running succeeded for a dead pid"
fi
elapsed=$(( $(date +%s) - start_s ))
[ "$elapsed" -le 2 ] || fail "poll took ${elapsed}s; must fail fast"
[ ! -s "$SLEEP_LOG" ] || fail "dead pid path slept $(wc -l < "$SLEEP_LOG") time(s); must not sleep"

# --- Failure paths must reap the launched child ---------------------------
# Removing the PID file while the child keeps running leaves an untracked
# process that a later start would duplicate.
for script in "$DHCP_SCRIPT" "$WEB_SCRIPT"; do
    grep -q 'terminate_child "\$child_pid"' "$script" \
        || fail "$script must reap \$child_pid before removing the PID file"
done

# terminate_child must actually stop a live process.
sleep 30 &
victim=$!
sh -c '. "$1"; terminate_child "$2"' _ "$DHCP_FUNCS" "$victim" 2>/dev/null || true
sleep 1
if kill -0 "$victim" 2>/dev/null; then
    kill -9 "$victim" 2>/dev/null || true
    fail "terminate_child left the process running"
fi

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
