# Aiden 与 MobileGym Benchmark Suite 兼容性设计

## 背景

当前项目存在两套独立的benchmark系统：

1. **Aiden原生benchmark** - `benchmark/runner/main.py`，运行 `benchmark/suites/*.json` 格式的suite，本地串行执行
2. **MobileGym集成** - `benchmark/mobilegym/scripts/run_aiden.py`，调用MobileGym registry，Docker环境支持并发

两套系统的数据格式和执行环境互不兼容：

- Aiden JSON suites 无法在 MobileGym 上跑
- MobileGym 内置 suites 无法通过 Aiden runner 跑
- Web UI (`/benchmark`) 只能触发 Aiden 原生 benchmark

## 目标

1. 让 Aiden 的 JSON suites 能在 MobileGym 环境下运行（利用其并发能力）
2. Web UI 能够触发 MobileGym benchmark 测试
3. 同一 Web UI 同时支持 Aiden 原生和 MobileGym 两种执行模式
4. MobileGym 内置 suites（clock, alipay, wechat 等）也能从 Web UI 选择运行
5. MobileGym 模式支持并发配置

## 非目标

- 不在 Aiden 原生 runner 中执行 MobileGym 内置 suites（这些任务依赖手机模拟器环境）
- 不修改 Aiden suite JSON 格式
- 不修改 MobileGym 上游代码

## 设计

### 1. Suite 类型分类

| 类型                | 来源                      | 数据格式       | 可执行模式             |
| ------------------- | ------------------------- | -------------- | ---------------------- |
| `aiden`             | `benchmark/suites/*.json` | Aiden JSON     | Aiden 原生 + MobileGym |
| `mobilegym_builtin` | MobileGym registry        | task_id 字符串 | 仅 MobileGym           |

### 2. 整体架构

```
┌─────────────────────────────────────────────────────────────┐
│                    Web UI (/benchmark)                      │
│  - 模式切换: Aiden 原生 / MobileGym                         │
│  - 列出对应模式下可用的 suites                              │
│  - MobileGym 模式：并发数、限制任务数                       │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│           Go Server (benchmark_runner.go)                   │
│  根据 mode + suite_type 分发：                              │
│    - mode=aiden                  → launchAidenRunner        │
│    - mode=mobilegym + aiden      → launchMobileGymRunner    │
│    - mode=mobilegym + builtin    → launchMobileGymRunner    │
└─────────────────────────────────────────────────────────────┘
        ↓                                    ↓
┌──────────────────────┐        ┌──────────────────────────────┐
│  Aiden 原生          │        │  MobileGym Docker            │
│  runner/main.py      │        │  parallel_run.sh             │
│  本地 agent 串行     │        │  Docker Compose 并发         │
└──────────────────────┘        └──────────────────────────────┘
                                           ↓
                               ┌──────────────────────────┐
                               │  Aiden Suite 适配层      │
                               │  (run_aiden.py)          │
                               │  JSON → MobileGym Task   │
                               └──────────────────────────┘
```

### 3. 各组件设计

#### 3.1 Suite 发现 API

**GET `/benchmark/suites?mode=aiden|mobilegym`**

返回结构：

```json
{
  "items": [
    {
      "name": "memory_v1",
      "path": "/path/to/benchmark/suites/memory_v1.json",
      "type": "aiden",
      "task_count": 15,
      "description": "Agent memory benchmark...",
      "concurrent": true
    },
    {
      "name": "clock",
      "type": "mobilegym_builtin",
      "task_count": 18,
      "concurrent": true
    }
  ]
}
```

**扫描逻辑：**

- `mode=aiden`：仅扫描 `benchmark/suites/*.json`，返回 `type=aiden`
- `mode=mobilegym`：
  - 扫描 `benchmark/suites/*.json` → `type=aiden`（标记为可在 MobileGym 跑）
  - 解析 `benchmark/mobilegym/suites/all_tasks.txt`，按 `<suite>.<task>` 聚合 → `type=mobilegym_builtin`

