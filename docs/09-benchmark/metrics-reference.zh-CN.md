---
sidebar_label: 指标字段参考（中文）
sidebar_position: 5
---

# Benchmark 指标字段参考

[English version](./metrics-reference.md)

本文说明 benchmark 结果字段和上报分数的定义、测量范围、统计分母、单位与缺失值含义。

## 1. 当前评测链路与基本概念

```text
Benchmark runner
  → 任务隔离与初始化
  → Agent 执行
  → 硬断言与可选的 LLM Judge
  → results.jsonl
  → metrics.json / summary.md / report.html
  → Langfuse Dataset Experiment 与评测分数
```

- **Task（任务）：** suite 中定义的一个评测题目。
- **Attempt（尝试）：** 计划执行任务的一次机会，有独立编号；即使在初始化阶段被跳过，也可能生成一条 attempt 结果，并不保证 Agent 已开始执行。
- **Run（运行）：** 一次 benchmark 执行，包含选中的任务及其重复尝试。
- **重复尝试与内部重试：** 同一道题运行三次是三个 attempt；Agent 在一次执行中重试工具，仍然属于同一个 attempt。

结果文件及其用途：

| 产物 | 用途 |
| --- | --- |
| `manifest.json`、`suite.json` | 确认代码、题目、模型、Judge、平台、Skill 和重复次数等实验条件 |
| `results.jsonl` | 查看每个任务、每次尝试的状态、断言、指标和有效性 |
| `metrics.json` | 查看汇总指标与任务级统计 |
| `history.json`、`trace.json`、可选的 `episode.json` | 查看 Agent 实际观察、调用工具和回答的过程 |
| `judge.json`、`hard_assertion_failures`、可选的 `environment_state.json` | 查看判定成功或失败的依据 |
| `summary.md`、`report.html` | 快速浏览结果，找到值得深入检查的任务 |

重复尝试的文件位置以对应结果行中的 `artifact_dir` 为准，不要假设所有尝试都存放在同一个任务目录里。

Langfuse 中，同名 suite 对应一个 Dataset，每个任务/尝试对应一个 Dataset Item，每次运行对应一个 Experiment Run。存在 Agent episode ID 时，可以进一步关联到 Agent 自身的遥测 Trace。

查看一次尝试时，先看 Experiment item 的 Output：

| 字段 | 含义与边界 |
| --- | --- |
| `final_response` | 从该次尝试的 `trace.json` 读取的最终回复。`null` 表示证据不可用，空字符串表示记录中回复确实为空 |
| Experiment Output 中的 `tool_calls` | 按顺序保存的工具名和参数摘要；不包含工具返回值，与数值分数 `benchmark.tool_calls` 不同。工具返回值在执行 Trace 或 `history.json` 中查看 |
| `trace_artifact.status` | `available` 表示已读取有效记录，`missing` 表示缺失，`invalid` 表示格式不符，`unreadable` 表示无法读取 |
| `trace_artifact.path` | 相对于本轮 run 目录的证据文件路径；缺失时可为 `null` |
| `aiden_episode_id` | Agent 的 episode 标识；无法唯一确定时为空 |
| `aiden_trace_id` | 对应执行 Trace 的 32 位十六进制 OTLP ID。新 item 成功关联执行记录时，侧滑栏使用同一个 ID；旧记录和回退回放可能使用不同 ID |
| `aiden_trace_url` | 执行 Trace 的查看链接。项目查询不可用或没有 episode ID 时为空；有链接不代表已成功上传 |
| `execution_trace_status` | 发布时的关联状态：`available` 表示已找到 benchmark 执行根节点并关联，但不保证所有子节点已上传；`pending_or_missing` 表示有 episode ID，但未找到执行根节点；`no_episode` 表示没有可唯一确定的 episode ID。旧 item 可能没有此字段 |

`execution_trace_status=available` 时，Experiment 侧滑栏直接显示关联的执行 Trace：`agent-run` 包含目标和最终回复，`agent-response` 包含模型输入输出，工具节点包含参数和返回值，`benchmark/<task>#attempt-<n>` 节点包含评测结果。找到根节点不保证全部子节点均已上传。

执行证据不可用时，item 使用独立的结果回放。已有 item 在重试发布时保留原来的关联；回放中的父子节点不代表两次 Agent 尝试。

