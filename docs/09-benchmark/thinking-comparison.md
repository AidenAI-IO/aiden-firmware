---
sidebar_position: 7
---

# Thinking 开关对照实验

用同一份 benchmark suite 对同一模型跑两组条件：`thinking-off` 和
`thinking-on`。两组唯一应变化的是模型的 reasoning/thinking 配置；任务、设备
状态、suite、judge、并发度和 repeats 都保持一致。

## 1. 条件配置

先复制 benchmark 配置模板：

```bash
cd benchmark
cp config/agent.toml.template /tmp/agent-thinking-off.toml
cp config/agent.toml.template /tmp/agent-thinking-on.toml
```

在两个文件中填入相同的 provider、model、base_url 和 API key，然后在模板已有的
`[model]` 段中只修改 `reasoning_effort`：

```toml
# /tmp/agent-thinking-off.toml（放在已有 [model] 段内）
reasoning_effort = "none"
```

```toml
# /tmp/agent-thinking-on.toml（放在已有 [model] 段内）
reasoning_effort = "medium" # 或 high；按模型支持的档位选择
```

`none`、`minimal`、`low`、`medium`、`high` 的支持取决于 provider 和模型。
以 Agent 配置文档及实际 provider API 为准；如果 provider 不支持 `none`，应
使用该 provider 的“关闭 thinking”参数，并在实验记录中写明映射关系。不要用
两个不同模型来替代开关对照。

## 2. 推荐 benchmark

先用确定性、能在当前环境稳定复现的 suite 做 smoke run：

```bash
uv run python -m runner run \
  --suite suites/mobilegym_basic.json \
  --environment-url http://127.0.0.1:19090 \
  --auto-agent-setup \
  --agent-config /tmp/agent-thinking-off.toml \
  --condition thinking-off \
  --repeats 3 \
  --run-id thinking-off-mobilegym
```

然后只替换 config、condition 和 run id：

```bash
uv run python -m runner run \
  --suite suites/mobilegym_basic.json \
  --environment-url http://127.0.0.1:19090 \
  --auto-agent-setup \
  --agent-config /tmp/agent-thinking-on.toml \
  --condition thinking-on \
  --repeats 3 \
  --run-id thinking-on-mobilegym
```

有真实手机或 Android bridge 时，再加入 `phone_control_v1.json`、
`quick_action_v1.json` 等交互 suite。建议先 `--no-judge` 做执行稳定性检查，
正式结果开启 judge，并固定 judge model/base URL。

## 3. 对比结果

人类可读报告：

```bash
uv run python -m runner compare \
  --runs runs/thinking-off-mobilegym runs/thinking-on-mobilegym
```

机器可读 JSON（便于写入表格或 CI）：

```bash
uv run python -m runner compare --json \
  --runs runs/thinking-off-mobilegym runs/thinking-on-mobilegym \
  > thinking-comparison.json
```

报告包含：

- 任务成功率：`status == passed` 的任务比例；
- rubric 完成度：所有 judge rubric 步骤中 verdict 为 `yes` 的比例；
- 答案准确率：声明了 `expected_answer` 的任务中答案匹配比例；
- 任务翻转：off 失败、on 成功的数量，以及 on 导致的回归数量；
- wall time 中位数/P95，以及只统计 Agent 请求阶段的 `agent_wall_ms` 中位数/P95；
- 工具调用量和 token 使用量中位数（旧 Agent 未返回 usage 时显示 `n/a`）。

### 指标定义

令 off 为基线，on 为候选：

```text
成功率提升（百分点） = success_on - success_off
成功率相对提升       = (success_on - success_off) / success_off
时间成本增加         = (time_on - time_off) / time_off
```

其中比例以百分数显示；基线为 0 时相对提升显示为 `n/a`，避免把无限提升误报
成一个数。若需要美元成本，可用结果中的 `input_tokens`、`output_tokens` 按
provider 价格计算：

```text
cost = input_tokens / 1e6 * input_price
     + output_tokens / 1e6 * output_price
```

## 4. 实验纪律

- 每个条件至少 3 次，正式结论建议 5 次或更多；报告均值/中位数和 P95，不要只
  看一次运行。
- 两组使用相同任务顺序、相同 repeats、相同环境 reset 和相同 judge。
- 交互任务要固定设备型号、系统版本、网络和初始页面；避免两组共享未 reset
  的手机状态。
- 尽量随机化两组的运行顺序，降低网络、设备温度和 provider 峰值造成的偏差。
- 同时记录模型名、provider、reasoning_effort、suite SHA、Agent commit SHA、
  时间、并发度和 API 价格；这些信息已分别保存在 run manifest 和 results 中。
- `reasoning_effort` 是 thinking 强度，不等于“模型是否有隐藏思考”的绝对证明。
  结论应写成“在该模型/provider 的该 effort 配置下，benchmark 指标变化”。
