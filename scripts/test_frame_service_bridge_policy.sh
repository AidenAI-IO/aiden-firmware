#!/bin/sh
set -eu

repo_root="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
config="$repo_root/overlay/etc/aiden_frame_service.conf"
init_script="$repo_root/overlay/etc/init.d/S52frame_service"
tc_edid="$repo_root/overlay/oem/usr/share/aiden/edid/hdmi_1080p30_cta.hex"

grep -q '^FRAME_SERVICE_FORCE_TRIGGER=auto$' "$config"
grep -q '^FRAME_SERVICE_EDID=auto$' "$config"
grep -q '^FRAME_SERVICE_TC358743_EDID=/oem/usr/share/aiden/edid/hdmi_1080p30_cta.hex$' "$config"
grep -q '^FRAME_SERVICE_TC358743_HPD_LOW_SECONDS=2$' "$config"
grep -q '^FRAME_SERVICE_TC358743_SETTLE_SECONDS=5$' "$config"
grep -q '^FRAME_SERVICE_WARMUP_FRAMES=$' "$config"
grep -q '^FRAME_SERVICE_WARMUP_FRAMES=$' "$init_script"
grep -q '^resolve_force_trigger() {' "$init_script"
grep -q '^resolve_edid() {' "$init_script"
grep -q '^prepare_tc358743_edid() {' "$init_script"
grep -q '^configured_keep_streamon() {' "$init_script"
grep -q '^AGENT_CONFIG=' "$init_script"
grep -q -- '--keep-streamon' "$init_script"
grep -q -- '--pause-between-captures' "$init_script"
grep -q '\*tc358743\*) echo 1' "$init_script"
grep -q '\*rk628-csi\*) echo 0' "$init_script"
grep -q '\*tc358743\*) echo "$FRAME_SERVICE_TC358743_EDID"' "$init_script"
grep -q 'force_trigger="$(resolve_force_trigger ' "$init_script"
grep -q 'edid="$(resolve_edid ' "$init_script"
grep -q -- '--clear-edid=pad=0' "$init_script"
grep -q -- '--set-edid="pad=0,file=$edid" --fix-edid-checksums' "$init_script"
grep -q 'prepare_tc358743_edid "$subdev" "$edid"' "$init_script"
grep -q '\[ "$bridge_prepared" = "1" \]' "$init_script"
grep -q '^[[:space:]]*force_trigger=0$' "$init_script"

prepare_line="$(grep -n 'prepare_tc358743_edid "$subdev" "$edid"' "$init_script" | tail -1 | cut -d: -f1)"
force_arg_line="$(grep -n 'set -- "$@" --force-trigger' "$init_script" | tail -1 | cut -d: -f1)"
no_force_arg_line="$(grep -n 'set -- "$@" --no-force-trigger' "$init_script" | tail -1 | cut -d: -f1)"
edid_arg_line="$(grep -n 'set -- "$@" --edid "$edid"' "$init_script" | tail -1 | cut -d: -f1)"
for marker_line in "$prepare_line" "$force_arg_line" "$no_force_arg_line" "$edid_arg_line"; do
    case "$marker_line" in
        ''|0|*[!0-9]*)
            echo "FAIL: frame_service ordering marker has an invalid line number" >&2
            exit 1
            ;;
    esac
done
if [ "$prepare_line" -ge "$force_arg_line" ] || \
        [ "$prepare_line" -ge "$no_force_arg_line" ] || \
        [ "$force_arg_line" -ge "$edid_arg_line" ] || \
        [ "$no_force_arg_line" -ge "$edid_arg_line" ]; then
    echo "FAIL: frame_service must select the force-trigger argument after bridge preparation" >&2
    exit 1
fi
cmp "$repo_root/edid/hdmi_1080p30_cta.hex" "$tc_edid"

run_stream_mode_fixture() {
    mode="$1"
    expected_flag="$2"
    fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/frame-service-policy.XXXXXX")"
    cleanup_fixture() {
        "$fixture_dir/init.sh" stop >/dev/null 2>&1 || true
        rm -rf "$fixture_dir"
    }
    trap cleanup_fixture EXIT INT TERM

    cat >"$fixture_dir/log.sh" <<'EOF'
aiden_log_to_file() { :; }
EOF
    cat >"$fixture_dir/frame_service" <<'EOF'
#!/bin/sh
exit 0
EOF
    cat >"$fixture_dir/env_run" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >> "$fixture_dir/args.log"
exit 0
EOF
    chmod +x "$fixture_dir/frame_service" "$fixture_dir/env_run"
    cat >"$fixture_dir/config.toml" <<EOF
ENABLE_FRAME_SERVICE=1
FRAME_SERVICE_BIN=$fixture_dir/frame_service
ENV_RUN_BIN=$fixture_dir/env_run
FRAME_SERVICE_SUBDEV=/dev/null
FRAME_SERVICE_FORCE_TRIGGER=0
FRAME_SERVICE_EDID=
FRAME_SERVICE_WARMUP_FRAMES=
FRAME_SERVICE_ALLOW_UNIFORM_FRAMES=1
SOCKET_PATH=$fixture_dir/frame.sock
LOG_PATH=$fixture_dir/service.log
PID_FILE=$fixture_dir/service.pid
WATCHDOG_PID_FILE=$fixture_dir/watchdog.pid
EOF
    sed "s#^CONFIG_FILE=.*#CONFIG_FILE=$fixture_dir/config.toml#" "$init_script" >"$fixture_dir/init.sh"
    chmod +x "$fixture_dir/init.sh"
    printf '[frame_service]\nkeep_streamon = %s\n' "$mode" >"$fixture_dir/agent.toml"

    AGENT_CONFIG="$fixture_dir/agent.toml" \
    AIDEN_LOG_HELPER="$fixture_dir/log.sh" \
        "$fixture_dir/init.sh" start >/dev/null
    i=0
    while [ ! -s "$fixture_dir/args.log" ] && [ "$i" -lt 20 ]; do
        sleep 0.05
        i=$((i + 1))
    done
    [ -s "$fixture_dir/args.log" ] || {
        echo "FAIL: frame service fixture did not launch for keep_streamon=$mode" >&2
        exit 1
    }
    args="$(head -n 1 "$fixture_dir/args.log")"
    keep_count="$(printf '%s\n' "$args" | awk '{n=0; for (i=1; i<=NF; i++) if ($i == "--keep-streamon") n++; print n}')"
    pause_count="$(printf '%s\n' "$args" | awk '{n=0; for (i=1; i<=NF; i++) if ($i == "--pause-between-captures") n++; print n}')"
    if [ "$expected_flag" = "--keep-streamon" ]; then
        [ "$keep_count" -eq 1 ] && [ "$pause_count" -eq 0 ] || {
            echo "FAIL: keep_streamon=$mode selected unexpected flags: $args" >&2
            exit 1
        }
    else
        [ "$keep_count" -eq 0 ] && [ "$pause_count" -eq 1 ] || {
            echo "FAIL: keep_streamon=$mode selected unexpected flags: $args" >&2
            exit 1
        }
    fi
    trap - EXIT INT TERM
    cleanup_fixture
}

run_stream_mode_fixture true --keep-streamon
run_stream_mode_fixture false --pause-between-captures

echo "frame_service HDMI bridge policy: ok"
