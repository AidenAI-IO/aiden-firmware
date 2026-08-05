# Logger 日志格式统一：现状审计与格式提案

> 状态：审计、格式决策和首轮实现已完成；Host、Luckfox 交叉编译和板端实机日志抽查均已完成。
>
> 审计基线：`origin/main`，commit `e962726e`。

## 1. 目标与范围

这份文档先回答两个问题：

1. 当前项目里有哪些日志输出格式不统一；
2. 后续应该统一成什么格式，以及按什么边界实施。

本轮纳入审计的对象是设备上的一方运行时日志：

- Go Agent 主进程及其内部包；
- Go OTA updater；
- C/C++ `frame_service`、`audio_service`、`config_web`、VAD helper，以及它们使用的 SDK/客户端代码；
- 向持久化日志文件写入内容的 init/watchdog Shell 脚本；
- 依赖现有日志文本格式的 Web UI、测试和文档。

以下内容不应为了“日志格式统一”而直接改成运行时日志：

- CLI 的机器可读 stdout，例如 `frame_service_cli health`、`audio_service_cli get-volume`；
- VAD helper 的 stdout 协议，例如 `READY`、`OK`、`P <probability>`、`ERR <message>`；
- 示例程序、benchmark 和测试命令的交互式输出；
- 三方进程自身的日志格式，例如 dnsmasq、wetty；
- `llm-http-*.log` 的 JSONL 请求/响应记录。它是独立审计数据格式，不是普通运行时日志。

## 2. 结论摘要

当前没有跨语言的统一 Logger 契约，主要问题如下：

1. **时间格式不统一**：有 `YYYY/MM/DD HH:MM:SS`、`[YYYY-MM-DD HH:MM:SS]`、`YYYY-MM-DD HH:MM:SS`，也有完全没有时间戳的记录。
2. **级别不统一**：Go 自定义 Logger 有 `DEBUG/INFO/WARN/ERROR`；大量 Go `log.Printf`、全部 C/C++ 和多数 Shell 日志没有真正的级别字段。
3. **服务和组件混在消息中**：同时存在 `[memory]`、`phone-bridge:`、`storage_monitor:`、`[audio_service]`、`[config]`、`ota:` 和无组件前缀。
4. **同一方括号位置语义冲突**：`[ERROR]` 有时是级别，`[listen]` 有时是组件/状态，`[CM]` 是缩写，`[vad:rknn]` 同时编码组件和后端。
5. **正常事件和错误混用 stdout/stderr**：例如 Audio Service 的 started/stopped 正常事件写 stderr，Frame Service 的启动成功写 stdout。
6. **消息字段写法不统一**：有 `key=value`、`(key=value)`、逗号分隔、冒号、自然语言、`->`、大小写句首和句末标点等多种写法。
7. **存在多行记录**：Go 日志调用里有前导 `\n`、尾随 `\n` 和多行 session banner，破坏“一行一条事件”。
8. **守护脚本和进程写入同一个文件却使用不同格式**：`agent.log`、Frame Service、Audio Service、OTA 都存在这种情况。
9. **低层库绕过服务 Logger**：`aiden_sdk.cpp` 直接写 stderr，调用方无法统一补充 service、component 和 level。
10. **消费者依赖脆弱的文本关键字**：Web UI 通过 `failed/error/warn` 等字符串猜测颜色，并通过固定字符串识别 OTA 退出码。

Go Agent 中 `Logger` 的注释称其为 “structured logging”，但当前实际输出仍是 `log.Printf` 风格的自由文本，不具备稳定字段结构。

## 3. 当前实际格式

| 来源 | 当前示例 | 主要问题 |
| --- | --- | --- |
| Go Agent 自定义 Logger | `2026/08/05 14:22:03 [INFO] Agent runtime initialized...` | 时间格式非 RFC 3339；无 service/component/event |
| Go 标准库 `log.*` | `2026/08/05 14:22:03 [listen] Recording audio...` | 无真实 level；方括号被用作组件或状态 |
| Go 标准库嵌入级别 | `2026/08/05 14:22:03 [WARN] [http-retry] ...` | level 是消息文本的一部分；与自定义 Logger 重复定义 |
| Agent `fmt.Printf` | `🚀 Aiden Agent daemon starting on 0.0.0.0:8080` | 无时间、级别和组件；含 Emoji |
| OTA Logger | `2026/08/05 14:22:03 ota: ota check: start` | service 重复；无 level/component/event |
| Audio Service C++ | `[audio_service] playback session 42 started (volume=80)` | 无时间和 level；字段放在括号中 |
| Frame Service C++ | `frame_service listening on /run/...sock` | 无时间、level，格式又不同于 Audio Service |
| SDK C++ | `Failed to open video device /dev/video0: ...` | 无 service/component/level，无法判断来自哪个进程 |
| init supervisor | `[agent] exited with status 1; restarting in 2s` | 与进程日志混写但无时间和 level |
| NTP/USB watchdog | `[2026-08-05 14:22:03] watchdog starting...` | 时间被方括号包裹；无 level/service/component |
| WLAN guard 文件日志 | `2026-08-05 14:22:03 wlan_guard: ...` | 与其他 watchdog 不同；同时还另写 syslog |
| RKNN VAD stderr | `[rknn_vad] api_version=...` | 无时间和 level；另有 `[split_vad]` 组件变体 |

