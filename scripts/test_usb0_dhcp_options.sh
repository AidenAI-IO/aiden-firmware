#!/bin/sh
# The usb0 DHCP server must never advertise itself as a router or DNS server.
#
# If option 3 (router) points at 192.168.42.1, Apple hosts promote the USB link
# to their primary route and lose internet access, because this board only
# serves the local 192.168.42.0/24 subnet and does no NAT or forwarding. This
# guards both layers of the defence:
#
#   1. the shipped overlay/etc/dnsmasq.d/usb0.conf, whose empty-value
#      dhcp-option lines are load-bearing and easy to "complete" by accident;
#   2. the runtime sanitizer in S55aiden_usb_dhcp, which allowlists the complete
#      local-only DHCP policy before handing it to dnsmasq.
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
CONF="$ROOT_DIR/overlay/etc/dnsmasq.d/usb0.conf"
USBHID="$ROOT_DIR/overlay/etc/init.d/S49usbhid"
INIT="$ROOT_DIR/overlay/etc/init.d/S55aiden_usb_dhcp"

for path in "$CONF" "$USBHID" "$INIT"; do
    [ -f "$path" ] || { echo "missing file: $path" >&2; exit 1; }
done

fail() { echo "FAIL: $*" >&2; exit 1; }

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

# directives_of strips comments and blank lines; every content check reads
# directives only, so the sanitizer's "# dropped by ..." audit comments (which
# quote the offending line verbatim) can never satisfy or trip a check.
directives_of() {
    sed 's/#.*//' "$1" | sed '/^[[:space:]]*$/d'
}

has_exact() {
    grep -Eq "^[[:space:]]*$2[[:space:]]*$" "$1"
}

count_exact() {
    grep -Ec "^[[:space:]]*$2[[:space:]]*$" "$1" || true
}

# assert_no_advertisement fails if $1 (a directives-only file) assigns any value
# to DHCP option 3 or 6, in any of dnsmasq's accepted spellings.
assert_no_advertisement() {
    file="$1"
    label="$2"
    if grep -Eq '^[[:space:]]*dhcp-option(-force)?=([a-z-]+:[^,]*,)*(option:)?router[[:space:]]*,' "$file"; then
        fail "$label advertises a default gateway; Apple hosts will hijack their default route"
    fi
    if grep -Eq '^[[:space:]]*dhcp-option(-force)?=([a-z-]+:[^,]*,)*3[[:space:]]*,' "$file"; then
        fail "$label advertises DHCP option 3 (router) numerically"
    fi
    if grep -Eq '^[[:space:]]*dhcp-option(-force)?=([a-z-]+:[^,]*,)*(option:)?dns-server[[:space:]]*,' "$file"; then
        fail "$label advertises a DNS server; hosts must keep their own resolver"
    fi
    if grep -Eq '^[[:space:]]*dhcp-option(-force)?=([a-z-]+:[^,]*,)*6[[:space:]]*,' "$file"; then
        fail "$label advertises DHCP option 6 (DNS) numerically"
    fi
    if grep -Eq '^[[:space:]]*dhcp-option(-force)?=([a-z-]+:[^,]*,)*(option:classless-static-route|121|249)([[:space:]]*,|[[:space:]]*$)' "$file"; then
        fail "$label contains a classless static route option"
    fi
    if grep -Eq '^[[:space:]]*(conf-file|conf-dir|conf-script|dhcp-optsfile|dhcp-optsdir)[[:space:]]*=' "$file"; then
        fail "$label can include DHCP options from an unvalidated external source"
    fi
    if grep -Eq '^[[:space:]]*(enable-ra|ra-param([[:space:]]*=|[[:space:]]*$))' "$file"; then
        fail "$label enables IPv6 Router Advertisements"
    fi
}

