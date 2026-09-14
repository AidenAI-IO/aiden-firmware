#!/usr/bin/env bash
set -euo pipefail
readonly REPO_ROOT=/work APPS_DIR=/apps OUTPUT_DIR=/out
readonly PKG_ROOT=${OUTPUT_DIR}/package-root
readonly VERSION=${AIDEN_BUSINESS_VERSION:-0.0.0}
readonly REVISION=${AIDEN_BUSINESS_REVISION:-1}
readonly PACKAGE_VERSION=${VERSION}-${REVISION}
readonly PACKAGE_NAME=aiden-business
rm -rf "${PKG_ROOT}" "${OUTPUT_DIR}/${PACKAGE_NAME}_"*.deb
install -d -m 0755 "${PKG_ROOT}/DEBIAN" "${PKG_ROOT}/usr/lib/aiden" "${PKG_ROOT}/usr/lib/aiden/lib" "${PKG_ROOT}/usr/share/aiden/config-web" "${PKG_ROOT}/usr/share/aiden/skills" "${PKG_ROOT}/usr/share/aiden/audio" "${PKG_ROOT}/usr/lib/aiden/models" "${PKG_ROOT}/usr/share/doc/${PACKAGE_NAME}"
for binary in agent audio_service ble_service cpu_vad frame_service rknn_vad ota abctl aiden-environment ttyd; do
  test -x "${APPS_DIR}/bin/${binary}" || { echo "missing application binary: ${binary}" >&2; exit 1; }
  install -m 0755 "${APPS_DIR}/bin/${binary}" "${PKG_ROOT}/usr/lib/aiden/${binary}"
done
rsync -aH --chown=0:0 "${REPO_ROOT}/src/config_web/web/" "${PKG_ROOT}/usr/share/aiden/config-web/"
rsync -aH --chown=0:0 "${REPO_ROOT}/src/agent/config/skills/" "${PKG_ROOT}/usr/share/aiden/skills/"
install -m 0644 "${REPO_ROOT}/src/agent/internal/agent/quick_actions.json" "${PKG_ROOT}/usr/share/aiden/quick_actions.json"
rsync -aH --chown=0:0 "${REPO_ROOT}/overlay-debian-oem/usr/share/aiden/audio/" "${PKG_ROOT}/usr/share/aiden/audio/"
rsync -aH --chown=0:0 "${REPO_ROOT}/overlay-debian-oem/usr/model/" "${PKG_ROOT}/usr/lib/aiden/models/"
# RGA is a BSP-owned shared library. Keep the business RPATH valid without copying it into the package.
ln -s /oem/usr/lib/librga.so "${PKG_ROOT}/usr/lib/aiden/lib/librga.so"
cat >"${PKG_ROOT}/DEBIAN/control" <<CTL
Package: ${PACKAGE_NAME}
Version: ${PACKAGE_VERSION}
Section: misc
Priority: optional
Architecture: armhf
Maintainer: Aiden AI <firmware@aiden.ai>
Depends: libc6, systemd
Description: Aiden business runtime
 Agent, hardware services, configuration web assets, bundled skills and runtime models.
CTL
cat >"${PKG_ROOT}/DEBIAN/postinst" <<'POST'
#!/bin/sh
set -e
if [ "$1" = configure ] && command -v systemctl >/dev/null 2>&1; then systemctl daemon-reload || true; fi
exit 0
POST
chmod 0755 "${PKG_ROOT}/DEBIAN/postinst"
printf '%s\n' "{\"format\":1,\"product\":\"aiden\",\"artifact_kind\":\"debian-package\",\"package\":\"${PACKAGE_NAME}\",\"business_release\":\"${VERSION}\",\"package_revision\":\"${REVISION}\",\"architecture\":\"armhf\",\"required_platform_contract\":{\"min\":\"2.0.0\",\"max_exclusive\":\"3.0.0\"},\"config_schema\":{\"min_supported\":3,\"target\":4},\"business_epoch\":1}" >"${PKG_ROOT}/usr/share/doc/${PACKAGE_NAME}/release-manifest.json"
find "${PKG_ROOT}" -type d -exec chmod 0755 {} +
find "${PKG_ROOT}" -type f ! -path '*/DEBIAN/*' -exec chmod 0644 {} +
for binary in agent audio_service ble_service cpu_vad frame_service rknn_vad ota abctl aiden-environment ttyd; do chmod 0755 "${PKG_ROOT}/usr/lib/aiden/${binary}"; done
dpkg-deb --build --root-owner-group "${PKG_ROOT}" "${OUTPUT_DIR}/${PACKAGE_NAME}_${PACKAGE_VERSION}_armhf.deb"
dpkg-deb --info "${OUTPUT_DIR}/${PACKAGE_NAME}_${PACKAGE_VERSION}_armhf.deb" >"${OUTPUT_DIR}/${PACKAGE_NAME}.deb.info"
dpkg-deb --contents "${OUTPUT_DIR}/${PACKAGE_NAME}_${PACKAGE_VERSION}_armhf.deb" >"${OUTPUT_DIR}/${PACKAGE_NAME}.deb.contents"