**实现位置：** `src/agent/internal/agent/benchmark.go::handleBenchmarkSuites`

#### 3.2 Suite 执行 API

**POST `/benchmark/run`**

请求结构：

```json
{
  "suite": "memory_v1",
  "suite_type": "aiden" | "mobilegym_builtin",
  "mode": "aiden" | "mobilegym",
  "parallel": 4,
  "limit": 10
}
```

**校验规则：**

- `mode=aiden`：仅允许 `suite_type=aiden`，`parallel` 忽略
- `mode=mobilegym`：`parallel` 必须 ≥ 1，`limit` 可选

**执行分发：**

```go
func (s *Server) handleBenchmarkRun(w http.ResponseWriter, r *http.Request) {
    var req benchmarkRunRequest
    // ... 解析与校验 ...

    switch {
    case req.Mode == "aiden":
        return s.launchAidenRunner(req.Suite)

    case req.Mode == "mobilegym":
        return s.launchMobileGymRunner(
            req.Suite, req.SuiteType, req.Parallel, req.Limit,
        )
    }
}
```

**`launchMobileGymRunner` 实现：**

```go
func (s *Server) launchMobileGymRunner(
    suite, suiteType string, parallel, limit int,
) error {
    var suiteFlag string
    if suiteType == "aiden" {
        suiteFlag = fmt.Sprintf("--aiden-suite %s", shellQuote(suite))
    } else {
        suiteFlag = fmt.Sprintf("--suite %s", shellQuote(suite))
    }

    limitFlag := ""
    if limit > 0 {
        limitFlag = fmt.Sprintf("--limit %d", limit)
    }

    script := fmt.Sprintf(`
        cd %s/benchmark/mobilegym/docker && \
        PARALLEL=%d ./parallel_run.sh %s %s \
        > /tmp/mobilegym_run.log 2>&1 &
    `, s.benchmarkDir, parallel, suiteFlag, limitFlag)

    return exec.Command("sh", "-c", script).Start()
}
```

#### 3.3 MobileGym 适配层

**新增 `--aiden-suite` 参数到 `run_aiden.py`：**

```python
target.add_argument(
    "--aiden-suite",
    help="Load Aiden JSON suite from benchmark/suites/<name>.json. "
         "Tasks will be converted to MobileGym format on the fly.",
)
```

**Suite 加载与转换：**

```python
def _load_aiden_suite_as_mobilegym_tasks(suite_name: str) -> list[Any]:
    """加载 Aiden JSON suite，返回 MobileGym Task 列表。"""
    suite_path = BENCHMARK_ROOT / "suites" / f"{suite_name}.json"
    if not suite_path.exists():
        raise LauncherError(f"Aiden suite not found: {suite_path}")

    sys.path.insert(0, str(BENCHMARK_ROOT))
    from runner.suite import load_suite

    aiden_suite = load_suite(suite_path)
    return [_convert_task(aiden_suite, task) for task in aiden_suite.tasks]


def _convert_task(suite: Any, task: Any) -> Any:
    """将单个 Aiden TaskSpec 转换为 MobileGym Task 对象。

    需要参考 MobileGym Task 类的实际签名，至少包含：
    - task_id: 唯一标识，使用 "{suite_name}.{task_id}"
    - instruction: prompt（如果有 prompt_prefix 则拼接）
    - 元数据：保留 rubric / hard_assertions 用于评判
    """
    full_id = f"{suite.name}.{task.id}"
    instruction = task.prompt
    if suite.prompt_prefix:
        instruction = f"{suite.prompt_prefix}\n\n{task.prompt}"

    # 实际实现需根据 MobileGym Task 类调整
    return MobileGymTaskAdapter(
        task_id=full_id,
        instruction=instruction,
        metadata={
            "category": task.category,
            "rubric": [r.__dict__ for r in task.rubric],
            "hard_assertions": task.hard_assertions.__dict__,
            "setup": task.setup,
            "global_reset": suite.global_reset,
            "expected_answer": task.expected_answer,
            "answer_format": task.answer_format,
            "expected_recalled_memory_ids": task.expected_recalled_memory_ids,
        },
    )
```

