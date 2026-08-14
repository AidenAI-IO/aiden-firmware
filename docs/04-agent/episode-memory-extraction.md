# Aiden Episode 到 Memory：第一版最终方案

> 状态：第一版已实现。
>
> 范围：只设计 `Episode → Device Memory`，不恢复 PEV，不涉及 Skill、Prompt、代码或模型参数演进。

## 1. 核心方案

移除 PEV 后，不再用同步 success/failure 决定是否学习。所有结束 Episode 先落盘并进入轻量扫描，只有包含明确学习信号的 Episode 才调用后台 LLM。

```text
所有结束 Episode 完整落盘
→ 代码硬预筛
→ 一次后台 LLM：判断结果并提炼 0～3 条 Candidate
→ 代码硬校验
→ create / update
→ update 内处理重复与冲突
→ 写入 Device Memory
```

关键约束：

- 前台新增 LLM、工具调用、截图和同步 Verifier 均为 0；
- 一个合格 Episode 正常只调用一次后台 LLM；仅当 proposal 尚未落盘前依赖的 Memory revision 并发变化时，作废旧 proposal 并重新提炼；
- 一条 Episode 最多输出 3 条独立 Memory；
- Episode 整体结果不决定 Memory 类型；
- LLM 提议，代码负责准入、目录和状态转换；
- Episode 自动经验只写 `DeviceMemoryStore`。

## 2. 三层筛选

### 2.1 代码硬预筛

预筛只判断“是否值得花一次 LLM”，不判断 Memory 类型，也不判断最终写入内容。

```text
eligible =
    hasDeviceToolCallAndResult
    OR hasNonCanceledStructuredError
```

- `hasDeviceToolCallAndResult`：存在设备类 tool call 及对应 result；
- `hasNonCanceledStructuredError`：存在非 canceled 的 `ToolError` 或 `IsError`。

`save_memory/recall_memory` 等 Memory 管理工具以及内部上下文工具不算设备动作。

`interrupted` 本身不是排除条件：中断前已有配对的设备 call/result 或非 canceled 结构化错误时仍可提炼；只有有效动作前取消才会被过滤。

同时要求 Episode：

- 已结束且可解析；
- 有用户目标；
- 尚未被当前提炼版本处理。

以下情况直接 `ignored`，不调用 LLM：

- 简单问候、感谢、道别和确认；
- 无设备操作的闲聊或普通问答；
- 用户在有效动作前取消；
- 只有 Agent 最终回答，没有行动或状态证据；
- 只有临时验证码、一次性文本或瞬时页面内容；
- 用户明确要求记住的信息已通过 `save_memory` 进入 long-term。

问候不依赖大型关键词表判断，主要依据“没有设备动作或结构化错误”。`steer` 不作为准入条件；Episode 已因设备轨迹进入 LLM 时，steer 作为结果纠正和 Failure 证据一起输入。

### 2.2 一次后台 LLM

通过预筛后，同一次调用完成：

- 判断 `goal_result`；
- 判断是否存在可复用 Lesson；
- 为 Lesson 分配类型；
- 判断 create/update；
- 在 update 时生成合并结果，并指出是否存在无法消解的冲突。

完全允许输出空 Candidate。进入 LLM 不代表一定生成 Memory。

### 2.3 代码硬校验

每条 Candidate 必须同时满足：

1. `evidence_refs` 真实存在；
2. 对未来相似任务可复用；
3. 会改变未来 Agent 行为或决策；
4. device、App、page、goal pattern 或 precondition Scope 明确；
5. 相对已有 Memory 有新结论、新 Scope 或新证据；
6. 不包含只适用于当次任务的临时值；
7. 满足对应类型的证据门槛；
8. 保存收益高于误召回和错误泛化风险。

不合格 Candidate 单独丢弃，不影响同次调用中的其他 Candidate。

## 3. Episode 结果判断

后台保留：

```text
goal_result = achieved | not_achieved | unknown
```

- `achieved`：有结果或最终状态证明用户目标满足；
- `not_achieved`：结果、产物、最终状态或用户纠正表明目标未满足；
- `unknown`：缺少足够的最终状态证据。

