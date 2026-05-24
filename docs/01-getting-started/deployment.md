# 部署到设备

## 通过完整固件部署

推荐生产式部署方式是构建或下载包含 overlay 的完整固件，并刷入设备。固件会将以下内容放到目标文件系统：

```text
/oem/usr/bin/                  # 应用二进制
/etc/init.d/S49usbhid          # USB HID gadget 初始化
/etc/init.d/S52frame_service   # Frame Service watchdog
/etc/init.d/S53audio_service   # Audio Service watchdog
/etc/init.d/S53agent           # Go Agent watchdog
/etc/init.d/S56config_web      # 配置网页
/userdata/agent/agent.toml     # Agent 默认配置
/userdata/wpa_supplicant.conf  # Wi-Fi 默认配置
```

## 手动复制二进制

开发时可先交叉编译，再通过 `scp` 将 `build/bin/` 中的目标程序复制到设备：

```bash
./build.sh
scp build/bin/frame_service root@<device-ip>:/oem/usr/bin/
scp build/bin/audio_service root@<device-ip>:/oem/usr/bin/
scp build/bin/agent root@<device-ip>:/oem/usr/bin/
```

也可以复制到 `/root` 或 `/userdata` 做临时测试，但启动脚本默认从 `/oem/usr/bin/` 查找。

## 服务启动顺序

随固件启动时，主要服务关系如下：

1. `S49usbhid` / `S50usbdevice` 配置 USB gadget；
2. `S52frame_service` 独占 `/dev/video0` 并提供截图/帧服务；
3. `S53audio_service` 提供音频录放服务；
4. `S53agent` 启动 Go Agent；
5. `S56config_web` 提供配置页面；
6. `S55aiden_usb_dhcp` / `S99usb0config` 配置 USB 网络相关能力。

## 常用服务命令

```bash
/etc/init.d/S52frame_service status
/etc/init.d/S52frame_service restart
/etc/init.d/S53audio_service status
/etc/init.d/S53audio_service restart
/etc/init.d/S53agent status
/etc/init.d/S53agent restart
/etc/init.d/S56config_web restart
```

## 关键配置文件

| 文件 | 说明 |
| --- | --- |
| `/etc/aiden_frame_service.conf` | frame service 二进制、socket、日志、pid 文件路径 |
| `/etc/aiden_audio_service.conf` | audio service 二进制、socket、日志、pid 文件路径 |
| `/userdata/agent/agent.toml` | Go Agent 运行配置 |
| `/userdata/wpa_supplicant.conf` | Wi-Fi 配置 |

## 日志位置

| 服务 | 日志 |
| --- | --- |
| Frame Service | `/var/log/frame_service/frame_service.log` |
| Audio Service | `/var/log/audio_service/audio_service.log` |
| Agent | `/var/log/agent/agent.log` |

## 注意事项

- `frame_service` 运行期间会独占 `/dev/video0`，直接运行 `example_camera_capture` 会出现 `Device or resource busy`；需要先停止服务。
- `agent` 的 `screenshot` 工具依赖 `frame_service`；语音和音量工具依赖 `audio_service`。
- 修改 `/userdata/agent/agent.toml` 后需要重启 Agent。
