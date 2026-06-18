# 路径、产物与配置速查

## 构建产物

| 路径 | 说明 |
| --- | --- |
| `build/lib/libaiden.a` | C++ SDK 静态库 |
| `build/lib/libaiden_image.a` | 图像处理静态库 |
| `build/lib/libcjson.a` | cJSON 静态库 |
| `build/bin/` | 应用二进制目录 |
| `build-host/tests/aiden_tests` | host-native 单元测试二进制 |
| `pico-sdk/output/image/` | 完整固件构建输出目录 |

## 重要源码路径

| 路径 | 说明 |
| --- | --- |
| `src/aiden_sdk.h` | C++ SDK API |
| `src/frame_service_main.cpp` | Frame Service 入口 |
| `src/audio_service_main.cpp` | Audio Service 入口 |
| `src/config_web.cpp` | Config Web 入口 |
| `src/agent/cmd/daemon` | Go Agent daemon |
| `src/agent/cmd/ota` | OTA CLI |
| `src/agent/cmd/abctl` | A/B metadata 诊断工具 |
| `src/agent/internal/agent` | Agent runtime 和工具实现 |
| `src/agent/internal/ota` | OTA manifest、下载、slot、health 和状态机 |
| `tests/` | 单元测试 |

## 设备默认路径

| 路径 | 说明 |
| --- | --- |
| `/oem/usr/bin/` | 应用二进制安装目录 |
| `/oem/usr/model/` | 随 OEM/OTA 更新的 VAD 模型和 weights |
| `/userdata/agent/agent.toml` | Agent 主配置 |
| `/userdata/agent/skills/` | Agent skills 目录 |
| `/userdata/agent/memory/` | Agent 记忆持久化目录 |
| `/userdata/system/env` | Device-wide environment file loaded by service launchers and SSH login shells |
| `/userdata/ota/` | OTA 配置、状态、下载缓存和 health marker |
| `/oem/etc/ota_pubkey.pem` | OTA manifest Ed25519 public key |
| `/userdata/wpa_supplicant.conf` | Wi-Fi 配置 |
| `/run/frame_service/frame_service.sock` | Frame Service socket |
| `/run/audio_service/audio_service.sock` | Audio Service socket |
| `/var/log/frame_service/frame_service.log` | Frame Service 日志 |
| `/var/log/audio_service/audio_service.log` | Audio Service 日志 |
| `/var/log/agent/agent.log` | Agent init 脚本日志 |

## 配置文件

| 文件 | 说明 |
| --- | --- |
| `overlay/etc/aiden_frame_service.conf` | Frame Service init 配置模板 |
| `overlay/etc/aiden_audio_service.conf` | Audio Service init 配置模板 |
| `overlay/etc/init.d/S20oemslot` | Slot-aware `/oem` 挂载脚本 |
| `overlay/etc/init.d/S49ntp` | ntpd daemon 启动 + `step` 一次性同步子命令 |
| `overlay/etc/init.d/S50ntp_watchdog` | NTP 同步周期检查，未同步时触发 `S49ntp step` |
| `overlay/etc/init.d/S54ota` | Boot-time OTA health one-shot |
| `overlay/etc/init.d/S99rtcinit` | RTC invalid-date calibration script replacing the SDK default |
| `overlay/etc/profile.d/aiden-env.sh` | SSH/login shell environment loader snippet |
| `overlay/oem/usr/bin/aiden-env-run` | Service environment launcher |
| `overlay/oem/usr/model/` | VAD 模型和 weights，随 OEM 分区更新 |
| `overlay/userdata/agent/agent.toml` | Agent 默认配置模板 |
| `overlay/userdata/system/env` | Default system environment template |
| `overlay/userdata/wpa_supplicant.conf` | Wi-Fi 默认配置模板 |

## 常用命令

```bash
# 构建
./build.sh
make test

# 完整固件
./build_image.sh
./upgrade_tool/upgrade_tool uf ./update.img

# 服务状态
/etc/init.d/S52frame_service status
/etc/init.d/S53audio_service status
/etc/init.d/S53agent status

# Frame 调试
frame_service_cli --socket /run/frame_service/frame_service.sock health
frame_service_cli --socket /run/frame_service/frame_service.sock screenshot --out /tmp/screenshot.bmp

# Audio 调试
audio_service_cli --socket /run/audio_service/audio_service.sock health
audio_service_cli --socket /run/audio_service/audio_service.sock get-volume

# Agent API
curl http://<device-ip>:8080/api/tools

# OTA
/oem/usr/bin/ota status
/oem/usr/bin/ota update
/oem/usr/bin/abctl read /dev/block/by-name/misc
```

## EDID 文件

`edid/` 目录包含常用 EDID：

- `1080p30.hex`
- `720p30.hex`
- `720p60.hex`
- `hdmi_1080p30_cta.hex`
- `hdmi_720p60_cta.hex`
- `hdmi_720p60_1080p30_cta.hex`
- `phone_vrt_552x1200p30.hex`
- `phone_vrt_640x1200p30.hex`
