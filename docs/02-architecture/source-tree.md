# 源码与目录结构

```text
aiden-hardware-demo/
├── CMakeLists.txt                 # C/C++ 主构建配置
├── Makefile                       # 本机快捷构建/测试入口
├── build.sh / _build.sh           # Docker 交叉编译应用
├── build_image.sh / _build_image.sh # 完整固件构建入口
├── cmake/                         # Toolchain 文件
├── docs/                          # 结构化文档
├── edid/                          # HDMI EDID hex 文件
├── overlay/                       # 固件注入文件：etc/oem/userdata
├── pico-sdk/                      # Luckfox SDK 子模块
├── scripts/                       # 设备辅助脚本
├── src/                           # C/C++ SDK、服务、工具
├── src/agent/                     # Go Agent
├── tests/                         # host-native 单元测试
├── third_party/                   # doctest、stb、opencv-mobile 等
└── upgrade_tool/                  # 刷机工具
```

## `src/` 重点文件

| 文件 / 模块 | 说明 |
| --- | --- |
| `aiden_sdk.*` | C++ SDK 入口，提供 Wakeup、AudioCapture、AudioPlayer、CameraCapture |
| `frame_*` | Frame Service、帧缓存、摄像头源、帧协议与客户端 |
| `audio_*` | Audio Service、录音/播放 session、协议与客户端 |
| `uds_*` | 通用 Unix domain socket 传输层 |
| `service_status.*` | 服务通用状态枚举 |
| `image_process.*` | 图像裁剪、去黑边、缩放、旋转 |
| `hid_server.*`、`example_usb_hid.cpp` | USB HID gadget 和 HTTP demo server |
| `config_web.*` | 设备配置 Web 服务，维护 Agent TOML 和 Wi-Fi 配置 |
| `agent_toml.*` | C++ 侧 Agent TOML 解析/写入 |

## `src/agent/` 重点目录

| 路径 | 说明 |
| --- | --- |
| `cmd/daemon` | 长运行 Agent daemon，提供 Web UI 或设备语音模式 |
| `cmd/demo` | 本地 CLI demo runner |
| `internal/agent/runtime.go` | Agent runtime、模型调用和工具循环 |
| `internal/agent/server.go` | HTTP server / Web UI / 工具 API |
| `internal/agent/tools*.go` | HID、截图、音频、shell 等内置工具 |
| `internal/agent/stt.go` / `internal/agent/tts/` / `vad.go` | STT、可插拔 TTS provider、RKNN/CPU VAD 编排 |
| `internal/agent/skills*.go` | Skill 加载、激活与工具限制 |
| `config/agent.toml` | Agent 配置示例 |

## `overlay/` 结构

```text
overlay/
├── etc/
│   ├── aiden_audio_service.conf
│   ├── aiden_frame_service.conf
│   ├── dnsmasq.d/usb0.conf
│   └── init.d/S*
├── oem/usr/bin/
├── oem/usr/model/
└── userdata/
    ├── agent/agent.toml
    └── wpa_supplicant.conf
```

`_build_image.sh` 会把应用二进制复制到 `overlay/oem/usr/bin/`，把 VAD 模型随 `overlay/oem/usr/model/` 注入 OEM 分区，并将 overlay 同步/注入到 `pico-sdk` 输出镜像。
