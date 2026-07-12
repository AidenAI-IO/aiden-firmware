#!/bin/sh
# On-board end-to-end test for the eMMC -> SD watermark migration
# (docs/04-agent/storage-modes.md §2.4).
#
# Strategy: instead of filling the real 3 GB /userdata, a small loop-mounted
# ext4 image (on /userdata, since /tmp is a ~73 MB tmpfs) acts as the "eMMC"
# via the AIDEN_STORAGE_EMMC_ROOT test hook (injected through
# /userdata/system/env, which aiden-env-run exports to the agent on every
# start). Backdated recordings push free space below the 10% start watermark;
# the test then asserts that the oldest files stream to the SD card until free
# space is back at the 50% stop watermark.
#
# Requirements: board reachable over ssh, an SD card inserted and mounted,
# firmware with the AIDEN_STORAGE_EMMC_ROOT hook.
#
# Usage:
#   BOARD=root@192.168.42.1 ./tests/board/test_storage_migration.sh
#
# The production agent is restarted twice (test start / cleanup); recordings
# made during the test window would land on the loop image, so do not run
# this while the device is in active use.

set -u

BOARD="${BOARD:-root@192.168.42.1}"
# SSH strategy for a frequently reflashed board:
#   - No BatchMode, so ssh falls back to a password prompt when key auth is
#     unavailable (reflashing wipes /userdata and thus authorized_keys).
#   - A shared ControlMaster connection: the FIRST ssh authenticates (one
#     password prompt if no key), every later ssh reuses it with zero prompts,
#     so the dozens of calls this test makes never re-ask.
#   - UserKnownHostsFile=/dev/null + StrictHostKeyChecking=no: a reflashed
#     board gets a new host key; this avoids the "identification changed"
#     refusal without touching your real ~/.ssh/known_hosts.
#   - ServerAlive* drops a hung command instead of blocking forever.
SSH_CTL="${TMPDIR:-/tmp}/stmig-ssh-$$"
SSH_OPTS="${SSH_OPTS:--o ConnectTimeout=10 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ServerAliveInterval=10 -o ServerAliveCountMax=3}"
SSH_OPTS="$SSH_OPTS -o ControlMaster=auto -o ControlPath=$SSH_CTL -o ControlPersist=600"

# The loop image lives on the real eMMC (/userdata, ~2.6 GB free); /tmp is a
# small tmpfs that cannot hold it. The loop mount point below is what the
# migrator sees as "eMMC" via AIDEN_STORAGE_EMMC_ROOT, and its statfs is
# independent of the host /userdata partition.
TEST_ROOT=/userdata/stmig
IMG="$TEST_ROOT/emmc.img"
EMMC="$TEST_ROOT/emmc"
PREFIX=stmigtest_
# 48 MB image (~40 MB usable after ext4 overhead); 18 x 2 MB files = 36 MB
# leaves the loop fs near ~10% free, below the start watermark.
IMG_MB=48
FILE_MB=2
NFILES=18
SYS_ENV=/userdata/system/env
SYS_ENV_BAK=/userdata/system/env.stmig.bak
STATE=/run/aiden/storage.state
SD_AUDIO=/mnt/sdcard/aiden/audio
AGENT_LOG=/var/log/agent/agent.log
ENV_LINE="AIDEN_STORAGE_EMMC_ROOT=$EMMC"

PASS=0
FAIL=0

bsh() {
    # shellcheck disable=SC2086
    ssh $SSH_OPTS "$BOARD" "$@"
}

log() { printf '%s\n' "$*"; }

check() { # check <description> <command...>
    desc="$1"
    shift
    if "$@"; then
        PASS=$((PASS + 1))
        log "  PASS: $desc"
    else
        FAIL=$((FAIL + 1))
        log "  FAIL: $desc"
    fi
}

state_get() { # state_get KEY
    bsh "sed -n 's/^$1=//p' $STATE 2>/dev/null" | head -1
}

# Free percentage of the loop filesystem, integer.
loop_free_pct() {
    bsh "df -k $EMMC 2>/dev/null" | awk 'NR==2 { if ($2 > 0) printf "%d", $4 * 100 / $2; else print 0 }'
}

# (Re)create the loop-mounted ext4 image that stands in for eMMC. Each step
# reports its own failure so a broken run points at the exact cause.
make_loop_image() {
    out="$(bsh "umount $EMMC 2>/dev/null; rm -rf $TEST_ROOT; mkdir -p $EMMC" 2>&1)" \
        || fatal "cannot prepare $TEST_ROOT: $out"
    out="$(bsh "dd if=/dev/zero of=$IMG bs=1M count=$IMG_MB 2>&1")" \
        || fatal "dd of ${IMG_MB}MB image failed: $out"
    out="$(bsh "mkfs.ext4 -F -q $IMG 2>&1")" \
        || fatal "mkfs.ext4 failed on $IMG: $out"
    out="$(bsh "mount -o loop $IMG $EMMC 2>&1")" \
        || fatal "mount -o loop failed for $IMG: $out"
    out="$(bsh "mkdir -p $EMMC/audio 2>&1")" \
        || fatal "cannot create $EMMC/audio: $out"
}