**集成到 task loading 流程：**

修改 `_run_serial`：

```python
async def _run_serial(args, config, factory, SerialRunner):
    # 优先级：--aiden-suite > --task-id/--suite/--split
    if args.aiden_suite:
        tasks = _load_aiden_suite_as_mobilegym_tasks(args.aiden_suite)
    else:
        tasks = factory.load_tasks(config)

    if args.limit is not None:
        tasks = tasks[: args.limit]
    # ... 后续逻辑不变 ...
```

**校验互斥：**

```python
def _validate_selection(args):
    selectors = [args.task_id, args.suite, args.split, args.aiden_suite]
    if not any(selectors):
        raise LauncherError(
            "select tasks via --task-id, --suite, --split, or --aiden-suite"
        )
    if args.aiden_suite and (args.task_id or args.suite or args.split):
        raise LauncherError(
            "--aiden-suite is mutually exclusive with --task-id/--suite/--split"
        )
```

#### 3.4 Setup/Reset 处理

Aiden suite 中的 `setup` 和 `global_reset` 是 `tool_sequence`，针对 Aiden agent 的工具调用（如 `shell`、`wait_ms`）。

复用 Aiden runner 中已有的 reset 逻辑（`benchmark/runner/reset.py`）：

- **`global_reset`** - 每个 task 开始前在 Aiden daemon 上执行（通过 daemon HTTP API）
- **`setup`** - task 开始前在 Aiden daemon 上执行
- **MobileGym 自身的环境 reset** - 模拟器层面的 reset，由 MobileGym 处理，不冲突

适配层在 `_convert_task` 中将 `setup` / `global_reset` 保留在 task 元数据，由一个 hook（在 MobileGym `SerialRunner` 的 task-start 钩子上）调用 Aiden daemon 执行。如果 MobileGym 没有合适的 hook，则在 `aiden_go_agent.py::reset(task)` 中执行——因为 reset 在每个 task 开始前必然调用。**plan 阶段需要先验证 reset() 钩子时机是否正确，再决定具体接入点。**

#### 3.5 评判处理（rubric / hard_assertions）

Aiden suite 的评判由 Aiden runner 的 judge（LLM-as-judge，参见 `benchmark/runner/judge.py`）和 hard_assertions 检查完成。MobileGym 自带 evaluator（`bench_env.evaluator`），与 Aiden judge 不兼容。

**策略：** MobileGym 模式下跑 Aiden suite 时，禁用 MobileGym evaluator，改在 task 完成后回调 Aiden judge：

- 在 `run_aiden.py` 中检测 `args.aiden_suite`，将 `eval_mode` 设为 `none` 或自定义模式
- task 完成后由 wrapper 收集 trace，调用 Aiden 的 judge 模块产出 rubric 评分
- hard_assertions 在 wrapper 中本地校验（min/max tool calls、超时）

MobileGym 内置 suite 仍走 MobileGym 原生 evaluator，行为不变。

#### 3.6 Web UI 改动

**位置：** `src/agent/internal/agent/benchmark_html.go::benchmarkIndexHTML`

**新增元素：**

1. **执行模式切换** - radio button (Aiden 原生 / MobileGym)
2. **Suite 列表分组显示**
   - Aiden 模式：仅显示 Aiden suites
   - MobileGym 模式：分两组显示 Aiden suites 与 MobileGym 内置 suites
3. **MobileGym 配置区**（仅 MobileGym 模式可见）
   - 并发 workers 输入框（默认 4）
   - 限制任务数输入框（可选）

**JS 行为：**

