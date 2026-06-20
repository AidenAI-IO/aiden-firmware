# 新手快速上手

本页是面向第一次接触 Aiden 硬件的人的主路径，按照「连线 → 刷机 → 联网 → 配置 → 跑起来 → 开发 → 升级 → 排障」的顺序，把你需要做的事串成一条线。每一步只讲关键动作，细节通过链接进入对应的专题文档。

> 语音交互是 Aiden 最核心的使用场景。`text` 模式主要用于开发和测试，方便用网页验证链路。

## 总览

| 步骤 | 目标 | 详细文档 |
| --- | --- | --- |
| 1 | 按硬件方案完成连线 | [硬件与连线](hardware.md) |
| 2 | 给 Pico Zero 刷入 `update.img` 固件 | [固件构建与刷机](firmware.md) |
| 3 | 进入配置页面并连上 Wi-Fi | [Config Web](../03-services/config-web.md) |
| 4 | 配置 model / tts / stt 等 key | [Agent 配置参考](../04-agent/configuration.md) |
| 5 | 让板子跑起来并验证 | [测试与验证](testing.md) |
| 6 | 了解系统架构和各模块 | [系统架构概览](../02-architecture/overview.md) |
| 7 | 固件构建与 OTA 升级 | [固件构建](firmware.md) / [OTA](../09-ota/README.md) |
| 8 | 遇到问题时排查 | [故障排查](../07-operations/troubleshooting.md) |

## 1. 硬件与连线

按照 [硬件与连线](hardware.md) 完成连线，核心连接关系：

- 外部 HDMI 画面源经 TC358743XBG 桥接芯片，从 CSI 进入 Pico Zero 的 `/dev/video0`；
- Pico Zero 通过 USB-C gadget 连接目标设备（iOS / PC），对外模拟 HID 键鼠/触控；
- 音频走板载 codec / ALSA，网络走 Wi-Fi 或 USB gadget network。

如果要用 USB HID 控制 iOS 设备，记得先在目标设备开启 `Settings > Accessibility > Touch > AssistiveTouch`，并建议启用 **Show Onscreen Keyboard**。

## 2. 刷入固件

把 Pico Zero 的 USB-C 口接到电脑，刷入我们编译好的 `update.img`。刷机方式有多种，主要参考 Pico Zero 官方文档：