# assert_suppressed fails unless $1 carries exactly one canonical bare
# suppressor for each of option 3 and option 6. Bare means empty value, which
# tells dnsmasq to omit the option entirely; duplicates would mean the sanitizer
# appended on top of lines it should have dropped.
assert_suppressed() {
    file="$1"
    label="$2"
    n=$(count_exact "$file" 'dhcp-option=option:router')
    [ "$n" = "1" ] \
        || fail "$label must contain exactly one bare \"dhcp-option=option:router\" (found $n)"
    n=$(count_exact "$file" 'dhcp-option=option:dns-server')
    [ "$n" = "1" ] \
        || fail "$label must contain exactly one bare \"dhcp-option=option:dns-server\" (found $n)"
}

# --- 1. Shipped config: no default gateway, no DNS server ---------------
SHIPPED="$TMP_DIR/shipped.directives"
directives_of "$CONF" > "$SHIPPED"

assert_no_advertisement "$SHIPPED" 'usb0.conf'
assert_suppressed "$SHIPPED" 'usb0.conf'

# --- 2. Scope: usb0 only, never the Wi-Fi client interface --------------
has_exact "$SHIPPED" 'interface=usb0' || fail 'usb0.conf must bind to interface=usb0'
has_exact "$SHIPPED" 'bind-interfaces' || fail 'usb0.conf must set bind-interfaces'
has_exact "$SHIPPED" 'except-interface=wlan0' \
    || fail 'usb0.conf must never serve DHCP on wlan0 (the board is a client there)'
has_exact "$SHIPPED" 'port=0' || fail 'usb0.conf must disable the DNS listener with port=0'
has_exact "$SHIPPED" 'dhcp-authoritative' \
    || fail 'usb0.conf must reject stale leases promptly with dhcp-authoritative'

# --- 3. The board's own static address must sit outside the pool --------
RANGE=$(sed -n 's/^[[:space:]]*dhcp-range=//p' "$SHIPPED" | head -1)
[ -n "$RANGE" ] || fail 'usb0.conf must define a dhcp-range'

RANGE_START=$(printf '%s' "$RANGE" | cut -d, -f1)
RANGE_END=$(printf '%s' "$RANGE" | cut -d, -f2)

# S49usbhid assigns usb0 its static address; the two must not collide.
USB0_ADDR=$(sed -n 's/^USB0_ADDR=//p' "$USBHID" | head -1)
[ -n "$USB0_ADDR" ] || fail "could not read USB0_ADDR from $USBHID"

last_octet() { printf '%s' "$1" | cut -d. -f4; }
prefix() { printf '%s' "$1" | cut -d. -f1-3; }

[ "$(prefix "$RANGE_START")" = "$(prefix "$USB0_ADDR")" ] \
    || fail "dhcp-range $RANGE_START is not on the same /24 as USB0_ADDR $USB0_ADDR"

addr_o=$(last_octet "$USB0_ADDR")
start_o=$(last_octet "$RANGE_START")
end_o=$(last_octet "$RANGE_END")

if [ "$addr_o" -ge "$start_o" ] && [ "$addr_o" -le "$end_o" ]; then
    fail "the board's own address $USB0_ADDR falls inside the DHCP pool $RANGE_START-$RANGE_END"
fi

# --- 4. Lease file path must match what the ECM watchdog reads ----------
LEASEFILE=$(sed -n 's/^[[:space:]]*dhcp-leasefile=//p' "$SHIPPED" | head -1)
[ -n "$LEASEFILE" ] || fail 'usb0.conf must set dhcp-leasefile'
WATCHDOG="$ROOT_DIR/overlay/etc/init.d/S60usb_ecm_watchdog"
if [ -f "$WATCHDOG" ]; then
    WD_LEASE=$(sed -n 's/^LEASE_FILE=//p' "$WATCHDOG" | head -1)
    [ "$WD_LEASE" = "$LEASEFILE" ] \
        || fail "lease file mismatch: usb0.conf=$LEASEFILE but S60usb_ecm_watchdog=$WD_LEASE"
fi

# --- 5. S55 must serve the sanitized config, not the raw one ------------
RUNTIME_CONF=$(sed -n 's/^RUNTIME_CONF=//p' "$INIT" | head -1)
[ -n "$RUNTIME_CONF" ] || fail "could not read RUNTIME_CONF from $INIT"

