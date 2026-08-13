#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_IMAGE_SH="$ROOT_DIR/build_image.sh"
INNER_BUILD_IMAGE_SH="$ROOT_DIR/_build_image.sh"
# Only sysdrv/Makefile, the two Buildroot defconfigs, and the small project-owned
# package override files are read below, so PR CI can point this at a sparse
# checkout of the pinned submodule commit instead of cloning the ~1GB pico-sdk
# working tree.
PICO_SDK="${PICO_SDK_DIR:-$ROOT_DIR/pico-sdk}"

if [ ! -f "$PICO_SDK/sysdrv/Makefile" ]; then
  echo "missing pico-sdk sysdrv/Makefile under $PICO_SDK; set PICO_SDK_DIR or check out the pico-sdk submodule" >&2
  exit 1
fi

for script in "$BUILD_IMAGE_SH" "$INNER_BUILD_IMAGE_SH"; do
  if ! grep -q 'AIDEN_REPRODUCIBLE_IMAGE_EPOCH' "$script"; then
    echo "$(basename "$script") must use AIDEN_REPRODUCIBLE_IMAGE_EPOCH for the default reproducible image timestamp" >&2
    exit 1
  fi

  if ! grep -q 'export SOURCE_DATE_EPOCH' "$script"; then
    echo "$(basename "$script") must export SOURCE_DATE_EPOCH for image packaging tools" >&2
    exit 1
  fi
done

if grep -Eq 'git .*log -1 --format=%ct|git .*log -1 .*%ct' "$BUILD_IMAGE_SH" "$INNER_BUILD_IMAGE_SH"; then
  echo "image builds must not derive the default SOURCE_DATE_EPOCH from the current commit time" >&2
  exit 1
fi

if grep -Eq 'AIDEN_REPRODUCIBLE_IMAGE_EPOCH:-0' "$BUILD_IMAGE_SH" "$INNER_BUILD_IMAGE_SH"; then
  echo "image builds must use a non-zero default SOURCE_DATE_EPOCH so falsey-zero package bugs cannot affect releases" >&2
  exit 1
fi

if ! grep -Eq 'AIDEN_REPRODUCIBLE_IMAGE_EPOCH:-[1-9][0-9]*' "$BUILD_IMAGE_SH" || \
   ! grep -Eq 'AIDEN_REPRODUCIBLE_IMAGE_EPOCH:-[1-9][0-9]*' "$INNER_BUILD_IMAGE_SH"; then
  echo "image builds must keep a deterministic non-zero default SOURCE_DATE_EPOCH" >&2
  exit 1
fi

for defconfig in \
  "$PICO_SDK/sysdrv/tools/board/buildroot/luckfox_pico_defconfig" \
  "$PICO_SDK/sysdrv/tools/board/buildroot/luckfox_pico_w_defconfig"; do
  if [ ! -f "$defconfig" ]; then
    echo "missing Buildroot defconfig: $defconfig" >&2
    exit 1
  fi

  if ! grep -q '^BR2_REPRODUCIBLE=y$' "$defconfig"; then
    echo "$(basename "$defconfig") must enable BR2_REPRODUCIBLE so package builds use Buildroot's reproducible timestamp policy" >&2
    exit 1
  fi

  if ! grep -q '^BR2_TAR_OPTIONS="--no-same-owner"$' "$defconfig"; then
    echo "$(basename "$defconfig") must set BR2_TAR_OPTIONS=--no-same-owner so Dockerized Buildroot extracts archives without restoring unmappable owners" >&2
    exit 1
  fi

  if ! grep -q '^BR2_PACKAGE_ANDROID_TOOLS_AIDEN=y$' "$defconfig"; then
    echo "$(basename "$defconfig") must include the android-tools-aiden adb client (1.0.41) so the board can act as an ADB host" >&2
    exit 1
  fi

  if ! grep -q '^BR2_PACKAGE_PYTHON_PIP=y$' "$defconfig"; then
    echo "$(basename "$defconfig") must include pip so the Agent can install scoped Python dependencies under /userdata" >&2
    exit 1
  fi

  if ! grep -q '^BR2_PACKAGE_PYTHON_CHARSET_NORMALIZER=y$' "$defconfig"; then
    echo "$(basename "$defconfig") must include charset-normalizer because the firmware aiohttp package requires it" >&2
    exit 1
  fi

  if grep -q '^BR2_PACKAGE_ANDROID_TOOLS_ADBD=y$' "$defconfig"; then
    echo "$(basename "$defconfig") must not enable adbd; Aiden expects the board to run the adb client instead" >&2
    exit 1
  fi
