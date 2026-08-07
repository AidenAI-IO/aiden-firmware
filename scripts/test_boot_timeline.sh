#!/bin/sh
# Guards the boot profiler: rcS must keep stock Buildroot semantics (every
# S??* runs once, in order, with "start"), and must keep booting the board
# even when the profiler is broken or absent.
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
RCS="$ROOT_DIR/overlay/etc/init.d/rcS"
HELPER="$ROOT_DIR/overlay/etc/aiden_boot_timeline.sh"

for path in "$RCS" "$HELPER"; do
    if [ ! -f "$path" ]; then
        echo "missing boot timeline file: $path" >&2
        exit 1
    fi
    if [ ! -x "$path" ]; then
        echo "must be executable: $path" >&2
        exit 1
    fi
done

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM
BOOT_TIMELINE_LOCK_FILE="$TMP_DIR/aiden_boot_timeline.lock"
export BOOT_TIMELINE_LOCK_FILE

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

# Fixture init.d: one forked script, one sourced *.sh, one non-matching file,
# and one dangling symlink.
make_fixture() {
    fixture="$1"
    mkdir -p "$fixture"
    cat > "$fixture/S10first" <<EOF
#!/bin/sh
echo "S10first \$1" >> "$TMP_DIR/order.log"
EOF
    cat > "$fixture/S20second.sh" <<EOF
echo "S20second.sh \$1" >> "$TMP_DIR/order.log"
EOF
    cat > "$fixture/S30third" <<EOF
#!/bin/sh
echo "S30third \$1" >> "$TMP_DIR/order.log"
exit 3
EOF
    cat > "$fixture/notaservice" <<EOF
#!/bin/sh
echo "notaservice ran" >> "$TMP_DIR/order.log"
EOF
    chmod +x "$fixture/S10first" "$fixture/S30third" "$fixture/notaservice"
    ln -sf "$fixture/missing-target" "$fixture/S40dangling"
}

run_rcs() {
    # rcS is a boot script: it must succeed even though a fixture returns 3.
    env INIT_D_DIR="$1" \
        BOOT_TIMELINE_HELPER="$2" \
        BOOT_TIMELINE_LOG="$3" \
        BOOT_TIMELINE_ARCHIVE_DIR="$TMP_DIR/archive" \
        sh "$RCS"
}

# --- 1. Stock semantics: order, "start" argument, sourcing, skips ------------
FIXTURE="$TMP_DIR/init.d"
TIMELINE="$TMP_DIR/timeline.log"
make_fixture "$FIXTURE"
: > "$TMP_DIR/order.log"

run_rcs "$FIXTURE" "$HELPER" "$TIMELINE" || fail "rcS exited non-zero"

expected="S10first start
S20second.sh start
S30third start"
actual=$(cat "$TMP_DIR/order.log")
[ "$actual" = "$expected" ] || fail "wrong execution order/args:
--- got ---
$actual
--- want ---
$expected"

grep -q "notaservice ran" "$TMP_DIR/order.log" && fail "ran a non-S??* file"

# --- 2. Timeline records every script, with a usable rc ---------------------
[ -f "$TIMELINE" ] || fail "no timeline written at $TIMELINE"

for event in "rcS:begin" "rcS:end"; do
    grep -q " $event\$" "$TIMELINE" || fail "timeline missing $event"
done
for name in S10first S20second.sh S30third; do
    grep -q "script:begin $name\$" "$TIMELINE" || fail "timeline missing begin for $name"
    grep -q "script:end $name " "$TIMELINE" || fail "timeline missing end for $name"
done
grep -q "script:end S30third rc=3" "$TIMELINE" || fail "timeline lost the exit status of S30third"
grep -q "S40dangling" "$TIMELINE" && fail "dangling symlink should be skipped"

# Records must be "<uptime> <delta> <event>" with numeric, non-decreasing uptime.
awk '
    { if ($1 !~ /^[0-9]+\.[0-9][0-9]$/ || $2 !~ /^[0-9]+\.[0-9][0-9]$/) { print "bad record: " $0; exit 1 }
      if ($1 + 0 < prev) { print "uptime went backwards: " $0; exit 1 }
      prev = $1 + 0 }
' "$TIMELINE" || fail "malformed timeline records"

# --- 3. Fail-open: a missing helper must not stop the boot ------------------
: > "$TMP_DIR/order.log"
run_rcs "$FIXTURE" "$TMP_DIR/no-such-helper.sh" "$TMP_DIR/unused.log" \
    || fail "rcS must still run scripts when the profiler helper is missing"
actual=$(cat "$TMP_DIR/order.log")
[ "$actual" = "$expected" ] || fail "scripts did not run without the profiler:
$actual"

