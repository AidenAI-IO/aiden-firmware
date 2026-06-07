# Config Web：设备配置网页

`config_web` is a lightweight C++ web service for maintaining the device Agent configuration, system environment variables, and Wi-Fi configuration.

## 默认部署

启动脚本：

```text
/etc/init.d/S56config_web
```

默认命令：

```bash
/oem/usr/bin/aiden-env-run /oem/usr/bin/config_web --bind=0.0.0.0 --config=/userdata/agent/agent.toml --wifi-config=/userdata/wpa_supplicant.conf --system-env=/userdata/system/env
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
config_web [--bind=IP] [--port=PORT] [--config=PATH] [--wifi-config=PATH] [--system-env=PATH]
```

| 参数 | 说明 |
| --- | --- |
| `--bind=IP` | Bind address, default `0.0.0.0` |
| `--port=PORT` | 监听端口 |
| `--config=PATH` | Agent TOML 配置路径 |
| `--wifi-config=PATH` | `wpa_supplicant.conf` 路径 |
| `--system-env=PATH` | System environment file path |

## 可配置内容

页面字段覆盖以下配置段：

- `agent`：`input_mode`、`trigger_mode`、VAD 参数、`max_iterations`、`instruction`、`additional_prompt`
- `model`：provider、model、api_key、base_url、temperature、max_tokens
- `stt`：provider、api_key、model、base_url、Tencent ASR 字段
- `tts`：provider、api_key、model、voice_id、emotion、speed
- `audio`：socket、sample_rate、channels、bit_width
- `hid`：keyboard_device、mouse_device、frame_socket
- `env`: shell-style environment text written to `/userdata/system/env`, including optional proxy variables such as `http_proxy`, `HTTPS_PROXY`, and `NO_PROXY`
- Wi-Fi：SSID / PSK 等（写入 `/userdata/wpa_supplicant.conf`）

## Runtime apply behavior

Config Web writes Agent, Wi-Fi, and system environment files. Saving Agent
config still schedules an Agent restart. OTA is restarted only when the
effective `/userdata/system/env` file changes, because OTA reads proxy settings
from that file at process startup:

```bash
/etc/init.d/S53agent restart
/etc/init.d/S54ota restart  # only when /userdata/system/env changes
```
