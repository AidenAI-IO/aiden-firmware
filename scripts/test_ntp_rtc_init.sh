#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
NTP_INIT="$ROOT_DIR/overlay/etc/init.d/S49ntp"
NTP_WATCHDOG="$ROOT_DIR/overlay/etc/init.d/S50ntp_watchdog"
RTC_INIT="$ROOT_DIR/overlay/etc/init.d/S99rtcinit"
BOOT_CONF="$ROOT_DIR/overlay/etc/aiden_boot.conf"
NTP_CONF="$ROOT_DIR/overlay/etc/ntp.conf"
CONFIG_WEB="$ROOT_DIR/src/config_web.cpp"
IPV4_OCTET='(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])'
IPV4_RE="${IPV4_OCTET}(\\.${IPV4_OCTET}){3}"

for script in "$NTP_INIT" "$NTP_WATCHDOG" "$RTC_INIT"; do
    if [ ! -x "$script" ]; then
        echo "missing executable $(basename "$script")" >&2
        exit 1
    fi
    if ! sh -n "$script"; then
        echo "$(basename "$script") must be valid POSIX shell syntax" >&2
        exit 1
    fi
done

if [ -e "$ROOT_DIR/overlay/etc/init.d/S39rtcinit" ]; then
    echo "rtc init must remain S99rtcinit so it overrides the SDK default script" >&2
    exit 1
fi

if [ ! -r "$BOOT_CONF" ]; then
    echo "aiden_boot.conf not found or unreadable" >&2
    exit 1
fi

# S49ntp must NOT block on network readiness — the watchdog handles sync
# attempts. The script should also no longer rely on hostname resolution:
# NTP_FALLBACK_SERVER defaults to a numeric IP.
if grep -q 'wait_for_network' "$NTP_INIT"; then
    echo "S49ntp must not poll for network readiness (use watchdog instead)" >&2
    exit 1
fi

if ! grep -Eq "^: \"\\$\\{NTP_FALLBACK_SERVER:=${IPV4_RE}\\}\"$" "$NTP_INIT"; then
    echo "S49ntp NTP_FALLBACK_SERVER default must be an IPv4 literal" >&2
    exit 1
fi

if ! grep -Eq '^[[:space:]]*step\)' "$NTP_INIT"; then
    echo "S49ntp must expose a 'step' subcommand for one-shot sync" >&2
    exit 1
fi

if ! grep -Eq 'ntpd -gq -c' "$NTP_INIT"; then
    echo "S49ntp 'step' must invoke 'ntpd -gq -c <conf>' for query-and-exit sync" >&2
    exit 1
fi

# Reference ntpd (4.2.x) is the implementation on this SDK: -p is "pidfile"
# in that impl, so the server MUST come via -c <conf>, never on argv.
if sed 's/[[:space:]]*#.*//' "$NTP_INIT" | grep -Eq '^[[:space:]]*/usr/sbin/ntpd([[:space:]].*)?[[:space:]]-p([[:space:]]|$)'; then
    echo "S49ntp must not pass servers via -p (reference ntpd treats -p as pidfile)" >&2
    exit 1
fi

# S50ntp_watchdog must periodically check clock sync status and call S49ntp step.
if ! grep -q 'is_clock_synced' "$NTP_WATCHDOG"; then
    echo "S50ntp_watchdog must implement clock sync status check" >&2
    exit 1
fi

if ! grep -q 'S49ntp step' "$NTP_WATCHDOG"; then
    echo "S50ntp_watchdog must call 'S49ntp step' when clock is not synced" >&2
    exit 1
fi

if ! grep -q 'NTP_WATCHDOG_INTERVAL' "$NTP_WATCHDOG"; then
    echo "S50ntp_watchdog must respect NTP_WATCHDOG_INTERVAL from boot config" >&2
    exit 1
fi

if ! grep -q 'NTP_WATCHDOG_TIMEOUT' "$NTP_WATCHDOG"; then
    echo "S50ntp_watchdog must respect NTP_WATCHDOG_TIMEOUT from boot config" >&2
    exit 1
fi

# config_web must NOT use the aiden.script hook (it no longer exists).
if grep -q -- '-s /etc/udhcpc/aiden.script' "$CONFIG_WEB"; then
    echo "config_web must not reference removed aiden.script hook" >&2
    exit 1
fi