执行 Trace 的 `success` 是 Agent 自身结果，`benchmark.success` 是 benchmark 判定结果。评测结果节点的耗时是发布耗时，不能把 Experiment 默认的 Latency、Cost 直接当成任务耗时和费用。任务性能应使用下述 benchmark 指标；上报成功不代表任务通过。

## 2. 如何理解现有指标

### 2.1 单次尝试的结果与有效性

下列字段通常以 `benchmark.<字段名>` 上报到 Langfuse。

| 字段 | 当前含义 | 使用方法 |
| --- | --- | --- |
| `status` | `passed`、`failed`、`timeout`、`judge_error` 或 `skipped` | 先看状态，再看有效性与失败类别 |
| `success` | 成功、失败或未知 | 未知不能当作失败分数 0，也不能当作成功 |
| `agent_eligible` | 该尝试是否参与 Agent 能力指标计算 | 必须与成功率一起检查 |
| `failure_class` | `agent`、`environment`、`evaluation`、`skipped` 或 `unknown` | 区分能力问题、环境问题与无效评估 |
| `quality_score` | 通常为 Judge 检查项通过比例；硬门槛失败通常为 0；无法评估时为空 | 用于理解部分完成情况，不等于任务成功 |
| `rubric_pass_rate` | 已通过的 rubric 检查项数 / rubric 总数 | 需要结合具体判定；硬断言失败可能导致 Judge 根本没有执行 |
| `expected_answer_match`、`expected_recalled_memory_match` | suite 配置后才执行的确定性答案、记忆召回检查 | 并非所有记忆任务都具备这些检查 |

使用 `--no-judge` 时，通过硬断言的尝试会获得 `quality_score=1`，但这不代表语义检查已经通过。

**不要仅根据 `status` 自己重建成功率分母。** 当前 runner 会显式设置 `agent_eligible`：例如，状态为 `failed` 的初始化断言失败也可能不参与能力统计；没有执行证据的超时可能被标为未知。被排除的记录仍需要排查，不能当成产品表现良好。

#### `status` 与 `failure_class` 为什么都有 `skipped`？

这两个字段回答不同的问题：

- `status` 是**结果状态**：runner 最后把这次尝试记录成什么结果？
- `failure_class` 是**失败或未完成的粗分类**：应该先沿哪一类问题调查？它不是严格的根因，也不代表责任团队。
- `first_failure_stage` 是**证据支持的阶段标签**：问题在执行链路哪个阶段被识别？
- `agent_eligible` 是**统计资格**：能否用这次结果衡量 Agent 能力？它决定是否进入能力指标分母。
- `success` 是**单次结果值**：记录成功、失败或未知；必须结合统计资格使用。

`status` 的枚举含义：

| 值 | 当前含义 | 不能据此推出什么 |
| --- | --- | --- |
| `passed` | 本次启用的评估通过 | 不代表未启用的检查也通过；例如关闭 Judge 后没有语义验证 |
| `failed` | 已记录失败，可能发生在任务或初始化阶段 | 不能单独推出是 Agent 能力失败，也不能推出一定进入成功率分母 |
| `timeout` | 任务执行被记为超时 | 初始化超时不一定使用这个状态；有没有执行证据还会影响有效性 |
| `judge_error` | 无法完成有效判定，包括 Judge 异常、必要召回证据缺失、环境状态读取失败 | 不限于 LLM Judge 服务报错；也不等于 Agent 做错了任务 |
| `skipped` | runner 把尝试记录为跳过或无法正常完成执行 | 不只包括人为跳过，也不保证完全没有执行过：外层异常处理也会生成这个状态 |

`failure_class` 的枚举含义：

| 值 | 当前含义与常见来源 | 后续动作 |
| --- | --- | --- |
| `agent` | 可评估的任务未满足断言、答案或 rubric，或有执行证据的普通超时 | 查任务 Trace 与判定依据；仍可能是评测要求不合理，不等于模型一定有错 |
| `environment` | 当前主要用于初始化断言、带 consolidation 失败证据的初始化错误、缺失初始化记忆 ID 等 | 查 setup、种子数据、consolidation 和配置；并非所有环境故障都会被分到此类 |
| `evaluation` | Judge 异常或判定所必需的证据不可用 | 恢复证据/评估能力后重测，不能按正常能力失败解读 |
| `skipped` | 当前用于跳过和多种执行准备/外层异常的兜底分类 | 读取 `metrics.error` 和 runner 日志，继续细分原因；这个词没有解释真正根因 |
| `unknown` | 无法可靠归入其他类别，例如 chat 请求异常或无执行证据的超时 | 同时检查 `agent_eligible`、`agent_error` 和 Trace；不等于一律无效 |
| `null` / 未上报 | 通常表示通过的尝试没有失败类别；旧记录也可能缺字段 | 不等于 `unknown`；未知失败用显式字符串 `unknown` |

