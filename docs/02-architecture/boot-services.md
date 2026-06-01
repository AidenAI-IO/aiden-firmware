# 启动服务与运行时布局

固件集成通过 `overlay/etc/init.d/` 中的脚本完成。多数长运行服务都带有轻量 watchdog：脚本本身启动一个循环，子进程退出后等待 2 秒重启。

## 启动脚本

| 脚本 | 作用 |
| --- | --- |
| `S20oemslot` | 根据 `aiden.slot_suffix` 挂载 slot-specific `/oem` |
| `S49usbhid` | 初始化 USB HID gadget |
| `S50usbdevice` | USB device 相关初始化 |
| `S52frame_service` | 启动并守护 HDMI 帧服务 |
| `S53audio_service` | 启动并守护音频服务 |
| `S53agent` | 启动并守护 Go Agent |
| `S54ota` | 启动并守护 OTA daemon |
| `S55aiden_usb_dhcp` | USB 网络 DHCP / dnsmasq 相关服务 |
| `S56config_web` | 启动配置网页 |
| `S99usb0config` | USB 网络接口后置配置 |

## Frame Service

配置文件：`/etc/aiden_frame_service.conf`

默认值：

```sh
FRAME_SERVICE_BIN=/oem/usr/bin/frame_service
SOCKET_PATH=/run/frame_service/frame_service.sock
LOG_PATH=/var/log/frame_service/frame_service.log
PID_FILE=/run/frame_service/frame_service.pid
WATCHDOG_PID_FILE=/run/frame_service/frame_service_watchdog.pid
```

常用命令：

```bash
/etc/init.d/S52frame_service start
/etc/init.d/S52frame_service stop
/etc/init.d/S52frame_service restart
/etc/init.d/S52frame_service status
```

## Audio Service

配置文件：`/etc/aiden_audio_service.conf`

默认值：

```sh
AUDIO_SERVICE_BIN=/oem/usr/bin/audio_service
SOCKET_PATH=/run/audio_service/audio_service.sock
LOG_PATH=/var/log/audio_service/audio_service.log
PID_FILE=/run/audio_service/audio_service.pid
WATCHDOG_PID_FILE=/run/audio_service/audio_service_watchdog.pid
```

## Agent

默认启动命令：

```bash
/oem/usr/bin/aiden-env-run /oem/usr/bin/agent -config /userdata/agent -addr :8080
```

运行时目录：

```text
/userdata/agent/
├── agent.toml
├── skills/       # 可选
├── log/          # Agent runtime 日志目录，由程序使用
└── memory/       # 对话记忆持久化目录
```

外层 init 脚本日志：`/var/log/agent/agent.log`。

## OTA

默认启动命令：

```bash
/oem/usr/bin/aiden-env-run /oem/usr/bin/ota daemon
```

运行时目录：

```text
/userdata/ota/
├── config.json          # 首刷镜像内置的 repo/channel/factory baseline
├── state.json           # OTA 状态机和 per-slot partition hashes
├── downloads/           # 下载缓存
├── pending_boot.json    # 目标 slot health pending 状态
└── health.ok            # 应用 ready 后写入的健康确认 marker
```

更多说明见 [OTA 概览](../09-ota/README.md)。

## Config Web

默认启动命令：

```bash
/oem/usr/bin/aiden-env-run /oem/usr/bin/config_web --config=/userdata/agent/agent.toml --wifi-config=/userdata/wpa_supplicant.conf --system-env=/userdata/system/env
```

用途：通过网页维护 Agent 配置和 Wi-Fi 配置。默认 bind / port 见 [Config Web](../03-services/config-web.md)。

## System Environment

`/userdata/system/env` is the device-wide environment file. `aiden-env-run` loads it before starting services, and SSH login shells load the same file through `/etc/profile.d/aiden-env.sh`. Proxy variables, API keys, and other shell-style environment values can live there instead of in `agent.toml`.

## 开发调试建议

- 修改二进制后可直接替换 `/oem/usr/bin/<name>` 并重启对应服务。
- 如果服务反复重启，先查看 `/var/log/<service>/<service>.log`。
- 若需要独占摄像头做 one-shot 测试，先停止 `S52frame_service`。
