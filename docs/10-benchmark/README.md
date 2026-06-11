# Agent Benchmark

Agent benchmark 是一个用于评测 Go agent 在手机控制任务上的能力测试框架。

## 快速开始

### 运行 benchmark

```bash
cd benchmark
uv run python -m runner run suites/memory_v1.json --agent http://192.168.1.100:8080
```

### 查看报告

运行完成后会自动：

- 在浏览器打开本地 HTML 报告
- 上传报告到板子的 `/userdata/agent/benchmark/report.html`
- 可通过 `http://<board-ip>:8080/benchmark` 访问

报告包含：

- 任务列表（可点击查看详情）
- 每个任务的 prompt、tool calls、agent response、rubric 评分
- 通过率、失败数、跳过数统计

### 目录结构

```text
benchmark/
├── runner/              # Python 包
│   ├── main.py          # CLI 入口
│   ├── suite.py         # Suite 加载
│   ├── runtask.py       # 任务执行
│   ├── judge.py         # LLM judge
│   └── html_report.py   # HTML 报告生成
├── suites/              # 任务集
│   ├── memory_v1.json   # 记忆测试（17 个任务）
│   ├── personamem_lt_recall_v1.json # PersonaMem-derived LT memory recall tests
│   ├── full_smoke.json  # 完整冒烟测试
│   └── phone_control_v1.json  # 手机控制测试
└── runs/<run_id>/       # 运行结果
    ├── manifest.json    # 运行元信息
    ├── results.jsonl    # 每任务一行结果
    └── tasks/<task_id>/ # 每个任务的详细数据
        ├── history.json # 对话历史
        ├── trace.json   # Tool call trace
        └── steps/       # 逐步截图
```

## CLI 命令

### run - 运行 benchmark

```bash
uv run python -m runner run <suite.json> [options]
```

选项：

- `--agent URL` - Agent HTTP 地址（默认 `http://localhost:8080`）
- `--no-judge` - 跳过 LLM judge，只跑硬断言
- `--repeats N` - 每个任务重复 N 次
- `--filter PATTERN` - 只跑匹配的任务 ID

### rejudge - 重新评判

```bash
uv run python -m runner rejudge runs/<run_id>
```

重新对已有 run 的所有任务调用 judge，不重新执行任务。

### compare - 对比两次运行

```bash
uv run python -m runner compare runs/<run_a> runs/<run_b>
```

输出两次运行之间哪些任务状态翻转、延迟差异等。

## 环境变量

- `OPENROUTER_API_KEY` - OpenRouter API key（judge 需要）
- `ANTHROPIC_API_KEY` - Anthropic API key（可选，judge 备用）

## 相关文档

- [架构设计](./architecture.md) - Benchmark 设计原理和判分机制
- [详细指南](./quickstart.md) - 完整使用说明

## 双模式执行

Benchmark 支持两种执行模式：

- **Aiden Native** — 通过 `benchmark/runner/main.py` 在本地 agent 上跑，串行。
- **MobileGym** — 通过 `benchmark/mobilegym/scripts/run_aiden.py` 在 Docker 模拟器上跑，
  支持并发（`PARALLEL=N ./parallel_run.sh`）。

同一份 `benchmark/suites/*.json` 在两种模式下都可执行。MobileGym 内置 suite
（clock、alipay、wechat 等）只在 MobileGym 模式可用，从 `benchmark/mobilegym/suites/all_tasks.txt`
聚合发现。

Web UI `/benchmark` 上的「Aiden Native / MobileGym」单选切换两种模式，MobileGym 模式
下额外提供并发数与任务数限制输入框。