## 4. 统一前的不一致位置清单

下面按“输出机制”罗列所有一方运行时代码中的不统一位置。计数来自静态搜索，目的是确定迁移面，不代表每条记录都需要保留原文。

### 4.1 Go Agent：Logger 机制本身

#### `src/agent/internal/agent/logger.go`

- `NewLogger` 使用 `log.New(os.Stderr, "", log.LstdFlags)`，格式固定为本地时间 `YYYY/MM/DD HH:MM:SS`，只有秒精度，没有时区。
- `write` 仅在消息前拼接 `[LEVEL]`，没有 service、component、event 或稳定字段编码。
- `NewLogger` 同时修改进程级默认 `log` 的 output/flags，但不能给默认 `log.Printf` 补充 level。
- 自定义 Logger 有自己的互斥锁，标准库全局 `log.*` 是另一条路径；两套调用最终混写同一 stderr/日志文件。
- 启动清理失败直接调用 `logger.Printf("[WARN] ...")`，没有走 `Warn` 方法。

### 4.2 Go Agent：自定义 Logger 消息格式

共有 232 个能在调用行直接识别字符串字面量的 `Info/Warn/Error/Debug` 调用。即使它们都能获得统一的时间和 level，消息内部仍存在三种组件表达方式：

- 方括号：60 处，例如 `[memory] ...`；
- 冒号：49 处，例如 `phone-bridge: ...`；
- 无组件前缀：123 处，例如 `Chat request completed: ...`。

完整文件级分布如下：

| 文件 | 调用数 | `[component]` | `component:` | 无组件前缀 |
| --- | ---: | ---: | ---: | ---: |
| `src/agent/internal/agent/coordinate_debug.go` | 1 | 0 | 0 | 1 |
| `src/agent/internal/agent/episode_exporter.go` | 2 | 2 | 0 | 0 |
| `src/agent/internal/agent/live_activity.go` | 16 | 0 | 0 | 16 |
| `src/agent/internal/agent/local_audio_playback.go` | 1 | 0 | 0 | 1 |
| `src/agent/internal/agent/memory.go` | 20 | 20 | 0 | 0 |
| `src/agent/internal/agent/memory_plane.go` | 3 | 3 | 0 | 0 |
| `src/agent/internal/agent/phone_bridge.go` | 15 | 0 | 15 | 0 |
| `src/agent/internal/agent/phone_bridge_http.go` | 8 | 0 | 8 | 0 |
| `src/agent/internal/agent/phone_bridge_queue.go` | 7 | 0 | 7 | 0 |
| `src/agent/internal/agent/phone_bridge_restore.go` | 1 | 0 | 1 | 0 |
| `src/agent/internal/agent/profile_debouncer.go` | 6 | 6 | 0 | 0 |
| `src/agent/internal/agent/runtime.go` | 43 | 20 | 0 | 23 |
| `src/agent/internal/agent/screen_mapping_prime.go` | 2 | 0 | 0 | 2 |
| `src/agent/internal/agent/server.go` | 63 | 5 | 0 | 58 |
| `src/agent/internal/agent/session_manager.go` | 10 | 3 | 0 | 7 |
| `src/agent/internal/agent/storage_manager.go` | 1 | 1 | 0 | 0 |
| `src/agent/internal/agent/storage_monitor.go` | 18 | 0 | 15 | 3 |
| `src/agent/internal/agent/stt_config_live_session.go` | 9 | 0 | 0 | 9 |
| `src/agent/internal/agent/tools_quick_actions.go` | 3 | 0 | 3 | 0 |
| `src/agent/internal/agent/tts/manager.go` | 3 | 0 | 0 | 3 |

这些调用中出现的方括号组件全集是：