restart_agent() {
    # S53agent forks a long-lived watchdog. Over ssh that child inherits the
    # session's stdout pipe, so ssh blocks until the watchdog exits (i.e.
    # forever). setsid + full fd redirection detaches it so ssh returns at
    # once; the ServerAlive guard in SSH_OPTS is a last resort.
    bsh 'setsid /etc/init.d/S53agent restart >/dev/null 2>&1 </dev/null & exit 0' \
        >/dev/null 2>&1
}

# agent_env_root reads AIDEN_STORAGE_EMMC_ROOT from the *running* agent
# process, so we can confirm a restart actually picked up the injected env
# instead of racing an old process that still uses /userdata.
agent_env_root() {
    bsh 'pid="$(cat /run/agent/agent.pid 2>/dev/null)"; [ -n "$pid" ] && tr "\0" "\n" < /proc/$pid/environ 2>/dev/null | sed -n "s/^AIDEN_STORAGE_EMMC_ROOT=//p"' | head -1
}

# restart_and_wait_env restarts the agent and blocks until the running agent
# reports the expected AIDEN_STORAGE_EMMC_ROOT ("" = must be unset).
restart_and_wait_env() {
    want="$1"
    restart_agent
    i=0
    while [ "$i" -lt 20 ]; do
        got="$(agent_env_root)"
        if [ "$got" = "$want" ]; then
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    log "  (warning) agent env root = '$(agent_env_root)', expected '$want' after 20s"
    return 1
}

cleanup() {
    log "--- cleanup"
    # Restore the system env exactly as it was and restart the agent on the
    # real /userdata root.
    bsh "if [ -f $SYS_ENV_BAK ]; then mv $SYS_ENV_BAK $SYS_ENV; else grep -v '^$ENV_LINE\$' $SYS_ENV > $SYS_ENV.tmp 2>/dev/null && mv $SYS_ENV.tmp $SYS_ENV; fi" 2>/dev/null
    restart_agent
    bsh "umount $EMMC 2>/dev/null; rm -rf $TEST_ROOT; rm -f $SD_AUDIO/${PREFIX}*" 2>/dev/null
    # Close the shared ssh connection.
    ssh $SSH_OPTS -O exit "$BOARD" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

fatal() {
    log "FATAL: $*"
    exit 1
}

log "=== storage migration on-board test (board: $BOARD) ==="

# --- preflight ---------------------------------------------------------------
# Open the shared master connection first. This is the one and only place a
# password may be prompted (when key auth is unavailable, e.g. right after a
# reflash); every bsh call afterwards reuses it silently.
log "--- connecting to $BOARD (enter the board password if prompted)"
ssh $SSH_OPTS "$BOARD" true \
    || fatal "cannot connect to $BOARD. Check the address (BOARD=user@host), that the board is reachable, and the password."
[ "$(state_get SD_MOUNTED)" = "1" ] || fatal "SD card not mounted (state: $(bsh "cat $STATE 2>/dev/null" | tr '\n' ' ')); insert a usable card first"
bsh "touch -t 202601010101 /tmp/.stmig-touch && rm -f /tmp/.stmig-touch" || fatal "busybox touch -t unsupported; cannot backdate files"
bsh "which mkfs.ext4 >/dev/null" || fatal "mkfs.ext4 not found on the device"

# --- phase 1: below start watermark => migrate until stop watermark ----------
log "--- phase 1: trigger below 10%, stop at 50%"
make_loop_image
bsh "rm -f $SD_AUDIO/${PREFIX}*"

# NFILES x FILE_MB files, mtimes staggered minutes into the past so the
# oldest (index 01) sorts first. Names zero-padded so lexical == numeric.
# touch -t stamp: 2026-01-01 01:MM, MM = index (01..NFILES, <60).
bsh 'i=1; while [ $i -le '"$NFILES"' ]; do
        n=$(printf %02d $i)
        dd if=/dev/zero of='"$EMMC"'/audio/'"$PREFIX"'$n.wav bs=1M count='"$FILE_MB"' 2>/dev/null || exit 1
        touch -t 2026010101$n '"$EMMC"'/audio/'"$PREFIX"'$n.wav || exit 1
        i=$((i + 1))
     done' || fatal "test file generation failed"

before_pct="$(loop_free_pct)"
log "  loop fs free before: ${before_pct}%"
[ "$before_pct" -lt 10 ] || fatal "setup did not push free space below 10% (got ${before_pct}%); increase NFILES/FILE_MB or shrink IMG_MB"

bsh "cp $SYS_ENV $SYS_ENV_BAK 2>/dev/null; grep -q '^$ENV_LINE\$' $SYS_ENV 2>/dev/null || echo '$ENV_LINE' >> $SYS_ENV" \
    || fatal "cannot inject $ENV_LINE into $SYS_ENV"

# Restart and confirm the *running* agent actually picked up the test root,
# otherwise the migration would run against the real /userdata and never fire.
restart_and_wait_env "$EMMC" \
    || fatal "agent did not start with AIDEN_STORAGE_EMMC_ROOT=$EMMC (running root: '$(agent_env_root)')"

log "  waiting for migration to finish (timeout 180s)..."
deadline=$(( $(date +%s) + 180 ))
status=""
while [ "$(date +%s)" -lt "$deadline" ]; do
    status="$(state_get MIGRATE_STATUS)"
    case "$status" in
        success|failed) break ;;
    esac
    sleep 2