当前典型组合如下。这是现有代码路径的说明，不是允许任意组合的配置表：

| 场景 | `status` | `failure_class` | 原始 `success` | `agent_eligible` | 进入能力分母？ |
| --- | --- | --- | --- | --- | --- |
| 所启用评估通过 | `passed` | `null` | `true` | `true` | 是，计成功 |
| 普通硬断言或 rubric 未通过 | `failed` | `agent` | `false` | `true` | 是，计失败 |
| 已有执行证据的普通任务超时 | `timeout` | `agent` | `false` | `true` | 是，计失败 |
| 没有执行证据的任务超时 | `timeout` | `unknown` | `null` | `false` | 否 |
| chat 请求异常，已有执行证据 | `failed` | `unknown` | `false` | `true` | 是，计失败 |
| chat 请求异常，没有执行证据 | `failed` | `unknown` | `null` | `false` | 否 |
| Agent 未就绪、输入截图缺失、普通 setup 错误 | `skipped` | `skipped` | `null` | `false` | 否 |
| setup 断言或特定 consolidation 初始化失败 | `failed` | `environment` | `false` | `false` | 否 |
| Judge 不可用或必要评估证据缺失 | `judge_error` | `evaluation` | `null` | `false` | 否 |

因此，看到 `status=skipped`、`failure_class=skipped` 时，可以读成：**“这次被 runner 记录为跳过，暂时归入跳过类，不用于能力统计；具体为什么，要继续看错误信息。”** 它们是两个维度上的重叠标签，不是两次失败；不能把 `status.skipped` 和 `failures.class.skipped` 相加。

外层异常产生的 skipped 也可能覆盖已经发生的部分执行。重要结论应以原始日志和产物为准，不能从单个标签反推完整执行过程。

#### `null`、0、false 与旧数据兼容

| 表达 | 含义 |
| --- | --- |
| `null` 或字段缺失 | 未测量、无法确定或不适用，具体取决于字段 |
| 数值 0 | 按当前埋点口径记录的零；不能自动推广成现实中完全没有该行为 |
| 布尔 `false` | 一个明确的否定值，例如未尝试恢复或不具备统计资格 |
| 字符串 `unknown` | 分类字段明确记录“未能分类”，不是空值 |

上报器对缺失的数值、布尔和分类分数会省略写入。`recovery_attempted=false` 只表示 runner 未进入超时恢复分支；`recovery_succeeded=null` 通常表示未执行恢复，不能读成恢复失败。

聚合器为旧记录提供状态回退：缺少合法布尔 `success` 时，会按 `passed → true`、`failed/timeout → false` 推导，**这也包括原始 `success=null` 的情况**。明确的 `agent_eligible=false` 仍被保留，因而这些无效尝试不会进入能力分母。诊断原始事件时看 `results.jsonl`；解释汇总时同时遵守有效性规则，不要把回退值当成新获得的执行证据。

没有显式有效性字段的旧记录，还会从状态和失败类别推导有效性；旧数据与当前数据的归类不保证完全一致。

#### 断言与 rubric 的布尔值也有方向

`hard_assertions` 中的布尔值表示**检查是否通过**，不是事件是否发生。例如 `hard_assertions.timeout=true` 表示超时检查通过，不表示发生了超时。

| 字段 | `true` 表示 |
| --- | --- |
| `min_tool_calls` / `max_tool_calls` | 调用数达到下限 / 未超过上限 |
| `required_tools` / `forbidden_tools` | 要求的工具出现 / 禁止的工具未出现 |
| `required_tool_calls` / `prohibited_actions` | 所需调用参数模式满足 / 禁止动作检查通过 |
| `response_exists` | 最终回答存在 |
| `expected_answer` / `expected_recalled_memory` | 所配置的答案 / 召回证据检查通过 |
| `environment_state` | 所配置的环境状态检查通过 |

空值的解释依检查而异，可能是未配置、未运行或证据不可用，应结合状态和错误字段。`hard_assertion_failures` 给出失败检查的 ID、要求和实际值；它比只看汇总分数更适合定位问题。

