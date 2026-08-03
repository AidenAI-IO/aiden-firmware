#!/bin/sh
# Aiden boot timeline recorder.
#
# Records monotonic (/proc/uptime) timestamps for each init script and for
# service-defined milestones, so "the board boots slowly" can be answered with
# numbers instead of impressions.
#
# Lives in /etc (not /etc/init.d) so it is available before /oem is mounted and
# so rcS does not try to run it as a service.
#
# Record format, one event per line:
#   <uptime_s> <delta_s> <event> [detail...]
#
# Usage:
#   . /etc/aiden_boot_timeline.sh          # source to get the helper functions
#   /etc/aiden_boot_timeline.sh mark LABEL # append a milestone from any script
#   /etc/aiden_boot_timeline.sh report     # print the current boot's summary

BOOT_TIMELINE_LOG=${BOOT_TIMELINE_LOG:-/var/log/aiden_boot_timeline.log}
BOOT_TIMELINE_ARCHIVE_DIR=${BOOT_TIMELINE_ARCHIVE_DIR:-/userdata/log/boot_timeline}
BOOT_TIMELINE_ARCHIVE_KEEP=${BOOT_TIMELINE_ARCHIVE_KEEP:-5}
BOOT_TIMELINE_UPTIME_PATH=${BOOT_TIMELINE_UPTIME_PATH:-/proc/uptime}

# Centiseconds since boot. Prints an integer, or 0 when /proc/uptime is
# unreadable, so callers never have to guard the result.
boot_timeline_now_cs() {
    _bt_raw=""
    # Guard readability first: an input redirection on a missing path reports on
    # stderr before any "2>/dev/null" on the same command can take effect.
    if [ -r "$BOOT_TIMELINE_UPTIME_PATH" ]; then
        read -r _bt_raw _ < "$BOOT_TIMELINE_UPTIME_PATH" 2>/dev/null || _bt_raw=""
    fi
    case "$_bt_raw" in
        ''|*[!0-9.]*) echo 0; return 0 ;;
    esac

    case "$_bt_raw" in
        *.*)
            _bt_whole="${_bt_raw%%.*}"
            _bt_frac="${_bt_raw#*.}"
            ;;
        *)
            _bt_whole="$_bt_raw"
            _bt_frac="00"
            ;;
    esac

    # Normalise the fraction to exactly two digits: pad, then keep the first two.
    _bt_frac="${_bt_frac}00"
    _bt_frac="${_bt_frac%"${_bt_frac#??}"}"

    # Strip leading zeroes so arithmetic does not treat them as octal.
    _bt_whole="${_bt_whole#"${_bt_whole%%[!0]*}"}"
    [ -n "$_bt_whole" ] || _bt_whole=0
    _bt_frac="${_bt_frac#"${_bt_frac%%[!0]*}"}"
    [ -n "$_bt_frac" ] || _bt_frac=0

    echo "$((_bt_whole * 100 + _bt_frac))"
}

# Render centiseconds as seconds with two decimals.
boot_timeline_fmt_cs() {
    _bt_cs="$1"
    case "$_bt_cs" in
        ''|*[!0-9]*) _bt_cs=0 ;;
    esac
    printf '%d.%02d' "$((_bt_cs / 100))" "$((_bt_cs % 100))"
}

boot_timeline_init() {
    mkdir -p "$(dirname "$BOOT_TIMELINE_LOG")" 2>/dev/null || return 0
    : > "$BOOT_TIMELINE_LOG" 2>/dev/null || return 0
    BOOT_TIMELINE_LAST_CS="$(boot_timeline_now_cs)"
    export BOOT_TIMELINE_LAST_CS
}

