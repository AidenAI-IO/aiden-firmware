# Audio Service：音频录放服务

`audio_service` 是 C++ 长运行服务，提供录音、播放和音量控制能力。Go Agent 的设备语音链路和 `audio_volume` 工具通过 Unix domain socket 调用该服务。

## 默认参数

| 参数 | 默认值 |
| --- | --- |
| Socket | `/run/audio_service/audio_service.sock` |
| 日志 | `/var/log/audio_service/audio_service.log` |
| 二进制 | `/oem/usr/bin/audio_service` |

开发直接运行时也默认使用 `/run/audio_service/audio_service.sock`，可通过参数或环境变量改写：

```bash
AUDIO_SERVICE_SOCKET=/tmp/audio_service.sock ./build/bin/audio_service
./build/bin/audio_service --socket /tmp/audio_service.sock
```

## 启动

```bash
/etc/init.d/S53audio_service start
/etc/init.d/S53audio_service status
/etc/init.d/S53audio_service restart
```

## CLI

```text
audio_service_cli [--socket PATH] <command>

Commands:
  health
  record-stream [--seconds N]
  play-stream [--rate N] [--ch N] [--bits N]
  get-volume
  set-volume --volume N
```

示例：

```bash
./build/bin/audio_service_cli --socket /run/audio_service/audio_service.sock health
./build/bin/audio_service_cli --socket /run/audio_service/audio_service.sock get-volume
./build/bin/audio_service_cli --socket /run/audio_service/audio_service.sock set-volume --volume 80
```

录音：

```bash
./build/bin/audio_service_cli --socket /run/audio_service/audio_service.sock record-stream --seconds 3 > /tmp/record.pcm
```

播放：

```bash
cat /tmp/record.pcm | ./build/bin/audio_service_cli --socket /run/audio_service/audio_service.sock play-stream --rate 16000 --ch 1 --bits 16
```

## Agent 配置

```toml
[audio]
socket = "/run/audio_service/audio_service.sock"
sample_rate = 16000
channels = 1
bit_width = 16
```

语音模式下，Agent 会通过 audio service：

1. 启动录音 session；
2. 读取 PCM chunk；
3. RKNN Silero VAD 判断语音边界；
4. 需要播放时启动 playback session 并写入 PCM。

## 音量

`audio_service` 暴露逻辑音量 `0..100`。底层设备最大音量初始化可使用：

```bash
scripts/setup_audio_volume.sh
```

详见 [音量初始化与调节](../07-operations/audio-volume.md)。
