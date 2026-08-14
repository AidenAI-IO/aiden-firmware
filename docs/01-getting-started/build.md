---
sidebar_position: 5
---

# Build and Development Environment

## Clone the Project

```bash
git clone --recursive git@github.com:AidenAI-IO/aiden-firmware.git
cd aiden-firmware
```

The project includes the `pico-sdk` submodule. It's recommended to use `--recursive` on the first clone. If you've already cloned but the submodule is missing:

```bash
git submodule update --init --recursive
```

## Local CMake Build

Standard out-of-source build:

```bash
cmake -S . -B build
cmake --build build
```

Build artifacts location:

- `build/lib/`: Static libraries, e.g., `libaiden.a`, `libaiden_image.a`.
- `build/bin/`: Executables, e.g., `frame_service`, `audio_service`, `config_web`, example tools, etc.
- `build/CMakeFiles/`: CMake intermediate files.

> Note: Full hardware targets depend on Rockchip / Luckfox SDK libraries. Local native builds are mainly suitable for code checking, partial tools, and host testing; for device-runnable artifacts, use the cross-compilation workflow.

## Luckfox Docker Cross-Compilation

The project provides `build.sh`, which starts the `luckfoxtech/luckfox_pico:1.0` container and executes `_build.sh`:

```bash
./build.sh
```

This workflow will:

1. Compile C/C++ programs using `cmake/toolchain-arm-rockchip830.cmake`;
2. Generate `build/lib/libaiden.a` and `build/bin/*`;
3. Install/use Go 1.26.0;
4. Cross-compile the Go Agent and BLE daemon: `build/bin/agent` and `build/bin/ble_service`, targeting `linux/arm GOARM=7`.

## macOS Apple Silicon + Colima

On Apple Silicon, it's recommended to use a native `aarch64` Colima VM and run the Luckfox image via Docker with `--platform linux/amd64`:

```bash
brew install docker docker-buildx colima
colima start --vm-type vz --vz-rosetta

docker buildx version
docker buildx ls

./build.sh
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
| `agent` | Go Agent daemon, additionally built by `_build.sh` |
| `ble_service` | Go BlueZ GATT/ANCS daemon, additionally built by `_build.sh` |
