# Benchmark Logging 增强说明

## 概述

增强了 benchmark 运行器的日志输出功能，现在可以在命令行和 Web 界面上看到每个测试用例的详细执行结果。

## 新增功能

### 1. 命令行详细输出 (--verbose)

使用 `--verbose` 或 `-v` 参数可以查看每个测试用例的详细评判结果：

```bash
python3 scripts/aiden_benchmark.py run --suite benchmark/suites/full_smoke.json --verbose
```

### 2. 增强的日志信息

每个任务执行后会显示：

#### 基础信息（默认显示）
- ✅ 任务状态（PASSED/FAILED/TIMEOUT/SKIPPED）
- 📊 Rubric 通过率（例如：rubric=3/5）
- ⏱️ 执行时间（wall time）
- 🔧 工具调用次数
- 📸 截图数量

#### 详细信息（--verbose 模式）
- 📋 每个 Rubric 项的详细结果
  - ✅/❌ 通过/未通过标识
  - 📝 评判原因（前3行预览）
- ⚠️ Hard Assertion 失败详情
- ❌ 错误信息（如果有）

### 3. 最终汇总报告

测试运行结束后会显示格式化的汇总报告：

```
============================================================
📊 Benchmark Summary - Full Smoke Test
============================================================
Total Tasks:   10
✅ Passed:     8
❌ Failed:     1
⏭️  Skipped:    1
⏱️  Timeout:    0

📁 Results saved to: benchmark/runs/2026-06-09_153045
============================================================
```

## 使用示例

### 普通模式（简洁输出）

```bash
python3 scripts/aiden_benchmark.py run --suite benchmark/suites/full_smoke.json
```

输出示例：
```
PASSED     task_001 attempt=1 rubric=3/3 wall=2450ms (tools=5, screenshots=2)
FAILED     task_002 attempt=1 rubric=1/3 wall=1820ms (tools=3, screenshots=1)
```

### 详细模式（verbose 输出）

```bash
python3 scripts/aiden_benchmark.py run --suite benchmark/suites/full_smoke.json --verbose
```

输出示例：
```
PASSED     task_001 attempt=1 rubric=3/3 wall=2450ms (tools=5, screenshots=2)
  📋 Rubric Details:
    ✅ [1/3] correct_action: YES
        → Agent correctly opened the target app
    ✅ [2/3] correct_parameter: YES
        → Parameters were set as expected
    ✅ [3/3] final_state: YES
        → Final screen shows expected result

FAILED     task_002 attempt=1 rubric=1/3 wall=1820ms (tools=3, screenshots=1)
  📋 Rubric Details:
    ✅ [1/3] app_opened: YES
        → App was successfully opened
    ❌ [2/3] correct_navigation: NO
        → Agent navigated to wrong section
        → Expected: Settings tab
        → Actual: Home tab
    ❌ [3/3] task_completed: NO
        → Task was not completed due to navigation error
  ⚠️  Hard assertion failures: response_exists
```

## 命令行参数

| 参数 | 简写 | 说明 |
|------|------|------|
| `--verbose` | `-v` | 显示详细的 rubric 结果和错误信息 |
| `--suite` | | 指定测试套件文件（必需） |
| `--agent-url` | | Agent 服务地址（默认：http://localhost:8080） |
| `--judge-model` | | 评判模型（默认：claude-sonnet-4-6） |
| `--no-judge` | | 跳过 LLM 评判，仅运行 hard assertions |
| `--repeats` | | 重复次数覆盖（覆盖套件中的设置） |

## 输出文件

所有详细结果都会保存到 `benchmark/runs/<timestamp>/` 目录：

- `manifest.json` - 运行元数据和汇总统计
- `results.jsonl` - 每个任务的详细结果（JSONL 格式）
- `summary.md` - Markdown 格式的汇总报告
- `report.html` - HTML 格式的完整报告
- `tasks/<task_id>/` - 每个任务的详细文件
  - `history.json` - 完整的对话历史
  - `trace.json` - 工具调用追踪
  - `judge.json` - 评判结果详情
  - `pre.jpg` / `steps/` - 截图文件

## 技术实现

修改了以下文件：
- `benchmark/runner/main.py`
  - 添加 `--verbose` 参数
  - 新增 `_log_task_result()` 函数用于格式化日志输出
  - 增强最终汇总报告的显示

## 向后兼容

所有现有的命令和脚本都保持兼容，默认行为（不使用 `--verbose`）输出简洁的单行结果，与之前基本一致，只是增加了工具调用和截图数量的提示信息。
