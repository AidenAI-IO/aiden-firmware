# Agent 概览

Aiden Go Agent 位于 `src/agent/`，基于 `github.com/tmc/langchaingo` 构建。它既是长运行 daemon，也是设备端工具控制面。

## 二进制

| 入口 | 说明 |
| --- | --- |
| `cmd/daemon` | 长运行 daemon，支持 Web UI 模式或设备语音模式 |
| `cmd/demo` | 本地 CLI runner，用于开发测试 |

交叉编译后的 daemon 产物为：

```text
build/bin/agent
```

固件中默认安装到：

```text
/oem/usr/bin/agent
```

## 当前能力

- OpenAI-compatible 模型调用：`openai`、`openrouter`；
- 本地文本模型：`ollama`；
- 内置工具调用：HID、截图、音频音量、shell；
- HTTP Tool API，供 Web UI、外部 Agent 或手工调用；
- 从 `SKILL.md` 自动发现并运行时激活 skills；
- 三阶段 role loop（`default` / `plan` / `execution`），简单任务由 planner 直执，复杂任务经 `commit_plan` 进入 executor-verifier 协作；详见 [Agent Context Lifecycle](context-lifecycle.md)；
- conversation memory 持久化，session memory compaction 见 [Session Memory Compaction](session-memory.md)；
- Device / Task Episode memory 设计见 [Memory Plane 设计](memory-plane.md)；
- Web UI：聊天历史、浏览器录音、附件、Tool Lab、Skill Export；
- iOS Live Activity / 灵动岛任务状态，见 [Live Activity / Dynamic Island](live-activity.md)；
- 设备侧语音链路：VAD / STT / TTS。

## 运行模式

由 `agent.toml` 的 `input_mode` 决定：

| 模式 | 行为 |
| --- | --- |
| `text` | 启动 HTTP server 和 Web UI |
| `stt` | 设备录音 → VAD → STT → LLM → TTS |
| `audio` | 设备录音 → 音频附件给 LLM → TTS |

当前一个 daemon 实例只能运行一种模式：Web UI 模式和设备语音模式不能在同一进程中同时运行。

## 启动

本地开发：

```bash
cd src/agent
go run ./cmd/daemon -config ./config -addr :8080
```

设备服务：

```bash
/oem/usr/bin/aiden-env-run /oem/usr/bin/agent -config /userdata/agent -addr :8080
```

CLI demo：

```bash
cd src/agent
go run ./cmd/demo -config ./config -input "What tools do you have?"
go run ./cmd/demo -config ./config -skills my-skill -input "Inspect the UI"
go run ./cmd/demo -config ./config -clear-memory -show-memory -input "Start fresh"
```

## 内置工具

- `skill_list`
- `skill_read`
- `skill_manage`
- `skill_mark_used`
- `keyboard_tap`
- `keyboard_text`
- `mouse_click`
- `mouse_move`
- `mouse_scroll`
- `touch_gesture`
- `screenshot`
- `audio_volume`
- `shell`

工具详情与 HTTP 调用方式见 [工具 HTTP API](tools-http-api.md)。
