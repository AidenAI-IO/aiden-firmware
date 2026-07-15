# Hermes 定时任务实现调研报告

> 调研日期：2026-07-15  
> 上游项目：[`NousResearch/hermes-agent`](https://github.com/NousResearch/hermes-agent)  
> 主要源码基线：`v0.18.2`，commit [`6997dc81c`](https://github.com/NousResearch/hermes-agent/tree/6997dc81cd21dc88c6cb808a1fb3626b6ce71254)（2026-07-14）  
> 本机安装版对照：`v0.13.0`  
> 目的：理解 Hermes 定时任务的架构、可靠性语义、安全边界，并评估 Aiden 的借鉴路径。

## 1. 结论摘要

Hermes 的定时任务不是系统 `crontab` 的薄封装，而是一套内置的 Agent 调度子系统：

```text
用户 / Agent / CLI
        │ 创建、编辑、暂停、触发
        ▼
~/.hermes/cron/jobs.json
        │
        ▼
CronScheduler 触发层
  ├─ builtin：Gateway 内 60 秒 ticker
  └─ chronos：外部托管 one-shot + 回调 webhook
        │
        ▼
due 检测、去重、预推进 next_run_at
        │
        ▼
独立 cron Agent 会话 / 纯脚本任务
        │
        ├─ 本地保存审计输出
        └─ 投递到 origin / Telegram / Feishu / 其他平台
```

其核心设计取舍是：

1. **任务定义和运行状态统一持久化到 JSON 文件**，通过临时文件、`fsync`、原子替换和跨进程文件锁保证并发安全。
2. **默认依赖 Gateway 常驻进程触发**，Gateway 每 60 秒扫描一次；没有独立 cron daemon，也不要求系统安装 `crond`。
3. **执行和触发解耦**。最新版本用 `CronScheduler` 抽象“何时触发”，内置 ticker 和 Chronos 共用同一套“执行、落盘、投递、更新状态”逻辑。
4. **偏向 at-most-once**。循环任务在执行前先推进下次运行时间；有限 one-shot 在副作用前先占用执行次数。进程崩溃时宁可漏掉一次，也尽量避免重放副作用。
5. **每次运行创建新的 Agent 会话**，不继承普通聊天历史，不读写用户记忆，并禁用 `cronjob`、`messaging`、`clarify` 等不适合无人值守执行的工具集。
6. **同时支持 Agent 任务和零 LLM 的脚本任务**。脚本可以提供上下文、作为 `wakeAgent` 前置门控，或在 `no_agent` 模式下直接产生最终通知。
7. **可靠性实现成熟，但复杂度已经较高**。大量代码用于处理多进程重复触发、长任务 claim 续租、进程重启、时区迁移、输出留存、Gateway 优雅退出和投递失败。

对 Aiden 的建议是：先实现一个单机、单进程、串行执行的 MVP，不要直接照搬 Hermes 当前全部复杂度。最值得复用的是任务状态机、执行前 claim、错过执行折叠、独立运行上下文、审计输出和 Agent/脚本双模式。

## 2. 调研范围与证据

重点阅读的 Hermes 文件：

| 文件 | 作用 |
| --- | --- |
| `cron/jobs.py` | 任务模型、时间解析、JSON 持久化、due 检测、claim、输出留存 |
| `cron/scheduler.py` | 任务执行、Agent 创建、脚本模式、并发池、结果投递、运行状态更新 |
| `cron/scheduler_provider.py` | 内置 ticker 与外部调度 provider 抽象 |
| `plugins/cron_providers/chronos/` | scale-to-zero 的外部 one-shot 调度实现 |
| `tools/cronjob_tools.py` | 模型可调用的统一 `cronjob` 工具与输入安全校验 |
| `hermes_cli/cron.py` | `hermes cron` CLI、状态检查和 Gateway 存活提示 |
| `gateway/run.py` | Gateway 启停时启动/停止调度 provider |
| `gateway/platforms/api_server.py` | Chronos `POST /api/cron/fire` 回调入口 |
| `website/docs/developer-guide/cron-internals.md` | 上游设计说明 |
| `website/docs/user-guide/features/cron.md` | 用户功能和配置说明 |

本次使用 Hermes 最新上游源码进行静态分析，并用本机 v0.13.0 Python 环境在 v0.18.2 源码上运行了以下核心测试集：

```text
tests/cron/test_jobs.py
tests/cron/test_scheduler_provider.py
tests/cron/test_claim_job_for_fire.py
tests/cron/test_parallel_pool.py
tests/cron/test_cron_profile_isolation.py
tests/cron/test_cron_no_agent.py
tests/cron/test_cron_workdir.py
```

结果：`227 passed in 26.29s`。上游当前仅 `tests/cron/` 就有 32 个测试文件；连同 Gateway、CLI 和 Chronos 相关测试，本次统计到约 729 个 cron 测试用例入口。

另外用最小 no-agent 脚本实测了 `workdir`：任务配置 `/tmp/hermes-cron-workdir-target`，脚本实际打印的 cwd 是 `/private/tmp/hermes-cron-workdir-test/scripts`，确认当前子进程仍使用脚本目录。

### 2.1 本机 v0.13.0 与最新 v0.18.2 的主要差异

本机安装版已经具备 JSON 任务库、Gateway ticker、Agent/脚本执行和平台投递等主体能力。v0.13.0 之后的改动主要集中在生产可靠性和托管场景，而不是推翻原架构：

- 增加 `CronScheduler` provider 和 Chronos 外部托管触发；
- 增加跨进程 jobs lock、profile store context 和多实例 fire claim；
- one-shot 从简单时间推进升级为持久 `run_claim`，并对长 Agent/脚本运行续租；
- due scan 对缺 ID、坏 schedule、坏时间字段和单任务异常做隔离修复；
- Gateway shutdown 能感知在途 cron，等待投递并把强制中断记录为 interrupted；
- 增加输出留存上限、SessionDB 初始化超时和 jobs lock 超时；
- 加强 workdir 并发隔离、prompt/skill 注入检查、secret scope 和 provider URL 校验；
- 增加未 pin 模型/provider 的成本漂移保护；
- `cronjob(action="run")` 可以走立即执行路径，而不必只等待下一分钟 ticker。

因此，如果基于本机 v0.13.0 行为做兼容或复刻，需要特别注意这些后来补齐的故障窗口。

## 3. 总体架构

### 3.1 三层分工

Hermes 实际将定时任务拆成三层：

| 层 | 主要职责 | 核心实现 |
| --- | --- | --- |
| 管理层 | create/list/update/pause/resume/run/remove | `tools/cronjob_tools.py`、`hermes_cli/cron.py` |
| 触发层 | 判断什么时候应该 fire | `CronScheduler`、builtin ticker、Chronos |
| 执行层 | Agent/脚本执行、输出保存、投递、状态提交 | `scheduler.run_one_job()`、`run_job()` |

触发层不能自己重新实现 Agent 或投递逻辑。无论由本地 ticker 还是外部 Chronos 触发，最终都进入 `run_one_job()`，这是 Hermes 避免两条执行链行为漂移的关键。

### 3.2 默认运行进程

内置模式下，调度器运行在 Hermes Gateway 进程内部：

```text
hermes gateway
  ├─ 消息平台 adapters
  ├─ cron-scheduler 后台线程
  │    └─ InProcessCronScheduler.start()
  │          └─ 每 60 秒 scheduler.tick(sync=False)
  └─ gateway-housekeeping 后台线程
```

`hermes cron create` 只负责写任务，不会启动长期调度服务。若使用 builtin provider 而 Gateway 没有运行，任务不会自动执行。CLI 会显式提示用户启动 `hermes gateway install`，`hermes cron status` 还会检查 ticker 心跳，而不只是检查 Gateway PID。

## 4. 任务模型与持久化

### 4.1 存储布局

默认 profile 的主要目录是：

```text
~/.hermes/cron/
├── jobs.json
├── .jobs.lock
├── .tick.lock
├── ticker_heartbeat
├── ticker_last_success
└── output/
    └── <job_id>/
        └── 2026-07-15_09-00-00.md
```

多 profile 模式下，每个 profile 使用自己的 `HERMES_HOME/cron`，任务、配置、凭证、技能和输出互相隔离。

### 4.2 主要字段

任务记录的关键字段如下：

```json
{
  "id": "a1b2c3d4e5f6",
  "name": "Daily briefing",
  "prompt": "Summarize today's AI news",
  "skills": ["research"],
  "schedule": {
    "kind": "cron",
    "expr": "0 9 * * *",
    "display": "0 9 * * *"
  },
  "repeat": {
    "times": null,
    "completed": 42
  },
  "enabled": true,
  "state": "scheduled",
  "next_run_at": "2026-07-16T09:00:00+08:00",
  "last_run_at": "2026-07-15T09:00:00+08:00",
  "last_status": "ok",
  "last_error": null,
  "last_delivery_error": null,
  "deliver": "origin",
  "origin": {},
  "model": null,
  "provider": null,
  "provider_snapshot": "openrouter",
  "model_snapshot": "some-model",
  "script": null,
  "no_agent": false,
  "context_from": null,
  "enabled_toolsets": null,
  "workdir": null,
  "created_at": "2026-07-01T12:00:00+08:00"
}
```

此外还有运行期去重字段：

- `run_claim`：保护长时间运行的 one-shot，避免另一个进程在任务尚未结束时再次执行。
- `fire_claim`：外部调度回调的 compare-and-set claim，用于多实例至多一次触发。
- `attach_to_session`：控制投递内容是否写入可继续对话的会话。

### 4.3 写入安全

`jobs.json` 的更新不是直接覆盖：

1. 获取进程内 `RLock`。
2. 获取跨进程 `.jobs.lock` advisory lock。
3. 在同目录创建临时文件。
4. 写入 JSON、`flush`、`fsync`。
5. 原子替换正式文件。
6. 将目录权限设为 `0700`，文件权限设为 `0600`。

锁等待有 30 秒上限，避免另一个异常进程长期持锁导致 ticker 心跳一起冻结。

读取时还会修复部分历史或人工编辑造成的问题，例如：

- 顶层误写成 JSON 数组；
- 字符串中存在未转义控制字符；
- 缺失 `id`；
- `schedule` 类型错误；
- `next_run_at`、`last_run_at` 不是合法 ISO 时间。

单个坏任务也被异常隔离，不应阻塞其他健康任务的 due 扫描。

## 5. 时间与调度语义

### 5.1 支持的 schedule

| 输入 | 解析结果 |
| --- | --- |
| `30m`、`2h`、`1d` | 从现在开始延迟一次 |
| `every 30m`、`every 2h` | 固定间隔循环 |
| `0 9 * * *` | cron 表达式，由 `croniter` 计算 |
| `2026-07-15T18:00:00+08:00` | 指定时间 one-shot |

one-shot 默认 `repeat.times = 1`；interval 和 cron 默认无限重复。

### 5.2 时区处理

Hermes 使用统一的 `hermes_time.now()` 获取配置时区的当前时间。没有时区的 ISO 时间在创建时按 Hermes 配置时区解释，而不是简单按服务器本地时区解释。

它还处理了配置时区变化后的迁移问题：当旧 `next_run_at` 的 UTC offset 与当前时区不一致，且直接执行会造成明显提前触发时，会重新按 cron 表达式计算下一次本地墙上时间。

代价是 DST 边界和“用户真的迁移了时区”无法完美区分；代码明确选择避免重复/提前触发，即使极少数情况下会跳过一次 DST 附近的待执行任务。

### 5.3 错过执行与 catch-up

循环任务的迟到宽限期为“周期的一半”，并限制在 120 秒到 2 小时之间：

```text
grace = clamp(period / 2, 120s, 2h)
```

当 Gateway 停机或前一次任务运行太久，导致某个循环任务已经错过多个周期时，Hermes 不会逐条补跑积压：

1. 将 `next_run_at` 快进到未来的一个时间点；
2. 当前恢复时仍执行一次；
3. 折叠所有累计 missed slots，防止重启后 burst fire。

one-shot 只有 120 秒恢复宽限。超过宽限才创建/恢复的 one-shot 会被拒绝，避免长期保留一个永远不会执行的幽灵任务。

## 6. 一次 builtin tick 的完整流程

```text
InProcessCronScheduler.start()
  │
  ├─ 写 ticker heartbeat
  └─ 循环：
       ├─ scheduler.tick(sync=False)
       │    ├─ 非阻塞获取 .tick.lock
       │    ├─ get_due_jobs()
       │    │    ├─ 修复异常记录
       │    │    ├─ 检查 enabled / next_run_at
       │    │    ├─ 折叠 missed runs
       │    │    └─ 为 one-shot 写 run_claim
       │    ├─ 对 recurring job 预先 advance_next_run()
       │    ├─ workdir job → 单线程池
       │    ├─ 其他 job → 并行线程池
       │    └─ 立即返回，不阻塞 ticker
       ├─ 更新 heartbeat / last_success
       └─ 等待 60 秒
```

`.tick.lock` 防止手工 `hermes cron tick`、Gateway ticker 或其他进程同时执行一轮扫描。任务真正运行在持久线程池中，tick 锁不会覆盖整个 Agent 执行时长。

同一进程还维护 `_running_job_ids`，如果一个循环任务的前一次运行仍未结束，下一次 tick 不会把同一任务再次提交。

## 7. 一次任务执行的完整流程

`run_one_job()` 是 builtin 和 Chronos 共用的执行入口：

1. **claim dispatch**：有限 one-shot 先持久化占用一次执行次数。
2. **建立 profile secret scope**：确保任务只能看到所属 profile 的凭证。
3. **执行 `run_job()`**：运行脚本或创建新的 `AIAgent`。
4. **保存完整审计输出**：成功、失败、静默结果都会写入本地 Markdown。
5. **计算是否投递**：成功响应可用 `[SILENT]` 抑制；失败始终尝试通知。
6. **执行平台投递**：记录独立的 `delivery_error`，不与 Agent 执行错误混为一谈。
7. **释放 Agent、HTTP client、MCP 子进程等资源**。
8. **`mark_job_run()`**：写入最后运行时间、状态、错误、重复次数和下一运行时间。

### 7.1 Agent 模式

每次创建临时 `AIAgent`，主要特性是：

- `platform="cron"`；
- fresh session，不继承普通聊天历史；
- `skip_memory=True`，避免无人值守任务污染用户画像和普通会话记忆；
- 加载 `SOUL.md`；
- 只有设置 `workdir` 时才加载项目 `AGENTS.md`、`CLAUDE.md`、`.cursorrules`；
- 工具集优先使用任务级 `enabled_toolsets`，否则使用 `hermes tools` 的 cron platform 配置；
- 强制禁用 `cronjob`、`messaging`、`clarify`，并叠加全局 denylist；
- 支持 provider fallback 和 credential pool rotation；
- 模型运行使用“无活动超时”，默认连续 600 秒没有 API、流式 token 或工具活动才中断，而不是限制总墙钟时长。

### 7.2 前置脚本模式

任务可配置 `script`。脚本必须位于 `HERMES_HOME/scripts/` 内，绝对路径、`..` 和符号链接逃逸都会被拒绝。

- `.sh` / `.bash` 使用 Bash；其他扩展名使用当前 Python。
- 默认超时为 3600 秒，可由环境变量或配置覆盖。
- 子进程环境会移除 Hermes 管理的敏感凭证。
- stdout/stderr 在进入日志、提示词或通知前做敏感信息脱敏。
- 成功 stdout 注入 Agent prompt，作为采集数据。
- stdout 最后一行若为 `{"wakeAgent": false}`，该轮完全跳过 LLM。
- 空 stdout 也会跳过 Agent，适合“无变化时零成本”。

### 7.3 no-agent 模式

`no_agent=true` 时脚本就是任务本体：

- stdout 直接成为投递内容；
- 空 stdout 表示静默成功；
- 非零退出或超时会发送错误通知；
- 不创建模型客户端，不消耗 token；
- 设计意图是让 `workdir` 成为脚本工作目录，但 v0.18.2 的实际实现存在偏差，见下文局限分析。

这是 Hermes 在成本和确定性方面很实用的设计：固定阈值告警、健康检查、CI 通知不需要经过 LLM。

### 7.4 技能和任务链

任务可挂一个或多个 skill。执行时按顺序读取 skill 内容并组装 prompt，同时记录 skill 使用次数。

`context_from` 可以读取其他 cron job 最近一次 Markdown 输出并注入当前 prompt，用于：

```text
采集 → 筛选 → 格式化 → 投递
```

它只读取“最近完成的输出”，不会等待同一个 tick 中仍在运行的上游任务，因此本质是松耦合数据依赖，不是严格 DAG 调度器。单个上游输出最多注入 8000 字符。

## 8. 并发、幂等与故障语义

### 8.1 Hermes 选择的语义

| 场景 | 实现 | 结果语义 |
| --- | --- | --- |
| recurring 执行前 | 先推进 `next_run_at` | 崩溃时可能漏一次，避免重启重复执行 |
| 有限 one-shot 执行前 | 先增加 `repeat.completed` | 副作用至多执行指定次数，崩溃可能造成未完成但不重试 |
| one-shot 长时间运行 | `run_claim` + 周期 heartbeat | 其他进程不重复提交；claim 失联后可清理 |
| 外部 provider 回调 | `fire_claim` CAS | 多实例/重复 webhook 中只有一个实例获胜 |
| 同一进程相同 job | `_running_job_ids` | 前次未结束时不重叠提交 |
| 多个 missed slots | 折叠为一次恢复执行 | 不产生补跑风暴 |

因此 Hermes 不是事务型 exactly-once 系统。它采用的是“调度层尽量 at-most-once + 任务自身承担副作用幂等”的策略。

对于发送消息、生成报告这类任务，这是合理选择；对于扣款、删除、创建唯一外部资源等操作，仍必须在业务工具层使用幂等 key、去重表或查询后写入。

### 8.2 并行执行

普通任务可通过持久 `ThreadPoolExecutor` 并行，最大并行度的优先级为：

```text
HERMES_CRON_MAX_PARALLEL
  > config.yaml cron.max_parallel_jobs
  > 不限制线程数
```

带 `workdir` 的任务进入单线程池。更重要的是，它们会修改进程级 `TERMINAL_CWD`，所以还通过 writer-preferring 读写锁排斥所有其他 cron 任务，防止别的任务误用当前工作目录。

这也暴露了 Hermes 当前实现的一个技术债：工作目录依赖进程全局环境变量，迫使调度器增加复杂的全局串行化。更理想的执行器应把 cwd 显式放在每次工具调用/运行上下文中。

### 8.3 Gateway 退出

Gateway 退出时不会简单同步 `join()` cron 线程，因为 cron 投递可能正把 coroutine 调度回 Gateway event loop；阻塞 event loop 会造成互相等待和消息丢失。

Hermes 使用异步轮询等待 cron 线程退出，保留 event loop 继续处理投递。若超时强制结束工具子进程，会把仍在运行的 cron job 标记为 interrupted，防止截断后的输出被错误地提交为成功。

## 9. 输出与投递

### 9.1 本地输出

每轮都会生成带任务名、时间、schedule、prompt、response/error 的 Markdown。默认每个任务保留最近 50 份，`cron.output_retention <= 0` 可关闭裁剪。

本地输出同时承担：

- 审计日志；
- 故障排查；
- `[SILENT]` 任务的唯一结果记录；
- `context_from` 的任务链数据源。

### 9.2 投递目标

`deliver` 支持：

- `local`：只落盘；
- `origin`：创建任务的会话；
- 平台 home channel，例如 `telegram`、`feishu`；
- 明确目标，例如 `telegram:<chat_id>:<thread_id>`；
- `all`：运行时展开为所有已连接 home channel；
- 逗号分隔的多目标组合。

目标在执行时解析，而不是创建时完全固化，因此后续接入的新平台可以被 `all` 自动包含。

Agent 的 final response 由调度器自动投递，任务 prompt 不应再调用 `send_message`。调度器还会拦截对自动投递目标的重复发送。

默认 cron 投递不会写入普通聊天历史，避免破坏消息角色交替和污染对话。可通过 `mirror_delivery` / `attach_to_session` 显式开启“可回复继续”的 cron delivery；支持 thread 的平台优先创建独立 thread，DM-only 平台才写入原 DM session。

## 10. 安全设计

Hermes 对 cron 采用比交互会话更严格的安全策略，因为 cron 无人值守且不会逐次询问用户：

1. **prompt 创建/更新扫描**：拦截明显的注入、凭证外传、不可见 Unicode 等模式。
2. **运行时 assembled prompt 再扫描**：覆盖动态加载 skill、脚本输出和上游任务输出的路径。
3. **脚本目录沙箱**：脚本只能位于 `HERMES_HOME/scripts`。
4. **脚本子进程凭证清理和输出脱敏**。
5. **provider/base_url 防外传**：禁止把某 provider 的存储凭证发送到不可信自定义 endpoint，并在真正调用前再次校验。
6. **模型/provider 漂移保护**：未显式 pin 的任务在创建时记录当前模型和 provider；全局默认发生变化时 fail closed，避免无人值守任务悄悄切换到付费模型。
7. **工具集限制**：禁用递归创建 cron、交互式澄清和普通消息工具。
8. **profile 隔离**：任务存储、配置、技能和 secret scope 全部跟随 profile。
9. **Gateway 生命周期命令保护**：任务不能调度 Gateway restart/stop 一类可能形成自重启循环的命令。
10. **外部回调认证**：Chronos fire webhook 使用短时、限定用途和 audience 的 NAS JWT，并通过 JWKS 验签。

需要注意，基于正则/模式的 prompt 扫描只能作为纵深防御，不能替代工具层权限、路径沙箱和副作用审批策略。

## 11. 外部调度 provider：Chronos

最新 Hermes 将“什么时候触发”抽象为实验性的 `CronScheduler`：

```python
class CronScheduler:
    name
    is_available()
    start(stop_event, adapters=None, loop=None, interval=60)
    stop()
    on_jobs_changed()
    fire_due(job_id, adapters=None, loop=None)
    reconcile()
```

provider 缺失、加载失败或不可用时，会回退到 builtin ticker，保证任务至少还有本地触发路径。

Chronos 面向托管环境的 scale-to-zero：

```text
任务创建/修改
  → Agent 计算 next_run_at
  → 向 NAS provision 一个精确时间的 one-shot
  → 实例可缩容到 0
  → 到点后 NAS 使用短时 JWT 调用 POST /api/cron/fire
  → Gateway 验证 JWT
  → fire_claim CAS
  → 共用 run_one_job() 执行
  → 成功后 provision 下一次 one-shot
```

重要设计点：

- Agent 不保存外部调度器账号或凭证，只和 NAS 通信。
- 每次只在 NAS 中 arm 下一次 one-shot，而不是把 cron 表达式交给两个系统分别计算，避免时间语义漂移。
- `dedup_key = job_id:fire_at`，重复 provision 幂等。
- `reconcile()` 把 NAS 已 arm 状态收敛到 `jobs.json` 的 desired state，补 arm、改时间、取消 orphan。
- Chronos 不运行 60 秒 loop；`start()` reconcile 后立即返回。

这套 provider 抽象对 Aiden 当前板端常驻场景不是 MVP 必需，但“触发层和执行层分离”值得从第一版就保留接口边界。

## 12. 对 Hermes 实现的评价

### 12.1 优点

- 管理、触发、执行边界清晰，最新 provider 抽象方向正确。
- 对长任务、重启、重复触发、时区、输出、投递错误的处理非常完整。
- Agent 模式、前置脚本门控和 no-agent 模式覆盖了从开放推理到确定性监控的不同成本区间。
- fresh session、skip memory、工具 denylist 符合无人值守 Agent 的安全要求。
- 状态和输出可审计，CLI 能区分“Gateway 活着”和“ticker 实际健康”。
- 测试覆盖广，很多边界行为都有明确的历史 issue 和回归测试。

### 12.2 局限与技术债

- `jobs.json` 适合单机小规模任务，不适合大量任务、高写入频率或复杂查询；锁粒度是整个任务库。
- JSON repair 逻辑、双层锁、多个 claim 和历史兼容字段使 `jobs.py` 已非常庞大。
- 默认并行度不限制，若同时到期任务多，可能瞬间创建较多线程、模型连接和工具进程。
- `TERMINAL_CWD` 是进程全局状态，导致 workdir 任务需要排斥其他所有 cron 任务。
- no-agent 的 `workdir` 当前实际无效：`run_job()` 虽先 `chdir(workdir)`，但 `_run_job_script()` 又固定给 `subprocess.run()` 传入 `cwd=script.parent`，最终脚本仍在脚本目录执行。本次最小样例已复现；这与 `create_job()` 注释和用户文档描述不一致，属于值得向上游修复的实现偏差。
- `context_from` 不是严格 DAG：不等待上游、没有数据版本/运行实例关联，容易读到旧输出。
- at-most-once 会以“可能漏执行”为代价；业务侧必须知道该取舍。
- builtin provider 强依赖 Gateway 常驻。如果 Gateway 没启动，任务只会静默变成 overdue，除非用户查看 CLI 提示或状态。
- prompt 安全扫描存在误报/漏报的天然限制，真正安全仍依赖工具最小权限。
- 上游开发文档有少量滞后，例如简化的状态机描述没有完全覆盖当前 claim、异步线程池和 external provider 逻辑；关键结论应以源码和测试为准。

## 13. Aiden 当前基础与差距

Aiden 当前 Go Agent 已具备定时任务所需的大部分执行基础：

- `src/agent/cmd/daemon/main.go` 启动长期运行的 Agent daemon 和 HTTP Server；
- `src/agent/internal/agent/runtime.go` 提供统一的 `Runtime.Run(ctx, RunRequest)`；
- Runtime 已有任务 episode、事件流、取消、日志、技能和工具调用能力；
- `ConfigDir` 已作为 `/userdata/agent` 一类持久化根目录；
- Server 已有异步 `/api/chat`、request ID、active run cancel 和结果查询；
- Phone Bridge 可作为部分结果/动作的手机侧通道。

但当前没有通用 cron job 模型、持久化任务库、due scanner、调度管理 API 或 Agent-facing cron tool。

另一个关键差异是：Aiden `Runtime.Run()` 通过 `runGate` 将同一个 Runtime 的运行全局串行化。即使调度器并行提交多个任务，最终也会在 `Runtime.Run()` 入口排队。因此 Aiden 第一版直接做多 worker 没有收益，反而会增加复杂度；应先明确调度任务与前台聊天的优先级、抢占和排队策略。

同时，Aiden 当前普通运行会进入共享 session/memory/episode 体系，而 Hermes cron 明确 `skip_memory=True` 并创建独立 session。若 Aiden 直接调用现有 `Runtime.Run()`，定时报告可能污染普通聊天记忆、用户画像和当前 session。这是实现前必须先解决的隔离问题。

## 14. Aiden 建议方案

### 14.1 MVP 范围

建议第一期只做：

- builtin 单机调度；
- one-shot、interval、5-field cron；
- `create/list/update/pause/resume/run/remove`；
- JSON 或 SQLite 持久化；
- `max_parallel = 1`；
- 独立 cron run context，不写普通会话记忆；
- 本地输出审计；
- `local` 和 Web UI/history 投递；
- 可选 Phone Bridge notification 投递；
- Agent prompt 模式和 no-agent 脚本模式；
- 执行前 claim、missed slots 折叠、开机恢复；
- HTTP 管理 API 和一个模型可调用的 `cronjob` 工具。

第一期暂不做：

- 外部 Chronos provider；
- 多机一致性；
- 多平台复杂 thread 投递；
- 严格 DAG；
- 多任务并行；
- LLM prompt 注入模式扫描的复杂规则集。

### 14.2 推荐模块边界

```text
src/agent/internal/agent/cron/
├── model.go        # Job、Schedule、RunRecord、状态
├── store.go        # 持久化接口
├── file_store.go   # MVP JSON 原子存储，或 sqlite_store.go
├── schedule.go     # parse、next run、grace、timezone
├── scheduler.go    # ticker、due、claim、missed-run policy
├── executor.go     # 调 Runtime / 脚本，保存输出
└── delivery.go     # local、Web UI、Phone Bridge

src/agent/internal/agent/tools_cron.go
src/agent/internal/agent/server_cron.go
```

建议保留三个接口：

```go
type CronStore interface {
    List(ctx context.Context) ([]CronJob, error)
    Mutate(ctx context.Context, fn func([]CronJob) ([]CronJob, error)) error
    SaveRun(ctx context.Context, run CronRunRecord) error
}

type CronTrigger interface {
    Start(ctx context.Context, fire func(context.Context, string) error) error
}

type CronExecutor interface {
    Execute(ctx context.Context, job CronJob) CronRunResult
}
```

这样未来增加外部 provider 时只替换 `CronTrigger`，不会复制 Agent 执行和投递代码。

### 14.3 运行隔离

建议为 `RunRequest` 增加明确的后台运行选项，而不是在 scheduler 内临时绕过 Runtime：

```go
type RunRequest struct {
    // existing fields...
    Source          string // "chat" | "voice" | "cron"
    SessionID       string
    SkipUserMemory  bool
    DisableTools    []string
    WorkingDir      string
}
```

cron 执行至少应满足：

- 使用 `cron:<job_id>:<run_id>` 独立 session；
- 不把输出追加到普通 active session；
- 不更新用户 profile/long-term memory；
- 禁用创建/修改 cron 自身的工具，避免递归调度；
- 禁用需要人机同步交互的工具；
- 允许 episode/telemetry 记录，但标记 `source=cron`；
- 根据任务设置独立超时和取消原因。

### 14.4 前台任务与 cron 的资源策略

由于当前 Runtime 全局串行，建议定义明确策略：

1. 到期 cron 进入队列，不抢占正在进行的 chat/voice run。
2. 前台新请求优先于尚未开始的 cron。
3. cron 等待超过一个 grace window 后仍只执行一次，不累计补跑。
4. 第一版不允许两个 cron 同时运行。
5. 后续若要并行，应先拆分共享 context/session 状态，或为 cron 创建隔离 Runtime 实例，而不是只增加 goroutine。

### 14.5 持久化选择

如果预计设备上任务数少于几十个，JSON 足够，并且部署/调试直观；但必须至少实现：

- 同目录临时文件；
- `fsync` 后 rename；
- 单进程 mutex；
- schema version；
- 单个坏任务隔离；
- 启动时备份损坏文件而不是直接覆盖。

如果希望较早支持运行历史查询、Web UI 分页、唯一 claim 和未来多进程，SQLite 更适合 Aiden。建议表结构至少分开：

```text
cron_jobs
cron_runs
cron_outputs / output_path
```

即使任务定义使用 JSON，也不要把无限运行历史继续塞回同一个 `jobs.json`。

### 14.6 建议默认语义

| 决策 | 建议 |
| --- | --- |
| ticker | 30 或 60 秒；开机立即 tick 一次 |
| one-shot grace | 2 分钟 |
| recurring catch-up | 恢复时最多执行一次，折叠 backlog |
| 默认并发 | 1 |
| recurring claim | 执行前推进下一次，at-most-once |
| one-shot claim | 执行前持久化 running/claim；是否崩溃重试做成配置 |
| Agent 超时 | 活动超时优于纯墙钟超时 |
| 输出留存 | 每任务最近 20～50 次，受总磁盘上限约束 |
| 静默 | 支持结构化 `silent=true`，不要只依赖模型文本 marker |
| 脚本 | 只允许运行配置目录下受控脚本，清理敏感环境 |
| 模型 | 建议任务创建时显式 pin provider/model，避免后台成本漂移 |

Hermes 使用 `[SILENT]` 文本 marker 是为了兼容模型输出；Aiden 若从零设计，可在内部执行结果增加结构化 `DeliveryMode`，同时保留 `[SILENT]` 作为模型兼容层。

## 15. 推荐实施顺序

### Phase 0：语义定稿

- 明确前台与 cron 的优先级；
- 明确 one-shot 崩溃后是重试还是 at-most-once；
- 明确 cron 是否允许操作手机 UI/HID；
- 明确结果进入 Web UI history、独立 cron history，还是 Phone Bridge notification。

### Phase 1：确定性调度内核

- Job/Schedule/Store；
- ticker、due、claim、pause/resume/manual run；
- no-agent 脚本和本地输出；
- 单元测试覆盖时间、重启、损坏数据和重复触发。

### Phase 2：Agent 执行

- cron 专用 RunRequest/session；
- memory 隔离；
- 工具 allowlist/denylist；
- activity timeout；
- skill 加载和结构化 silent。

### Phase 3：管理与投递

- HTTP API；
- Web UI 管理页；
- Agent-facing `cronjob` 工具；
- Web UI/Phone Bridge 通知；
- 状态、最近运行和错误可观察性。

### Phase 4：高级能力

- `context_from` 或显式 DAG；
- 多 worker 与资源配额；
- 外部 trigger provider；
- 远程唤醒/scale-to-zero；
- 多设备或云端任务同步。

## 16. 验收测试清单

至少覆盖：

1. one-shot、interval、cron、带/不带时区的 ISO 时间。
2. 创建过去时间、刚过期但仍在 grace 内、超过 grace。
3. daemon 停机多个周期后只恢复执行一次。
4. 执行前崩溃、执行中崩溃、状态提交前崩溃。
5. 同一任务两个 tick 同时触发只能执行一次。
6. 手工 run 与自动 tick 竞争。
7. 前台 chat 占用 Runtime 时 cron 的排队和取消。
8. cron 不写普通 session memory/profile。
9. cron 不能递归创建 cron。
10. 脚本路径穿越、符号链接逃逸、超时、非零退出、敏感输出。
11. 空输出、silent、Agent 失败、投递失败分别记录正确状态。
12. jobs store 损坏时健康任务仍可运行或系统能安全降级。
13. 时区变更和夏令时边界。
14. 输出留存和磁盘上限。
15. daemon 优雅退出时正在运行的 cron 被标记 interrupted。

## 17. 最终建议

Hermes 最值得 Aiden 借鉴的不是具体 Python 文件结构，而是以下六条原则：

1. **触发与执行解耦**，所有 trigger 共用一个执行入口。
2. **执行前持久化 claim/下一次时间**，先决定故障语义，再写代码。
3. **后台 Agent 与普通会话/记忆隔离**。
4. **LLM 只用于需要推理的任务**，确定性监控优先 no-agent 脚本。
5. **所有运行都有本地审计输出，执行错误与投递错误分开记录**。
6. **先做单机串行 MVP，再按真实需求增加并行、多机和外部调度**。

结合 Aiden 当前 `Runtime.Run()` 全局串行和共享 memory/session 的实现，第一优先级不是写 ticker，而是定义 cron 专用运行上下文和资源仲裁。隔离做好后，调度内核本身反而是相对简单的一部分。