- [Luckfox Pico Zero 刷机指南](https://wiki.luckfox.com/zh/Luckfox-Pico-Zero/Flash-image/)

核心流程是先让板子进入烧录（Maskrom / Loader）模式，再用刷机工具写入 `update.img`：

```bash
./upgrade_tool/upgrade_tool uf ./update.img
```

进入烧录模式最常见的方式是按住板子的 BOOT 按键再插入 USB-C。**如果 BOOT 按键触发不好用**，可以先用 adb 或 TTL 串口登录到板子上，执行：

```bash
reboot loader
```

即可进入烧录模式。预构建固件、本地构建路径、`upgrade_tool` 用法和分区布局见 [固件构建与刷机](firmware.md)。

## 3. 连上 Wi-Fi

刷机完成、设备启动后，用 USB-C 连接到电脑，浏览器访问设备的配置页面：

```text
http://192.168.42.1
```

`192.168.42.1` 是 USB 网络的网关地址，配置页面由设备上的 `config_web` 服务（默认 80 端口）提供。

进入页面后，首先配置 Wi-Fi：

- **只支持 2.4GHz 网络**，5GHz 不可用；
- 填入 SSID 和密码，保存后写入 `/userdata/wpa_supplicant.conf`。

配置页面还能维护 Agent 配置和系统环境变量，详见 [Config Web：设备配置网页](../03-services/config-web.md)。

## 4. 配置各种 Key

同样在配置页面里填写各服务的 key。其中：

- **`[model]` 是必须配置的**，且必须是支持多模态的 LLM（Agent 需要把截图作为图像输入发给模型）；
- **`[tts]`** 用于支持语音播报（把模型回复转成语音）；
- **`[stt]`** 用于支持语音输入转文字。

### 选择 Agent 模式

由 `agent.toml` 的 `input_mode` 决定，三种模式：

| 模式 | 行为 | 用途 |
| --- | --- | --- |
| `audio` | 设备录音 → 直接把音频附件发给 LLM → TTS 播报 | 语音交互（音频直送多模态模型） |
| `stt` | 设备录音 → VAD → STT 转文字 → LLM → TTS 播报 | 语音交互（先转文字再发给 LLM） |
| `text` | 启动 HTTP server + Web UI | 主要用于测试，访问 8080 端口网页做文字交互 |

`audio` 和 `stt` 是语音交互的两种方式：前者直接把音频发给 LLM，后者先转文字再发给 LLM。`text` 模式主要用于测试，可以访问设备 `8080` 端口的网页进行文字交互。

> 注意区分两个网页：`http://192.168.42.1`（80 端口）是设备配置页面；`http://<device-ip>:8080` 是 `text` 模式下的 Agent Web UI。

各字段含义、最小可用配置示例和 TTS/STT provider 取值见 [Agent 配置参考](../04-agent/configuration.md)。

## 5. 让板子跑起来

完成上面 4 步后，板子就可以跑起来了。Agent 由 `S53agent` 守护进程随固件启动，配置改动后通常需要重启 Agent：

```bash
/etc/init.d/S53agent restart
```

验证链路是否正常（Frame / Audio / Agent 服务、截图、语音）的命令见 [测试与验证](testing.md)。各服务日志位置和常用服务命令见 [部署到设备](deployment.md)。

接下来是开发部分。

## 6. 系统架构与模块

开始开发前，建议先建立整体认识：

- [系统架构概览](../02-architecture/overview.md)：HDMI 采集、音频录放、USB HID、LLM Agent 如何组合，以及主要数据流。

然后再深入各模块：

- [Frame Service](../03-services/frame-service.md)：HDMI 帧捕获服务
- [Audio Service](../03-services/audio-service.md)：音频录放服务
- [USB HID 与设备控制](../03-services/usb-hid.md)
- [Config Web](../03-services/config-web.md)：设备配置网页
- [Go Agent 概览](../04-agent/overview.md)：LLM runtime、工具调用、语音链路
- [工具 HTTP API](../04-agent/tools-http-api.md)

源码与目录结构见 [源码与目录结构](../02-architecture/source-tree.md)，启动服务与运行时布局见 [启动服务与运行时布局](../02-architecture/boot-services.md)。

## 7. 固件构建与 OTA 升级

开发完成后，构建固件并升级设备：

- 本机 / 交叉编译开发环境见 [构建与开发环境](build.md)；
- 完整固件构建（`./build_image.sh`）和刷机见 [固件构建与刷机](firmware.md)；
- 在线升级见 [OTA 概览](../09-ota/README.md)。

### 非 main 分支固件的 OTA

CI 会按分支区分发布渠道：`main` 是 `stable` 正式发布，**其他分支发布为 prerelease**。默认的 `ota update` 走 `releases/latest`，只会拿到最新的正式发布，因此不会误装到开发分支固件。

要把某个开发分支的固件刷到设备测试，必须通过 `--manifest-url` 显式指定该 dev release 的 manifest，绕过 `releases/latest` 查找：

```bash
TAG="20260604-120000-abc1234"
REPO="AidenAI-IO/aiden-hardware-demo"

ota update \
  --manifest-url "https://github.com/$REPO/releases/download/$TAG/manifest.json" \
  --public-key /oem/etc/ota_pubkey.pem
```

官方签名公钥已预置在设备 `/oem/etc/ota_pubkey.pem`，官方仓库构建无需额外指定公钥。建议先加 `--dry-run` 只下载校验、不切换 slot。详见 [OTA Release Channels](../09-ota/ota-release-channels.md)。

## 8. 故障排查

遇到问题时，先看 [故障排查](../07-operations/troubleshooting.md)，里面覆盖了常见的连接、服务、视频/音频、Agent 和 OTA 问题。日志位置见 [部署到设备](deployment.md)。
