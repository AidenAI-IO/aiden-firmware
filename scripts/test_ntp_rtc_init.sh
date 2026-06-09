#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
NTP_INIT="$ROOT_DIR/overlay/etc/init.d/S49ntp"
RTC_INIT="$ROOT_DIR/overlay/etc/init.d/S99rtcinit"
BOOT_CONF="$ROOT_DIR/overlay/etc/aiden_boot.conf"

for script in "$NTP_INIT" "$RTC_INIT"; do
    if [ ! -x "$script" ]; then
        echo "missing executable $(basename "$script")" >&2
        exit 1
    fi
    if ! sh -n "$script"; then
        echo "$(basename "$script") must be valid POSIX shell syntax" >&2
        exit 1
    fi
done

if ! grep -q 'wait_for_network' "$NTP_INIT"; then
    echo "S49ntp must wait for network readiness before starting ntpd" >&2
    exit 1
fi

if ! grep -q 'ip route show default' "$NTP_INIT"; then
    echo "S49ntp must check for a default route" >&2
    exit 1
fi

if ! grep -q 'ip addr show "$iface"' "$NTP_INIT"; then
    echo "S49ntp must check that the routed interface has IPv4" >&2
    exit 1
fi

if ! grep -q 'nslookup "$NTP_WAIT_DNS_HOST"' "$NTP_INIT"; then
    echo "S49ntp must check DNS readiness" >&2
    exit 1
fi

wait_line=$(grep -n 'wait_for_network' "$NTP_INIT" | tail -1 | cut -d: -f1)
start_line=$(grep -n '/usr/sbin/ntpd -g' "$NTP_INIT" | head -1 | cut -d: -f1)
if [ "$wait_line" -ge "$start_line" ]; then
    echo "S49ntp must wait before launching ntpd" >&2
    exit 1
fi

for setting in \
    '^NTP_WAIT_NETWORK=1$' \
    '^NTP_WAIT_TIMEOUT=60$' \
    '^NTP_WAIT_INTERVAL=2$' \
    '^NTP_WAIT_DNS_HOST=ntp.aliyun.com$'
do
    if ! grep -q "$setting" "$BOOT_CONF"; then
        echo "aiden_boot.conf missing $setting" >&2
        exit 1
    fi
done

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
