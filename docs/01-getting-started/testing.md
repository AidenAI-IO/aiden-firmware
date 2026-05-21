# 测试与验证

## Host 单元测试

项目使用 doctest，并提供 host-native 测试构建：

```bash
make test
```

或手动执行：

```bash
cmake -S . -B build-host -DAIDEN_TESTS=ON
cmake --build build-host
build-host/tests/aiden_tests
```

测试覆盖 UDS 消息、Frame Service 协议、Audio Service 协议、Ring Buffer、Wi-Fi / Agent TOML 配置解析、图像处理等模块。

## Frame Service 验证

服务运行后：

```bash
./build/bin/frame_service_cli --socket /run/frame_service/frame_service.sock health
./build/bin/frame_service_cli --socket /run/frame_service/frame_service.sock screenshot --out /tmp/screenshot.bmp
./build/bin/frame_service_cli --socket /run/frame_service/frame_service.sock latest-frame --out /tmp/frame.raw
./build/bin/frame_service_cli --socket /run/frame_service/frame_service.sock list-frames
```

开发机临时运行服务默认 socket 是 `/tmp/frame_service.sock`：

```bash
./build/bin/frame_service --socket /tmp/frame_service.sock
./build/bin/frame_service_cli --socket /tmp/frame_service.sock health
```

## Audio Service 验证

```bash
./build/bin/audio_service_cli --socket /run/audio_service/audio_service.sock health
./build/bin/audio_service_cli --socket /run/audio_service/audio_service.sock get-volume
./build/bin/audio_service_cli --socket /run/audio_service/audio_service.sock set-volume --volume 80
```

录音到 PCM：

```bash
./build/bin/audio_service_cli --socket /run/audio_service/audio_service.sock record-stream --seconds 3 > /tmp/record.pcm
```

播放 PCM：

```bash
cat /tmp/record.pcm | ./build/bin/audio_service_cli --socket /run/audio_service/audio_service.sock play-stream --rate 16000 --ch 1 --bits 16
```

## USB HID 验证

```bash
sudo ./build/bin/example_usb_hid setup composite
sudo ./build/bin/example_usb_hid keyboard tap ENTER
sudo ./build/bin/example_usb_hid keyboard text "hello from pico"
sudo ./build/bin/example_usb_hid touch click 16000 16000
```

## Agent 验证

在设备上检查服务：

```bash
/etc/init.d/S53agent status
```

Web UI 模式下打开：

```text
http://<device-ip>:8080
```

工具 API：

```bash
curl http://<device-ip>:8080/api/tools
curl -X POST http://<device-ip>:8080/api/tools/shell \
  -H 'Content-Type: application/json' \
  -d '{"input":{"command":"pwd"}}'
```

## 音频模型回环脚本

`scripts/test_audio_roundtrip.sh` 用于通过 OpenRouter audio model 生成音频，再把音频作为输入发回识别：

```bash
export OPENROUTER_KEY=sk-or-...
./scripts/test_audio_roundtrip.sh
```

可选设置代理：

```bash
export HTTP_PROXY=http://127.0.0.1:7890
export HTTPS_PROXY=http://127.0.0.1:7890
```
