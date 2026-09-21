#!/usr/bin/env bash

# Sourced in the rootfs builder so the probe uses the actual build network.
# Only http_proxy is selected here; HTTPS downloads keep their own settings.
configure_apt_cache_proxy() {
    local mirror=$1
    local candidate=${DEBIAN_SYSTEM_APT_CACHE_PROXY:-}
    local metadata

    [ -n "${candidate}" ] || return 0
    if [ -n "${http_proxy:-}${HTTP_PROXY:-}${all_proxy:-}${ALL_PROXY:-}" ]; then
        echo 'Preserving the existing proxy settings; skipping the optional APT cache'
        return 0
    fi
    case "${candidate}" in
    http://?*) ;;
    *)
        echo 'Optional APT cache requires an HTTP proxy URL; keeping the existing download settings' >&2
        return 0
        ;;
    esac

    metadata=$(mktemp)
    # Ignore no_proxy only for the probe, to ensure the candidate is tested.
    # A successful request must contain metadata signed by a Debian archive
    # key, rather than an arbitrary HTTP response or a proxy error page.
    if http_proxy="${candidate}" HTTP_PROXY= all_proxy= ALL_PROXY= \
        no_proxy= NO_PROXY= \
        timeout 20 wget --quiet --timeout=5 --tries=1 --max-redirect=5 \
            -O "${metadata}" "${mirror}/dists/trixie/InRelease" \
        && gpgv --keyring /usr/share/keyrings/debian-archive-keyring.gpg \
            "${metadata}" >/dev/null 2>&1 \
        && grep -qx 'Codename: trixie' "${metadata}"; then
        export http_proxy="${candidate}"
        echo 'Using the optional APT cache after validating signed Debian metadata'
    else
        echo 'Optional APT cache validation failed; keeping the existing download settings' >&2
    fi
    rm -f "${metadata}"
    return 0
}
