---
sidebar_position: 5
---

# Build and Development Environment

## Clone the Project

```bash
git clone --recursive git@github.com:AidenAI-IO/aiden-firmware.git
cd aiden-firmware
```

The project includes Git submodules such as `pico-sdk` and MobileGym. It's recommended to use `--recursive` on the first clone. For an existing checkout, synchronize submodule URLs before initializing or updating them:

```bash
git submodule sync --recursive
git submodule update --init --recursive
```

## Host Test Build

Build the host-native test targets without Rockchip dependencies:

```bash
cmake -S . -B build-host -DAIDEN_TESTS=ON
cmake --build build-host
ctest --test-dir build-host --output-on-failure
```

Full hardware targets depend on Rockchip/Luckfox armhf libraries. Use the Debian
cross-compilation workflow for device-runnable artifacts.

## Debian ARM Cross-Compilation

Build and audit the Debian armhf application bundle with:

```bash
scripts/debian-stage2/build-apps.sh all
```

This workflow will:

1. Build the pinned Debian armhf toolchain container and opencv-mobile;
2. Compile C/C++ programs using `cmake/toolchains/armhf-debian.cmake`;
3. Install/use Go 1.26.0;
4. Cross-compile the Go Agent and BLE daemon for `linux/arm GOARM=7`;
5. Audit the application and shared-library bundle under `output/debian-stage2/`.

Use `./debian_build.sh` for the complete signed local firmware image set. It
builds Stage 2 applications, the Debian rootfs, the RV1106 BSP, A/B images, and
the local OTA manifest in one workflow.

```bash
./debian_build.sh
```

## macOS Apple Silicon + Colima

On Apple Silicon, it's recommended to use a native `aarch64` Colima VM and run the Luckfox image via Docker with `--platform linux/amd64`:

```bash
brew install docker docker-buildx colima
colima start --vm-type vz --vz-rosetta

docker buildx version
docker buildx ls

./debian_build.sh
```

Do not start a `--arch x86_64` Colima VM for this workflow; keep the native VM and let the container run as `linux/amd64`.

## Makefile Shortcuts

```bash
make build        # cmake -S . -B build && cmake --build build
make clean        # Remove build/
make test         # Build and run host-native unit tests
make test-clean   # Remove build-host/
```

`make test` is equivalent to:

```bash
cmake -S . -B build-host -DAIDEN_TESTS=ON
cmake --build build-host
cd build-host && ctest --output-on-failure
```

## Main Build Targets

| Target | Description |
| --- | --- |
| `libaiden.a` | C++ hardware SDK and service common library |
| `libaiden_image.a` | Image processing library |
| `frame_service` / `frame_service_cli` | HDMI frame capture service and CLI |
| `audio_service` / `audio_service_cli` | Audio recording/playback service and CLI |
| `config_web` | Device configuration web service |
| `image_process` | Image processing CLI |
| `example_*` | Wake word, audio, camera, USB HID examples |
| `agent` | Go Agent daemon, additionally built by the application cross-build task |
| `ble_service` | Go BlueZ GATT/ANCS daemon, additionally built by the application cross-build task |