grep -q -- "--conf-file=\"\$RUNTIME_CONF\"" "$INIT" \
    || fail 'S55aiden_usb_dhcp must launch dnsmasq with --conf-file="$RUNTIME_CONF"'
if grep -q -- '--conf-file="$CONF_FILE"' "$INIT"; then
    fail 'S55aiden_usb_dhcp must not hand the raw usb0.conf to dnsmasq'
fi
grep -q 'build_runtime_conf' "$INIT" \
    || fail 'S55aiden_usb_dhcp must build the runtime config before starting dnsmasq'
# is_running identifies our dnsmasq by its command line; matching the raw path
# would let a hand-started dnsmasq on usb0.conf pass as the managed one.
sed -n '/^is_running()/,/^}/p' "$INIT" | grep -q '"\$RUNTIME_CONF"' \
    || fail 'is_running must match the runtime config path, not the shipped one'
grep -q '^is_legacy_running()' "$INIT" \
    || fail 'S55aiden_usb_dhcp must recognize the pre-runtime-config process during upgrades'
sed -n '/^start()/,/^}/p' "$INIT" | grep -q 'is_legacy_running' \
    || fail 'start must replace a legacy dnsmasq instance before binding the sanitized one'
sed -n '/^stop()/,/^}/p' "$INIT" | grep -q 'is_managed_running' \
    || fail 'stop must terminate both current and legacy managed dnsmasq processes'
grep -q -- '--test --conf-file="$RUNTIME_CONF"' "$INIT" \
    || fail 'start must syntax-check the generated config with the device dnsmasq binary'

# --- 6. Sanitizer leaves an already-correct config alone ----------------
SANITIZED="$TMP_DIR/sanitized.conf"
sh "$INIT" sanitize-conf "$CONF" > "$SANITIZED" \
    || fail 'sanitize-conf failed on the shipped usb0.conf'

if grep -q '^# dropped by' "$SANITIZED"; then
    fail 'sanitizer had to strip the shipped usb0.conf; it should already be clean'
fi

SANITIZED_DIRECTIVES="$TMP_DIR/sanitized.directives"
directives_of "$SANITIZED" > "$SANITIZED_DIRECTIVES"

assert_no_advertisement "$SANITIZED_DIRECTIVES" 'sanitized usb0.conf'
assert_suppressed "$SANITIZED_DIRECTIVES" 'sanitized usb0.conf'

for directive in 'interface=usb0' 'bind-interfaces' 'except-interface=wlan0' \
    'port=0' 'dhcp-authoritative' \
    "dhcp-range=$RANGE" "dhcp-leasefile=$LEASEFILE"; do
    has_exact "$SANITIZED_DIRECTIVES" "$(printf '%s' "$directive" | sed 's/[.[\*^$]/\\&/g')" \
        || fail "sanitizer dropped a directive it must preserve: $directive"
done

# --- 7. Sanitizer rejects pools that can lease the board address --------
BOARD_COLLISION="$TMP_DIR/board-collision.conf"
cat > "$BOARD_COLLISION" <<'EOF'
dhcp-range=192.168.42.1,192.168.42.100,255.255.255.0,12h
dhcp-range=192.168.42.100,192.168.42.1,255.255.255.0,12h
dhcp-range=192.168.42.2,192.168.42.254,255.255.255.0,12h
EOF

BOARD_COLLISION_OUT="$TMP_DIR/board-collision.sanitized"
sh "$INIT" sanitize-conf "$BOARD_COLLISION" > "$BOARD_COLLISION_OUT" \
    || fail 'sanitize-conf failed on board-address collision cases'

BOARD_COLLISION_DIRECTIVES="$TMP_DIR/board-collision.directives"
directives_of "$BOARD_COLLISION_OUT" > "$BOARD_COLLISION_DIRECTIVES"
n=$(count_exact "$BOARD_COLLISION_DIRECTIVES" 'dhcp-range=.*')
[ "$n" = "1" ] || fail "sanitizer must preserve only the valid DHCP pool (found $n)"
has_exact "$BOARD_COLLISION_DIRECTIVES" \
    'dhcp-range=192\.168\.42\.2,192\.168\.42\.254,255\.255\.255\.0,12h' \
    || fail 'sanitizer dropped a valid DHCP pool that excludes the board address'
