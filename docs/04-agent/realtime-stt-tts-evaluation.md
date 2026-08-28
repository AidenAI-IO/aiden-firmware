# Realtime 与独立 STT/TTS 的职责评估

> 评估基线：`origin/main` commit `e97998e9`，2026-08-28。
> 本文只评估当前职责和建议改造边界，不包含业务代码修改。

## 结论

当 `input_mode = "realtime"` 且 `[voice_model]` 可用时，设备主语音对话不再需要独立 STT/TTS：麦克风 PCM 直接进入 Realtime 会话，服务端返回用户转写、助手字幕和助手 PCM 音频。

但这不等于可以从系统中删除 STT/TTS。当前仍有独立的“音频转文字”和“指定文本播报”入口，它们不属于 Realtime 主对话。最明显的问题是 Voice Notifications：它只挂在传统最终 TTS 出口，Realtime 路径没有调用通知 manager，因此通知会一直 pending，既不会通过 Realtime 播报，也不会被确认 delivered。

建议把选择逻辑从“全局 input mode”改成“按场景选择 capability”：

1. 连续对话优先 Realtime；
2. 指定文本播报优先已配置的独立 TTS，未配置时再尝试活跃 Realtime 会话；
3. 音频文件/录音转写优先已配置的独立 STT，只有 provider 明确支持独立转写时才使用 Realtime fallback；
4. 固定关键错误继续保留本地 WAV/提示音作为最后降级。

## 当前代码事实

### Realtime 主对话已经包含 ASR 和语音输出

- `Config.Validate` 在 `input_mode=realtime` 时只要求 `voice_model.api_key`；只有 `input_mode=stt` 才要求 `stt.provider` 和 `tts.provider`（`internal/agent/config.go`）。
- daemon 在 realtime 模式直接进入 `runRealtimeWakeupModeWithServer`，不会创建 `AudioDialog`（`cmd/daemon/main.go`）。
- Realtime session 配置输出模态为 `audio + text`，直接追加麦克风 PCM，并处理：
  - `conversation.item.input_audio_transcription.completed`；
  - `response.audio_transcript.delta/done`；
  - `response.audio.delta`。
- 仓库内同步的 Qwen 官方事件参考明确说明 Realtime 会话包含输入 ASR、文本/音频响应和 TTS voice 设置（`internal/agent/rtclient/client-events.md`、`server-events.md`、`websocket-api.md`）。

因此，对“GPIO 唤醒后的实时语音对话”而言，独立 STT/TTS 是可选能力，不是必需依赖。

### Voice Notifications 尚未进入 Realtime 路径

Voice Notification manager 的交付协议是：

1. `PrepareSpokenText` 在最终播报前选择 replacement/tail，并创建 delivery token；
2. 播放完成后调用 `ReportSpokenTextDelivery`；
3. 只有完成播放才把通知记为 delivered。

这些调用存在于 `AudioDialog` 和 HTTP server 的传统 TTS 路径中。Realtime 事件循环在 `response.done` 只完成播放、持久化助手文本并处理 task updates，没有调用上述两个方法。

实际结果：

- storage 等 persistent notification 在 realtime 模式会进入 manager；
- 但没有 consumer 取出并播报；
- pending 状态只会等待过期、resolved 或以后切回传统 TTS 路径。

Realtime 连接错误也没有复用 Voice Notifications 的 network/quota/service failure replacement，当前行为是直接结束 session 并记录错误。

## 仍需保留独立 STT/TTS 的场景

| 场景 | 当前依赖 | Realtime 能否直接替代 | 建议 |
| --- | --- | --- | --- |
| GPIO 连续语音对话 | Realtime voice model | 已替代独立 STT/TTS | Realtime |
| Voice Notifications | 最终文本经 TTS | 当前未接通 | TTS 优先，Realtime fallback |
| `run_script` 的 `tts` step | 独立 TTS speaker | 不能自动替代 | 接统一 speech router |
| legacy tool/progress speech | 独立 TTS | realtime foreground已有自己的自然语音；后台 legacy 路径仍可能需要 | 避免双播，按 owner 路由 |
| Web/HTTP 上传音频转写 | 独立 STT | 当前 realtime chat 只接受 text；audio attachment 在 realtime mode 没有可用转换路径 | STT 优先；以后加 provider capability fallback |
| STT/TTS 配置测试 | 对应独立 provider | 不应被 Realtime 替代 | 保留，作为可选 provider 测试 |
| `input_mode=stt` | STT + LLM + TTS | 是另一种运行模式 | 保留兼容和可审计路径 |
| 固定错误/提示音 | 本地 WAV/PCM | 不需要云模型 | 保留最后降级 |

## 为什么不应把 Realtime 简单当作通用 TTS

Realtime 模型能从 text input 产生 audio output，但这是一次生成式模型响应，不等价于“逐字朗读指定文本”：

- 文本可能被改写、补充或省略；
- 会污染或改变实时对话上下文；
- 需要处理 response busy、用户插话、取消和音频播放 ownership；
- 并非所有 Realtime provider 都保证 text injection 或独立 text-to-audio；provider adapter 必须显式声明 capability。