| 标签 | 数量 | 问题 |
| --- | ---: | --- |
| `memory` | 40 | 与 `component:` 和无前缀风格不同 |
| `profile-debouncer` | 6 | 使用 kebab-case |
| `telemetry` | 3 | 部分 telemetry 记录又是无前缀形式 |
| `state` | 3 | 状态和组件含义容易混淆 |
| `preempt` | 3 | 同一组件也存在全局 `log.Printf` 路径 |
| `audio` | 2 | 与 Audio Service 的 service 名称容易混淆 |
| `storage`、`sse`、`runtime` | 各 1 | 没有统一组件字段 |

冒号组件全集是：

| 标签 | 数量 | 问题 |
| --- | ---: | --- |
| `phone-bridge` | 24 | kebab-case，且有些消息字段放在括号中 |
| `storage_monitor` | 9 | snake_case，与 `phone-bridge` 不同 |
| `phone-bridge-queue` | 7 | 组件层级被编码在名称中 |
| `storage_cleanup` | 4 | 同一文件还存在 `storage monitor` 自然语言写法 |
| `quick_actions` | 3 | 与其他组件表达方式不同 |
| `storage_alert` | 2 | 同一业务被拆成多个文本前缀 |

### 4.3 Go Agent：绕过自定义 Logger 的全局 `log.*`

在 daemon 和 `internal/agent` 的非测试代码中，共有 242 个 `log.Print*`/`log.Fatal*` 调用绕过自定义 Logger。完整文件级清单如下：

| 文件 | 数量 |
| --- | ---: |
| `src/agent/cmd/daemon/main.go` | 120 |
| `src/agent/internal/agent/audio_dialog.go` | 47 |
| `src/agent/internal/agent/skill_sync.go` | 12 |
| `src/agent/internal/agent/skill_merge.go` | 9 |
| `src/agent/internal/agent/agent_loop.go` | 6 |
| `src/agent/internal/agent/model_metadata.go` | 4 |
| `src/agent/internal/agent/models.go` | 4 |
| `src/agent/internal/agent/skill_usage.go` | 4 |
| `src/agent/internal/agent/contextmanager/session_persist.go` | 3 |
| `src/agent/internal/agent/skill_loader.go` | 3 |
| `src/agent/internal/agent/text_input_common.go` | 3 |
| `src/agent/internal/agent/agentpath/filepath.go` | 2 |
| `src/agent/internal/agent/connection_warmup.go` | 2 |
| `src/agent/internal/agent/runtime.go` | 2 |
| `src/agent/internal/agent/tools_shell.go` | 2 |
| `src/agent/internal/agent/tools_shell_session.go` | 2 |
| `src/agent/internal/agent/tts/adapters/minimax/websocket.go` | 2 |
| `src/agent/internal/agent/tts/adapters/volcengine/adapter.go` | 2 |
| `src/agent/internal/agent/vad.go` | 2 |
| `src/agent/internal/agent/voice_run_control.go` | 2 |
| `src/agent/internal/agent/compactor/compactor.go` | 1 |
| `src/agent/internal/agent/contextmanager/context_manager.go` | 1 |
| `src/agent/internal/agent/coordinate_debug.go` | 1 |
| `src/agent/internal/agent/prompt_cache_policy.go` | 1 |
| `src/agent/internal/agent/tools_enter_text.go` | 1 |
| `src/agent/internal/agent/tools_post_action_screenshot.go` | 1 |
| `src/agent/internal/agent/touchscreen_rca_debug.go` | 1 |
| `src/agent/internal/agent/tts/adapters/alicloud/adapter.go` | 1 |
| `src/agent/internal/agent/tts/adapters/fishaudio/adapter.go` | 1 |

这些全局调用使用的方括号标签全集如下。这里的标签有时表示 level，有时表示组件、状态或流程阶段：

`listen`、`error`、`ready`、`steer`、`skill_sync`、`tts`、`audio`、`skill_merge`、`interrupt`、`exit`、`WARN`、`wakeup`、`session`、`manual`、`vad`、`stt`、`utterance`、`skill_usage`、`llm`、`text-input`、`skill_loader`、`init`、`warmup`、`vad:%s`、`reply`、`ota`、`adb`、`INFO`、`text`、`storage_monitor`、`server`、`preempt`、`loop guard`、`history`、`debug`、`audio_archive`、`ERROR`、`CM`。

具体不一致包括：

