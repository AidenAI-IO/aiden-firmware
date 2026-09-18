#!/usr/bin/env bash
set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly TEST_ROOT=$(mktemp -d)
trap 'rm -rf "${TEST_ROOT}"' EXIT
readonly TEST_SNAPSHOT=http://snapshot.debian.org/archive/debian/20260803T000000Z
readonly TEST_CANDIDATE=http://cache.example:3128

fail() {
    echo "Debian APT cache test failure: $*" >&2
    exit 1
}

source "${REPO_ROOT}/scripts/debian-system/configure-apt-cache.sh"
bash -n "${REPO_ROOT}/scripts/debian-system/configure-apt-cache.sh"
mkdir -p "${TEST_ROOT}/bin"
export PROBE_LOG=${TEST_ROOT}/probe-log
export EXPECTED_CANDIDATE=${TEST_CANDIDATE}
export EXPECTED_METADATA_URL=${TEST_SNAPSHOT}/dists/trixie/InRelease
export PATH=${TEST_ROOT}/bin:${PATH}

cat >"${TEST_ROOT}/bin/wget" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo wget >>"${PROBE_LOG}"
[ "${http_proxy:-}" = "${EXPECTED_CANDIDATE}" ]
[ -z "${no_proxy:-}${NO_PROXY:-}${HTTP_PROXY:-}${all_proxy:-}${ALL_PROXY:-}" ]
[ "${!#}" = "${EXPECTED_METADATA_URL}" ]
output=
while [ "$#" -gt 0 ]; do
    case "$1" in
    -O) output=$2; shift ;;
    esac
    shift
done
case "${PROBE_RESULT:-valid}" in
unreachable) exit 4 ;;
timeout) exit 124 ;;
wrong-release) printf 'Codename: bookworm\n' >"${output}" ;;
*) printf 'Codename: trixie\n' >"${output}" ;;
esac
EOF
cat >"${TEST_ROOT}/bin/gpgv" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo gpgv >>"${PROBE_LOG}"
[ "$1" = --keyring ]
[ "$2" = /usr/share/keyrings/debian-archive-keyring.gpg ]
[ -s "$3" ]
[ "${PROBE_RESULT:-valid}" != invalid-signature ]
EOF
chmod +x "${TEST_ROOT}/bin/wget" "${TEST_ROOT}/bin/gpgv"

reset_proxy_settings() {
    unset http_proxy HTTP_PROXY https_proxy HTTPS_PROXY all_proxy ALL_PROXY \
        no_proxy NO_PROXY DEBIAN_SYSTEM_APT_CACHE_PROXY PROBE_RESULT
    : >"${PROBE_LOG}"
}

(
    reset_proxy_settings
    configure_apt_cache_proxy "${TEST_SNAPSHOT}"
    [ ! -s "${PROBE_LOG}" ] || fail 'an unconfigured cache triggered a probe'
    [ -z "${http_proxy+x}" ] || fail 'an unconfigured cache changed http_proxy'
)
(
    reset_proxy_settings
    export DEBIAN_SYSTEM_APT_CACHE_PROXY=${TEST_CANDIDATE}
    export no_proxy=snapshot.debian.org https_proxy=http://https-proxy.example:8080
    configure_apt_cache_proxy "${TEST_SNAPSHOT}"
    [ "${http_proxy:-}" = "${TEST_CANDIDATE}" ] || fail 'a validated cache was not selected'
    [ "${no_proxy}" = snapshot.debian.org ] || fail 'the probe changed no_proxy'
    [ "${https_proxy}" = http://https-proxy.example:8080 ] || fail 'the cache changed https_proxy'
    [ "$(cat "${PROBE_LOG}")" = $'wget\ngpgv' ] || fail 'metadata was not downloaded and verified'
)
for proxy_name in http_proxy HTTP_PROXY all_proxy ALL_PROXY; do
    (
        reset_proxy_settings
        export DEBIAN_SYSTEM_APT_CACHE_PROXY=${TEST_CANDIDATE}
        export "${proxy_name}=http://existing.example:8080"
        configure_apt_cache_proxy "${TEST_SNAPSHOT}"
        [ "${!proxy_name}" = http://existing.example:8080 ] || fail 'an existing proxy was replaced'
        [ ! -s "${PROBE_LOG}" ] || fail 'an existing proxy did not prevent cache probing'
    )
done
for result in unreachable timeout invalid-signature wrong-release; do
    (
        reset_proxy_settings
        export DEBIAN_SYSTEM_APT_CACHE_PROXY=${TEST_CANDIDATE} PROBE_RESULT=${result}
        configure_apt_cache_proxy "${TEST_SNAPSHOT}"
        [ -z "${http_proxy+x}" ] || fail "${result} enabled an unusable cache"
        grep -qx wget "${PROBE_LOG}" || fail "${result} did not attempt the metadata request"
    )
done
(
    reset_proxy_settings
    export DEBIAN_SYSTEM_APT_CACHE_PROXY=https://cache.example:3128
    configure_apt_cache_proxy "${TEST_SNAPSHOT}"
    [ ! -s "${PROBE_LOG}" ] || fail 'an unsupported proxy URL triggered a probe'
    [ -z "${http_proxy+x}" ] || fail 'an unsupported proxy URL changed http_proxy'
)

# The validation must run in the rootfs container before bootstrap, and the
# host launcher must pass the candidate separately from standard proxies.
grep -Fq 'configure_apt_cache_proxy "${SNAPSHOT}"' \
    "${REPO_ROOT}/scripts/debian-system/container-build-rootfs.sh"
grep -Fq -- '-e "DEBIAN_SYSTEM_APT_CACHE_PROXY=${DEBIAN_SYSTEM_APT_CACHE_PROXY:-}"' \
    "${REPO_ROOT}/scripts/debian-system/build.sh"
echo 'Debian APT cache tests passed.'
