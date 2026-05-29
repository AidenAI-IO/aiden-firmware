# 系统架构概览

Aiden Hardware Demo 将 HDMI 画面采集、音频录放、USB HID 控制和 LLM Agent 组合成一个可在设备端运行的自动化演示系统。

## 总体架构

```text
                 ┌──────────────────────────┐
                 │        Go Agent          │
                 │ Web UI / HTTP Tool API   │
                 │ LLM / Skills / Memory    │
                 └───────┬─────────┬────────┘
                         │         │
          screenshot UDS │         │ audio UDS / volume
                         │         │
             ┌───────────▼───┐ ┌───▼────────────┐
             │ frame_service │ │ audio_service   │
             │ C++ daemon    │ │ C++ daemon      │
             └───────┬───────┘ └───────┬────────┘
                     │                 │
           /dev/video0 + subdev        │ ALSA / RK MPI
                     │                 │
             ┌───────▼──────┐          │
             │ TC358743 HDMI│          │
             │ capture path │          │
             └──────────────┘          │

                 ┌──────────────────────────┐
                 │ USB HID Gadget           │
                 │ /dev/hidg0 / /dev/hidg1  │
                 └───────────▲──────────────┘
                             │
                         Agent tools
```

## 模块分层

| 层级 | 模块 | 说明 |
| --- | --- | --- |
| 硬件抽象 | `src/aiden_sdk.*` | 封装 GPIO、音频、视频、HID 相关底层能力 |
| 公共传输 | `src/uds_*` | Unix domain socket 一次请求/响应传输层 |
| C++ 服务 | `frame_service`、`audio_service` | 将硬件资源集中管理并暴露给其他进程 |
| 工具程序 | `*_cli`、`example_*`、`image_process` | 调试、验证和单项能力演示 |
| Go Agent | `src/agent` | LLM runtime、工具调用、Web UI、HTTP Tool API、语音链路 |
| 固件集成 | `overlay/`、`pico-sdk/` | 启动脚本、配置文件、userdata/oem 注入 |

## 关键设计原则

1. **硬件资源单所有者**：例如 `/dev/video0` 由 `frame_service` 独占，其他消费者通过 UDS 读取帧，避免多进程同时打开视频设备造成冲突。
2. **跨语言协议**：UDS envelope 使用 JSON header + binary payload，Go / C++ / 其他语言都可以实现客户端。
3. **Agent 与硬件解耦**：Agent 不直接依赖 C++ ABI，通过 socket 和 HID 设备完成观察/操作。
4. **服务守护**：overlay 中的 init 脚本通过简单 watchdog 在服务退出后自动重启。

## 主要数据流

### 截图 / 视觉观察

```text
TC358743 → /dev/video0 → frame_service ring buffer → Go screenshot tool → LLM image input
```

### 设备控制

```text
LLM tool call → Go HID tool → /dev/hidg0 or /dev/hidg1 → target device
```

### 语音交互

```text
audio_service recording → RKNN Silero VAD → STT or audio attachment → LLM → TTS → audio_service playback
```