- level 大小写混用：`[error]`、`[debug]`、`[WARN]`、`[INFO]`、`[ERROR]`；
- 组件命名混用：snake_case、kebab-case、空格、全大写缩写和带参数标签；
- `[ready]`、`[exit]`、`[manual]` 等实际上是流程状态，不是稳定组件；
- `models.go` 使用 `[WARN] [http-retry]`，而其他地方通常只放一个标签；
- `coordinate_debug.go` 在有自定义 Logger 和无自定义 Logger 时会输出两种不同格式；
- `runtime.go` 的 preempt panic 在 Logger 不可用时切换到另一种格式；
- `agentpath/filepath.go` 使用 `log.Fatalf`，直接决定进程退出且不走统一 severity/flush 契约。

### 4.4 Go Agent：空行、多行、Emoji 和自由文本

- 静态扫描发现约 110 个 `log.Print*` 调用显式带前导或尾随换行，主要集中在：
  - `src/agent/cmd/daemon/main.go`；
  - `src/agent/internal/agent/audio_dialog.go`；
  - `src/agent/internal/agent/agent_loop.go`；
  - `src/agent/internal/agent/contextmanager/session_persist.go`；
  - `src/agent/internal/agent/vad.go`；
  - `src/agent/internal/agent/voice_run_control.go`；
  - `src/agent/internal/agent/tts/adapters/minimax/websocket.go`。
- `src/agent/internal/agent/session_manager.go:159-173` 用 8 条日志组成 `==== / NEW SESSION STARTED / Session ID / Reason / ...` banner。同一事件被拆成多条记录，且字段使用标题式文本。
- `src/agent/cmd/daemon/main.go:139-160` 用 `fmt.Printf` 输出 🚀、📂、🔀、🌐、📝 等启动信息，完全绕过 Logger。
- 消息句首同时存在 `Failed...`、`failed...`、`TTS...`、`phone-bridge...` 等写法。
- 同类字段同时存在 `error=%v`、`err=%v`、`: %v`；`len=%d`、`output_len=%d`、`%d chars`；`elapsed_ms=%d`、`after %s` 等写法。
- 正常路径也存在不合适的 severity，例如 `memory.go` 的 “repaired truncated session events” 使用 `Warn`。

### 4.5 Go OTA

| 位置 | 当前方式 | 问题 |
| --- | --- | --- |
| `src/agent/cmd/ota/main.go:189` | `log.New(os.Stderr, "ota: ", log.LstdFlags)` | 与 Agent Logger 格式不同，无 level；消息本身又普遍以 `ota ...:` 开头 |
| `src/agent/internal/ota/updater.go:206-1055` | `u.logf("ota check: ...")` 等 | phase/component 被写进自由文本，所有记录共用同一默认级别 |
| `src/agent/internal/ota/github.go`、`download.go`、`updater.go` | `fmt.Fprintf(os.Stderr, "ota: ...")` | 绕过 OTA Logger，且没有时间戳 |
| `overlay/etc/init.d/S54ota` | 直接 `echo "[ota] ..." >> ota.log` | 与 Go OTA 输出混写，格式不同 |

OTA 的 phase 当前至少通过文本前缀表达为 `check`、`manifest`、`release`、`partition`、`asset`、`download`、`verify`、`write`、`misc`、`reboot`、`health`、`cleanup`、`space`，适合后续改成稳定 component/event，而不是继续嵌在 message 中。

### 4.6 C/C++ 运行时

C/C++ 当前没有公共 Logger。以下位置直接使用 `fprintf(stderr, ...)`、`std::cerr`、`printf` 或 `std::cout`：

| 文件 | 直接 stderr 点 | 直接 stdout 点 | 当前特点 |
| --- | ---: | ---: | --- |
| `src/aiden_sdk.cpp:211-1440` | 36 | 0 | 全部无时间、level、service/component；会污染调用进程日志 |
| `src/frame_service_main.cpp:54-179` | 5 | 1 | usage、错误和启动成功格式不同；没有 `[frame_service]` 一致前缀 |
| `src/audio_service_main.cpp:25-83` | 2 | 2 | runtime 使用 `[audio_service]`，但正常事件在 stdout、错误在 stderr；句首大小写和句号不一致 |
| `src/audio_service_server.cpp:72-150` | 2 | 0 | 正常请求和未知操作都写 stderr，无 level 区分 |
| `src/audio_session_manager.cpp:27-386` | 12 | 0 | 正常 lifecycle、warning、error 全部写 stderr 且只靠自然语言区分 |
| `src/audio_record_session.cpp:113-196` | 4 | 0 | 统一带 `[audio_service]`，但没有 component/event/level |
| `src/audio_playback_session.cpp:22-138` | 5 | 0 | 统一带 `[audio_service]`，但没有 component/event/level |
| `src/audio_service_client.cpp:99` | 1 | 0 | 使用另一个 `[audio_service_client]` 前缀 |
| `src/config_web.cpp:1676-6989` | 17 | 4 | 部分用 `[config]`，部分完全无前缀；启动信息写 stdout |
| `src/rknn_vad.cpp:748-1282` | 5 | 10 | stderr 同时使用 `[rknn_vad]`、`[split_vad]`；stdout 是协议，不应纳入 Logger |
| `src/cpu_vad.cpp:54-210` | 1 | 4 | stderr 无组件；stdout 是协议，不应纳入 Logger |
| `src/vad_common.h:38-657` | 0 | 2 | stdout 是 helper 协议，不应改成普通日志 |
| `src/hid_server.cpp:710-1007` | 4 | 2 | 示例 HTTP/HID server，无统一格式；不是当前核心 daemon，优先级较低 |

