#!/bin/sh

IFACE="${WLAN_GUARD_IFACE:-wlan0}"
CHECK_INTERVAL="${WLAN_GUARD_INTERVAL:-30}"
FAIL_THRESHOLD="${WLAN_GUARD_FAIL_THRESHOLD:-2}"
RECOVER_COOLDOWN="${WLAN_GUARD_RECOVER_COOLDOWN:-20}"
PIDFILE="/tmp/wlan_guard.pid"
LOGFILE="/tmp/wlan_guard.log"

log() {
	msg="wlan_guard: $*"
	logger -t wlan_guard "$*" 2>/dev/null || true
	printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$msg" >> "$LOGFILE"
}

is_running() {
	pid="$1"
	[ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

default_gateway() {
	if [ -n "$WLAN_GUARD_GATEWAY" ]; then
		echo "$WLAN_GUARD_GATEWAY"
		return 0
	fi
	ip route show default 2>/dev/null | sed -n "s/^default via \([^ ]*\) dev $IFACE.*/\1/p" | sed -n '1p'
}

target_host() {
	default_gateway
}

tune_radio() {
	iw dev "$IFACE" set power_save off >/dev/null 2>&1 || true
}

wlan_associated() {
	state="$(wpa_cli -i "$IFACE" status 2>/dev/null | sed -n 's/^wpa_state=//p' | sed -n '1p')"
	[ "$state" = "COMPLETED" ]
}

target_reachable() {
	host="$(target_host)"
	if [ -z "$host" ]; then
		log "no default gateway for $IFACE"
		return 1
	fi

	ping -c 1 -W 1 -I "$IFACE" "$host" >/dev/null 2>&1
}

recover_wlan() {
	log "recovering $IFACE after connectivity failures"
	tune_radio
	wpa_cli -i "$IFACE" reassociate >/dev/null 2>&1 || true
	sleep 8

	if target_reachable; then
		log "connectivity recovered after reassociate"
		return 0
	fi

	log "reassociate did not recover connectivity; cycling $IFACE"
	ifconfig "$IFACE" down >/dev/null 2>&1 || true
	sleep 2
	ifconfig "$IFACE" up >/dev/null 2>&1 || true
	tune_radio
	sleep 2
	wpa_cli -i "$IFACE" reassociate >/dev/null 2>&1 || true
	sleep 8
	udhcpc -n -q -i "$IFACE" >/dev/null 2>&1 || true
}

watch_loop() {
	tune_radio
	initial_target="$(target_host)"
	if [ -n "$initial_target" ]; then
		log "watchdog started for $IFACE via $initial_target"
	else
		log "watchdog started for $IFACE without default gateway"
	fi
	fail_count=0

	while true; do
		if wlan_associated && target_reachable; then
			if [ "$fail_count" -ne 0 ]; then
				log "connectivity healthy again"
			fi
			fail_count=0
		else
			fail_count=$((fail_count + 1))
			log "connectivity check failed ($fail_count/$FAIL_THRESHOLD)"
			if [ "$fail_count" -ge "$FAIL_THRESHOLD" ]; then
				recover_wlan
				fail_count=0
				sleep "$RECOVER_COOLDOWN"
			fi
		fi

		sleep "$CHECK_INTERVAL"
	done
}

start() {
	if [ -f "$PIDFILE" ]; then
		old_pid="$(sed -n '1p' "$PIDFILE" 2>/dev/null)"
		if is_running "$old_pid"; then
			echo "wlan_guard already running: $old_pid"
			return 0
		fi
	fi

	watch_loop >/dev/null 2>&1 &
	echo "$!" > "$PIDFILE"
	echo "started wlan_guard: $(sed -n '1p' "$PIDFILE")"
}

stop() {
	if [ -f "$PIDFILE" ]; then
		old_pid="$(sed -n '1p' "$PIDFILE" 2>/dev/null)"
		if is_running "$old_pid"; then
			kill "$old_pid" 2>/dev/null || true
		fi
		rm -f "$PIDFILE"
	fi
	echo "stopped wlan_guard"
}

status() {
	if [ -f "$PIDFILE" ]; then
		old_pid="$(sed -n '1p' "$PIDFILE" 2>/dev/null)"
		if is_running "$old_pid"; then
			echo "running: $old_pid"
			return 0
		fi
	fi
	echo "stopped"
	return 1
}

case "$1" in
	start|"") start ;;
	stop) stop ;;
	restart|reload) stop; start ;;
	status) status ;;
	once)
		tune_radio
		if wlan_associated && target_reachable; then
			echo "healthy"
		else
			recover_wlan
		fi
		;;
	*) echo "Usage: $0 {start|stop|restart|status|once}"; exit 1 ;;
esac
