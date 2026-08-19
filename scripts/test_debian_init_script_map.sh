#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly INIT_DIR=${REPO_ROOT}/overlay/etc/init.d
readonly MAP_FILE=${REPO_ROOT}/scripts/debian/init-script-map.tsv
readonly ENV_MAP_FILE=${REPO_ROOT}/scripts/debian/environment-service-map.tsv
readonly TEST_ROOT=$(mktemp -d)
trap 'rm -rf "${TEST_ROOT}"' EXIT

fail() {
    echo "Debian init map test failure: $*" >&2
    exit 1
}

expected_header=$'script\tclassification\tsystemd_target\tacceptance\tmigration_note'
[ "$(sed -n '1p' "${MAP_FILE}")" = "${expected_header}" ] \
    || fail "unexpected init-script-map.tsv header"

find "${INIT_DIR}" -maxdepth 1 -type f \
    \( -name 'S*' -o -name rcS \) -printf '%f\n' \
    | LC_ALL=C sort >"${TEST_ROOT}/actual-init-scripts"

awk -F '\t' '
    NR == 1 { next }
    NF != 5 { printf "invalid field count on line %d\n", NR > "/dev/stderr"; failed = 1; next }
    $1 == "" || $3 == "" || $4 == "" || $5 == "" {
        printf "empty required field on line %d\n", NR > "/dev/stderr"; failed = 1
    }
    $2 != "migrate" && $2 != "replaced-by-debian" && $2 != "retire" && $2 != "no-op" {
        printf "invalid classification on line %d: %s\n", NR, $2 > "/dev/stderr"; failed = 1
    }
    seen[$1]++ { printf "duplicate script on line %d: %s\n", NR, $1 > "/dev/stderr"; failed = 1 }
    { print $1 }
    END { if (failed) exit 1 }
' "${MAP_FILE}" | LC_ALL=C sort >"${TEST_ROOT}/mapped-init-scripts"

cmp "${TEST_ROOT}/actual-init-scripts" "${TEST_ROOT}/mapped-init-scripts" \
    || fail "init script inventory does not match overlay/etc/init.d"
[ "$(wc -l <"${TEST_ROOT}/mapped-init-scripts")" -eq 32 ] \
    || fail "expected 31 S scripts plus rcS"

awk -F '\t' '$1 == "rcS" { found = 1; if ($2 != "replaced-by-debian" || $3 != "systemd") exit 1 }
    END { if (!found) exit 1 }' "${MAP_FILE}" \
    || fail "rcS must be replaced by systemd"

expected_env_header=$'init_script\tsystemd_unit\tenvironment_file\tinvalid_environment_policy'
[ "$(sed -n '1p' "${ENV_MAP_FILE}")" = "${expected_env_header}" ] \
    || fail "unexpected environment-service-map.tsv header"

rg -l 'ENV_RUN_BIN=.*aiden-env-run' "${INIT_DIR}"/S* \
    | xargs -r -n1 basename | LC_ALL=C sort \
    >"${TEST_ROOT}/actual-environment-services"

awk -F '\t' '
    NR == 1 { next }
    NF != 4 { printf "invalid environment map field count on line %d\n", NR > "/dev/stderr"; failed = 1; next }
    $1 == "" || $2 == "" || $3 != "/run/aiden/system.env" || $4 == "" {
        printf "invalid environment map row on line %d\n", NR > "/dev/stderr"; failed = 1
    }
    seen[$1]++ { printf "duplicate environment service on line %d: %s\n", NR, $1 > "/dev/stderr"; failed = 1 }
    { print $1 }
    END { if (failed) exit 1 }
' "${ENV_MAP_FILE}" | LC_ALL=C sort \
    >"${TEST_ROOT}/mapped-environment-services"

cmp "${TEST_ROOT}/actual-environment-services" \
    "${TEST_ROOT}/mapped-environment-services" \
    || fail "aiden-env-run dependency inventory is incomplete"
[ "$(wc -l <"${TEST_ROOT}/mapped-environment-services")" -eq 8 ] \
    || fail "expected exactly eight aiden-env-run service dependencies"