C/C++ 侧还存在以下横向问题：

- 库代码不知道当前 service，导致同一个 `aiden_sdk.cpp` 在 Frame Service、示例程序等进程里输出完全相同的无来源文本；
- `errno` 有时只输出 `strerror(errno)`，有时同时输出 errno 数值，字段不稳定；
- session id、volume、attempt、device 等数据有自然语言、冒号和括号三种表达；
- 没有统一的线程安全、单行转义和最大长度约束；
- CLI usage/error 与 daemon runtime log 共用直接输出 API，实施时必须保留两者边界。

### 4.7 Shell init/watchdog

| 文件 | 直接日志写入点 | 当前格式/问题 |
| --- | ---: | --- |
| `overlay/etc/init.d/S53agent` | 3 | `[agent] ...`，无时间/level，与 Go Agent 混写同一文件 |
| `overlay/etc/init.d/S52frame_service` | 3 | `[frame_service] ...`，无时间/level，与 C++ 混写同一文件 |
| `overlay/etc/init.d/S53audio_service` | 3 | `[audio_service] ...`，无时间/level，与 C++ 混写同一文件 |
| `overlay/etc/init.d/S54ota` | 10 | `[ota] ...`，无时间/level，与 Go OTA 混写同一文件 |
| `overlay/etc/init.d/S53adb_server` | 4 | `[adb] ...`，无时间/level；还混入 adb 自身原始输出 |
| `overlay/etc/init.d/S50ntp_watchdog` | 1 个 `log()` | `[YYYY-MM-DD HH:MM:SS] message`，无 level/service/component |
| `overlay/etc/init.d/S60usb_ecm_watchdog` | 1 个 `log()` + 3 个原始 stderr 重定向 | 自定义时间格式；`ifconfig`/sysfs 错误会以无格式原文混入 |
| `overlay/oem/usr/bin/wlan_guard.sh` | 文件 + syslog 两条路径 | 文件为 `YYYY-MM-DD HH:MM:SS wlan_guard: ...`，syslog 格式由系统决定 |
| `overlay/etc/init.d/S55aiden_usb_dhcp` | dnsmasq `--log-facility` | 三方格式，应该标为外部日志而不是强行改写 |

`start/stop/status` 命令返回给终端的短文本不等于持久化 runtime log，不需要全部套 Logger 格式；只有写入日志文件或 syslog 的路径需要统一。

### 4.8 格式消费者和兼容影响

格式迁移不能只改生产者，还必须同步这些依赖：

| 位置 | 当前依赖 |
| --- | --- |
| `src/config_web_html.h:568` | `classifyLine` 通过 `failed/error/warn/fallback/...` 等自由文本关键字猜 level/颜色 |
| `src/config_web_html.h:584` | 通过固定字符串 `[config_web] ota update exited rc=` 提取 OTA 退出码 |
| `src/config_web.cpp:3416` | 生成上述 OTA 固定字符串 |
| `tests/config_web_e2e_test.cpp:2138-2192` | 断言上述 OTA marker |
| `tests/config_web_source_test.cpp:399-403,750` | 断言日志分类函数和 OTA marker |
| `src/agent/internal/agent/memory_test.go:1852` | 断言多行 session banner 中存在 `NEW SESSION STARTED` |
| `docs/02-architecture/paths-and-artifacts.md:122-128` | 文档示例固定为 Go 当前的日期/`[INFO]`/多行 banner 格式 |
| `scripts/test_agent_log_cap.sh` | 验证日志路径和截断；格式迁移时要确认仍保留完整最新行 |