`rubric_spec` 是题目定义，`rubric` 是实际返回的逐项 verdict 和 reason，`rubric_total` 是题目检查数，`rubric_pass_count` 是通过数。由于前置硬门槛失败时 Judge 可以不运行，`rubric_total>0`、`rubric_pass_count=0` 不一定表示 Judge 逐项判了失败；必须看 `rubric` 和 `judge.json` 是否存在。

### 2.2 能力、首次成功与稳定性

以下运行级分数在 Langfuse 中使用 `capability.` 前缀。

`k` 来自 `manifest.metrics_k`，当前取所选任务中最小的计划重复次数。可以使用 `--repeats N` 统一重复次数；当前 CLI 没有单独的 `--metrics-k` 参数。

| 指标 | 当前计算口径 | 主要回答的问题 |
| --- | --- | --- |
| `pass_at_1` | 实际第 1 次尝试成功的任务数 / 第 1 次尝试有效的任务数 | 用户第一次请求能否成功？ |
| `pass_at_k` | 前 k 次至少成功一次的任务数 / 前 k 次均存在且有效的任务数 | 这项能力是否至少能做对一次？ |
| `pass_pow_k` | 前 k 次全部成功的任务数 / 前 k 次均存在且有效的任务数 | 这项能力是否稳定？ |
| `attempt_success_rate` | 成功的有效尝试数 / 全部有效尝试数，包含超出 k 的尝试 | 所有有效执行中，有多少成功？ |
| `oracle_best_score_at_k` | 每个完整任务前 k 次中最高有效质量分数的平均值 | 已观察到的最好表现如何？ |
| `first_success_attempt.mean/p50/count` | 从 attempt 1 开始连续有效的尝试序列中，首次成功的位置；可超过 k | 成功任务通常需要尝试几次？ |

这些是固定前 k 次的实际观测结果，不是从任意尝试组合中估算的分数。始终没有成功的任务不进入 `first_success_attempt` 的均值。

如果 `pass_at_k` 高、`pass_pow_k` 低，说明任务可能做对，但不够稳定。优先检查初始化、决策波动、状态验证和恢复逻辑。

如果 `k=1`，三个 pass 指标在同一个有效任务集合上相同，不能据此判断重复执行的稳定性。

每个 run 的 k 值以 `manifest.metrics_k` 为准；不同 run 不会自动合并为同一任务的 k 次尝试。

三个 pass 指标还包含成功数、有效任务数、总任务数和 Wilson 95% 置信区间。`attempt_success_rate` 也有置信区间，但同一个任务的重复尝试并非相互独立，现有简单区间没有处理这种相关性。发布决策应结合任务配对分析、独立复测或按任务分组的不确定性分析。小幅百分比提高，或两个区间是否重叠，都不能单独证明优化显著。

### 2.3 覆盖率与失败诊断

| 指标或字段 | 含义与注意事项 |
| --- | --- |
| `coverage.pass_at_1/pass_at_k/pass_pow_k` | 对应指标的有效任务数 / 结果中出现的不同任务数 |
| `coverage.unique_tasks/attempts/metrics_k` | 实际任务数、尝试数和固定 k；旧字段 `tasks` 统计的是尝试数 |
| `coverage.agent_eligible_attempts/invalid_attempts` | 可以和不可以用于评估 Agent 能力的尝试数 |
| `capability.attempt_success_rate.coverage` | 有效尝试数 / 实际结果行数 |
| `failures.class.<class>` | 各失败类别的数量，不是比例 |
| `failures.stage.<stage>` | 有效失败尝试的阶段分布，包括 `unknown` |
| `diagnostics.failure_stage_coverage` | 有效失败中被归入已知阶段的比例；没有有效失败时为空 |
| `benchmark.first_failure_stage` | 有证据支持时记录的失败阶段，不是完整根因结论 |
| 结果中的 `failure_event_ref` | 指向 history 或 episode 的证据位置；不是独立的 Langfuse 分数 |

覆盖率的分母是已经出现的任务。因此，还要核对 manifest 中的计划任务/尝试列表，避免整道题没有生成结果却未被发现。

阶段定义包括初始化、感知、规划、动作选择、设备执行、状态验证、恢复、最终回答、评估和未知。**支持这些标签不代表当前系统可以自动识别所有阶段。** 很多语义失败目前仍为 `unknown`。