`achieved/not_achieved` 必须引用直接证据；`unknown` 必须说明缺少什么证据。

结果判断只用于审计和约束 Candidate，不作为学习入口，也不直接映射 Memory 类型：

- `not_achieved` 禁止生成完整目标的成功 Procedure；
- `not_achieved/unknown` 仍可生成有独立状态证据的局部 Procedure；
- `achieved` 不自动生成 Procedure；
- 结果与用户要求明确不符时，应判为 `not_achieved`。

`not_achieved` 也不一定生成 Failure Memory。只有存在可复用的 Guard、检查或恢复动作时才生成 `failure`；偶发错误且无可复用经验时不生成 Memory。

## 4. Memory 类型

一条 Episode 可以拆出多条 Candidate，但只有在 Lesson 能独立 Recall、Scope 不同或可独立复用时才拆分，最多保留 3 条。

| 类型 | 用途 | 最低证据门槛 | 目录 |
| --- | --- | --- | --- |
| `procedure` | 面向目标的可复用多步路径 | tool call/result，以及关键步骤后的截图或结果 | `device/procedures/` |
| `navigation` | 独立页面入口或页面跳转 | 动作前后可比较的截图/Observation 及中间动作 | `device/navigation/` |
| `calibration` | 坐标、屏幕、控件或坐标空间关系 | 坐标输入、post-action screenshot 或工具结果 | `device/calibration/` |
| `failure` | Guard、失败恢复或行动前检查 | 问题/风险证据及可执行方案 | `device/failures/` |
| `fact` | 稳定的 App、页面或设备事实 | 环境或工具的直接观察 | `device/memories/` |

Navigation 如果只是 Procedure 内的一步且没有独立复用价值，只保留在 Procedure steps 中。

## 5. LLM 输入与输出

### 5.1 输入

本地先从 Episode 整理：

- tool steps 和 tool results；
- 已落盘的 screenshot 和 post-action screenshot；
- coordinates 和 screen 信息；
- errors 和 recovery；
- user correction；
- Episode 已 Recall 的 Memory ID。

当前没有 PEV，因此不读取 `verifier_decision/ObservedState`。页面跳转、完成状态和坐标效果由 LLM 基于动作前后截图、tool result、结构化错误和用户 steer 判断；代码只校验引用的事件和截图真实存在。

再从本地索引检索相关 Device Memory，LLM 不读取全库：

```yaml
memory_limit: 8
memory_token_budget: 3000
```

优先顺序：已实际 Recall 的 Memory、相同 Scope、相同 Scope 的 disputed Memory、其他高相关 Memory。每个 Episode 基于最新 revision 重新检索，不能在串行处理过程中复用旧快照。

### 5.2 输出

```json
{
  "episode_assessment": {
    "goal_result": "achieved | not_achieved | unknown",
    "reason": "判断理由",
    "evidence_refs": ["evt_15"]
  },
  "candidates": [
    {
      "lesson_key": "verify_message_sent",
      "type": "failure",
      "action": "create | update",
      "memory_id": "update 时填写",
      "unresolved_conflict": false,
      "conflict_reason": "只有无法安全合并时填写",
      "situation": "适用场景",
      "guidance": "未来 Agent 应怎么做",
      "expected_effect": "应观察到的状态",
      "scope": {},
      "tags": [],
      "evidence_refs": ["evt_12", "evt_15"]
    }
  ]
}
```

空数组表示没有值得沉淀的经验。`lesson_key` 在当前 Episode 内唯一，用于写入幂等。模型不能选择文件路径或最终 Memory 状态。

## 6. Create、Update 与冲突

### Create

只有不存在同 Scope、同语义 Memory 时才能 create：

- 初始状态为 `active`；
- 保存 Episode、Lesson 和 evidence 来源；
- 根据 `type` 映射目录；
- 代码再次检查同类型、同 Scope Memory；即使模型误报 `create`，也不会产生第二条 active 记录；
- 第一版不实现 `pending → active`，低证据 Candidate 直接丢弃。

### Update

发现对应 Memory 后必须 update，不能再 create 近似记录。LLM 直接输出合并后的完整 `situation/guidance/scope`，不再分类 reinforce、refine 或 supersede。

