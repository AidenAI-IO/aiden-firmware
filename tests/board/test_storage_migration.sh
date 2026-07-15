#!/bin/sh
# On-board end-to-end test for the eMMC -> SD watermark migration
# (docs/04-agent/storage-modes.md §2.4).
#
# Strategy: instead of filling the real 3 GB /userdata, a 100 MB loop-mounted
# ext4 image acts as the "eMMC" via the AIDEN_STORAGE_EMMC_ROOT test hook
# (injected through /userdata/system/env, which aiden-env-run exports to the
# agent on every start). Twenty 4 MB backdated recordings push free space
# below the 10% start watermark; the test then asserts that the oldest files
# stream to the SD card until free space is back at the 30% stop watermark.
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
SSH_OPTS="${SSH_OPTS:--o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=no}"

TEST_ROOT=/tmp/stmig
IMG="$TEST_ROOT/emmc.img"
EMMC="$TEST_ROOT/emmc"
PREFIX=stmigtest_
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

restart_agent() {
    bsh "/etc/init.d/S53agent restart" >/dev/null 2>&1
    sleep 5
}

cleanup() {
    log "--- cleanup"
    # Restore the system env exactly as it was and restart the agent on the
    # real /userdata root.
    bsh "if [ -f $SYS_ENV_BAK ]; then mv $SYS_ENV_BAK $SYS_ENV; else grep -v '^$ENV_LINE\$' $SYS_ENV > $SYS_ENV.tmp 2>/dev/null && mv $SYS_ENV.tmp $SYS_ENV; fi" 2>/dev/null
    restart_agent
    bsh "umount $EMMC 2>/dev/null; rm -rf $TEST_ROOT; rm -f $SD_AUDIO/${PREFIX}*" 2>/dev/null
}
trap cleanup EXIT INT TERM

fatal() {
    log "FATAL: $*"
    exit 1
}

log "=== storage migration on-board test (board: $BOARD) ==="

# --- preflight ---------------------------------------------------------------
bsh "true" || fatal "cannot ssh to $BOARD (set BOARD=user@host)"
[ "$(state_get SD_MOUNTED)" = "1" ] || fatal "SD card not mounted (state: $(bsh "cat $STATE 2>/dev/null" | tr '\n' ' ')); insert a usable card first"
bsh "touch -t 202601010101 /tmp/.stmig-touch && rm -f /tmp/.stmig-touch" || fatal "busybox touch -t unsupported; cannot backdate files"
bsh "which mkfs.ext4 >/dev/null" || fatal "mkfs.ext4 not found on the device"

# --- phase 1: below start watermark => migrate until stop watermark ----------
log "--- phase 1: trigger below 10%, stop at 30%"
bsh "umount $EMMC 2>/dev/null; rm -rf $TEST_ROOT; mkdir -p $EMMC" || fatal "test dir setup failed"
bsh "dd if=/dev/zero of=$IMG bs=1M count=100 2>/dev/null && mkfs.ext4 -F -q $IMG && mount -o loop $IMG $EMMC" \
    || fatal "loop image setup failed"
bsh "mkdir -p $EMMC/audio && rm -f $SD_AUDIO/${PREFIX}*"

# 20 x 4 MB files, mtimes staggered and hours in the past (oldest = 01).
bsh 'for i in $(seq -w 1 20); do
        dd if=/dev/zero of='"$EMMC"'/audio/'"$PREFIX"'$i.wav bs=1M count=4 2>/dev/null || exit 1
        touch -t 2026010100$i '"$EMMC"'/audio/'"$PREFIX"'$i.wav || exit 1
     done' || fatal "test file generation failed"

before_pct="$(loop_free_pct)"
log "  loop fs free before: ${before_pct}%"
[ "$before_pct" -lt 10 ] || fatal "setup did not push free space below 10% (got ${before_pct}%); adjust file count"

bsh "cp $SYS_ENV $SYS_ENV_BAK 2>/dev/null; grep -q '^$ENV_LINE\$' $SYS_ENV 2>/dev/null || echo '$ENV_LINE' >> $SYS_ENV" \
    || fatal "cannot inject $ENV_LINE into $SYS_ENV"
restart_agent

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

after_pct="$(loop_free_pct)"
moved_files="$(state_get MIGRATE_MOVED_FILES)"
detail="$(state_get MIGRATE_DETAIL)"
log "  migration: status=$status moved_files=$moved_files free_after=${after_pct}% detail=$detail"

check "migration finished with status=success" [ "$status" = "success" ]
check "run ended at the stop watermark (detail mentions it)" \
    sh -c "echo \"$detail\" | grep -q 'stop watermark'"
check "loop fs free space recovered to >= 30% (got ${after_pct}%)" [ "$after_pct" -ge 30 ]
check "moved at least one file" [ "${moved_files:-0}" -ge 1 ]
check "did not move everything (newest files stay on eMMC)" [ "${moved_files:-0}" -lt 20 ]

# Oldest-first ordering: every index on SD must be smaller than every index
# still on the loop fs.
max_sd="$(bsh "ls $SD_AUDIO/${PREFIX}*.wav 2>/dev/null" | sed "s/.*${PREFIX}0*//; s/\.wav//" | sort -n | tail -1)"
min_src="$(bsh "ls $EMMC/audio/${PREFIX}*.wav 2>/dev/null" | sed "s/.*${PREFIX}0*//; s/\.wav//" | sort -n | head -1)"
log "  newest index on SD: ${max_sd:-none}; oldest index left on eMMC: ${min_src:-none}"
check "oldest files migrated first (max SD index < min eMMC index)" \
    sh -c "[ -n \"$max_sd\" ] && [ -n \"$min_src\" ] && [ \"$max_sd\" -lt \"$min_src\" ]"
check "newest file (${PREFIX}20.wav) still on eMMC" \
    bsh "test -f $EMMC/audio/${PREFIX}20.wav"
check "migrated files intact on SD (4 MB each)" \
    sh -c "[ -z \"\$(bsh \"find $SD_AUDIO -name '${PREFIX}*.wav' ! -size +4194303c 2>/dev/null\" )\" ]"
check "no partial files left on SD" \
    sh -c "[ -z \"\$(bsh \"ls $SD_AUDIO/${PREFIX}*.aiden-partial 2>/dev/null\")\" ]"

# --- phase 2: above start watermark => no migration ---------------------------
log "--- phase 2: no trigger above 10%"
bsh "umount $EMMC && dd if=/dev/zero of=$IMG bs=1M count=100 2>/dev/null && mkfs.ext4 -F -q $IMG && mount -o loop $IMG $EMMC && mkdir -p $EMMC/audio" \
    || fatal "phase 2 loop image setup failed"
bsh "rm -f $SD_AUDIO/${PREFIX}*"
bsh "dd if=/dev/zero of=$EMMC/audio/${PREFIX}01.wav bs=1M count=4 2>/dev/null && touch -t 202601010001 $EMMC/audio/${PREFIX}01.wav"
restart_agent
sleep 15

check "migration stays idle with plenty of free space" [ "$(state_get MIGRATE_STATUS)" = "idle" ]
check "file untouched on eMMC" bsh "test -f $EMMC/audio/${PREFIX}01.wav"
check "nothing appeared on SD" \
    sh -c "[ -z \"\$(bsh \"ls $SD_AUDIO/${PREFIX}*.wav 2>/dev/null\")\" ]"

# --- report -------------------------------------------------------------------
log "--- agent [storage] log tail"
bsh "grep '\[storage\]' $AGENT_LOG 2>/dev/null | tail -20" | sed 's/^/  | /'

log "=== result: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