- 模式切换时重新调用 `GET /benchmark/suites?mode=...`
- 提交时按当前模式构造 `POST /benchmark/run` payload

#### 3.7 并发与状态管理

**MobileGym 并发的隔离机制（已存在，复用）：**

- 每个 worker 独立的 Docker Compose project
- 独立的 simulator + Aiden daemon + 配置 + 网络
- 仅共享结果输出根目录

**state.json 状态管理：**

- 当前 Aiden runner 写入 `benchmark/state.json`，由 Go server 在启动时写 `running`，由 launcher shell 脚本结束时写 `idle`
- MobileGym 模式同样由 Go server 写入 `state.json`，调用 `parallel_run.sh` 的 wrapper 脚本结束时写 `idle`
- 沿用同一个 `state.json`，新增字段 `mode`：

```json
{
  "status": "running",
  "mode": "mobilegym",
  "suite": "memory_v1",
  "parallel": 4
}
```

进度信息由 MobileGym 自己的 run report 提供（`runs/mobilegym/<batch-id>/`），不放在 `state.json` 里。Web UI 通过轮询日志 + run 列表展示进度。

**日志路径：**

- Aiden 模式：`/tmp/benchmark_run.log`（已存在）
- MobileGym 模式：`/tmp/mobilegym_run.log`（新增）
- `handleBenchmarkLog` 根据 `mode` 返回对应日志

### 4. 兼容性策略

**向后兼容：**

- 旧的 `POST /benchmark/run` 不带 `mode` 字段时，默认 `mode=aiden`，行为不变
- `run_aiden.py` 不带 `--aiden-suite` 时，行为不变
- Aiden runner 完全不动

**前向兼容：**

- API 字段以可选方式增加
- Web UI 默认显示 Aiden 模式，与当前行为一致

### 5. 测试策略

**单元测试：**

- Go：`handleBenchmarkSuites` 在不同 mode 下的返回
- Go：`launchMobileGymRunner` 的命令构造
- Python：`_load_aiden_suite_as_mobilegym_tasks` 的转换正确性
- Python：`_convert_task` 处理各种 Aiden task 字段

**集成测试：**

- Aiden suite 在 MobileGym 模式下执行（验证 prompt、setup 都正确传递）
- MobileGym 内置 suite 通过 Web UI 触发
- 并发模式下多 worker 状态隔离

**回归验证：**

- 现有 Aiden 原生 benchmark 不受影响
- 现有 MobileGym `--task-id`/`--suite` 参数不受影响

### 6. 实施清单

**Go 端：**

- `benchmark.go::handleBenchmarkSuites` - 增加 mode 参数与 MobileGym 内置 suite 扫描
- `benchmark_runner.go::handleBenchmarkRun` - 增加 mode/suite_type/parallel/limit 参数
- `benchmark_runner.go::launchMobileGymRunner` - 新增函数
- `benchmark.go::handleBenchmarkLog` - 根据 mode 返回不同日志
- `benchmark_html.go` - UI 改造（模式切换、配置区、分组列表）

**Python 端：**

- `run_aiden.py` - 增加 `--aiden-suite` 参数与 task loading 分支
- `run_aiden.py::_load_aiden_suite_as_mobilegym_tasks` - 新增函数
- `run_aiden.py::_convert_task` - 新增函数
- `parallel_run.sh` - 透传 `--aiden-suite` 参数

**测试：**

- `benchmark_test.go` - 新增 mode 切换、MobileGym suite 列出测试
- `tests/benchmark/` - 新增 Aiden suite → MobileGym 转换测试
- 新增 e2e 集成测试

**文档：**

- 更新 `benchmark/mobilegym/README.md` - 说明 `--aiden-suite` 用法
- 更新 `benchmark/mobilegym/suites/README.md` - 移除"自定义 YAML 不支持"的提示
- 更新 `docs/10-benchmark/README.md` - 说明双模式架构