排查时应找“第一次偏离正确路径的位置”。第一个工具报错可能只是前面规划错误的结果。

下面是阶段词汇的解释，**用于读标签或人工归因，不代表每一项已有自动识别能力**：

| 阶段值 | 应表达的阶段 | 示例 |
| --- | --- | --- |
| `setup` | 正式任务之前的准备 | 初始化断言不满足 |
| `perception` | 理解当前观测 | 把截图中的控件识别成另一个控件 |
| `planning` | 选择完成任务的路径 | 在正确识别页面后选择错误流程 |
| `action_selection` | 选择具体工具和参数 | 目标正确但工具或输入参数选错 |
| `device_execution` | 执行已选设备动作 | 设备动作返回错误；不必然说明上游选择正确 |
| `state_verification` | 判断动作后状态及任务是否完成 | 页面未变化却认为已经完成 |
| `recovery` | 异常后的恢复阶段 | 无法从执行异常恢复；需区分 Agent 恢复与 runner 恢复 |
| `final_response` | 最终向用户报告结果 | 状态正确但回答错误 |
| `evaluation` | 评测判定阶段 | Judge 或必要证据不可用 |
| `unknown` | 尚无可靠阶段证据 | 仅知道 rubric 失败，未定位首个错误 |

当前提取器主要能根据工具错误记录 `device_execution` 或 `unknown`，并由 runner 标记部分 setup/evaluation 路径。成功结果会清空失败阶段和证据指针，但仍可保留非零 `tool_errors`。因此“工具曾报错”与“最终任务失败”不是同一件事。

`failures.class.*` 统计所有带失败类别的结果，`failures.stage.*` 只统计有效且失败的尝试。两组计数的分母不同，不能直接对齐，也不能直接当作产品根因占比。

还有两个分母差异：

- `category.<category>.pass_rate` 按该类别的全部尝试计算，包括无效尝试，与有效样本口径的能力成功率不同。
- `observations.<id>.*` 实际统计被评估的尝试行，虽然部分字段名仍叫 `passed_tasks`、`observed_tasks`。Trace observation 描述工具使用等行为，本身不会直接判定任务失败。

### 2.4 耗时、工作量与成本

单次指标通常使用 `benchmark.<字段名>`。运行级分布通常使用 `efficiency.<字段名>.{count,sum,mean,p50,p90,p95}`；工具错误、重规划和重试则使用 `reliability.` 前缀。

| 指标 | 实际测量范围 |
| --- | --- |
| `task_wall_ms` | 任务 Agent chat 请求耗时，不包含初始化、Judge、后续产物收集和超时后的恢复；当前结果中的旧字段 `wall_ms` 与它一致 |
| `setup_ms` | 任务隔离、初始化时间，应与任务执行时间分开看 |
| `llm_time_ms` | 带 usage 的 assistant/tool-call 消息上记录的 duration 总和；这些消息的耗时不完整时为空 |
| `vision_llm_calls/vision_llm_time_ms` | 根据模型调用前的图片、截图观测推断，属于埋点口径，不等于供应商计费分类 |
| `time_to_first_token_ms` | episode 提供的首 token 时间，未提供则为空 |
| `device_execution_ms` | episode 中识别为设备动作的工具结果耗时之和；所需耗时缺失时为空，动作类型依赖显式名单 |
| `screenshot_capture_ms` | runner 自己计时的截图操作，不是 Agent 内部全部截图耗时；结合 `screenshot_capture_source` 查看 |
| `tool_calls/device_actions` | 全部工具调用数 / 被识别为改变设备状态的动作数；只读 bridge 查询不属于设备动作 |
| `llm_calls` | 从带 usage 的消息计数；完全没有 usage 时为空，不能未经核验就认为覆盖了全部供应商请求 |
| `input_tokens/output_tokens/total_tokens` | Agent 上报的用量；episode 总量可以覆盖 history 推导结果，不代表整个 run、Judge 和初始化的全部消耗 |
| `cached_input_tokens/reasoning_tokens` | 可选用量明细，不应作为独立额外消耗再次加到总量上 |
| `tool_errors` | 工具结果中的错误数，包括随后恢复成功的错误 |
| `replan_count` | 依赖 episode 中显式的 `needs_replan` 证据，不根据工具名称猜测 |
| `retry_count/cost_usd` | 已有字段与上报支持，但当前任务 runner 没有实现重试计数或费用计算；没有明确数据源时应为空 |
| `recovery_attempted/recovery_succeeded` | runner 在 Agent 超时后执行恢复的结果，不是用户任务成功，也不是 Agent 策略重试次数 |

