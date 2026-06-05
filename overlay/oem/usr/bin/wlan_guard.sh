#!/bin/sh

IFACE="${WLAN_GUARD_IFACE:-wlan0}"
CHECK_INTERVAL="${WLAN_GUARD_INTERVAL:-10}"
FAIL_THRESHOLD="${WLAN_GUARD_FAIL_THRESHOLD:-5}"
PING_COUNT="${WLAN_GUARD_PING_COUNT:-3}"
PING_TIMEOUT="${WLAN_GUARD_PING_TIMEOUT:-1}"
RECOVER_COOLDOWN="${WLAN_GUARD_RECOVER_COOLDOWN:-20}"
PIDFILE="/tmp/wlan_guard.pid"
LOGFILE="/tmp/wlan_guard.log"

log() {
	msg="wlan_guard: $*"
	logger -t wlan_guard "$*" 2>/dev/null || true
	printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$msg" >> "$LOGFILE"
}

strip_leading_zeroes() {
	value="$1"
	while [ "${value#0}" != "$value" ]; do
		value="${value#0}"
	done
	[ -n "$value" ] || value=0
	echo "$value"
}

greater_than() {
	left="$(strip_leading_zeroes "$1")"
	right="$(strip_leading_zeroes "$2")"
	if [ "${#left}" -gt "${#right}" ]; then
		return 0
	fi
	if [ "${#left}" -lt "${#right}" ]; then
		return 1
	fi
	[ "$left" -gt "$right" ]
}

sanitize_positive_int() {
	name="$1"
	value="$2"
	default_value="$3"
	max_value="$4"

	case "$value" in
		''|*[!0-9]*)
			log "invalid $name=$value; using default $default_value"
			echo "$default_value"
			return
			;;
	esac

	value="$(strip_leading_zeroes "$value")"
	if [ "$value" = "0" ] || greater_than "$value" "$max_value"; then
		log "invalid $name=$2; using default $default_value"
		echo "$default_value"
		return
	fi

	echo "$value"
}

validate_config() {
	CHECK_INTERVAL="$(sanitize_positive_int WLAN_GUARD_INTERVAL "$CHECK_INTERVAL" 10 3600)"
	FAIL_THRESHOLD="$(sanitize_positive_int WLAN_GUARD_FAIL_THRESHOLD "$FAIL_THRESHOLD" 5 100)"
	PING_COUNT="$(sanitize_positive_int WLAN_GUARD_PING_COUNT "$PING_COUNT" 3 10)"
	PING_TIMEOUT="$(sanitize_positive_int WLAN_GUARD_PING_TIMEOUT "$PING_TIMEOUT" 1 30)"
	RECOVER_COOLDOWN="$(sanitize_positive_int WLAN_GUARD_RECOVER_COOLDOWN "$RECOVER_COOLDOWN" 20 3600)"
}

is_wlan_guard_process() {
	pid="$1"
	cmdline_path="/proc/$pid/cmdline"
	[ -r "$cmdline_path" ] || return 1

	cmdline="$(tr '\000' ' ' < "$cmdline_path" 2>/dev/null)" || return 1
	case "$cmdline" in
		*wlan_guard.sh*) return 0 ;;
	esac
	return 1
}

is_running() {
	pid="$1"
	case "$pid" in
		''|*[!0-9]*) return 1 ;;
	esac
	kill -0 "$pid" 2>/dev/null || return 1
	is_wlan_guard_process "$pid"
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
	host="${1:-}"
	if [ -z "$host" ]; then
		host="$(target_host)"
	fi
	if [ -z "$host" ]; then
		log "no default gateway for $IFACE"
		return 1
	fi

	ping -c "$PING_COUNT" -W "$PING_TIMEOUT" -I "$IFACE" "$host" >/dev/null 2>&1
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
	validate_config
	tune_radio
	initial_target="$(target_host)"
	if [ -n "$initial_target" ]; then
		log "watchdog started for $IFACE via $initial_target"
	else
		log "watchdog started for $IFACE without default gateway"
	fi
	fail_count=0
	wait_reason=""

	while true; do
		if ! wlan_associated; then
			if [ "$wait_reason" != "not_associated" ]; then
				log "wlan not associated; waiting"
			fi
			wait_reason="not_associated"
			fail_count=0
		else
			host="$(target_host)"
			if [ -z "$host" ]; then
				if [ "$wait_reason" != "no_gateway" ]; then
					log "no default gateway for $IFACE; waiting"
				fi
				wait_reason="no_gateway"
				fail_count=0
			elif target_reachable "$host"; then
				wait_reason=""
				if [ "$fail_count" -ne 0 ]; then
					log "connectivity healthy again"
				fi
				fail_count=0
			else
				wait_reason=""
				fail_count=$((fail_count + 1))
				log "gateway $host unreachable ($fail_count/$FAIL_THRESHOLD)"
				if [ "$fail_count" -ge "$FAIL_THRESHOLD" ]; then
					recover_wlan
					fail_count=0
					sleep "$RECOVER_COOLDOWN"
				fi
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

	validate_config
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

case "$1" in
	start) start ;;
	stop) stop ;;
	restart|reload) stop; start ;;
	*) echo "Usage: $0 {start|stop|restart|reload}"; exit 1 ;;
esac
