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
mkdir -p "$ARCHIVE"
i=1
while [ "$i" -le 8 ]; do
    printf 'old boot %s\n' "$i" > "$ARCHIVE/boot-2020010$i-000000.log"
    i=$((i + 1))
done

BOOT_TIMELINE_LOG="$TIMELINE" \
BOOT_TIMELINE_ARCHIVE_DIR="$ARCHIVE" \
BOOT_TIMELINE_ARCHIVE_KEEP=3 \
    "$HELPER" archive || fail "archive subcommand failed"

kept=$(ls -1 "$ARCHIVE" | wc -l | tr -d ' ')
[ "$kept" -eq 3 ] || fail "archive kept $kept files, expected 3"

# The newest archive must be this boot's timeline, not a pruned old one.
newest=$(ls -1 "$ARCHIVE" | sort -r | head -1)
grep -q "rcS:begin" "$ARCHIVE/$newest" || fail "newest archive is not the current boot timeline"

# Archiving must be a no-op, not an error, when there is no timeline to save.
BOOT_TIMELINE_LOG="$TMP_DIR/absent.log" \
BOOT_TIMELINE_ARCHIVE_DIR="$ARCHIVE" \
    "$HELPER" archive || fail "archive must succeed when no timeline exists"

echo "PASS: boot timeline profiler"
