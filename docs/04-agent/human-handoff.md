# Human Handoff Feature

## 概述

Human Handoff（人工接管）功能允许 Agent 在遇到需要人类判断、输入凭证或执行超出其能力范围的操作时，主动请求人工介入。

这个功能参考了 [Open-AutoGLM](https://github.com/zai-org/Open-AutoGLM) 项目的 `take_over` 机制，但采用了**非阻塞的对话流程设计**，更适合语音连续对话场景。

## 核心设计

与传统的阻塞回调不同，这个实现采用**自然对话流程**：

1. Agent 调用 `request_human_handoff` 工具
2. **工具立即返回**，告诉 Agent 需要通知用户什么
3. Agent 通过 TTS 或文本告诉用户需要做什么
4. **对话轮次结束**，等待用户的下一条消息
5. 用户完成任务后说"好了"/"继续"等
6. **新的对话轮次开始**，Agent 截图验证并继续执行

这就像人与人对话一样自然：
```
用户: "帮我登录 App Store"
Agent: "我需要你的帮助。请在屏幕上输入你的 Apple ID 密码，然后点击登录。完成后告诉我继续。"
[用户输入密码]
用户: "好了"
Agent: [截图] "好的，我看到已经成功登录了。现在..."
```

## 使用场景

Agent 可以在以下场景下请求人工接管：

### 1. **认证场景** (`authentication`)
- 登录界面需要用户名和密码
- 需要输入 Agent 无法获取的凭证
- 需要多因素认证

### 2. **验证码场景** (`captcha`, `verification_code`)
- 图形验证码（CAPTCHA）
- 短信验证码
- 邮箱验证码
- 身份验证挑战

### 3. **敏感操作** (`sensitive_operation`)
- 支付确认
- 银行转账
- 删除重要数据
- 权限变更

### 4. **屏幕不可见** (`black_screen`)
- 截图返回黑屏
- 敏感页面（支付、银行应用）自动屏蔽截图
- 无法获取屏幕内容

### 5. **模糊情况** (`ambiguous_situation`)
- 不确定应该选择哪个选项
- 需要用户偏好决策
- 多个可能的操作路径

### 6. **不支持的操作** (`unsupported_action`)
- 需要的功能超出工具能力
- 硬件限制
- 系统限制

### 7. **卡住** (`stuck`)
- 多次尝试后仍然无法继续
- 遇到未知错误
- 界面状态异常

### 8. **其他** (`other`)
- 其他需要人工介入的情况

## 工具使用

### Tool 名称
`request_human_handoff`

### 输入格式

```json
{
  "reason": "authentication",
  "details": "Login screen requires password for user@example.com",
  "suggested_action": "Please enter your password in the password field"
}
```

### 参数说明

- **reason** (必需): 请求接管的原因类别，可选值见上述使用场景
- **details** (必需): 详细描述当前情况，让人类了解发生了什么
- **suggested_action** (可选): 建议人类采取的操作

### 输出

工具**立即返回**，不会阻塞。返回格式：

```
HUMAN_HANDOFF_REQUESTED. You need to ask the user to perform a manual action. 
Reason: Login screen requires password for user@example.com. 
Tell the user: "Please enter your password in the password field" 
Then ask the user to tell you when they have completed the task (e.g., say 'done', 'continue', or 'finished'). 
After the user confirms, take a screenshot to verify the current state before proceeding.
```

Agent 会理解这个返回值，并将其转化为对用户的语音或文本提示。

## 代码集成

### 简单集成（推荐）

不需要任何回调函数，工具会自动注册：

```go
package main

import (
    "agent/internal/agent"
)

func main() {
    // 直接创建 ToolSet，human_handoff 工具会自动注册
    toolSet := agent.NewBuiltinToolSet(
        hidCfg,
        audioCfg,
        searchCfg,
        proxyCfg,
    )
    
    // 验证工具已注册
    if tool, ok := toolSet.Get("request_human_handoff"); ok {
        fmt.Printf("Human handoff tool ready: %s\n", tool.Name())
    }
}
```

### 在 Agent 提示词中说明

在系统提示词中告知 Agent 可以使用这个工具：

```
When you encounter situations that require human intervention, use the request_human_handoff tool:
- Login screens requiring passwords
- CAPTCHA or verification codes
- Sensitive operations like payments
- Black screens or inaccessible content
- Situations where you're stuck after multiple attempts

The tool returns immediately with instructions. Communicate these instructions to the user through your response,
then wait for the user to tell you they have completed the task in their next message.
After they confirm, take a screenshot to verify the result before continuing.
```

## 交互流程示例

### 语音模式

**Turn 1 - Agent 请求接管**:
```
用户 (语音): "帮我登录微信"
Agent: [截图分析] → [判断需要密码] → [调用 request_human_handoff]
       Tool 返回: "HUMAN_HANDOFF_REQUESTED. Tell user to enter password..."
Agent (TTS): "我需要你的帮助。微信登录需要输入密码。请在屏幕上输入你的密码并点击登录。完成后告诉我'继续'。"
[对话轮次结束]
```

**用户操作**:
```
[用户在手机上输入密码，点击登录]
```

**Turn 2 - 用户确认完成**:
```
用户 (语音): "好了" / "继续" / "我登录完了"
Agent: [截图] → [分析验证]
Agent (TTS): "很好，我看到已经成功登录了。现在我帮你..."
[继续执行后续任务]
```

### 文本对话模式

同样的流程，只是用文字代替语音：

```
用户: 帮我打开支付宝并充值话费
Agent: [导航到支付宝充值页面] → [发现需要支付密码]
Agent: 我需要你的帮助。支付页面需要输入支付密码来确认充值 50 元话费。请在手机上输入你的支付密码并确认。完成后告诉我"继续"。
[等待用户输入]
用户: 好了
Agent: [截图验证] 支付成功！话费已经充值到你的手机号。
```

## Agent 行为指导

在 Agent 的系统提示词中，建议包含以下指导：

```markdown
## Human Handoff Guidelines

Use the request_human_handoff tool when:

1. You need credentials you don't have (passwords, API keys, etc.)
2. You encounter CAPTCHA or verification challenges
3. The screen shows a black image or is not accessible
4. You're performing sensitive operations that need human approval
5. You've tried multiple approaches and are stuck
6. The task fundamentally requires human judgment

Always provide:
- Clear reason category
- Detailed explanation of the situation
- Specific suggestion of what the human should do

**Important**: This tool returns immediately with instructions for you to communicate.
After you tell the user what to do:
1. End your response and wait for their next message
2. When they say they're done (e.g., "continue", "done", "finished"), take a screenshot
3. Verify the result before proceeding with the task
```

## 测试

运行单元测试：

```bash
cd src/agent/internal/agent
go test -v -run TestHumanHandoff
```

## 示例场景

### 场景 1: 登录需要密码

```json
{
  "tool": "request_human_handoff",
  "input": {
    "reason": "authentication",
    "details": "App Store 登录界面要求输入 Apple ID 密码。我无法访问用户的密码。",
    "suggested_action": "请在密码输入框中输入您的 Apple ID 密码，然后点击登录按钮。"
  }
}
```

**Agent 的响应流程**：
1. 调用工具，工具立即返回指令
2. Agent 对用户说："我需要你的帮助。App Store 登录界面要求输入 Apple ID 密码。请在密码输入框中输入您的 Apple ID 密码，然后点击登录按钮。完成后告诉我'继续'。"
3. 等待用户回复
4. 用户说"好了"后，Agent 截图验证登录状态

### 场景 2: 遇到验证码

```json
{
  "tool": "request_human_handoff",
  "input": {
    "reason": "captcha",
    "details": "网站显示图形验证码，需要识别并输入图片中的字符。",
    "suggested_action": "请查看屏幕上的验证码图片，输入正确的字符。"
  }
}
```

**交互流程**：
```
Agent: "我遇到了验证码。请查看屏幕上的验证码图片，输入正确的字符。完成后告诉我。"
[用户输入验证码]
用户: "输完了"
Agent: [截图验证] "好的，验证码已通过，继续..."
```

### 场景 3: 支付确认

```json
{
  "tool": "request_human_handoff",
  "input": {
    "reason": "sensitive_operation",
    "details": "即将执行支付操作，金额为 ¥299。这是敏感操作，需要用户确认。",
    "suggested_action": "请检查支付金额和收款方是否正确，然后确认支付。"
  }
}
```

**交互流程**：
```
Agent: "我需要你确认一个支付操作。即将支付 ¥299。请检查支付金额和收款方是否正确，然后确认支付。完成后告诉我。"
[用户检查并确认支付]
用户: "支付完了"
Agent: [截图验证] "支付成功！订单已确认。"
```

### 场景 4: 黑屏

```json
{
  "tool": "request_human_handoff",
  "input": {
    "reason": "black_screen",
    "details": "屏幕截图返回全黑图像，可能是银行应用启用了防截屏保护。",
    "suggested_action": "请手动完成银行应用中的操作，完成后通知我继续。"
  }
}
```

**交互流程**：
```
Agent: "我无法看到屏幕内容，可能是银行应用启用了防截屏保护。请手动完成银行应用中的操作，完成后通知我继续。"
[用户完成操作]
用户: "完成了"
Agent: [尝试截图或执行下一步操作]
```

## 与阻塞模式的区别

### 阻塞模式（Open-AutoGLM 原始设计）
适合**脚本式一次性任务执行**：
```python
agent.run("在京东买一本《三体》")
# → 遇到登录
# → 工具调用阻塞
# → 用户按 Enter
# → 继续执行
# → 完成购买
```

### 非阻塞模式（本实现）
适合**连续语音/文本对话**：
```
用户: "在京东买一本《三体》"
Agent: "好的，正在打开京东... 我需要你的帮助。请输入你的京东账号密码。完成后告诉我。"
[对话轮次结束，等待用户]
用户: "好了"
[新对话轮次开始]
Agent: "登录成功。正在搜索《三体》..."
```

## 优势

1. **自然对话流程**：符合人类对话习惯
2. **适合语音交互**：通过 TTS 告知用户，用户语音回复即可
3. **无需回调实现**：简化集成，只需注册工具即可
4. **状态清晰**：每个对话轮次都是独立的，不会出现"卡住"的感觉
5. **易于理解**：对于用户来说，就像和真人助手对话一样

## 注意事项

1. **验证结果**：Agent 在用户确认完成后，务必截图验证结果
2. **提示词指导**：需要在系统提示词中明确告诉 Agent 这个工具的工作方式
3. **用户确认关键词**：Agent 应该能识别多种确认表达（"好了"/"继续"/"完成了"/"OK"等）
4. **超时处理**：如果用户长时间没有回复，应该有合理的超时机制

## 相关文件

- `src/agent/internal/agent/tools_human_handoff.go` - 核心实现
- `src/agent/internal/agent/tools_human_handoff_test.go` - 单元测试
- `src/agent/internal/agent/tools.go` - ToolSet 集成
- `docs/04-agent/human-handoff-examples.go` - 集成示例（已过期，待更新）
