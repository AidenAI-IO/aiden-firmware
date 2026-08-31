#!/bin/sh
set -eu

repo_root="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
debian_config="$repo_root/overlay-debian/etc/aiden_frame_service.conf"
debian_start="$repo_root/overlay-debian/usr/lib/aiden/aiden-frame-start"
sdk_source="$repo_root/src/aiden_sdk.cpp"
frame_main="$repo_root/src/frame_service_main.cpp"
frame_source="$repo_root/src/frame_camera_capture_source.cpp"

# Debian is the maintained deployment target. Buildroot overlays are outside
# the supported solution and intentionally are not asserted here.
grep -q '^FRAME_SERVICE_PIXEL_FORMAT=nv12$' "$debian_config"
grep -q '^FRAME_SERVICE_JPEG_ENCODER=software$' "$debian_config"
grep -q '^# FRAME_SERVICE_VENC_CHANNEL=63$' "$debian_config"
grep -q -- 'FRAME_SERVICE_PIXEL_FORMAT:-nv12' "$debian_start"
grep -q -- '--pixel-format "${pixel_format}"' "$debian_start"
grep -q -- '--auto-subdev' "$debian_start"
grep -q -- '--auto-subdev' "$frame_main"
grep -q 'detect_hdmi_subdev' "$frame_source"
grep -q 'device_lock_open_pending' "$frame_source"
grep -q 'allow_edid_fallback' "$frame_source"
grep -q 'if (!config.allow_edid_fallback)' "$sdk_source"

# Exercise the no-HDMI path with a fake service binary. The helper must reach
# exec successfully and delegate bridge discovery to the C++ capture source.
mock_root=$(mktemp -d)
trap 'rm -rf "$mock_root"' EXIT
cat >"$mock_root/frame_service" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >"${FRAME_SERVICE_TEST_ARGS}"
exit 0
EOF
chmod 0755 "$mock_root/frame_service"

FRAME_SERVICE_BIN="$mock_root/frame_service" \
FRAME_SERVICE_TEST_ARGS="$mock_root/args" \
SOCKET_PATH="$mock_root/frame.sock" \
FRAME_SERVICE_SUBDEV= \
FRAME_SERVICE_FORCE_TRIGGER=auto \
FRAME_SERVICE_EDID=auto \
FRAME_SERVICE_PIXEL_FORMAT=nv12 \
    "$debian_start"
grep -q -- '--auto-subdev' "$mock_root/args"
grep -q -- '--no-force-trigger' "$mock_root/args"
grep -q -- '--pixel-format nv12' "$mock_root/args"

# The explicit `auto` sentinel behaves identically to an empty subdevice, and
# a compatibility pixel format is forwarded unchanged.
FRAME_SERVICE_BIN="$mock_root/frame_service" \
FRAME_SERVICE_TEST_ARGS="$mock_root/args-auto" \
SOCKET_PATH="$mock_root/frame-auto.sock" \
FRAME_SERVICE_SUBDEV=auto \
FRAME_SERVICE_FORCE_TRIGGER=auto \
FRAME_SERVICE_EDID=auto \
FRAME_SERVICE_PIXEL_FORMAT=uyvy \
    "$debian_start"
grep -q -- '--auto-subdev' "$mock_root/args-auto"
grep -q -- '--no-force-trigger' "$mock_root/args-auto"
grep -q -- '--pixel-format uyvy' "$mock_root/args-auto"

# Invalid formats fail closed before starting the service binary.
if FRAME_SERVICE_BIN="$mock_root/frame_service" \
    FRAME_SERVICE_TEST_ARGS="$mock_root/args-invalid" \
    FRAME_SERVICE_PIXEL_FORMAT=rgb24 \
    "$debian_start"; then
    echo "FAIL: invalid FRAME_SERVICE_PIXEL_FORMAT was accepted" >&2
    exit 1
fi

echo "Debian frame_service bridge policy: ok"
