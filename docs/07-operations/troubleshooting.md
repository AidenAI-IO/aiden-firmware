# Troubleshooting

## `example_camera_capture` reports `Device or resource busy`

Cause: `frame_service` is exclusively using `/dev/video0`.

Solution:

```bash
/etc/init.d/S52frame_service stop
./build/bin/example_camera_capture
/etc/init.d/S52frame_service start
```

For daily screenshots and frame testing, use:

```bash
frame_service_cli screenshot --out /tmp/screenshot.bmp
```

## `screenshot` tool fails

Check:

```bash
/etc/init.d/S52frame_service status
frame_service_cli --socket /run/frame_service/frame_service.sock health
ls -l /run/frame_service/frame_service.sock
```

Common causes:

- `frame_service` is not running;
- `[hid].frame_socket` path in `agent.toml` is inconsistent;
- HDMI input is not synced;
- RK628D or TC358743 HDMI subdevice status is abnormal.

## Frame Service keeps restarting

View logs:

```bash
tail -f /var/log/frame_service/frame_service.log
```

Check:

- Whether `/oem/usr/bin/frame_service` exists and is executable;
- Whether `/dev/video0` and an RK628D or TC358743 subdevice exist;
- Whether EDID / HDMI signal is normal;
- Whether other processes are using `/dev/video0`.

Find the HDMI bridge subdevice without assuming a fixed index. RK628D reports
`rk628-csi`; TC358743 reports `tc358743`:

```bash
for f in /sys/class/video4linux/v4l-subdev*/name; do echo "$f: $(cat "$f")"; done
```

If the matching `rk628-csi` or `tc358743` node was not selected, set
`FRAME_SERVICE_SUBDEV=/dev/v4l-subdevX` in
`/etc/aiden_frame_service.conf` to that node and restart `S52frame_service`.

## Agent Web UI won't open

Check:

```bash
/etc/init.d/S53agent status
tail -f /userdata/agent/log/agent.log
netstat -lntp | grep 8080
```

Verify:

- The daemon is listening on port `8080`; both `text` and `stt` modes serve the Web UI;
- `agent.toml` TOML syntax is correct;
- Model configuration and API key are available;
- Firewall, USB network, or Wi-Fi IP is correct.

## Wi-Fi connection is unstable

Solution:

- Connect an external 2.4 GHz antenna, antenna connector uses IPEX1 generation;
- Or try switching Wi-Fi channel to avoid heavily interfered channels.

## Voice mode has no sound or cannot record audio

Check:

```bash
/etc/init.d/S53audio_service status
audio_service_cli --socket /run/audio_service/audio_service.sock health
audio_service_cli --socket /run/audio_service/audio_service.sock get-volume
amixer sget 'DAC HPMIX'
amixer sget 'DAC LINEOUT'
```

Recommendations:

- First run `scripts/setup_audio_volume.sh`;
- Confirm `[audio].socket` path matches the service;
- Use `record-stream` and `play-stream` to verify recording/playback separately;
- Check if `ffmpeg` exists when TTS fails.

## RKNN VAD inference fails

First run helper self-test directly on the board:

```bash
/oem/usr/bin/rknn_vad --model /oem/usr/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn --weights /oem/usr/model/silero_vad_6_2_lstm_decoder_weights.bin --self-test
/oem/usr/bin/cpu_vad --weights /oem/usr/model/silero_vad_6_2_lstm_decoder_weights.bin --self-test
```

On success, it will output `P <probability>`. Current RV1106 helper uses RKNN zero-copy IO; if it outputs `rknn_set_io_mem failed`, `rknn_run failed`, or old helper outputs `rknn_inputs_set failed`, check:

- Whether `/oem/usr/lib/librknnmrt.so` version matches the model;
- Whether `silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn` is the encoder model re-converted for RV1106 target;
- Whether input/output tensor type, size, scale, zero-point in helper logs are normal.

## HID input is ineffective

Check:

```bash
ls -l /dev/hidg*
mount | grep configfs
lsmod | grep -E 'dwc2|libcomposite'
```

Try reinitializing:

```bash
sudo ./build/bin/example_usb_hid cleanup
sudo ./build/bin/example_usb_hid setup composite
```

For iOS target devices, confirm AssistiveTouch is enabled.

## Cannot capture screen from iPhone 16e

Cause: iPhone 16e's USB-C port has compatibility issues.

Solution:

- Currently unable to support screen capture from iPhone 16e;
- Switch to other compatible models for testing.

## Board reboots frequently after connecting phone

Cause: Insufficient power supply.

Solution:

