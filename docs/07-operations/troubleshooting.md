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
- TC358743 / `/dev/v4l-subdev2` status is abnormal.

## Frame Service keeps restarting

View logs:

```bash
tail -f /var/log/frame_service/frame_service.log
```

Check:

- Whether `/oem/usr/bin/frame_service` exists and is executable;
- Whether `/dev/video0`, `/dev/v4l-subdev2` exist;
- Whether EDID / HDMI signal is normal;
- Whether other processes are using `/dev/video0`.

## Agent Web UI won't open

Check:

```bash
/etc/init.d/S53agent status
tail -f /userdata/agent/log/agent.log
netstat -lntp | grep 8080
```

Verify:

- `input_mode = "text"`;
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