done

after_pct="$(loop_free_pct)"; after_pct="${after_pct:-0}"
moved_files="$(state_get MIGRATE_MOVED_FILES)"; moved_files="${moved_files:-0}"
detail="$(state_get MIGRATE_DETAIL)"
newest=$(printf %02d "$NFILES")
min_file_bytes=$(( FILE_MB * 1024 * 1024 - 1 ))
# Resolve all remote queries into host-side variables BEFORE the checks, so
# every check is a plain local comparison (a check running bsh inside sh -c
# would not see the function).
max_sd="$(bsh "ls $SD_AUDIO/${PREFIX}*.wav 2>/dev/null" | sed "s/.*${PREFIX}0*//; s/\.wav//" | sort -n | tail -1)"
min_src="$(bsh "ls $EMMC/audio/${PREFIX}*.wav 2>/dev/null" | sed "s/.*${PREFIX}0*//; s/\.wav//" | sort -n | head -1)"
newest_present="$(bsh "test -f $EMMC/audio/${PREFIX}${newest}.wav && echo yes")"
undersized="$(bsh "find $SD_AUDIO -name '${PREFIX}*.wav' ! -size +${min_file_bytes}c 2>/dev/null")"
partials="$(bsh "ls $SD_AUDIO/${PREFIX}*.aiden-partial 2>/dev/null")"

log "  migration: status=$status moved_files=$moved_files free_after=${after_pct}% detail=$detail"
log "  newest index on SD: ${max_sd:-none}; oldest index left on eMMC: ${min_src:-none}"

check "migration finished with status=success" [ "$status" = "success" ]
case "$detail" in *"stop watermark"*) check "run ended at the stop watermark" true ;; *) check "run ended at the stop watermark (got: $detail)" false ;; esac
check "loop fs free space recovered to >= 50% (got ${after_pct}%)" [ "$after_pct" -ge 50 ]
check "moved at least one file" [ "$moved_files" -ge 1 ]
check "did not move everything (newest files stay on eMMC)" [ "$moved_files" -lt "$NFILES" ]
check "oldest files migrated first (max SD index ${max_sd:-none} < min eMMC index ${min_src:-none})" \
    test -n "$max_sd" -a -n "$min_src" -a "${max_sd:-0}" -lt "${min_src:-0}"
check "newest file (${PREFIX}${newest}.wav) still on eMMC" [ "$newest_present" = "yes" ]
check "migrated files intact on SD (${FILE_MB} MB each)" [ -z "$undersized" ]
check "no partial files left on SD" [ -z "$partials" ]

# --- phase 2: above start watermark => no migration ---------------------------
log "--- phase 2: no trigger above 10%"
make_loop_image
bsh "rm -f $SD_AUDIO/${PREFIX}*"
bsh "dd if=/dev/zero of=$EMMC/audio/${PREFIX}01.wav bs=1M count=$FILE_MB 2>/dev/null && touch -t 202601010101 $EMMC/audio/${PREFIX}01.wav"
p2_pct="$(loop_free_pct)"; p2_pct="${p2_pct:-100}"
log "  loop fs free (phase 2): ${p2_pct}%"
[ "$p2_pct" -ge 10 ] || fatal "phase 2 setup unexpectedly below 10% (got ${p2_pct}%)"

restart_and_wait_env "$EMMC" \
    || fatal "agent did not restart with the test root for phase 2 (running root: '$(agent_env_root)')"
sleep 15

p2_status="$(state_get MIGRATE_STATUS)"
p2_file_present="$(bsh "test -f $EMMC/audio/${PREFIX}01.wav && echo yes")"
p2_sd="$(bsh "ls $SD_AUDIO/${PREFIX}*.wav 2>/dev/null")"
check "migration stays idle with plenty of free space (got '$p2_status')" [ "$p2_status" = "idle" ]
check "file untouched on eMMC" [ "$p2_file_present" = "yes" ]
check "nothing appeared on SD" [ -z "$p2_sd" ]

# --- report -------------------------------------------------------------------
log "--- agent [storage] log tail"
bsh "grep '\[storage\]' $AGENT_LOG 2>/dev/null | tail -20" | sed 's/^/  | /'

log "=== result: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