[ "$(grep -c '^# dropped by .*dhcp-range=' "$BOARD_COLLISION_OUT" || true)" = "2" ] \
    || fail 'sanitizer must record both rejected board-address collision ranges'

# --- 8. Sanitizer strips every spelling of a gateway/DNS advertisement --
POISONED="$TMP_DIR/poisoned.conf"
cat > "$POISONED" <<'EOF'
interface=usb0
bind-interfaces
except-interface=wlan0
no-resolv
dhcp-range=192.168.42.100,192.168.42.200,255.255.255.0,12h
dhcp-option=option:router,192.168.42.1
dhcp-option=option:dns-server,192.168.42.1
dhcp-option=3,192.168.42.1
dhcp-option = 6 , 192.168.42.1
dhcp-option=tag:mac,option:router,192.168.42.1
dhcp-option-force=3,192.168.42.1
dhcp-option=121,0.0.0.0/0,192.168.42.1
dhcp-option=249,0.0.0.0/0,192.168.42.1
dhcp-option=option:classless-static-route,0.0.0.0/0,192.168.42.1
dhcp-option=option:ntp-server,192.168.42.1
conf-file=/tmp/extra-dnsmasq.conf
conf-dir=/tmp/extra-dnsmasq.d
conf-script=/tmp/extra-dnsmasq.sh
dhcp-optsfile=/tmp/extra-dhcp-options.conf
dhcp-optsdir=/tmp/extra-dhcp-options.d
enable-ra
ra-param=usb0,60,1200
dhcp-range=::100,::1ff,constructor:usb0,64,12h
dhcp-leasefile=/tmp/dnsmasq.leases
EOF

POISON_OUT="$TMP_DIR/poisoned.sanitized"
sh "$INIT" sanitize-conf "$POISONED" > "$POISON_OUT" \
    || fail 'sanitize-conf failed on the poisoned config'

POISON_DIRECTIVES="$TMP_DIR/poisoned.directives"
directives_of "$POISON_OUT" > "$POISON_DIRECTIVES"

assert_no_advertisement "$POISON_DIRECTIVES" 'sanitized poisoned config'
assert_suppressed "$POISON_DIRECTIVES" 'sanitized poisoned config'

# Allowlisted local-only directives must survive untouched. All other
# dhcp-options and includes are intentionally removed.
for directive in 'interface=usb0' 'bind-interfaces' 'except-interface=wlan0' \
    'no-resolv' \
    'dhcp-leasefile=/tmp/dnsmasq\.leases'; do
    has_exact "$POISON_DIRECTIVES" "$directive" \
        || fail "sanitizer dropped an unrelated directive: $directive"
done

if grep -Eq '^(dhcp-option=.*(ntp-server|121|249|classless-static-route)|conf-(file|dir|script)=|dhcp-opts(file|dir)=|enable-ra|ra-param=|dhcp-range=::)' "$POISON_DIRECTIVES"; then
    fail 'sanitizer preserved a directive outside the local-only allowlist'
fi

grep -q '^# dropped by' "$POISON_OUT" \
    || fail 'sanitizer must record what it stripped so the regression is visible in the log'

# --- 9. Both generated configs must parse (only if dnsmasq is available) -
DNSMASQ=$(command -v dnsmasq 2>/dev/null || true)
if [ -z "$DNSMASQ" ] && [ -x /usr/sbin/dnsmasq ]; then
    DNSMASQ=/usr/sbin/dnsmasq
fi
if [ -n "$DNSMASQ" ]; then
    for generated in "$SANITIZED" "$POISON_OUT"; do
        "$DNSMASQ" --test --conf-file="$generated" >/dev/null 2>&1 \
            || fail "dnsmasq rejected the generated config $generated"
    done
    echo "dnsmasq --test accepted both generated configs"
else
    echo "note: dnsmasq not installed; skipped the generated-config syntax check"
fi

echo "PASS: usb0 DHCP options (no router, no DNS, pool excludes $USB0_ADDR, sanitizer enforced)"