done

make_define_body() {
  local name="$1"
  sed -n "/^define ${name}$/,/^endef$/p" "$PICO_SDK/sysdrv/Makefile" |
    sed '/^[[:space:]]*#/d'
}

make_target_recipe() {
  local name="$1"
  awk -v target="$name" '
    $0 ~ "^" target ":[^=]*$" { in_target = 1; next }
    in_target && $0 ~ /^[^[:space:]#][^=]*:/ { exit }
    in_target && $0 !~ /^[[:space:]]*#/ { print }
  ' "$PICO_SDK/sysdrv/Makefile"
}

active_buildroot_recipe="$(make_target_recipe buildroot)"
sync_buildroot_board_config="$(make_define_body sync_buildroot_board_config)"
if [ -z "$active_buildroot_recipe" ] || \
   ! printf '%s\n' "$active_buildroot_recipe" | grep -Fq '$(call sync_buildroot_board_config)'; then
  echo "pico-sdk sysdrv Makefile must sync Buildroot board config before each Buildroot build" >&2
  exit 1
fi

if ! printf '%s\n' "$active_buildroot_recipe" | grep -Fq '$(call refresh_buildroot_config_state)'; then
  echo "pico-sdk active Buildroot target must refresh the Buildroot configuration state before building packages" >&2
  exit 1
fi

charset_pin_dir="$PICO_SDK/sysdrv/tools/board/buildroot/python-charset-normalizer-aiden"
charset_pin_mk="$charset_pin_dir/python-charset-normalizer.mk"
charset_pin_hash="$charset_pin_dir/python-charset-normalizer.hash"
for pin_file in "$charset_pin_mk" "$charset_pin_hash"; do
  if [ ! -f "$pin_file" ]; then
    echo "missing project-owned charset-normalizer pin: $pin_file" >&2
    exit 1
  fi
done

if ! grep -q '^PYTHON_CHARSET_NORMALIZER_VERSION = 2\.1\.1$' "$charset_pin_mk"; then
  echo "firmware charset-normalizer must stay pinned to 2.1.1, which satisfies aiohttp 3.8.3's >=2.0,<3.0 requirement" >&2
  exit 1
fi

if ! grep -q '^sha256  5a3d016c7c547f69d6f81fb0db9449ce888b418b5b9952cc5e6e66843e9dd845  charset-normalizer-2\.1\.1\.tar\.gz$' "$charset_pin_hash"; then
  echo "charset-normalizer 2.1.1 source hash must remain pinned to the verified PyPI artifact" >&2
  exit 1
fi

if [ -z "$sync_buildroot_board_config" ] || \
   ! printf '%s\n' "$sync_buildroot_board_config" | grep -Fq '$(call inject_python_charset_normalizer_aiden_pkg)'; then
  echo "pico-sdk sysdrv Makefile must inject the compatible charset-normalizer recipe before every Buildroot build" >&2
  exit 1
fi

buildroot_config_state_value="$(make_define_body buildroot_config_state_value)"
if ! printf '%s\n' "$buildroot_config_state_value" | grep -q 'PYTHON_CHARSET_NORMALIZER_AIDEN_SRC'; then
  echo "pico-sdk Buildroot state must include the project-owned charset-normalizer recipe so pin changes invalidate cached package output" >&2
  exit 1
fi

if ! grep -q 'define refresh_buildroot_config_state' "$PICO_SDK/sysdrv/Makefile" || \
   ! grep -q '.aiden_buildroot_config_state' "$PICO_SDK/sysdrv/Makefile" || \
   ! grep -q 'Buildroot configuration changed; rebuilding generated Buildroot state' "$PICO_SDK/sysdrv/Makefile"; then
  echo "pico-sdk sysdrv Makefile must invalidate generated Buildroot state when reproducible config inputs change" >&2
  exit 1
fi

refresh_buildroot_config_state="$(sed -n '/^define refresh_buildroot_config_state$/,/^endef$/p' "$PICO_SDK/sysdrv/Makefile")"
if ! printf '%s\n' "$refresh_buildroot_config_state" | grep -Fq '$(call buildroot_config_state_value)'; then
  echo "pico-sdk Buildroot state refresh must use the shared Buildroot state value" >&2
  exit 1
fi

# The stamp records which configuration produced the tree in output/, so it must
# be written by the same define that decides whether to clean -- before packages
# are built, not after a successful build. Writing it only on success leaves a
# half-built tree labelled with the previous configuration's hash, so the next
# build using that configuration sees a match, skips the clean and reuses
# packages configured for a different one. A branch enabling opkg (which selects
# BR2_PACKAGE_LIBARCHIVE) left mpv configured with --enable-libarchive in a tree
# main reused, breaking every main build until the stamp was invalidated.
if ! printf '%s\n' "$refresh_buildroot_config_state" | grep -Fq '> "$$stamp"'; then
  echo "pico-sdk Buildroot state refresh must record the configuration that generated output/ in the same step that decides whether to clean, so an interrupted build cannot leave a stale stamp that suppresses the next clean" >&2
  exit 1
fi

# Assert the property, not one spelling of it: the stamp must be written where
# the clean decision is made and nowhere else. Naming a new define, or inlining
# the write straight into the buildroot recipe, reintroduces the same bug, so
# check the recipe body rather than only the old define name.
# Match the target on its name alone. Anchoring on the full "buildroot: prepare"
# line made an unrelated edit -- adding a prerequisite -- collapse the range to
# empty and fail this check for a reason that has nothing to do with the stamp.
buildroot_target="$(sed -n '/^buildroot:[[:space:]]/,/^buildroot_clean:/p' "$PICO_SDK/sysdrv/Makefile")"
if [ -z "$buildroot_target" ]; then
  echo "could not locate the buildroot target in pico-sdk sysdrv/Makefile" >&2
  exit 1
fi

if printf '%s\n' "$buildroot_target" | grep -Fq '"$$stamp"'; then
  echo "pico-sdk buildroot target must not write the Buildroot state stamp in its own recipe; the stamp belongs in refresh_buildroot_config_state, beside the clean decision it feeds" >&2
  exit 1
fi

# Find every write to the stamp path, wherever it lives and whatever it is
# called. Scanning only defines whose name contains "stamp" missed a rename to
# a name like persist_br_state, and matching only the '$$stamp' variable missed
# a write that spells the path out in full. Both are the same bug, so key off
# the stamp filename -- the one thing any such write has to mention -- and
# require every mention to sit inside refresh_buildroot_config_state.
stamp_basename='.aiden_buildroot_config_state'
# One awk pass rather than grep|grep|cut: a grep that filters everything out
# exits 1, and under `set -o pipefail` that aborts the script before the
# explicit diagnostics below can run. Comment lines are skipped -- prose naming
# the stamp is not a write.
stamp_mentions="$(awk -v needle="$stamp_basename" '
  { line = $0; sub(/^[[:space:]]+/, "", line) }
  line ~ /^#/ { next }
  index($0, needle) { print NR }
' "$PICO_SDK/sysdrv/Makefile")"
if [ -z "$stamp_mentions" ]; then
  echo "pico-sdk sysdrv Makefile never mentions $stamp_basename; the Buildroot state stamp must exist to gate the cross-config clean" >&2
  exit 1
fi

refresh_start="$(awk '/^define refresh_buildroot_config_state$/ { print NR; exit }' "$PICO_SDK/sysdrv/Makefile")"
if [ -z "$refresh_start" ]; then
  echo "could not locate 'define refresh_buildroot_config_state' in pico-sdk sysdrv/Makefile" >&2
  exit 1
fi
refresh_end="$(awk -v start="$refresh_start" 'NR > start && /^endef$/ { print NR; exit }' "$PICO_SDK/sysdrv/Makefile")"
if [ -z "$refresh_end" ]; then
  echo "'define refresh_buildroot_config_state' in pico-sdk sysdrv/Makefile is not terminated by endef" >&2
  exit 1
fi

for line in $stamp_mentions; do
  if [ "$line" -lt "$refresh_start" ] || [ "$line" -gt "$refresh_end" ]; then
    echo "pico-sdk sysdrv Makefile touches $stamp_basename at line $line, outside refresh_buildroot_config_state (lines $refresh_start-$refresh_end); the stamp must be written only beside the clean decision it feeds, since splitting the two is what let them drift apart" >&2
    exit 1
  fi
done

buildroot_config_state_policy="$buildroot_config_state_value
$refresh_buildroot_config_state"
if ! printf '%s\n' "$buildroot_config_state_policy" | grep -q 'SOURCE_DATE_EPOCH'; then
  echo "pico-sdk Buildroot state must include SOURCE_DATE_EPOCH so stale package outputs are rebuilt when the reproducible epoch changes" >&2
  exit 1
fi

if ! printf '%s\n' "$buildroot_config_state_policy" | grep -q 'AIDEN_BUILDROOT_REPRODUCIBLE_STATE_VERSION'; then
  echo "pico-sdk Buildroot state must include an Aiden reproducibility state version to invalidate older runner caches" >&2
  exit 1
fi

if ! printf '%s\n' "$buildroot_config_state_policy" | grep -Fq 'sha256sum "$(SYSDRV_DIR)/Makefile"'; then
  echo "pico-sdk Buildroot state must include sysdrv/Makefile so reproducibility policy changes invalidate stale package outputs" >&2
  exit 1
fi

if ! grep -q 'define refresh_boardtools_config_state' "$PICO_SDK/sysdrv/Makefile" || \
   ! grep -q '.aiden_boardtools_config_state' "$PICO_SDK/sysdrv/Makefile" || \
   ! grep -q 'Board tools reproducibility inputs changed; rebuilding generated board tool state' "$PICO_SDK/sysdrv/Makefile"; then
  echo "pico-sdk sysdrv Makefile must invalidate generated board tool state when reproducible inputs change" >&2
  exit 1
fi

refresh_boardtools_config_state="$(sed -n '/^define refresh_boardtools_config_state$/,/^endef$/p' "$PICO_SDK/sysdrv/Makefile")"
if ! printf '%s\n' "$refresh_boardtools_config_state" | grep -q 'SOURCE_DATE_EPOCH'; then
  echo "pico-sdk board tool state must include SOURCE_DATE_EPOCH so stale board tool outputs are rebuilt when the reproducible epoch changes" >&2
  exit 1
fi

if ! printf '%s\n' "$refresh_boardtools_config_state" | grep -q 'AIDEN_BOARDTOOLS_REPRODUCIBLE_STATE_VERSION'; then
  echo "pico-sdk board tool state must include an Aiden reproducibility state version to invalidate older runner caches" >&2
  exit 1
fi

if ! printf '%s\n' "$refresh_boardtools_config_state" | grep -q 'tools_board-clean'; then
  echo "pico-sdk board tool state must clean generated board tool outputs when reproducibility inputs change" >&2
  exit 1
fi

if ! printf '%s\n' "$refresh_boardtools_config_state" | grep -q 'tools/board/toolkits/openssl'; then
  echo "pico-sdk board tool state must include board OpenSSL inputs because adb-related tooling links against that cached output" >&2
  exit 1
fi

if ! grep -q '^boardtools: refresh_boardtools_config_state$' "$PICO_SDK/sysdrv/Makefile" || \
   ! sed -n '/^boardtools:/,/^$/p' "$PICO_SDK/sysdrv/Makefile" | grep -q 'tools_board-builds'; then
  echo "pico-sdk boardtools target must refresh board tool state before reusing cached outputs" >&2
  exit 1
fi

echo "reproducible rootfs timestamp policy tests passed"