另外，`/api/logs/export` 当前只打包 Agent 主日志、最新 episode 和最新 LLM HTTP 日志，并不会聚合 Frame/Audio/watchdog 日志。即使每个文件内部格式统一，也要保留 `service` 字段，便于未来扩展统一导出或集中查看。

### 4.9 已扫描但不属于 runtime Logger 的输出

以下文件也有 `printf`、`std::cout`、`fmt.Print*` 等输出，但它们承担 CLI、协议、示例或测试职责。本次不把这些输出误计为“格式缺陷”；将来若要统一 CLI UX，应单独立项：

- C/C++ CLI：`src/audio_service_cli.cpp`、`src/frame_service_cli.cpp`、`src/image_process_cli.cpp`；
- C/C++ 示例/实验程序：`src/example_camera_capture.cpp`、`src/example_audio_capture.cpp`、`src/example_audio_play.cpp`、`src/example_wakeup.cpp`、`src/example_usb_hid.cpp`、`src/trigger.c`、`src/hello.c`；
- VAD stdout 协议：`src/rknn_vad.cpp`、`src/cpu_vad.cpp`、`src/vad_common.h`；
- Go CLI/benchmark：`src/agent/cmd/abctl/main.go`、`src/agent/cmd/benchmark-http/main.go`、`src/agent/cmd/daemon/config_commands.go`、`src/agent/cmd/test-warmup/main.go`，以及 `src/agent/cmd/ota/main.go` 的命令结果 stdout；
- `tests/`、`scripts/test_*` 和部署/维护脚本面向操作者的进度输出。

边界判断原则是：如果输出会长期进入设备运行日志或被 support bundle/日志查看器消费，就走统一 Logger；如果 stdout 是 API、协议或一次性命令结果，就保持它的接口语义。

## 5. 格式方案比较

| 方案 | 示例 | 优点 | 缺点 |
| --- | --- | --- | --- |
| JSONL | `{"ts":"...","level":"INFO",...}` | 机器解析最稳定 | C++/Shell 实现和转义成本较高；人手 `tail` 可读性较差；日志体积较大 |
| 纯 logfmt | `ts=... level=INFO service=...` | 字段清晰、易 grep | 所有字符串都要正确 quote/escape；人眼不易快速识别固定前缀 |
| **固定前缀 + key=value** | `... [INFO] [agent] [voice] recording_started session_id=42` | 易读、易 grep、跨 Go/C++/BusyBox Shell 实现简单 | 需要定义并测试 value 转义规则 |

建议采用第三种：**固定前缀 + 稳定 event + `key=value` 字段**。

## 6. 推荐统一格式

### 6.1 行格式

```text
<UTC timestamp> [<LEVEL>] [<service>] [<component>] <event> [key=value ...]
```

示例：

```text
2026-08-05T06:22:03Z [INFO] [agent] [phone_bridge] client_connected platform=ios phone_id=p1
2026-08-05T06:22:04Z [WARN] [agent] [stt] streaming_fallback provider=tencent error="upload timeout"
2026-08-05T06:22:05Z [ERROR] [frame_service] [camera] device_open_failed device=/dev/video0 errno=16 error="Device or resource busy"
2026-08-05T06:22:06Z [INFO] [audio_service] [playback] session_started session_id=42 volume=80
2026-08-05T06:22:07Z [WARN] [agent] [supervisor] process_exited status=1 restart_delay_s=2
```

建议用 UTC 的原因：Go、C/C++ 和 BusyBox Shell 都能稳定生成 `YYYY-MM-DDTHH:MM:SSZ`，跨设备和 OTA/support bundle 对时也更容易。第一版先统一到秒精度；耗时和高频时序使用 `duration_ms`、`elapsed_ms` 等字段表达，不在不同语言间引入不可靠的毫秒时间实现。

### 6.2 固定字段规则

| 字段 | 规则 |
| --- | --- |
| timestamp | UTC，`YYYY-MM-DDTHH:MM:SSZ`，固定 20 字符 |
| level | 仅 `DEBUG`、`INFO`、`WARN`、`ERROR` |
| service | lowercase snake_case；进程/日志文件级身份 |
| component | lowercase snake_case；进程内稳定模块名 |
| event | lowercase snake_case；稳定、可搜索，不写自然语言句子 |
| fields | `key=value`，key 使用 lowercase snake_case |

推荐 service 名：

- `agent`
- `frame_service`
- `audio_service`
- `config_web`
- `ota`
- `wlan_guard`
- `usb_ecm_watchdog`
- `ntp_watchdog`
- `adb_server`

