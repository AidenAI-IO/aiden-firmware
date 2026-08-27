#!/bin/sh

if [ -n "${AIDEN_LOW_LEVEL_COMMON_LOADED:-}" ]; then
    return 0 2>/dev/null || exit 0
fi
AIDEN_LOW_LEVEL_COMMON_LOADED=1

default_run_id() {
    date -u +%Y%m%dT%H%M%SZ 2>/dev/null || date +%Y%m%dT%H%M%S
}

: "${RUN_ID:=$(default_run_id)}"
: "${AIDEN_LOW_LEVEL_ARTIFACT_DIR:=/tmp}"
: "${FAILS:=0}"
: "${WARNS:=0}"

die() {
    echo "error: $*" >&2
    exit 2
}

require_uint() {
    case "${2:-}" in
        ''|*[!0-9]*)
            die "$1 must be numeric"
            ;;
    esac
}

section() {
    printf '\n== %s ==\n' "$*"
}

pass() {
    printf '[PASS] %s\n' "$*"
}

warn() {
    WARNS=$((WARNS + 1))
    printf '[WARN] %s\n' "$*"
}

fail() {
    FAILS=$((FAILS + 1))
    printf '[FAIL] %s\n' "$*"
}

skip() {
    printf '[SKIP] %s\n' "$*"
}

indent() {
    sed 's/^/    /'
}

run_check() {
    aiden_check_name="$1"
    shift
    aiden_check_tmp="$(mktemp "${TMPDIR:-/tmp}/aiden_low_level_check.XXXXXX")" || {
        fail "$aiden_check_name: cannot create temporary log"
        return 1
    }
    "$@" >"$aiden_check_tmp" 2>&1
    aiden_check_rc=$?
    if [ "$aiden_check_rc" -eq 0 ]; then
        pass "$aiden_check_name"
    else
        fail "$aiden_check_name (rc=$aiden_check_rc)"
    fi
    [ -s "$aiden_check_tmp" ] && indent < "$aiden_check_tmp"
    rm -f "$aiden_check_tmp"
    return "$aiden_check_rc"
}

run_warn_check() {
    aiden_check_name="$1"
    shift
    aiden_check_tmp="$(mktemp "${TMPDIR:-/tmp}/aiden_low_level_warn.XXXXXX")" || {
        fail "$aiden_check_name: cannot create temporary log"
        return 1
    }
    "$@" >"$aiden_check_tmp" 2>&1
    aiden_check_rc=$?
    if [ "$aiden_check_rc" -eq 0 ]; then
        pass "$aiden_check_name"
    else
        warn "$aiden_check_name (rc=$aiden_check_rc)"
    fi
    [ -s "$aiden_check_tmp" ] && indent < "$aiden_check_tmp"
    rm -f "$aiden_check_tmp"
    return 0
}

find_bin() {
    aiden_bin_name="$1"
    for aiden_bin_path in "/oem/usr/bin/$aiden_bin_name" "/usr/local/bin/$aiden_bin_name" "/usr/bin/$aiden_bin_name" "/bin/$aiden_bin_name"; do
        if [ -x "$aiden_bin_path" ]; then
            printf '%s\n' "$aiden_bin_path"
            return 0
        fi
    done
    command -v "$aiden_bin_name" 2>/dev/null
}

check_exists() {
    aiden_check_name="$1"
    aiden_check_path="$2"
    if [ -e "$aiden_check_path" ]; then
        pass "$aiden_check_name exists: $aiden_check_path"
        ls -l "$aiden_check_path" 2>/dev/null | indent
        return 0
    fi
    fail "$aiden_check_name missing: $aiden_check_path"
    return 1
}

check_socket() {
    aiden_check_name="$1"
    aiden_check_path="$2"
    if [ -S "$aiden_check_path" ]; then
        pass "$aiden_check_name socket exists: $aiden_check_path"
        ls -l "$aiden_check_path" 2>/dev/null | indent
        return 0
    fi
    fail "$aiden_check_name socket missing: $aiden_check_path"
    return 1
}

service_check() {
    aiden_service_name="$1"
    aiden_service_units="$2"
    aiden_service_init="$3"

    if command -v systemctl >/dev/null 2>&1; then
        for aiden_service_unit in $aiden_service_units; do
            aiden_load_state="$(systemctl show -p LoadState --value "$aiden_service_unit" 2>/dev/null)"
            if [ -n "$aiden_load_state" ] && [ "$aiden_load_state" != "not-found" ]; then
                run_check "$aiden_service_name systemd unit active: $aiden_service_unit" systemctl is-active "$aiden_service_unit"
                systemctl --no-pager --lines=8 status "$aiden_service_unit" 2>/dev/null | indent
                return 0
            fi
        done
    fi

    if [ -x "$aiden_service_init" ]; then
        run_check "$aiden_service_name init status: $aiden_service_init" "$aiden_service_init" status
        return 0
    fi

    warn "$aiden_service_name service status path not found"
    return 0
}

ensure_artifact_dir() {
    mkdir -p "$AIDEN_LOW_LEVEL_ARTIFACT_DIR" 2>/dev/null || die "cannot create artifact dir: $AIDEN_LOW_LEVEL_ARTIFACT_DIR"
}

artifact_path() {
    ensure_artifact_dir
    printf '%s/aiden_low_level_%s_%s\n' "$AIDEN_LOW_LEVEL_ARTIFACT_DIR" "$RUN_ID" "$1"
}

print_artifact() {
    printf '  %-12s %s\n' "$1:" "$2"
}

print_summary() {
    section "summary"
    printf 'warnings=%s failures=%s\n' "$WARNS" "$FAILS"
    [ "$FAILS" -eq 0 ]
}
