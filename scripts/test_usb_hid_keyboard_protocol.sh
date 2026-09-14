#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
INIT_SCRIPT="$ROOT_DIR/overlay/etc/init.d/S49usbhid"
EXAMPLE_SRC="$ROOT_DIR/src/example_usb_hid.cpp"

fail() {
    echo "$*" >&2
    exit 1
}

sh -n "$INIT_SCRIPT" || fail "S49usbhid has invalid shell syntax"

python3 - "$INIT_SCRIPT" <<'PY'
import re
import subprocess
import sys

source = open(sys.argv[1], encoding="utf-8").read()
if re.search(r"printf\s+'(?:\\x[0-9a-fA-F]{2})+?'", source):
    raise SystemExit("HID descriptors must not use non-POSIX \\xNN printf escapes")

formats = re.findall(r"printf\s+'((?:\\[0-7]{3})+)'", source)
expected = [
    "05010906a101050719e029e71500250175019508810295017508810195067508150025650507190029658100c0",
    "050d0904a1010922a102050d09420932150025017501950281027506950181030951750895011500250f810205010930093116000026ff7f751095028102c0c0",
    "05010902a1010901a1000509190129081500250195087501810205010930093116000026ff7f75109502810209381581257f750895018106c0c0",
    "050c0901a101150026ff031a00002aff03751095018100c0",
    "050c0901a1011500250109e209e909ea09cd09b709b509b609b409b30965096f09707501950c8102750195048103c0",
]
if len(formats) != len(expected):
    raise SystemExit(f"expected {len(expected)} HID descriptors, found {len(formats)}")

for index, (fmt, expected_hex) in enumerate(zip(formats, expected), start=1):
    output = subprocess.run(
        ["/bin/sh", "-c", "printf '" + fmt + "'"],
        check=True,
        stdout=subprocess.PIPE,
    ).stdout
    if output.hex() != expected_hex:
        raise SystemExit(
            f"HID descriptor {index} differs after /bin/sh printf: "
            f"expected {expected_hex}, got {output.hex()}"
        )
PY

grep -Fq 'echo 1 > "$GADGET_DIR/functions/hid.usb0/protocol"' "$INIT_SCRIPT" ||
    fail "S49usbhid must keep keyboard HID boot protocol=1 for host compatibility"

grep -Fq 'echo 1 > "$GADGET_DIR/functions/hid.usb0/subclass"' "$INIT_SCRIPT" ||
    fail "S49usbhid must keep keyboard HID boot subclass=1 for host compatibility"

awk \
    -v bind='echo "$UDC" > "$GADGET_DIR/UDC"' \
    -v unbind='echo "" > "$GADGET_DIR/UDC"' \
    -v udc_write='> "$GADGET_DIR/UDC"' \
    -v function_link='ln -s "$GADGET_DIR/functions/' '
    /^start\(\)[[:space:]]*\{/ { in_start=1; next }
    /^stop\(\)[[:space:]]*\{/ { in_stop=1; next }
    in_start && /^\}/ { in_start=0; next }
    in_stop && /^\}/ { in_stop=0; next }

    in_start && index($0, function_link) {
        if (bound) {
            print "function link found after UDC bind: " $0 > "/dev/stderr"
            invalid=1
        }
        link_count++
        next
    }

    index($0, udc_write) {
        if (in_start && index($0, bind)) {
            bind_count++
            if (bind_count > 1) {
                print "multiple UDC binds found in start(): " $0 > "/dev/stderr"
                invalid=1
            }
            bound=1
            next
        }
        if (in_start && index($0, unbind)) {
            if (bound) {
                print "UDC unbind found after initial bind: " $0 > "/dev/stderr"
                invalid=1
            }
            next
        }
        if (in_stop && index($0, unbind)) {
            next
        }

        print "unexpected UDC write outside initial bind or cleanup: " $0 > "/dev/stderr"
        invalid=1
    }

    END {
        if (link_count != 4) {
            print "expected four function links before UDC bind, found " link_count > "/dev/stderr"
            invalid=1
        }
        if (bind_count != 1) {
            print "expected exactly one UDC bind in start(), found " bind_count > "/dev/stderr"
            invalid=1
        }
        exit(invalid ? 1 : 0)
    }
' "$INIT_SCRIPT" ||
    fail "S49usbhid must link the completed composite before one startup bind and never re-enumerate it"

grep -Fq 'write_text_file(function_path + "/protocol", "1");' "$EXAMPLE_SRC" ||
    fail "example_usb_hid setup must keep keyboard HID boot protocol=1"

grep -Fq 'write_text_file(function_path + "/subclass", "1");' "$EXAMPLE_SRC" ||
    fail "example_usb_hid setup must keep keyboard HID boot subclass=1"

echo "usb HID keyboard protocol checks passed"