### 6.3 value 编码

- 无空白且不含 `"`、`\\`、`=` 的简单值可直接输出；
- 其他字符串使用双引号，并按 JSON 字符串规则转义 `"`、`\\`、`\n`、`\r`、`\t`；
- bool 固定为 `true/false`；
- duration 优先换算为整数并在 key 标单位，例如 `duration_ms=120`、`timeout_s=10`；
- size 使用 `size_bytes`，不要同时出现 `10MB`、`10 MB`、`10485760 bytes` 三种写法；
- 错误文本统一使用 `error="..."`；系统错误可增加 `errno=16`；
- ID 使用明确名称，例如 `session_id`、`request_id`、`runtime_id`，不要只写 `id`；
- 不允许原始换行进入日志记录。

### 6.4 severity 规则

| Level | 使用场景 |
| --- | --- |
| `DEBUG` | 高频诊断、内部决策、原始协议辅助信息；默认可关闭 |
| `INFO` | 服务启动停止、连接建立、一次操作成功、重要状态变更 |
| `WARN` | 可恢复错误、重试、fallback、降级、忽略异常输入 |
| `ERROR` | 当前请求/操作失败，或服务无法完成关键职责 |

禁止再用 `[error]`、`[debug]`、消息中的 `WARN:` 等方式模拟 severity。

### 6.5 一行一事件

- 每次 Logger 调用只能生成一行；
- 调用方不传前导/尾随 `\n`；
- session banner 改为单条事件，例如：

```text
2026-08-05T06:30:00Z [INFO] [agent] [session] session_started session_id=abc123 reason=time_gap_long closed_session_id=def456 archive=def456
```

- 若需要记录外部多行 stderr，每一物理行都应重新包装为独立事件，例如 `helper_stderr line="..."`；
- 不用 Emoji、分隔线或句末标点表达结构和重要性。

### 6.6 stdout/stderr 边界

- daemon 普通日志统一写 stderr；
- CLI 的数据/协议继续写 stdout；
- CLI 参数错误继续写 stderr，但不强制伪装成 daemon runtime log；
- VAD helper 的 stdout 协议保持原样，只有 helper 的 stderr 进入统一 Logger；
- init script 仍可把 daemon stdout/stderr 合并进文件，但 daemon 自身不再依赖 stdout 写日志。

## 7. 现有标签到新字段的建议映射

| 当前写法 | 建议映射 |
| --- | --- |
| `[error] ...` | 使用 `ERROR` level；component 由调用位置确定 |
| `[ready]`、`[exit]`、`[manual]` | component=`voice` 或 `runtime`，用 `ready`、`stopped`、`manual_recording_started` 等 event |
| `[listen]`、`[utterance]` | component=`voice`，event=`recording_started`、`utterance_detected` 等 |
| `[vad]`、`[vad:rknn]` | component=`vad`，增加 `backend=rknn` 字段 |
| `[tts] provider: ...` | component=`tts`，增加 `provider=...` 字段 |
| `[memory]` | component=`memory` |
| `phone-bridge:` | component=`phone_bridge` |
| `phone-bridge-queue:` | component=`phone_bridge_queue` |
| `storage_monitor:` | component=`storage_monitor` |
| `storage_cleanup:` | component=`storage_cleanup` |
| `[WARN] [http-retry]` | level=`WARN`，component=`http_retry` |
| `[audio_service] ...` | service=`audio_service`，component 细分为 `recording`、`playback`、`volume`、`server` |
| `[config] ...` | service=`config_web`，component=`agent_config` |
| `[rknn_vad]` / `[split_vad]` | service=`agent` 或 helper 进程名，component=`vad_helper`，增加 `backend`/`model_type` 字段 |

## 8. 实际实现

### 8.1 Go

- 新增 `src/agent/internal/logging`，统一生成 UTC、level、service、component、event 和字段。
- `agent.Logger` 保留为兼容 facade，同时增加显式的 `InfoEvent`、`WarnEvent`、`ErrorEvent`、`DebugEvent` API。
- daemon 启动时安装标准库 `log.*` 兼容 writer；旧调用会解析已有 level/component 前缀并输出统一格式。
- legacy 多行消息被拆成逐行事件，空白分隔行被丢弃；session banner 已合并为单条 `session_started`。
- daemon、存储清理和 OTA 中直接写 stderr 的第一方 runtime 日志已改为统一事件。

### 8.2 C/C++

