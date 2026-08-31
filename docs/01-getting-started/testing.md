---
sidebar_position: 7
---

# Testing and Validation

## Host Unit Tests

The project uses doctest and provides a host-native test build:

```bash
make test
```

Or execute manually:

```bash
cmake -S . -B build-host -DAIDEN_TESTS=ON
cmake --build build-host
build-host/tests/aiden_tests
```

Tests cover UDS messaging, Frame Service protocol, Audio Service protocol, Ring Buffer, Wi-Fi / Agent TOML configuration parsing, image processing, and other modules.

## Frame Service Validation

After the service is running:

```bash
frame_service_cli --socket /run/frame_service/frame_service.sock health
frame_service_cli --socket /run/frame_service/frame_service.sock screenshot --out /tmp/screenshot.bmp
frame_service_cli --socket /run/frame_service/frame_service.sock latest-frame --out /tmp/frame.raw
frame_service_cli --socket /run/frame_service/frame_service.sock list-frames
```

For temporary service execution on development machines, the default socket is `/tmp/frame_service.sock`:

```bash
/oem/usr/bin/frame_service --socket /tmp/frame_service.sock
frame_service_cli --socket /tmp/frame_service.sock health
```

## Audio Service Validation

```bash
audio_service_cli --socket /run/audio_service/audio_service.sock health
audio_service_cli --socket /run/audio_service/audio_service.sock get-volume
audio_service_cli --socket /run/audio_service/audio_service.sock set-volume --volume 80
```

Record to PCM:

```bash
audio_service_cli --socket /run/audio_service/audio_service.sock record-stream --seconds 3 > /tmp/record.pcm
```

Play PCM:

```bash
cat /tmp/record.pcm | audio_service_cli --socket /run/audio_service/audio_service.sock play-stream --rate 16000 --ch 1 --bits 16
```

## USB HID Validation

```bash
sudo example_usb_hid setup composite
sudo example_usb_hid keyboard tap ENTER
sudo example_usb_hid keyboard text "hello from pico"
sudo example_usb_hid touch click 16000 16000
```

## Agent Validation

Check the service on the device:

```bash
systemctl status aiden-agent.service --no-pager
```

The Agent Web UI is available in every input mode. Open:

```text
http://<device-ip>:8080
```

Tool API:

```bash
curl http://<device-ip>:8080/api/tools
curl -X POST http://<device-ip>:8080/api/tools/shell \
  -H 'Content-Type: application/json' \
  -d '{"input":{"command":"pwd"}}'
```

## Audio Model Roundtrip Script

`scripts/test_audio_roundtrip.sh` is used to generate audio via OpenRouter audio model, then send the audio back as input for recognition:

```bash
export OPENROUTER_KEY=sk-or-...
./scripts/test_audio_roundtrip.sh
```

Optionally set proxy:

```bash
export HTTP_PROXY=http://127.0.0.1:7890
export HTTPS_PROXY=http://127.0.0.1:7890
```
