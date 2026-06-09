#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
NTP_INIT="$ROOT_DIR/overlay/etc/init.d/S49ntp"
RTC_INIT="$ROOT_DIR/overlay/etc/init.d/S99rtcinit"
DHCPCD_INIT="$ROOT_DIR/overlay/etc/init.d/S41dhcpcd"
UDHCPC_HOOK="$ROOT_DIR/overlay/etc/udhcpc/aiden.script"
BOOT_CONF="$ROOT_DIR/overlay/etc/aiden_boot.conf"
NTP_CONF="$ROOT_DIR/overlay/etc/ntp.conf"

for script in "$NTP_INIT" "$RTC_INIT" "$DHCPCD_INIT" "$UDHCPC_HOOK"; do
    if [ ! -x "$script" ]; then
        echo "missing executable $(basename "$script")" >&2
        exit 1
    fi
    if ! sh -n "$script"; then
        echo "$(basename "$script") must be valid POSIX shell syntax" >&2
        exit 1
    fi
done

# S49ntp must NOT block on network readiness anymore — udhcpc hook handles
# the post-DHCP step now. The script should also no longer rely on hostname
# resolution: NTP_FALLBACK_SERVER defaults to a numeric IP.
if grep -q 'wait_for_network' "$NTP_INIT"; then
    echo "S49ntp must not poll for network readiness (use udhcpc hook instead)" >&2
    exit 1
fi

if ! grep -Eq '^: "\$\{NTP_FALLBACK_SERVER:=([0-9]{1,3}\.){3}[0-9]{1,3}\}"$' "$NTP_INIT"; then
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
if grep -Eq 'ntpd .* -p ' "$NTP_INIT"; then
    echo "S49ntp must not pass servers via -p (reference ntpd treats -p as pidfile)" >&2
    exit 1
fi

# udhcpc hook must delegate to the system default script (so DHCP-driven IP /
# route / DNS configuration still happens) AND kick the NTP step on bound.
if ! grep -q '/usr/share/udhcpc/default.script' "$UDHCPC_HOOK"; then
    echo "udhcpc hook must delegate to /usr/share/udhcpc/default.script" >&2
    exit 1
fi

if ! grep -q 'S49ntp step' "$UDHCPC_HOOK"; then
    echo "udhcpc hook must call 'S49ntp step' on lease events" >&2
    exit 1
fi

if ! grep -Eq 'bound\||\|bound|^[[:space:]]*bound\)' "$UDHCPC_HOOK"; then
    echo "udhcpc hook must handle the 'bound' event" >&2
    exit 1
fi

# S41dhcpcd must wire udhcpc to the aiden hook via -s.
if ! grep -q 'udhcpc .*-s /etc/udhcpc/aiden.script' "$DHCPCD_INIT"; then
    echo "S41dhcpcd must launch udhcpc with -s /etc/udhcpc/aiden.script" >&2
    exit 1
fi

# Boot conf must carry the new IP-based default and must not carry the
# obsolete NTP_WAIT_* knobs.
if ! grep -Eq '^NTP_FALLBACK_SERVER=([0-9]{1,3}\.){3}[0-9]{1,3}$' "$BOOT_CONF"; then
    echo "aiden_boot.conf must define NTP_FALLBACK_SERVER as an IPv4 literal" >&2
    exit 1
fi

for stale in NTP_WAIT_NETWORK NTP_WAIT_TIMEOUT NTP_WAIT_INTERVAL NTP_WAIT_DNS_HOST; do
    if grep -q "^${stale}=" "$BOOT_CONF"; then
        echo "aiden_boot.conf still carries obsolete $stale setting" >&2
        exit 1
    fi
done

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

if ! grep -Eq '^[[:space:]]*server[[:space:]]+([0-9]{1,3}\.){3}[0-9]{1,3}\b' "$NTP_CONF"; then
    echo "overlay/etc/ntp.conf must list at least one IPv4 server line" >&2
    exit 1
fi

if ! grep -q 'RTC_DEFAULT_DATE:=2026-06-09' "$RTC_INIT"; then
    echo "S99rtcinit must default invalid RTC values to 2026-06-09" >&2
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

echo "ntp/rtc init tests passed"
