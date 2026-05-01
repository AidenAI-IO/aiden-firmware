# Aiden DEMO

## 相关硬件

[Luckfox Pico Zero](https://wiki.luckfox.com/Luckfox-Pico-Zero)

[TC358743XBG](https://toshiba.semicon-storage.com/eu/semiconductor/product/interface-bridge-ics-for-mobile-peripheral-devices/hdmir-interface-bridge-ics/detail.TC358743XBG.html)

[CH375B](https://easyelecmodule.com/ch375b-u-disk-read-write-module-development-guide/)

## Build

Standard out-of-source CMake build:

```bash
cmake -S . -B build
cmake --build build
```

For the Luckfox cross-compilation environment, use:

```bash
./build.sh
```

All build artifacts are placed in `build/`:
- `build/lib/` - Static libraries
- `build/bin/` - Executables
- `build/CMakeFiles/` - CMake metadata and intermediate files

## AI Agent

纯C++实现的AI agent，直接运行在Pico Zero上，通过语音控制设备的键盘和触摸屏。

### 特性

- **GPIO唤醒触发**: 等待GPIO 33的wakeup事件后才开始录音（不是持续监听）
- **实时音频捕获**: 从设备麦克风实时捕获16kHz/16bit/mono音频
- **VAD断句**: 基于能量的语音活动检测，自动检测说话停顿并断句
- **LLM处理**: 通过OpenRouter调用支持音频输入的模型
- **流式TTS**: MiniMax流式语音合成，边收边解码边播放，低延迟
- **工具调用**: 键盘输入和触摸屏控制
- **调试信息**: 详细的HTTP请求/响应日志用于调试

### 设置

1. 确保设备上安装了 `curl`
2. 复制配置文件并填入API key：
   ```bash
   cp agent.conf.example agent.conf
   vi agent.conf
   ```

### 使用

```bash
# GPIO触发模式（生产环境）
sudo ./build/bin/agent_main

# 手动触发模式（调试用）
sudo ./build/bin/agent_main --manual
# 第一次按 Enter: 开始录音
# 第二次按 Enter: 立即停止录音并发送已录音频（绕过VAD等待）

# 指定配置文件
sudo ./build/bin/agent_main --manual /etc/agent.conf
```

### 配置文件 (`agent.conf`)

```ini
[openrouter]
api_key = sk-or-v1-...
llm_model = openai/gpt-4o-audio-preview

[minimax]
api_key = YOUR_MINIMAX_API_KEY
voice_id = male-qn-qingse
speed = 1.0
emotion = happy

[agent]
hid_binary = ./build/bin/example_usb_hid
energy_threshold = 300   # 语音能量阈值
silence_ms = 800         # 静音多久判定语句结束
min_speech_ms = 300      # 最短语句长度
```

### 工作原理

1. **等待唤醒**: Agent启动后等待GPIO 33的wakeup事件（不消耗CPU）
2. **开始录音**: 检测到wakeup后，AudioCapture开始捕获音频流
3. **VAD断句**: 检测到说话停顿（默认800ms静音）后，判定语句结束
4. **发送LLM**: 音频转WAV后base64编码，通过curl发送给OpenRouter
5. **工具调用**: LLM可以调用工具来控制设备：
   - `keyboard_tap`: 按键组合（如ENTER, CTRL+C）
   - `keyboard_text`: 输入文本
   - `touch_click`: 在绝对坐标点击（0-32767范围）
   - `touch_swipe`: 滑动手势
6. **流式TTS播放**: LLM回复发送给MiniMax TTS API
   - 流式接收MP3音频chunks（hex编码）
   - 边接收边喂给ffmpeg解码成PCM
   - 边解码边播放，实现低延迟语音输出
7. **返回等待**: 完成后返回等待下一次wakeup事件

### 调试

Agent会打印详细的调试信息：
- `[wakeup]` - GPIO触发事件
- `[listen]` - 开始录音
- `[utterance]` - 捕获到的语音时长
- `[debug]` - WAV大小等调试信息
- `[http]` - HTTP请求/响应详情
- `[llm]` - LLM请求状态
- `[tools]` - 工具调用和结果
- `[tts]` - TTS合成状态
- `[error]` - 错误信息

需要USB HID设置（参见src/README.md）。

## Scripts

- `start-dev.sh`: 启动 Docker 编译环境
- `build.sh`: 使用交叉编译工具链构建项目
