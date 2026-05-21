# 故障排查

## `example_camera_capture` 报 `Device or resource busy`

原因：`frame_service` 正在独占 `/dev/video0`。

处理：

```bash
/etc/init.d/S52frame_service stop
./build/bin/example_camera_capture
/etc/init.d/S52frame_service start
```

日常截图和帧测试建议使用：

```bash
frame_service_cli screenshot --out /tmp/screenshot.bmp
```

## `screenshot` 工具失败

检查：

```bash
/etc/init.d/S52frame_service status
frame_service_cli --socket /run/frame_service/frame_service.sock health
ls -l /run/frame_service/frame_service.sock
```

常见原因：

- `frame_service` 未运行；
- `agent.toml` 中 `[hid].frame_socket` 路径不一致；
- HDMI 输入未同步；
- TC358743 / `/dev/v4l-subdev2` 状态异常。

## Frame Service 一直重启

查看日志：

```bash
tail -f /var/log/frame_service/frame_service.log
```

检查：

- `/oem/usr/bin/frame_service` 是否存在且可执行；
- `/dev/video0`、`/dev/v4l-subdev2` 是否存在；
- EDID / HDMI 信号是否正常；
- 是否有其他进程占用 `/dev/video0`。

## Agent Web UI 打不开

检查：

```bash
/etc/init.d/S53agent status
tail -f /var/log/agent/agent.log
netstat -lntp | grep 8080
```

确认：

- `input_mode = "text"`；
- `agent.toml` TOML 语法正确；
- 模型配置和 API key 可用；
- 防火墙、USB 网络或 Wi-Fi IP 正确。

## 语音模式没有声音或录不到音

检查：

```bash
/etc/init.d/S53audio_service status
audio_service_cli --socket /run/audio_service/audio_service.sock health
audio_service_cli --socket /run/audio_service/audio_service.sock get-volume
amixer sget 'DAC HPMIX'
amixer sget 'DAC LINEOUT'
```

建议：

- 先运行 `scripts/setup_audio_volume.sh`；
- 确认 `[audio].socket` 路径与服务一致；
- 使用 `record-stream` 和 `play-stream` 分别验证录音/播放；
- TTS 失败时检查 `ffmpeg` 是否存在。

## HID 输入无效

检查：

```bash
ls -l /dev/hidg*
mount | grep configfs
lsmod | grep -E 'dwc2|libcomposite'
```

尝试重新初始化：

```bash
sudo ./build/bin/example_usb_hid cleanup
sudo ./build/bin/example_usb_hid setup composite
```

iOS 目标设备请确认 AssistiveTouch 已开启。

## HTTP Tool API 访问异常

- 对设备私有 IP / USB 网卡地址设置 `NO_PROXY`；
- 先访问 `GET /api/tools` 确认服务可达；
- 工具失败时检查 JSON 响应中的 `is_error` 和 `output`；
- transport failure 与 tool failure 分开判断。

## Docker 构建失败

检查：

```bash
docker buildx version
docker info
```

Apple Silicon 推荐：

```bash
colima start --vm-type vz --vz-rosetta
./build.sh
```

确认没有使用 `--arch x86_64` 启动 Colima VM。
