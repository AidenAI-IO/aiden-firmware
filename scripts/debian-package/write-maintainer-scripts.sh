#!/usr/bin/env bash
set -euo pipefail
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
destination=${1:?Usage: write-maintainer-scripts.sh DEBIAN_DIRECTORY}
mkdir -p "${destination}"
contract_check=$(mktemp)
trap 'rm -f "${contract_check}"' EXIT
python3 "${script_dir}/../release/contract.py" preinst-check "${contract_check}"
for phase in preinst postinst prerm postrm; do
    {
        printf '#!/bin/sh\nset -eu\nphase=%s\n' "${phase}"
        if [ "${phase}" = preinst ]; then cat "${contract_check}"; fi
        cat "${script_dir}/maintainer-script.sh"
    } > "${destination}/${phase}"
    chmod 0755 "${destination}/${phase}"
done