效率分布使用有效尝试中的数值；`setup_ms` 使用全部结果中的已知数值。每次都要看 `count`：测量数量变少，也可能造成均值下降。

缺失指标在 Langfuse 中被省略，不会作为 0 上报。分位数采用插值，小样本的 p95 不能当作可靠的长期尾延迟。不同耗时字段范围不同或互有包含，不能简单相加得到总时间。

`cost_to_first_success.{task_wall_ms,total_tokens,cost_usd}.{count,mean,p50}` 会从第 1 次累计到首次成功。序列遇到缺失或无效尝试即中断，且某字段必须在成功前完整才参与统计。

**始终失败的任务不进入这个均值。** 因此要与成功率、覆盖率和总体消耗一起看，不能单独用它证明“服务所有请求的成本下降了”。

### 2.5 数值后缀、单位与其他字段

| 名称或后缀 | 解释 |
| --- | --- |
| `value` | JSON 中该指标的主值；比例是 0–1，0.8 应展示为 80%。Langfuse 主分数名通常不带 `.value` |
| `count` | 实际参与该数值统计的样本数；时间/Token 分布通常按尝试，首次成功累计消耗按任务 |
| `sum` | 已知有效数值之和；样本不完整时不是总账单或全量总耗时 |
| `mean` | 算术平均值 |
| `p50` | 插值得到的中位数，单位与原指标一致 |
| `p90` / `p95` | 插值得到的第 90/95 百分位，描述这批样本的尾部，不是置信度 |
| `ci95.lower/upper` | JSON 中比例的 Wilson 95% 置信区间上下界；Langfuse 使用 `ci95_lower/ci95_upper` |
| `successes` | 对应指标的成功数；pass 系列按任务，attempt success 按尝试 |
| `eligible_tasks/total_tasks` | 该任务级指标的有效任务数 / 已观测不同任务数 |
| `eligible_attempts/total_attempts` | 有效尝试数 / 已观测尝试数 |
| `_ms` | 毫秒；看板展示秒时除以 1000 |
| `_tokens` | Token 数，不是字数或美元；价格与模型和缓存计费有关 |
| `_usd` | 美元，仅在明确费用来源且有数值时解释 |

补充单次结果字段：

| 字段 | 解释与边界 |
| --- | --- |
| `screenshots_taken` | trace 中标记 `has_screenshot` 的工具调用数，不是所有截图次数，也不包含所有 runner 前后截图 |
| `pre_screenshot_file/post_screenshot_file` | 本地对应截图文件是否存在，不验证图片是否正确或清晰 |
| `screenshot_capture_source/cost_source` | 采集来源标识，保存在结果中；不是独立的 Langfuse 数值分数 |
| `error/agent_error/judge_error/environment_state_error` | 不同执行/评估环节记录的错误详情，是进一步解释粗分类的入口 |
| `episode_error/pre_screenshot_error/post_screenshot_error` | 产物获取失败；未必使所有任务失败，但可能导致相关指标缺失或评估失败 |
| `episode_id/active_skills` | 关联 Agent 遥测 / 记录启用 Skill；名称相同不证明内容相同 |
| `started_at/finished_at` | 结果记录的起止时间；跨度不是 `task_wall_ms` 的同义词 |
| `environment_state_assertions` | 逐条环境检查及其实际值，不应仅以最终页面截图替代 |
| `trace_observations` | 已配置行为观察的逐条结果；不配置就没有相应分数 |

`aggregate.per_task` 中：`total_attempts` 包含无效尝试，`eligible_attempts` 只含有效尝试，`passed/failed/pass_rate` 使用有效尝试，`best_quality_score/avg_quality_score/avg_wall_ms` 使用有效尝试中的已知数值。当前 `first_attempt_passed` 实际取**排序后的第一个有效尝试**，与严格取编号 1 的 `pass_at_1` 不同；如果第 1 次无效、第 2 次成功，前者可能为 true，而该任务不会进入 `pass_at_1` 分母。

`oracle_best_score_at_k.count` 是存在质量分数的完整任务数，可能少于它的 `eligible_tasks`。判断任一聚合数值，都要先确定样本单位与分母，再看变化方向。
