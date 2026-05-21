# 构建与开发环境

## 克隆项目

```bash
git clone --recursive git@github.com:AidenAI-IO/aiden-hardware-demo.git
cd aiden-hardware-demo
```

项目包含 `pico-sdk` 子模块，首次克隆建议使用 `--recursive`。如果已经克隆但缺少子模块：

```bash
git submodule update --init --recursive
```

## 本机 CMake 构建

标准 out-of-source 构建：

```bash
cmake -S . -B build
cmake --build build
```

产物位置：

- `build/lib/`：静态库，例如 `libaiden.a`、`libaiden_image.a`。
- `build/bin/`：可执行程序，例如 `frame_service`、`audio_service`、`config_web`、示例工具等。
- `build/CMakeFiles/`：CMake 中间文件。

> 注意：完整硬件目标依赖 Rockchip / Luckfox SDK 库。本机 native 构建主要适合代码检查、部分工具和 host 测试；面向设备运行的产物请使用交叉编译流程。

## Luckfox Docker 交叉编译

项目提供 `build.sh`，会启动 `luckfoxtech/luckfox_pico:1.0` 容器并执行 `_build.sh`：

```bash
./build.sh
```

该流程会：

1. 使用 `cmake/toolchain-arm-rockchip830.cmake` 编译 C/C++ 程序；
2. 生成 `build/lib/libaiden.a` 和 `build/bin/*`；
3. 安装/使用 Go 1.26.0；
4. 交叉编译 Go Agent：`build/bin/agent`，目标 `linux/arm GOARM=7`。

## macOS Apple Silicon + Colima

在 Apple Silicon 上建议使用原生 `aarch64` Colima VM，并通过 Docker `--platform linux/amd64` 运行 Luckfox 镜像：

```bash
brew install docker docker-buildx colima
colima start --vm-type vz --vz-rosetta

docker buildx version
docker buildx ls

./build.sh
```

不要为该工作流启动 `--arch x86_64` 的 Colima VM；保持原生 VM，再让容器以 `linux/amd64` 运行即可。

## Makefile 快捷命令

```bash
make build        # cmake -S . -B build && cmake --build build
make clean        # 删除 build/
make test         # 构建并运行 host-native 单元测试
make test-clean   # 删除 build-host/
```

`make test` 等价于：

```bash
cmake -S . -B build-host -DAIDEN_TESTS=ON
cmake --build build-host
build-host/tests/aiden_tests
```

## 主要构建目标

| 目标 | 说明 |
| --- | --- |
| `libaiden.a` | C++ 硬件 SDK 与服务公共库 |
| `libaiden_image.a` | 图像处理库 |
| `frame_service` / `frame_service_cli` | HDMI 帧捕获服务及 CLI |
| `audio_service` / `audio_service_cli` | 音频录放服务及 CLI |
| `config_web` | 设备配置 Web 服务 |
| `image_process` | 图像处理 CLI |
| `example_*` | 唤醒、音频、相机、USB HID 示例 |
| `agent` | Go Agent daemon，由 `_build.sh` 额外构建 |