# --- 4. Fail-open: an unwritable timeline must not stop the boot ------------
: > "$TMP_DIR/order.log"
run_rcs "$FIXTURE" "$HELPER" "/proc/definitely/not/writable/timeline.log" \
    || fail "rcS must survive an unwritable timeline path"
actual=$(cat "$TMP_DIR/order.log")
[ "$actual" = "$expected" ] || fail "scripts did not run with an unwritable timeline:
$actual"

# --- 5. Uptime parsing, including leading-zero fractions --------------------
check_cs() {
    raw="$1"
    want="$2"
    printf '%s\n' "$raw" > "$TMP_DIR/uptime"
    got=$(
        BOOT_TIMELINE_UPTIME_PATH="$TMP_DIR/uptime"
        export BOOT_TIMELINE_UPTIME_PATH
        # shellcheck source=/dev/null
        . "$HELPER"
        boot_timeline_now_cs
    )
    [ "$got" = "$want" ] || fail "uptime '$raw' parsed as $got centiseconds, want $want"
}

check_cs "7717.92 6832.62" 771792
check_cs "51.08 40.00" 5108      # leading-zero fraction must not read as octal
check_cs "08.09 1.00" 809        # leading-zero whole part too
check_cs "100 50" 10000          # no fractional part
check_cs "0.00 0.00" 0

# A garbage /proc/uptime must yield 0 rather than an error.
check_cs "garbage" 0

# --- 6. Milestone marks and the report ------------------------------------
BOOT_TIMELINE_LOG="$TIMELINE" "$HELPER" mark config_web:listening
grep -q "mark config_web:listening\$" "$TIMELINE" || fail "mark subcommand did not append"

# mark must not create a timeline out of thin air (no boot context).
BOOT_TIMELINE_LOG="$TMP_DIR/absent.log" "$HELPER" mark agent:listening
[ ! -f "$TMP_DIR/absent.log" ] || fail "mark created a timeline with no boot context"

report=$("$HELPER" report "$TIMELINE")
for section in "boot timeline" "slowest init scripts" "milestones"; do
    printf '%s' "$report" | grep -q "$section" || fail "report missing section: $section"
done
printf '%s' "$report" | grep -q "config_web:listening" || fail "report omitted the milestone"

# --- 7. Archiving keeps a bounded history ---------------------------------
ARCHIVE="$TMP_DIR/archive2"
ARCHIVE_STATE="$TMP_DIR/archive2.state"
mkdir -p "$ARCHIVE"
i=1
while [ "$i" -le 8 ]; do
    printf 'old boot %s\n' "$i" > "$ARCHIVE/boot-2020010$i-000000.log"
    i=$((i + 1))
done

BOOT_TIMELINE_LOG="$TIMELINE" \
BOOT_TIMELINE_ARCHIVE_DIR="$ARCHIVE" \
BOOT_TIMELINE_ARCHIVE_STATE="$ARCHIVE_STATE" \
BOOT_TIMELINE_ARCHIVE_KEEP=3 \
    "$HELPER" archive || fail "archive subcommand failed"

kept=0
for archive_path in "$ARCHIVE"/boot-[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]-[0-9][0-9][0-9][0-9][0-9][0-9].log; do
    [ -f "$archive_path" ] || continue
    kept=$((kept + 1))
done
[ "$kept" -eq 3 ] || fail "archive kept $kept files, expected 3"

# The newest archive must be this boot's timeline, not a pruned old one.
newest="$(cat "$ARCHIVE_STATE")"
[ -f "$newest" ] || fail "archive state points at a missing file: $newest"
grep -q "rcS:begin" "$newest" || fail "newest archive is not the current boot timeline"

# Archiving must be a no-op, not an error, when there is no timeline to save.
BOOT_TIMELINE_LOG="$TMP_DIR/absent.log" \
BOOT_TIMELINE_ARCHIVE_DIR="$ARCHIVE" \
    "$HELPER" archive || fail "archive must succeed when no timeline exists"

# A failed copy must not install a partial completed archive. Simulate cat
# writing some bytes before it fails, which is the case that exposed the direct
# write to boot-*.log.
ATOMIC_BIN="$TMP_DIR/atomic_bin"
ATOMIC_ARCHIVE="$TMP_DIR/archive_atomic"
mkdir -p "$ATOMIC_BIN" "$ATOMIC_ARCHIVE"
cat > "$ATOMIC_BIN/cat" <<'EOF'
#!/bin/sh
printf 'partial\n'
exit 1
EOF
chmod +x "$ATOMIC_BIN/cat"

PATH="$ATOMIC_BIN:$PATH" \
BOOT_TIMELINE_LOG="$TIMELINE" \
BOOT_TIMELINE_ARCHIVE_DIR="$ATOMIC_ARCHIVE" \
BOOT_TIMELINE_ARCHIVE_STATE="$TMP_DIR/atomic.state" \
    "$HELPER" archive || fail "archive must remain fail-open when the copy fails"

