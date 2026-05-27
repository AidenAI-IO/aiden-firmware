# Benchmark 架构设计

## 设计原理

Agent benchmark 采用 **HTTP API 驱动 + 离线判分** 的架构，将任务执行和评判分离。

### 核心组件

```
┌─────────────┐
│   Runner    │  Python CLI，本机执行
│  (Python)   │
└──────┬──────┘
       │ HTTP
       ▼
┌─────────────┐
│ Go Agent    │  常驻 daemon，监听 :8080
│  (Daemon)   │
└──────┬──────┘
       │ UDS/HID
       ▼
┌─────────────┐
│   Phone     │  被测设备
└─────────────┘
```

### 执行流程

每个任务的执行流程：

1. **清空历史** - `POST /api/clear` 隔离任务
2. **全局 reset** - 通过 `/api/tools/invoke` 直接调 HID 工具回主屏
3. **Per-task setup** (可选) - 构造特定起点状态
4. **Pre 截图** - `POST /api/tools/invoke screenshot`
5. **执行任务** - `POST /api/chat` 发送 prompt
6. **硬断言** - 检查 tool call 数、超时、response 存在
7. **LLM judge** (可选) - 离线评判，可重跑

### 判分机制

采用 **硬断言 + LLM judge** 两段式：

#### 硬断言 (Hard Assertions)

用于快速失败，避免浪费 judge 成本：

- `response_required: true` - Agent 必须有回复
- `min_tool_calls: N` - 至少调用 N 个 tool
- `max_tool_calls: N` - 最多调用 N 个 tool
- `must_complete_within_sec: N` - 必须在 N 秒内完成

硬断言失败 → 直接标记 `failed`，不调 judge。

#### LLM Judge

吃 artifact（截图 + trace），不碰硬件，可离线重跑：

**输入：**

- Task description
- Rubric（2-4 条独立验收点）
- Pre/post 截图
- Tool call trace
- Agent 最终回复

**输出：**

- 每条 rubric 的 yes/no + 理由
- Overall notes

**判定规则：**

- 所有 rubric 都是 yes → `passed`
- 任何一条 no → `failed`

**缓存机制：**

Judge 结果按 `(pre, post, trace, description, final_response, model)` 缓存，避免重复调用 API。

### 状态隔离

每个任务独立执行，互不干扰：

1. **对话历史隔离** - 每任务前 `POST /api/clear`
2. **手机状态隔离** - 全局 reset 回主屏
3. **Per-task setup** - 可选的任务特定起点构造

### Trace 结构化

`ChatResponse.History` 直接返回结构化消息列表：

```json
[
  { "type": "user", "content": "打开设置" },
  { "type": "tool_call", "tool": "screenshot", "input": {} },
  {
    "type": "tool_result",
    "tool": "screenshot",
    "output": "data:image/jpeg;base64,..."
  },
  {
    "type": "tool_call",
    "tool": "mouse_click",
    "input": { "x": 540, "y": 960 }
  },
  { "type": "tool_result", "tool": "mouse_click", "output": "clicked" },
  { "type": "assistant", "content": "已打开设置" }
]
```

每个输入 tool 自动附带 post-action screenshot。

### 跨 run 对比

`compare` 命令对比两次运行：

- 哪些任务状态翻转（pass → fail 或 fail → pass）
- 延迟差异（wall_ms 变化超过阈值）
- 通过率变化

用于回归检测和性能监控。

## 目录结构

```
benchmark/
├── runner/              # Python 包
│   ├── main.py          # CLI 入口
│   ├── suite.py         # Suite 加载和校验
│   ├── runtask.py       # 任务执行核心
│   ├── agent_client.py  # Go agent HTTP 客户端
│   ├── capture.py       # 截图获取
│   ├── trace.py         # Trace 提取
│   ├── assertions.py    # 硬断言
│   ├── judge.py         # LLM judge
│   ├── metrics.py       # 指标聚合
│   ├── report.py        # JSONL 报告
│   ├── html_report.py   # HTML 报告
│   ├── rejudge.py       # 重新评判
│   ├── compare.py       # 跨 run 对比
│   └── reset.py         # 全局 reset + setup
├── suites/              # 任务集
│   ├── memory_v1.json
│   ├── full_smoke.json
│   └── phone_control_v1.json
└── runs/<run_id>/       # 运行结果
    ├── manifest.json    # 运行元信息
    ├── results.jsonl    # 每任务一行
    └── tasks/<task_id>/
        ├── history.json # 原始对话历史
        ├── trace.json   # 结构化 trace
        ├── steps/       # 逐步截图
        └── judge.json   # Judge 输出
```

## 设计决策

### 为什么用 HTTP API？

- Agent 已经是常驻 daemon，不需要 spawn 子进程
- HTTP 接口稳定，不依赖 stdout 解析
- 可以远程执行（runner 在本机，agent 在板子上）

### 为什么分离执行和判分？

- **可重跑** - judge 失败或调整 rubric 后可以 rejudge，不需要重新执行任务
- **可缓存** - 相同输入的 judge 结果缓存，节省 API 成本
- **可离线** - judge 不需要硬件在环，可以在 CI 或本地跑

### 为什么用 LLM judge？

- **灵活** - 可以评判复杂的视觉和语义目标
- **可解释** - 每条 rubric 都有 yes/no + 理由
- **可调整** - 修改 rubric 后 rejudge 即可，不需要改代码

### 为什么不用 docker？

- 硬件在环（HID 设备、frame_service socket）都在测试机本机
- Docker 化只增加复杂度，不增加隔离
- Python 依赖用 uv 锁版本即可

## 相关文档

- [快速开始](./README.md)
- [编写任务](./writing-tasks.md)
- [Judge 机制](./judging.md)
- [故障排查](./troubleshooting.md)
