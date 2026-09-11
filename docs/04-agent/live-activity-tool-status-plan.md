# 灵动岛实时展示 Agent 工具状态方案

> 关联任务：**【Aiden】灵动岛展示更详细的实时信息：任务状态**  
> Feishu GUID：`2e106f68-d8a1-4a4b-a833-2de2eecb5896`

## 1. 目标与边界

灵动岛用于降低 Agent 执行过程中的等待焦虑。它需要让用户随时知道 Agent 当前在做什么、当前工具处于什么状态，以及是否需要 App 或用户介入。

本方案不展示完整 thinking，也不还原模型内部思维链。思考阶段只展示接口允许暴露的摘要或简短阶段文案；工具阶段优先展示工具动作和工具生命周期。

展示分为两层：

- **收起态**：只保留一眼可读的当前动作和状态，不展示任务标题、原始工具名、URL 或敏感参数。
- **展开态**：展示当前阶段的一条说明、当前工具、工具状态、最近结果和下一步，不做完整事件日志。

## 2. 收起态设计

收起态固定为：

```text
[Logo + 当前动作]        [时间 / 结果 / 等待状态]
```

没有任务时，左侧只显示 Logo：

```text
[Logo]                    [待命]
[Logo]                    [已连接]
[Logo]                    [连接中]
[Logo]                    [未连接]
```

工具执行中，右侧显示当前工具已经持续的时间，不再显示语义重复的“运行中”：

```text
[Logo 看屏幕]              [8秒]
[Logo 打开应用]            [2秒]
[Logo 点击操作]            [1秒]
[Logo 输入文字]            [4秒]
[Logo 滚动页面]            [3秒]
[Logo 搜索信息]            [6秒]
[Logo 剪贴板]              [2秒]
[Logo 确认结果]            [5秒]
```

工具完成、失败或等待外部条件时，右侧短暂显示结果：

```text
[Logo 看屏幕]              [完成]
[Logo 打开应用]            [失败]
[Logo 重试]                [第2次]
[Logo 等待 App]            [打开]
[Logo 等你]                [请接管]
```

收起态动作文案应控制在约 3～5 个汉字，超出时使用固定降级词。建议映射：

| 工具或阶段 | 收起态动作 |
| --- | --- |
| `screenshot` | 看屏幕 |
| `open_app` | 打开应用 |
| `touch_gesture` | 点击操作 |
| `keyboard_text` / `enter_text` | 输入文字 |
| `mouse_scroll` | 滚动页面 |
| `web_search` | 搜索信息 |
| `bridge_clipboard` | 剪贴板 |
| `bridge_calendar` | 日历操作 |
| `bridge_contacts` | 联系人 |
| `bridge_notification` | 发通知 |
| 验证阶段 | 确认结果 |
| 规划阶段 | 准备执行 |
| 回答阶段 | 整理结果 |

如果目标名称很短，可以显示“打开设置”“打开微信”等；目标过长、包含 URL 或可能包含敏感信息时，降级为“打开应用”。

## 3. 展开态设计

展开态最多展示 3～4 行核心信息，采用“当前状态 + 说明 + 详情 + 下一步”的结构。它展示最新状态，不累积历史事件。

### 3.1 思考阶段

思考阶段必须显示“思考中”，但只显示摘要：

```text
思考中 · 8秒

正在确认当前页面状态，
并判断下一步操作

下一步：打开设置
```

如果模型返回允许展示的 reasoning summary，则显示摘要的最新累计内容：

```text
思考中 · 8秒

已确认当前处于系统设置页面，
接下来需要查找网络选项

下一步：检查 Wi‑Fi
```

如果模型没有返回可展示摘要，则使用 Agent 生成的兜底文案：

```text
思考中 · 8秒

正在分析当前任务并准备下一步操作
```

摘要采用累计替换，最多两三行；不展示完整 reasoning_content，不直接展示原始 JSON。

### 3.2 工具执行阶段

工具调用开始后，工具信息优先于 thinking：

```text
操作中 · 看屏幕

工具：截图
状态：执行中 · 3秒
说明：正在读取当前页面

下一步：确认设置项是否出现
```

### 3.3 工具完成阶段

```text
已完成 · 看屏幕

工具：截图
结果：已获取当前页面

下一步：查找 Wi‑Fi 选项
```

### 3.4 重试、等待与人工接管

```text
重试中 · 第 2 次

工具：打开应用
原因：设备暂时没有响应
```

```text
等待 Aiden App

当前操作：读取剪贴板
请打开 Aiden App 继续
```

```text
等待你操作

请在手机上输入验证码
完成后 Agent 会继续执行
```