for atomic_entry in "$ATOMIC_ARCHIVE"/*; do
    [ -e "$atomic_entry" ] || continue
    fail "failed copy left an archive artifact: $atomic_entry"
done
[ ! -e "$TMP_DIR/atomic.state" ] \
    || fail "failed copy published an archive state file"

# --- 8. Late milestones must reach the archive -----------------------------
# rcS archives at rcS:end, but agent:listening and swap:active are written by
# asynchronous workers and routinely land afterwards. A snapshot taken at
# rcS:end would silently drop them.
LATE_ARCHIVE="$TMP_DIR/archive_late"
LATE_STATE="$TMP_DIR/archive_late.state"
LATE_LOG="$TMP_DIR/late_timeline.log"
mkdir -p "$LATE_ARCHIVE"
printf '0.10 0.10 rcS:begin\n25.52 0.03 rcS:end\n' > "$LATE_LOG"

BOOT_TIMELINE_LOG="$LATE_LOG" \
BOOT_TIMELINE_ARCHIVE_DIR="$LATE_ARCHIVE" \
BOOT_TIMELINE_ARCHIVE_STATE="$LATE_STATE" \
    "$HELPER" archive || fail "archive failed"

[ -s "$LATE_STATE" ] || fail "archive did not publish its path for later refresh"
archived_path="$(cat "$LATE_STATE")"
[ -f "$archived_path" ] || fail "archive state points at a missing file: $archived_path"
grep -q "swap:active" "$archived_path" && fail "archive should not contain the late mark yet"

# A milestone written after rcS finished must refresh the archive in place.
BOOT_TIMELINE_LOG="$LATE_LOG" \
BOOT_TIMELINE_ARCHIVE_STATE="$LATE_STATE" \
    "$HELPER" mark swap:active

grep -q "mark swap:active" "$archived_path" \
    || fail "late milestone did not reach the archive: $(cat "$archived_path")"
grep -q "rcS:end" "$archived_path" || fail "refresh dropped earlier records"
late_archive_count=0
for late_archive_path in "$LATE_ARCHIVE"/*; do
    [ -e "$late_archive_path" ] || continue
    late_archive_count=$((late_archive_count + 1))
done
[ "$late_archive_count" -eq 1 ] \
    || fail "refresh created $late_archive_count archive files; expected one"

# With no archive yet (marks during rcS), refreshing must stay a silent no-op.
BOOT_TIMELINE_LOG="$LATE_LOG" \
BOOT_TIMELINE_ARCHIVE_STATE="$TMP_DIR/no-such.state" \
    "$HELPER" mark usbhid:udc-ready || fail "mark must succeed with no archive state"

# Reproduce the original review race exactly: pause the initial archive after
# its timeline snapshot has been copied but before the archive is installed and
# its state path is published. A milestone arriving in that window must wait on
# the shared lock, then append and refresh the newly published archive.
RACE_BIN="$TMP_DIR/race_bin"
RACE_ARCHIVE="$TMP_DIR/archive_race"
RACE_LOG="$TMP_DIR/race_timeline.log"
RACE_STATE="$TMP_DIR/race.state"
RACE_LOCK="$TMP_DIR/race.lock"
RACE_PAUSED="$TMP_DIR/race.paused"
RACE_RELEASE="$TMP_DIR/race.release"
RACE_UPTIME="$TMP_DIR/race.uptime"
REAL_CAT="$(command -v cat)"
mkdir -p "$RACE_BIN" "$RACE_ARCHIVE"
printf '0.10 0.10 rcS:begin\n25.52 0.03 rcS:end\n' > "$RACE_LOG"
printf '31.41 5.00\n' > "$RACE_UPTIME"
cat > "$RACE_BIN/cat" <<EOF
#!/bin/sh
if [ "\$#" -eq 1 ] && [ "\$1" = "\$RACE_TIMELINE" ] && [ ! -e "\$RACE_PAUSED" ]; then
    "$REAL_CAT" "\$@"
    : > "\$RACE_PAUSED"
    while [ ! -e "\$RACE_RELEASE" ]; do
        sleep 0.01
    done
    exit 0
fi
exec "$REAL_CAT" "\$@"
EOF
chmod +x "$RACE_BIN/cat"

PATH="$RACE_BIN:$PATH" \
RACE_TIMELINE="$RACE_LOG" \
RACE_PAUSED="$RACE_PAUSED" \
RACE_RELEASE="$RACE_RELEASE" \
BOOT_TIMELINE_LOG="$RACE_LOG" \
BOOT_TIMELINE_ARCHIVE_DIR="$RACE_ARCHIVE" \
BOOT_TIMELINE_ARCHIVE_STATE="$RACE_STATE" \
BOOT_TIMELINE_LOCK_FILE="$RACE_LOCK" \
    "$HELPER" archive &
race_archive_pid=$!

i=0
while [ ! -e "$RACE_PAUSED" ]; do
    i=$((i + 1))
    if [ "$i" -ge 500 ]; then
        kill "$race_archive_pid" 2>/dev/null || true
        fail "archive did not reach the pre-publication race point"
    fi
    sleep 0.01
done

PATH="$RACE_BIN:$PATH" \
RACE_TIMELINE="$RACE_LOG" \
RACE_PAUSED="$RACE_PAUSED" \
RACE_RELEASE="$RACE_RELEASE" \
BOOT_TIMELINE_LOG="$RACE_LOG" \
BOOT_TIMELINE_ARCHIVE_DIR="$RACE_ARCHIVE" \
BOOT_TIMELINE_ARCHIVE_STATE="$RACE_STATE" \
BOOT_TIMELINE_LOCK_FILE="$RACE_LOCK" \
BOOT_TIMELINE_UPTIME_PATH="$RACE_UPTIME" \
    "$HELPER" mark agent:listening &
race_mark_pid=$!

sleep 0.1
if ! kill -0 "$race_mark_pid" 2>/dev/null; then
    : > "$RACE_RELEASE"
    wait "$race_archive_pid" 2>/dev/null || true
    fail "milestone writer did not wait for archive publication"
fi
if grep -q 'agent:listening' "$RACE_LOG"; then
    : > "$RACE_RELEASE"
    wait "$race_archive_pid" 2>/dev/null || true
    wait "$race_mark_pid" 2>/dev/null || true
    fail "milestone was appended inside the archive publication window"
fi

: > "$RACE_RELEASE"
wait "$race_archive_pid" || fail "paused archive failed"
wait "$race_mark_pid" || fail "blocked milestone writer failed"

[ -s "$RACE_STATE" ] || fail "race archive did not publish its state"
race_archive_path="$(cat "$RACE_STATE")"
[ -f "$race_archive_path" ] || fail "race state points at a missing archive"
grep -q 'mark agent:listening' "$race_archive_path" \
    || fail "pre-publication milestone was lost from the archive"
[ ! -d "${RACE_LOCK}.d" ] || fail "fallback timeline lock was not released"

# A refresh interrupted between cat and mv leaves a .tmp file in the archive
# directory. It must not count toward retention, or it would evict a real
# archive; and it should be cleaned up.
RETAIN="$TMP_DIR/archive_retain"
mkdir -p "$RETAIN"
i=1
while [ "$i" -le 3 ]; do
    printf 'boot %s\n' "$i" > "$RETAIN/boot-2020010$i-000000.log"
    i=$((i + 1))
done
printf 'partial\n' > "$RETAIN/boot-20200109-000000.log.tmp.999"
printf 'partial\n' > "$RETAIN/boot-20200108-000000.log.tmp.998"
# Anything else that shares the directory must also be ignored by retention,
# not counted as an archive and not deleted.
printf 'notes\n' > "$RETAIN/zz-unrelated.txt"
printf 'not a managed archive\n' > "$RETAIN/boot-not-an-archive.log"

BOOT_TIMELINE_LOG="$TIMELINE" \
BOOT_TIMELINE_ARCHIVE_DIR="$RETAIN" \
BOOT_TIMELINE_ARCHIVE_STATE="$TMP_DIR/retain.state" \
BOOT_TIMELINE_ARCHIVE_KEEP=3 \
    "$HELPER" archive || fail "archive failed with stale temp files present"

for stale_tmp in "$RETAIN"/boot-*.log.tmp.*; do
    [ -e "$stale_tmp" ] || continue
    fail "stale refresh temp file was not cleaned up: $stale_tmp"
done
kept_logs=0
for kept_log in "$RETAIN"/boot-[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]-[0-9][0-9][0-9][0-9][0-9][0-9].log; do
    [ -f "$kept_log" ] || continue
    kept_logs=$((kept_logs + 1))
done
[ "$kept_logs" -eq 3 ] || fail "expected 3 retained archives, got $kept_logs"
# Unrelated files, including an archive-shaped name that the helper never
# generates, must neither be deleted nor consume a retention slot.
[ -f "$RETAIN/zz-unrelated.txt" ] || fail "retention deleted an unrelated file"
[ -f "$RETAIN/boot-not-an-archive.log" ] \
    || fail "retention deleted a non-canonical boot log"
# The current boot's archive is the newest and must have survived retention.
retained_path="$(cat "$TMP_DIR/retain.state")"
[ -f "$retained_path" ] || fail "retention state points at a missing archive"
grep -q "rcS:begin" "$retained_path" \
    || fail "current boot archive was evicted by stale temp files"

echo "PASS: boot timeline profiler"
