# Config Web：设备配置网页

`config_web` 是 C++ 实现的轻量 Web 服务，用于维护设备上的 Agent 配置和 Wi-Fi 配置。

## 默认部署

启动脚本：

```text
/etc/init.d/S56config_web
```

默认命令：

```bash
/oem/usr/bin/config_web --config=/userdata/agent/agent.toml --wifi-config=/userdata/wpa_supplicant.conf
```

常用命令：

```bash
/etc/init.d/S56config_web start
/etc/init.d/S56config_web stop
/etc/init.d/S56config_web restart
```

## 参数

源码中的 usage：

```text
config_web [--bind=IP] [--port=PORT] [--config=PATH] [--wifi-config=PATH]
```

| 参数 | 说明 |
| --- | --- |
| `--bind=IP` | 监听地址 |
| `--port=PORT` | 监听端口 |
| `--config=PATH` | Agent TOML 配置路径 |
| `--wifi-config=PATH` | `wpa_supplicant.conf` 路径 |

## 可配置内容

页面字段覆盖以下配置段：

- `agent`：`input_mode`、`trigger_mode`、VAD 参数、`max_iterations`、`instruction`、`additional_prompt`
- `model`：provider、model、api_key、base_url、temperature、max_tokens
- `stt`：provider、api_key、model、base_url、Tencent ASR 字段
- `tts`：provider、api_key、model、voice_id、emotion、speed
- `audio`：socket、sample_rate、channels、bit_width
- `hid`：keyboard_device、mouse_device、frame_socket
- `proxy`：http_proxy、https_proxy、all_proxy、no_proxy（Agent 外部请求代理）
- Wi-Fi：SSID / PSK 等（写入 `/userdata/wpa_supplicant.conf`）

## 使用建议

1. 通过 USB 网络或 Wi-Fi 访问设备 IP；
2. 在网页中修改 Agent 配置；
3. 保存后重启 Agent：

```bash
/etc/init.d/S53agent restart
```

Config Web 本身只负责配置文件写入，不替代对应服务的重启逻辑。
