#!/usr/bin/env bash
set -euo pipefail
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
destination=${1:?Usage: write-maintainer-scripts.sh DEBIAN_DIRECTORY}
mkdir -p "${destination}"
for phase in preinst postinst prerm postrm; do
    {
        printf '#!/bin/sh\nphase=%s\n' "${phase}"
        cat "${script_dir}/maintainer-script.sh"
    } > "${destination}/${phase}"
    chmod 0755 "${destination}/${phase}"
done
