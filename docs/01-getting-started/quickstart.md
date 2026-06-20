# Newcomer Quickstart

This page is the main path for anyone touching Aiden hardware for the first time. It follows the order **wire up → flash → connect Wi-Fi → configure → run → develop → upgrade → troubleshoot**, stringing the whole onboarding flow into one page. Each step covers only the key action; details are linked to the corresponding topic docs.

> Voice interaction is Aiden's core usage scenario. `text` mode is mainly for development and testing, making it convenient to verify the pipeline through a web page.

## Overview

| Step | Goal | Detailed doc |
| --- | --- | --- |
| 1 | Wire up per the hardware design | [Hardware & Wiring](hardware.md) |
| 2 | Flash the `update.img` firmware onto the Pico Zero | [Firmware Build & Flashing](firmware.md) |
| 3 | Open the config page and connect to Wi-Fi | [Config Web](../04-agent/configuration.md#config-web-the-device-config-page) |
| 4 | Configure keys such as model / tts / stt | [Agent Configuration](../04-agent/configuration.md) |
| 5 | Bring the board up and verify | [Testing & Verification](testing.md) |
| 6 | Understand the architecture and modules | [Architecture Overview](../02-architecture/overview.md) |
| 7 | Firmware build & OTA upgrade | [Firmware](firmware.md) / [OTA](../08-ota/README.md) |
| 8 | Troubleshoot when something breaks | [Troubleshooting](../07-operations/troubleshooting.md) |

## 1. Hardware & Wiring

Follow [Hardware & Wiring](hardware.md) to complete the wiring. Key connection flow:

- The target device (iPhone / PC) connects to a **USB-C hub**;
- The hub's **HDMI output** goes to the TC358743XBG HDMI-to-CSI bridge, which feeds the Pico Zero's `/dev/video0` via CSI;
- The hub's **USB-C output** connects to the Pico Zero for data and power;
- The Pico Zero emulates an HID keyboard/mouse/touch device (`/dev/hidg0`, `/dev/hidg1`) back to the target over the USB connection;
- Audio goes through the Pico Zero's onboard codec / ALSA; networking goes through Wi-Fi or the USB gadget network.

To control an iOS device over USB HID, first enable `Settings > Accessibility > Touch > AssistiveTouch` on the target device, and it is recommended to enable **Show Onscreen Keyboard** as well.

## 2. Flash the Firmware

Connect the Pico Zero's USB-C port to a computer and flash the prebuilt `update.img`. There are several flashing methods; the primary reference is the official Pico Zero documentation:

- [Luckfox Pico Zero Flashing Guide](https://wiki.luckfox.com/zh/Luckfox-Pico-Zero/Flash-image/)

The core flow is to put the board into flashing (Maskrom / Loader) mode first, then write `update.img` with the flashing tool:

```bash
./upgrade_tool/upgrade_tool uf ./update.img
```

The most common way to enter flashing mode is to hold the BOOT button while plugging in USB-C. **If the BOOT button does not work reliably**, log in to the board over adb or a TTL serial console first and run:

```bash
reboot loader
```

to enter flashing mode. Prebuilt firmware, local build paths, `upgrade_tool` usage, and the partition layout are covered in [Firmware Build & Flashing](firmware.md).

## 3. Connect to Wi-Fi

After flashing and the device boots, connect it to a computer over USB-C and open the device config page in a browser:

```text
http://192.168.42.1
```

`192.168.42.1` is the gateway address of the USB network. The config page is served by the `config_web` service on the device (default port 80).

On the page, configure Wi-Fi first:

- **Only 2.4GHz networks are supported**; 5GHz does not work;
- Enter the SSID and password; on save it is written to `/userdata/wpa_supplicant.conf`.

The config page also maintains Agent configuration and system environment variables. See [Config Web](../04-agent/configuration.md#config-web-the-device-config-page) for details.

## 4. Configure the Keys

Fill in the keys for each service on the same config page. Among them:

- **`[model]` is required**, and it must be a multimodal LLM (the Agent needs to send screenshots to the model as image input);
- **`[tts]`** enables voice playback (turning the model's reply into speech);
- **`[stt]`** enables speech-to-text for voice input.

### Choosing the Agent mode

Determined by `input_mode` in `agent.toml`. Three modes:

| Mode | Behavior | Use case |
| --- | --- | --- |
| `audio` | Device records → sends the audio attachment directly to the LLM → TTS playback | Voice interaction (audio sent straight to a multimodal model) |
| `stt` | Device records → VAD → STT to text → LLM → TTS playback | Voice interaction (transcribe to text first, then send to the LLM) |
| `text` | Starts the HTTP server + Web UI | Mainly for testing; use the web page on port 8080 for text interaction |

`audio` and `stt` are the two ways to do voice interaction: the former sends audio directly to the LLM, the latter transcribes to text first. `text` mode is mainly for testing — open the web page on the device's port `8080` for text interaction.

> Note the two distinct web pages: `http://192.168.42.1` (port 80) is the device config page; `http://<device-ip>:8080` is the Agent Web UI in `text` mode.

Field meanings, minimal working config examples, and TTS/STT provider values are in [Agent Configuration](../04-agent/configuration.md).

## 5. Bring the Board Up

After the four steps above, the board is ready to run. The Agent is supervised by the `S53agent` watchdog and starts with the firmware. After config changes, the Agent usually needs a restart:

```bash
/etc/init.d/S53agent restart
```

Commands to verify the pipeline (Frame / Audio / Agent services, screenshots, voice) are in [Testing & Verification](testing.md). Service log locations and common service commands are in [Deployment](deployment.md).

Next comes the development part.

## 6. Architecture & Modules

Before developing, build an overall understanding first:

- [Architecture Overview](../02-architecture/overview.md): how HDMI capture, audio record/playback, USB HID, and the LLM Agent fit together, plus the main data flows.

Then dive into each module:

- [Frame Service](../03-services/frame-service.md): HDMI frame capture service
- [Audio Service](../03-services/audio-service.md): audio record/playback service
- [USB HID & Device Control](../03-services/usb-hid.md)
- [Go Agent Overview](../04-agent/overview.md): LLM runtime, tool calling, voice pipeline
- [Agent Configuration](../04-agent/configuration.md): agent.toml reference and the Config Web config page
- [Tools HTTP API](../04-agent/tools-http-api.md)

Source layout is in [Source Tree](../02-architecture/source-tree.md); boot services and runtime layout are in [Boot Services & Runtime Layout](../02-architecture/boot-services.md).

## 7. Firmware Build & OTA Upgrade

After development, build the firmware and upgrade the device:

- Native / cross-compile dev environment: [Build & Development Environment](build.md);
- Full firmware build (`./build_image.sh`) and flashing: [Firmware Build & Flashing](firmware.md);
- Over-the-air upgrade: [OTA Overview](../08-ota/README.md).

### OTA for non-main branch firmware

CI assigns release channels by branch: `main` is the `stable` official release, while **other branches are published as prereleases**. The default `ota update` uses `releases/latest` and only picks up the latest official release, so it never accidentally installs dev-branch firmware.

To flash a specific dev-branch build onto a device for testing, you must point at that dev release's manifest explicitly with `--manifest-url`, bypassing the `releases/latest` lookup:

```bash
TAG="20260604-120000-abc1234"
REPO="AidenAI-IO/aiden-hardware-demo"

ota update \
  --manifest-url "https://github.com/$REPO/releases/download/$TAG/manifest.json" \
  --public-key /oem/etc/ota_pubkey.pem
```

The official signing public key is already provisioned on the device at `/oem/etc/ota_pubkey.pem`, so official-repo builds need no extra public key. It is recommended to add `--dry-run` first to only download and verify without switching slots. See [OTA Release Channels](../08-ota/ota-release-channels.md) for details.

## 8. Troubleshooting

When you hit problems, start with [Troubleshooting](../07-operations/troubleshooting.md), which covers common connection, service, video/audio, Agent, and OTA issues. Log locations are in [Deployment](deployment.md).