if ! grep -q 'udhcpc -i .* -n -q' "$CONFIG_WEB"; then
    echo "config_web must still invoke udhcpc for DHCP (without hook)" >&2
    exit 1
fi

# Boot conf must carry the new watchdog settings and must not carry obsolete
# DHCP hook or wait settings.
if ! grep -Eq "^NTP_WATCHDOG_INTERVAL=[0-9]+$" "$BOOT_CONF"; then
    echo "aiden_boot.conf must define NTP_WATCHDOG_INTERVAL" >&2
    exit 1
fi

if ! grep -Eq "^NTP_WATCHDOG_TIMEOUT=[0-9]+$" "$BOOT_CONF"; then
    echo "aiden_boot.conf must define NTP_WATCHDOG_TIMEOUT" >&2
    exit 1
fi

if ! grep -Eq "^NTP_FALLBACK_SERVER=${IPV4_RE}$" "$BOOT_CONF"; then
    echo "aiden_boot.conf must define NTP_FALLBACK_SERVER as an IPv4 literal" >&2
    exit 1
fi

for stale in ENABLE_DHCPCD NTP_WAIT_NETWORK NTP_WAIT_TIMEOUT NTP_WAIT_INTERVAL NTP_WAIT_DNS_HOST; do
    if grep -q "^${stale}=" "$BOOT_CONF"; then
        echo "aiden_boot.conf still carries obsolete $stale setting" >&2
        exit 1
    fi
done

# S41dhcpcd and udhcpc hook must be removed (watchdog replaces them).
if [ -e "$ROOT_DIR/overlay/etc/init.d/S41dhcpcd" ]; then
    echo "S41dhcpcd must be removed (watchdog replaces DHCP-driven sync)" >&2
    exit 1
fi

if [ -e "$ROOT_DIR/overlay/etc/udhcpc/aiden.script" ]; then
    echo "udhcpc/aiden.script must be removed (watchdog replaces hook-driven sync)" >&2
    exit 1
fi

# overlay must ship an IP-only /etc/ntp.conf to override the SDK's
# pool.ntp.org default — DNS may not be up at the time ntpd starts.
if [ ! -f "$NTP_CONF" ]; then
    echo "overlay/etc/ntp.conf must exist (IP-based override of SDK default)" >&2
    exit 1
fi

if grep -Eq '^[[:space:]]*server[[:space:]]+[a-zA-Z]' "$NTP_CONF"; then
    echo "overlay/etc/ntp.conf must not reference servers by hostname" >&2
    exit 1
fi

if ! grep -Eq "^[[:space:]]*server[[:space:]]+${IPV4_RE}([[:space:]]|$)" "$NTP_CONF"; then
    echo "overlay/etc/ntp.conf must list at least one IPv4 server line" >&2
    exit 1
fi

if ! grep -q 'RTC_DEFAULT_DATE:=2026-06-09' "$RTC_INIT"; then
    echo "S99rtcinit must default invalid RTC values to 2026-06-09" >&2
    exit 1
fi

if ! grep -q 'baseline_year="${RTC_DEFAULT_DATE%%-*}"' "$RTC_INIT"; then
    echo "S99rtcinit must derive the stale RTC threshold from RTC_DEFAULT_DATE" >&2
    exit 1
fi

if grep -q 'RTC_MIN_YEAR' "$RTC_INIT"; then
    echo "S99rtcinit must not duplicate the RTC baseline year" >&2
    exit 1
fi

if ! grep -q 'rtc_needs_calibration' "$RTC_INIT"; then
    echo "S99rtcinit must validate the RTC year against its baseline" >&2
    exit 1
fi

if ! grep -q 'system_date_before_default' "$RTC_INIT"; then
    echo "S99rtcinit must not clobber an already-sane system clock" >&2
    exit 1
fi

if ! grep -q 'date -s "$RTC_DEFAULT_DATE"' "$RTC_INIT"; then
    echo "S99rtcinit must set the system time from RTC_DEFAULT_DATE" >&2
    exit 1
fi

if ! grep -q 'hwclock -w' "$RTC_INIT"; then
    echo "S99rtcinit must write the calibrated date back to RTC" >&2
    exit 1
fi

if ! grep -q 'RTC write-back failed' "$RTC_INIT"; then
    echo "S99rtcinit must fail clearly when RTC write-back fails" >&2
    exit 1
fi

echo "ntp/rtc init tests passed"