Update 只有两个结果：

```text
unresolved_conflict = false
→ 合并新证据和 Scope
→ 当前 revision 保持或更新为 active
→ 正文变化时保留旧 revision

unresolved_conflict = true
→ 无法在相同 Scope 下得到一个安全结论
→ Memory 标记为 disputed
→ 不进入普通 Recall
```

可通过版本、页面、账号状态或其他前置条件区分的表面矛盾，应直接合并成条件化 Memory，并设置 `unresolved_conflict=false`。只有无法安全合并时才进入 disputed。

任何 update 都不能静默删除旧证据或旧 Procedure steps，也不能让相同 Scope、结论互斥的 Memory 同时 active。`disputed` Memory 仍参与后续后台检索，等待新 Episode 继续消解。

## 7. 存储与旧链路边界

```text
<config_dir>/memory/
├── episodes/                   # 不可变原始 Episode
├── device/
│   ├── procedures/
│   ├── navigation/
│   ├── calibration/
│   ├── failures/
│   ├── memories/
│   └── apps/
└── long_term/                  # 用户显式保存的长期信息
```

Episode Worker 只写 `device/`。普通 Recall 只读取 `active` Memory，最多 5 条，并限制为约 4800 字符（约 1200 Token）。`disputed/conflicted/pending` 只供后台合并使用。

新 Worker 启用后，停止旧的语义型自动提取，避免双写：

- long-term `Verified task path`；
- `recordVerifiedProcedure`；
- `recordNavigationFacts`；
- `recordCoordinateCalibration`。

确定性的 `device_profile/app_profile` 继续保留，不计入每个 Episode 最多 3 条 Lesson 的限制。

## 8. 幂等与 Latency

处理账本：

```text
episode_id + extractor_version
pending → processing → proposed → done/ignored
```

- `done/ignored` 直接跳过；
- LLM 输出先保存为 `proposed`，恢复时不重复调用；
- 模型生成、空响应或 JSON 解析失败直接记为 `ignored`，不再次调用模型；
- 进程在 `processing` 后、`proposed` 前退出时，租约到期后记为 `ignored`，避免崩溃恢复重复调用；
- Candidate 使用 `episode_id + lesson_key` 幂等写入；
- update 前检查 Memory revision，版本变化时作废尚未应用的 proposal 并重新排队；这是允许再次调用 LLM 的唯一例外。

账本继续沿用 `lifecycle/reflection.yaml` 和对应锁文件，作为已有安装的持久化兼容路径；代码职责和类型命名统一为 Episode Memory。

Latency 约束：

- 前台新增 LLM、工具调用、截图和 OCR 均为 0；
- Episode commit 后只做非阻塞通知；
- 后台并发数为 1；
- 新前台任务到来时，后台调用及时暂停或取消；
- 后台不与前台争用模型并发槽。

## 9. 第一版不做

- 恢复同步 PEV 或新增同步 Verifier；
- 多 Episode 一次 LLM 的批量 consolidation；
- `pending → active` 和多 Episode 晋升；
- 全库 pruning、自动删除、TTL 淘汰和灰度回滚；
- Skill、Prompt、代码或模型参数演进；
- 向量数据库和默认全量历史回放。

## 10. 验收标准

- 问候、感谢、闲聊和无学习信号 Episode 不调用 LLM；
- 合格 Episode 正常只调用一次 LLM，并输出带证据的 `goal_result`；只有 apply 前 Memory revision 并发变化时允许重新提炼；
- 每个 Episode 最多生成 3 条独立 Candidate；
- 结果与用户要求不符时判为 `not_achieved`，不得生成完整成功 Procedure；
- 每条 Memory 都能追溯到真实 Episode event；
- failed Episode 可以产生有证据的局部 Procedure；
- success Episode 可以产生 Failure/Guard；
- 重复经验进入 update，不创建近似 Memory；
- 无法消解的冲突进入 disputed，不参与普通 Recall；
- 同一 Episode 重试不会重复调用已保存的 proposal 或重复写 Memory；
- Episode 自动经验只写 Device Memory；
- 正常 Agent Loop latency 不发生可测回退。