- Switch to a USB hub with better power supply capability;
- Or use a USB hub with external power support.

## HTTP Tool API access exception

- Set `NO_PROXY` for device private IP / USB network adapter address;
- First access `GET /api/tools` to confirm service is reachable;
- When a tool invocation fails, check `is_error` and `output` in the response
  from `POST /api/tools/{tool_name}`;
- Separate transport failure from tool failure judgement.

## `opkg update` prints nothing, and no package can be found

```text
# opkg update
# opkg install htop
error: opkg_prepare_url_for_install: Couldn't find anything to satisfy 'htop'.
```

`opkg update` prints one `Downloading` line per configured source, so silence
means no source is configured. Two different causes produce it:

```bash
cat /etc/opkg/*.conf | grep -E '^\s*(src|dist)'   # any sources at all?
/etc/init.d/S22opt status                          # is /opt actually bound?
```

Every `src` line lives in `/opt/etc/opkg/userfeeds.conf`, so an unbound `/opt`
looks exactly like "no source was ever configured". Check both before writing a
source into a file that cannot be read.

Note this is *not* opkg refusing to start. `/etc/opkg/90-userfeeds.conf` is a
symlink into `/opt`, and when it dangles uClibc-ng's `glob()` silently drops it —
opkg never tries to open it and runs normally. The protection against installing
into the A/B rootfs comes from the read-only seal `S22opt` applies to `/opt`,
plus the fact that no source is reachable.

### Recover the `/opt` mount

`opt=bound` is the only healthy status. Other states require mount recovery
before running opkg:

- `opt=sealed`: `/userdata` was unavailable or the bind mount failed, so
  `S22opt` protected the rootfs with a read-only tmpfs. Confirm that `/userdata`
  is mounted, fix the reported cause, then run `/etc/init.d/S22opt restart`.
- `opt=wrong-source`: something other than `/userdata/opt` is mounted at
  `/opt`. Do not run opkg or unmount it blindly; stop processes using that mount,
  remove the configuration that created it, and reboot.
- `opt=unbound`: run `/etc/init.d/S22opt restart` and use its error output to
  diagnose the missing `/userdata` mount, directory creation failure, or bind
  failure.

After recovery, require a successful status check before continuing:

```bash
/etc/init.d/S22opt status
# opt=bound source=/userdata/opt
```

### Configure a package feed

Package sources belong only in `/opt/etc/opkg/userfeeds.conf`. The init script
seeds the Entware source when it creates this file for the first time, but it
never changes an existing file, including an empty one. To enable or change a
feed, first verify that `/opt` is bound, then edit the file so it contains one
source declaration per feed:

```text
src/gz entware https://bin.entware.net/armv7sf-k3.2
```

Avoid declaring the same source more than once. After saving the file, refresh
the package lists and confirm that packages are visible:

```bash
opkg update
opkg list | head
```

Run plain `opkg` commands without `-f`; supplying a standalone configuration
file bypasses the fixed fragments under `/etc/opkg` that keep package files,
metadata, caches, and locks in their intended locations.

### Recover opkg metadata

If an interrupted `opkg update` leaves only the downloaded package lists or
cache inconsistent, remove those regenerable files and download them again:

```bash
rm -f /opt/var/lib/opkg/lists/* /opt/var/cache/opkg/*
opkg update
```

Do not use that procedure for a truncated `/opt/var/lib/opkg/status` file. The
status file, `/opt/var/lib/opkg/info`, and the installed package files form one
state and must come from the same backup. Stop programs running from `/opt`,
unmount `/opt`, move the current `/userdata/opt` aside instead of overwriting
it, restore the complete backed-up tree at `/userdata/opt`, and start `S22opt`
again. Preserve file ownership, modes, and symlinks during the restore. The
`S22opt stop` action deliberately leaves `/opt` mounted because package
processes may still be running; it is not a substitute for stopping those
processes and running `umount /opt` before the replacement.

If no consistent backup exists, reset the whole package root rather than mixing
old package files with a new database. Stop `/opt` programs, unmount `/opt`, move
`/userdata/opt` to a recovery path, and run `/etc/init.d/S22opt start`. The init
script creates a clean package layout and feed file; then run `opkg update` and
reinstall the required packages. If `/opt` is busy, identify and stop its users
or reboot into a maintenance state, then repeat the unmount check before
replacing or restoring the package root.

## Docker build fails

Check:

```bash
docker buildx version
docker info
```

Recommended for Apple Silicon:

```bash
colima start --vm-type vz --vz-rosetta
./build.sh
```

Confirm not using `--arch x86_64` to start Colima VM.
