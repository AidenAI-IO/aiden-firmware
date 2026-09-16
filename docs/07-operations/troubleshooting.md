---
sidebar_position: 1
---

# Troubleshooting

## `example_camera_capture` reports `Device or resource busy`

Cause: `frame_service` is exclusively using `/dev/video0`.

Solution:

```bash
systemctl stop aiden-frame.service
example_camera_capture
systemctl start aiden-frame.service
```

For daily screenshots and frame testing, use:

```bash
frame_service_cli screenshot --out /tmp/screenshot.bmp
```

## `screenshot` tool fails

Check:

```bash
systemctl status aiden-frame.service --no-pager
frame_service_cli --socket /run/frame_service/frame_service.sock health
ls -l /run/frame_service/frame_service.sock
```

Common causes:

- `frame_service` is not running;
- `[advanced_settings.hardware.hid].frame_socket` path in `agent.toml` is inconsistent;
- HDMI input is not synced;
- RK628D or TC358743 HDMI subdevice status is abnormal.

On the Debian firmware, the first two cases are distinguishable with:

```bash
systemctl status aiden-frame.service --no-pager
frame_service_cli --socket /run/frame_service/frame_service.sock health
```

The service is intentionally socket-ready even when no HDMI source is present.
In that state `health` returns `state=RECOVERING` (or `STARTING`) and a
screenshot is unavailable until a frame arrives; a missing socket indicates a
real service/startup failure. Connecting HDMI later causes the capture manager
to retry automatically and transition to `RUNNING`.

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
`/etc/aiden_frame_service.conf` to that node and restart `aiden-frame.service`.

## Agent Web UI won't open

Check:

```bash
systemctl status aiden-agent.service --no-pager
tail -f /userdata/agent/log/agent.log
ss -lntp | grep 8080
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
systemctl status aiden-audio.service --no-pager
audio_service_cli --socket /run/audio_service/audio_service.sock health
audio_service_cli --socket /run/audio_service/audio_service.sock get-volume
amixer sget 'DAC HPMIX'
amixer sget 'DAC LINEOUT'
```

Recommendations:

- First run `scripts/setup_audio_volume.sh`;
- Confirm `[voice_settings.classic.audio].socket` path matches the service;
- Use `record-stream` and `play-stream` to verify recording/playback separately;
- Check if `ffmpeg` exists when TTS fails.

## RKNN VAD inference fails

First run helper self-test directly on the board:

```bash
/oem/usr/bin/rknn_vad --model /oem/usr/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn --weights /oem/usr/model/silero_vad_6_2_lstm_decoder_weights.bin --self-test
/oem/usr/bin/cpu_vad --weights /oem/usr/model/silero_vad_6_2_lstm_decoder_weights.bin --self-test
```

On success, it will output `P <probability>`. Current RV1106 helper uses RKNN zero-copy IO; if it outputs `rknn_set_io_mem failed`, `rknn_run failed`, or old helper outputs `rknn_inputs_set failed`, check:

- Whether the RKNN mini runtime embedded in `/oem/usr/bin/rknn_vad` matches the model;
- Whether `silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn` is the encoder model re-converted for RV1106 target;
- Whether input/output tensor type, size, scale, zero-point in helper logs are normal.

### Why the mini runtime is embedded

The RV1106 VAD encoder/decoder models are Rockchip **mini runtime split**
format. The official armhf/glibc 2.3.2 release only ships a **full** runtime,
which opens the NPU but rejects these models with:

```text
Verify ModelBuffer failed!
Invalid RKNN format
Import rknn model failed!
```

`/oem/usr/bin/rknn_vad` therefore statically embeds the armhf-uclibc mini
runtime (`librknnmrt.a`, 2.3.2) instead of linking a dynamic `librknnrt.so`.
Because that archive was built against uClibc ctype tables,
`src/rknn_glibc_compat.c` provides the two data symbols it references
(`__ctype_b`, `__ctype_tolower`) from the glibc locale tables. Do not replace
this with the full runtime or a dynamic RKNN library, or the model-load failure
returns. At runtime this helper only needs `/dev/rknpu`.

## NPU or media devices are not accessible

`/dev/rknpu` and `/dev/mpi/*` are expected to be `0660 root:video`, and the
`aiden` user must be in the `video` and `audio` groups:

```bash
ls -l /dev/rknpu /dev/mpi/ 2>/dev/null
id aiden
```

- Udev rules set the NPU and media nodes to `0660 root:video`.
- `aiden-media-modules.service` loads the Rockchip media/NPU modules and
  creates the DMA heap nodes and links, including `/dev/dma_heap/system`,
  before the frame and audio services start.
- The mini RKNN runtime only needs `/dev/rknpu`; a missing
  `/dev/dma_heap/system` only affects the full runtime path.

## HID input is ineffective

Check:

```bash
ls -l /dev/hidg*
mount | grep configfs
lsmod | grep -E 'dwc2|libcomposite'
```

Try reinitializing:

```bash
systemctl restart aiden-usb-gadget.service
```

For iOS target devices, confirm AssistiveTouch is enabled.

If the host reports `unknown main item tag`, `item fetching failed`, or
`hid-generic ... error -22`, the HID report descriptor was emitted as ASCII
text instead of raw bytes. The descriptors must be written with POSIX octal
escapes so they survive `dash`; see
[USB HID and ECM](../02-architecture/boot-services.md#usb-hid-and-ecm). A bare
`error -71` (`device not accepting address`) is a lower-level USB
control-transfer failure and points at cabling, power, or the host port rather
than the descriptor.

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

## A Python dependency is unavailable

The Debian image does not use opkg or a mutable `/opt` package root. For an
Agent-side Python dependency, first check storage health, then install one exact
wheel into the persistent userbase and validate the environment:

```bash
cat /run/agent/storage_level
/usr/bin/python3 -m pip install --only-binary=:all: 'packaging==24.2'
/usr/bin/python3 -m pip check
```

Packages are stored under `/userdata/agent/python` and survive reboot and A/B
updates. Do not use pip to replace firmware-provided pip, setuptools, or wheel,
and do not use `apt` to mutate the production A/B rootfs. See
[Persistent Python Packages](../04-agent/python-packages.md) for cleanup and
storage behavior.

## Docker build fails

Check:

```bash
docker buildx version
docker info
```

Recommended for Apple Silicon:

```bash
colima start --vm-type vz --vz-rosetta
./debian_build.sh
```

Confirm not using `--arch x86_64` to start Colima VM.