- 新增 `src/aiden_log.{h,cpp}` 并接入公共 `aiden` library、`config_web` 和 VAD targets。
- 提供 `AIDEN_LOG_DEBUG/INFO/WARN/ERROR`，统一 UTC、标识符标准化、线程安全写入和单行转义。
- `frame_service`、`audio_service`、`config_web`、`cpu_vad`、`rknn_vad` 在启动时设置 process-wide service。
- Audio lifecycle、Frame/SDK camera/HDMI/GPIO、config_web runtime 和 VAD stderr 已迁移；CLI usage 与 VAD stdout 协议保持原样。
- C++ 兼容 API 当前把 printf-style payload 放入 `message="..."` 字段；component/event 已稳定，后续新代码可逐步增加更细粒度字段 API。

### 8.3 Shell

- 新增 `overlay/oem/usr/lib/aiden-log.sh`，统一 UTC、level 和标识符，并转义 message 保证单行输出。
- 已迁移 Agent/Frame/Audio supervisor、OTA health、ADB bootstrap、NTP、USB ECM 和 WLAN watchdog 的第一方持久化日志。
- USB watchdog 不再把 sysfs/ifconfig 原始 stderr 直接混入日志；失败由稳定 event 记录。
- adb、dnsmasq、wetty 等外部进程自身输出不做重写，仍视为 external/raw 日志。

### 8.4 消费者和测试

- Web UI 先按 `[ERROR]`/`[WARN]` 判断颜色，再保留旧文本关键字作为 external/legacy fallback。
- OTA 完成 marker 改为 `[config_web] [ota] update_exited exit_code=N`。
- Agent startup excerpt 同时识别新 supervisor event 和旧 `[agent] starting/waiting`，便于滚动升级。
- Go、C++ 和 Shell 均有 formatter/conformance 测试。

## 9. 已知兼容层和剩余工作

- Go 代码仍有一批标准库 `log.Print*` 调用；生产 daemon 已通过兼容 writer 统一最终输出，但这些调用尚未全部改成显式 event/fields API。
- 第三方输出和 external/raw 行不会天然匹配第一方格式，日志查看器继续保留旧关键字 fallback。
- 低优先级 example、benchmark、CLI 输出未迁移，这是刻意保留的接口边界。
- Go legacy 调用显式 event 化仍可作为后续可读性重构，但不影响当前最终输出契约。

### 9.1 已完成验证

- `go test ./...`
- Host CMake build 与 `ctest --output-on-failure`，包含 C++ Logger 和 Shell conformance test
- Luckfox ARM C/C++ 与 Go binaries 交叉编译
- 板端部署并重启 Agent、Frame Service、Audio Service、config_web、WLAN/NTP/USB ECM/ADB watchdog
- 板端抽查 `agent.log`、Frame/Audio、OTA、ADB、NTP、USB ECM、WLAN 日志，第一方新记录匹配统一前缀
- CLI health、Agent Phone Bridge status 和 config_web 首页 smoke test

## 10. 验收标准

完成统一后，第一方 daemon/runtime 的每一条日志应匹配：

```text
^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z \[(DEBUG|INFO|WARN|ERROR)\] \[[a-z0-9_]+\] \[[a-z0-9_]+\] [a-z0-9_]+(?: .*)?$
```

并满足：

- Agent daemon 最终 runtime 输出必须全部经过统一 formatter；兼容期允许内部保留 `log.Print*` 调用；
- C/C++ daemon 和公共 SDK 不再绕过公共 Logger 直接写 runtime stderr；
- 写入同一日志文件的 init supervisor 使用相同格式；
- 没有前导空行、尾随双换行、原始多行 message 或 Emoji；
- level、service、component、event 可被 Web UI 直接解析；自由文本关键字只作为 external/legacy fallback；
- OTA 退出状态不再依赖不可扩展的整句文本，至少改为稳定 event/field；
- `llm-http-*.log`、CLI stdout 和 VAD stdout 协议保持原有职责；
- Host tests 覆盖格式、转义、并发单行写入、旧日志 UI 兼容；
- 板端重启 Agent、Frame Service、Audio Service、OTA health 后，实际日志逐行抽查通过。

## 11. 已确认决策

1. 使用 UTC、秒精度。
2. `event` 必填。
3. 本轮只统一格式，不扩大到 transcript/reply 内容治理；脱敏和截断另行设计。
4. 首批范围包含 Agent、OTA、Frame/Audio、config_web、VAD stderr 和第一方 init/watchdog。
5. Web UI 和 Agent startup excerpt 保留一版旧格式兼容；生产者不双写。