## 4. 工具状态模型

现有 `status` 和 `phase` 已能覆盖大部分状态。实现时统一按以下生命周期投影：

| 运行事件 | 展示状态 |
| --- | --- |
| `tool_call` | 工具执行中，立即显示动作和目标 |
| 工具持续执行 | 保持执行中，本地计时递增 |
| 成功 `tool_result` | 显示已完成和结果摘要 |
| 可恢复失败 | 显示重试中和重试次数 |
| 需要 Phone Bridge | 显示等待 Aiden App |
| 需要用户操作 | 显示等待你和接管说明 |
| 截图或操作后的确认 | 显示确认结果 |
| 无当前工具 | 显示思考中、准备执行或整理结果 |

## 5. Phone Bridge 不在线时的第三方 App 跳转

`open_app` 在 Phone Bridge 在线时可以直接由伴侣 App 执行；不在线时，现有逻辑会尝试恢复 Aiden App，或使用可见系统搜索/HID 作为兜底。灵动岛需要把“恢复 Aiden App”和“打开第三方 App”区分开，让用户知道即将发生的跳转。

推荐流程：

1. Agent 确认 Phone Bridge 不在线，但存在可用的 Aiden App 中转路径。
2. 在执行点击灵动岛或恢复 Aiden App 之前，先发布一条 Live Activity 状态：

   ```text
   收起态：[Logo 打开应用]    [准备]
   展开态：准备打开微信
           正在唤回 Aiden App
   ```

3. Agent 通过已确认的 Dynamic Island return entry 恢复 Aiden App。
4. Aiden App 前台恢复并重新建立 Phone Bridge 后，由 App 执行第三方 App 跳转。若系统或当前界面要求用户手动进入 Aiden App，则展开态改为“请点击灵动岛打开 Aiden App”，不能假设自动恢复一定成功。
5. 跳转后继续通过截图验证目标 App 是否真的打开；`ok:true` 只代表系统接受了请求。

如果没有可用的 Dynamic Island return entry，则显示：

```text
无法中转打开应用

请先打开 Aiden App，或允许 Agent 使用手机上的搜索/HID 方式继续
```

此场景不应伪装成“已打开”。只有收到 App 的实际结果并完成屏幕验证后，才显示“已完成”。

## 6. 协议改造范围

不需要改变 `/api/live-activity/current` 的接口路径、轮询机制或 BLE Wake + USB ECM 链路。现有字段已经能支持第一版展示：

```text
status
phase
current_step
current_action
current_target
current_app
last_tool_name
last_error
started_at
updated_at
```

建议做两个向后兼容的可选字段扩展：

```text
thinking_summary   // 可展示的思考摘要
tool_started_at    // 当前工具开始时间，用于准确计时
```

后续若需要同时展示工具状态、最近结果和下一步，再增加：

```text
tool_status
tool_result_summary
next_step
```

这些字段应同步加入 Go `LiveActivityState`、App TypeScript 类型、ActivityKit `ContentState`，旧版本 App 忽略未知字段即可继续工作。

## 7. 实现拆分

### Agent 侧

- 将允许展示的 `ReasoningContent` 摘要写入 `thinking_summary` 或等价字段。
- 在 `tool_call` 时记录 `tool_started_at`。
- 保持工具输入、结果和错误的摘要化处理，不传敏感参数和过长 JSON。
- 为 Phone Bridge 不在线的第三方 App 中转增加“准备打开目标 App”状态。
- 保留现有状态转换和 `/api/live-activity/current` 路由。

### App / iOS 侧

- 重写 Dynamic Island 收起态为“Logo + 动作 | 时间/结果”。
- 在展开态实现思考摘要、工具执行、结果、重试、等待和接管状态。
- 用 `tool_started_at` 在本地刷新工具计时；Agent 更新频率目标约 1 秒。
- 将第三方 App 中转前的“准备打开”状态显示为明确的预告。
- 对过长动作、目标和摘要做固定截断与降级。

## 8. 验收标准

1. 默认待命状态收起态只显示 Logo 和“待命”。
2. 工具执行时收起态显示自然的动作词，例如“看屏幕”，右侧显示持续时间。
3. 展开态在没有工具时显示“思考中”和一条摘要或兜底文案。
4. 工具执行、完成、失败、重试、等待 App、等待用户均能区分。
5. Phone Bridge 不在线时，第三方 App 跳转前先显示目标 App 和中转提示。
6. 未完成屏幕验证前，不显示“已打开”或“已完成”。
7. 收起态不出现原始工具名、URL、敏感参数或过长文本。
8. iOS 合并或延迟更新时，仍保留最后一次有效状态，不出现空白状态。