因此，系统通知、脚本旁白这类需要确定文本的场景，应优先用独立 TTS。Realtime 只适合作为允许自然改写的 fallback，或在 provider 提供“exact speech / response instructions”语义并经过验证后使用。

## 为什么不应把 Realtime 简单当作通用 STT

活跃 Realtime 会话确实返回用户转写，但当前实现是连续麦克风会话，不是通用 WAV transcription API。把上传文件临时塞入对话会带来：

- provider 对 manual commit、audio item、transcription-only 支持不一致；
- 可能触发助手回答，而不仅是转写；
- 会改变实时会话上下文；
- 需要处理采样率、文件解码、会话 busy 和无活跃 session。

所以音频附件和配置测试仍应优先使用独立 STT；只有 provider capability 明确支持独立 transcription 时再降级到 Realtime。

## 推荐的路由策略

### Speech output

```text
需要播报确定文本
  -> configured and healthy TTS?       -> TTS
  -> active Realtime text-to-audio?    -> Realtime fallback
  -> known fixed local asset?          -> local WAV / prompt PCM
  -> keep pending / return unavailable
```

建议引入统一 `SpeechRouter` 或等价窄接口，调用方只提交：

- text；
- purpose（reply / notification / script / progress / failure）；
- exactness（必须逐字 / 可自然表达）；
- interruption policy；
- delivery callback/token。

Voice Notifications 不应直接依赖 TTS manager，而应依赖这个统一出口。

### Transcription

```text
需要把独立音频转成文字
  -> configured and healthy STT?       -> STT
  -> Realtime provider supports one-shot/manual transcription? -> Realtime
  -> target LLM supports audio attachment? -> audio attachment
  -> return capability unavailable
```

## Voice Notifications 的最小改造建议

第一阶段不要改通知 manager 的去重/lease/delivery-token 语义，只增加 Realtime consumer：

1. 在一轮 Realtime 正常响应结束且设备仍 idle 时，调用 `PrepareSpokenText` 获取 pending tail；
2. 不要把正常回复再次播报，只取通知 tail 本身。为此最好给 manager 增加“取下一条 notification speech”的明确 API，而不是从拼接后的字符串反向截取；
3. 独立 TTS 可用时，先释放/协调 Realtime playback session，再用 TTS 播报；
4. TTS 不可用时，如果当前 provider 支持 text input/audio output，则注入一条内部 notification request；
5. 只有实际播放完成才 `ReportSpokenTextDelivery(token, nil)`；取消、barge-in、provider error 必须报告失败，使通知保持 pending；
6. 内部通知请求不得出现在用户可见聊天历史，且必须避免再次触发工具调用；
7. 如果无法保证 Realtime 逐字播报，通知文案语义应允许自然表达，并用 response transcript 做最低限度确认。

之后再考虑把普通 final reply 的 tail 在生成前注入同一 Realtime response。该方案声音更一致，但会改变模型上下文、用户可见 transcript 和 delivery 确认逻辑，风险高于“response.done 后单独播报”。

## 配置建议

不建议用 `input_mode` 同时表达“对话模式”和“辅助语音 provider 是否启用”。保持三者独立：

- `input_mode = realtime`：选择主对话链路；
- `[voice_model]`：Realtime provider；
- `[stt]` / `[tts]`：可选的 standalone provider。

可增加策略配置，默认符合“有 STT/TTS 时优先”：

```toml
[speech_routing]
standalone_tts_priority = ["tts", "realtime", "local"]
standalone_stt_priority = ["stt", "realtime", "audio_attachment"]
```

实现上优先使用 capability 和健康状态，不要只检查 API key 是否存在。

## 建议验收

1. `input_mode=realtime`，不配置 STT/TTS：主对话正常。
2. Realtime + TTS：Voice Notification 经 TTS 播放一次，Realtime 回复不重复。
3. Realtime only：通知可经支持 text input 的 Realtime provider 播放；不支持时保持 pending。
4. 用户在通知播报中插话：播放取消，delivery 不确认，下次仍可播。
5. TTS provider 失败：自动尝试 Realtime；两者都失败时保留 pending。
6. Web audio attachment：配置 STT 时可转写；无 STT 时返回明确 capability 错误或走经过验证的 fallback。
7. `run_script` TTS step 通过同一路由工作，不再硬编码“tts is not configured”。
8. 多 provider Realtime adapter 分别覆盖 text input、manual commit、interrupt、tool-call suppression capability。

## 一手来源

- Aiden runtime/config/voice notification/realtime source code in `origin/main` (`e97998e9`).
- Alibaba Cloud Model Studio, [实时语音对话（Qwen-Audio-Realtime）](https://help.aliyun.com/zh/model-studio/fun-audiochat-realtime).
- Alibaba Cloud Model Studio, [Qwen-Audio-Realtime WebSocket API](https://help.aliyun.com/zh/model-studio/fun-audiochat-realtime-websocket-api).
- Repository-synced official protocol references:
  - `src/agent/internal/agent/rtclient/client-events.md`
  - `src/agent/internal/agent/rtclient/server-events.md`
  - `src/agent/internal/agent/rtclient/websocket-api.md`