# boot_timeline_record EVENT [DETAIL...]
# Never fails: boot must not depend on the profiler.
boot_timeline_record() {
    [ -n "${BOOT_TIMELINE_LOG:-}" ] || return 0
    _bt_now="$(boot_timeline_now_cs)"
    case "${BOOT_TIMELINE_LAST_CS:-}" in
        ''|*[!0-9]*) BOOT_TIMELINE_LAST_CS="$_bt_now" ;;
    esac
    _bt_delta=$((_bt_now - BOOT_TIMELINE_LAST_CS))
    [ "$_bt_delta" -ge 0 ] || _bt_delta=0
    BOOT_TIMELINE_LAST_CS="$_bt_now"
    export BOOT_TIMELINE_LAST_CS
    printf '%s %s %s\n' \
        "$(boot_timeline_fmt_cs "$_bt_now")" \
        "$(boot_timeline_fmt_cs "$_bt_delta")" \
        "$*" >> "$BOOT_TIMELINE_LOG" 2>/dev/null || true
    return 0
}

# Milestone marker for use from service scripts. Unlike boot_timeline_record
# this runs in its own process, so it reports absolute uptime only and leaves
# the caller's delta chain untouched.
boot_timeline_mark() {
    [ -n "${BOOT_TIMELINE_LOG:-}" ] || return 0
    [ -f "$BOOT_TIMELINE_LOG" ] || return 0
    printf '%s %s mark %s\n' \
        "$(boot_timeline_fmt_cs "$(boot_timeline_now_cs)")" \
        "0.00" \
        "$*" >> "$BOOT_TIMELINE_LOG" 2>/dev/null || true
    return 0
}

# Keep the last N boots on persistent storage so boots can be compared.
boot_timeline_archive() {
    [ -f "$BOOT_TIMELINE_LOG" ] || return 0
    mkdir -p "$BOOT_TIMELINE_ARCHIVE_DIR" 2>/dev/null || return 0
    _bt_stamp="$(date '+%Y%m%d-%H%M%S' 2>/dev/null || echo unknown)"
    cat "$BOOT_TIMELINE_LOG" > "$BOOT_TIMELINE_ARCHIVE_DIR/boot-$_bt_stamp.log" 2>/dev/null || return 0

    _bt_count=0
    for _bt_old in $(ls -1 "$BOOT_TIMELINE_ARCHIVE_DIR" 2>/dev/null | sort -r); do
        _bt_count=$((_bt_count + 1))
        if [ "$_bt_count" -gt "$BOOT_TIMELINE_ARCHIVE_KEEP" ]; then
            rm -f "$BOOT_TIMELINE_ARCHIVE_DIR/$_bt_old" 2>/dev/null || true
        fi
    done
    return 0
}

# Human-readable summary: full sequence plus the slowest scripts.
boot_timeline_report() {
    _bt_src="${1:-$BOOT_TIMELINE_LOG}"
    if [ ! -f "$_bt_src" ]; then
        echo "no boot timeline at $_bt_src" >&2
        return 1
    fi

    echo "=== boot timeline ($_bt_src) ==="
    printf '%10s %8s  %s\n' "uptime_s" "delta_s" "event"
    awk '{ ev = $3; for (i = 4; i <= NF; i++) ev = ev " " $i; printf "%10s %8s  %s\n", $1, $2, ev }' "$_bt_src"

    echo
    echo "=== slowest init scripts ==="
    printf '%8s  %s\n' "secs" "script"
    awk '$3 == "script:end" { printf "%8s  %s\n", $2, $4 }' "$_bt_src" | sort -rn | head -15

    echo
    echo "=== milestones ==="
    awk '$3 == "mark" { ev = $4; for (i = 5; i <= NF; i++) ev = ev " " $i; printf "%10s  %s\n", $1, ev }' "$_bt_src"
    return 0
}

# Dispatch only when this file is executed directly. When it is sourced, $0 is
# the caller (rcS, an S?? script, ...) and its positional parameters are ours —
# an init script sourcing us while invoked as "S49usbhid start" must not be read
# as the subcommand "start".
case "${0##*/}" in
    aiden_boot_timeline.sh) ;;
    *) return 0 2>/dev/null || exit 0 ;;
esac

case "${1:-}" in
    mark)
        shift
        boot_timeline_mark "$@"
        ;;
    report)
        shift
        boot_timeline_report "$@"
        ;;
    archive)
        boot_timeline_archive
        ;;
    *)
        echo "Usage: $0 {mark LABEL|report [FILE]|archive}" >&2
        exit 1
        ;;
esac
