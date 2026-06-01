# Agent Prompt 策略建议

本文档评估 Open-AutoGLM 的手机 Agent prompt，并结合 Aiden 当前 prompt，整理后续改造建议。重点目标是让 Aiden 更适合国外 geek 用户，同时避免把国内 App 的固定 playbook 写进默认 system prompt。

## 背景

参考 prompt：

```text
https://github.com/zai-org/Open-AutoGLM/blob/86f55382/phone_agent/config/prompts_zh.py
```

Aiden 当前 prompt 主要来自：

```text
src/agent/internal/agent/prompt.go
src/agent/config/agent.toml
overlay/userdata/agent/agent.toml
```

当前 Aiden prompt 已经包含一批手机控制相关规则，例如截图确认、避免重复点击、优先搜索、`keyboard_text` 只能输入 ASCII、点击优先使用 normalized 坐标、手机边缘手势等。这些规则方向是对的，但仍有几个问题：默认中文语境过重，`agent.toml` 和 `prompt.go` 有重复，且缺少对当前运行环境的明确建模。

## 总体结论

Open-AutoGLM 的 prompt 更像面向中文 Android benchmark 的任务执行器：它规定了强输出格式、坐标系、动作集合和大量国内 App 场景规则。Aiden 不应直接照搬这些内容。

Aiden 应该吸收它的通用执行纪律，例如操作后观察、有限重试、错页恢复、敏感操作确认、多候选询问和结束前自检；但不应把小红书、外卖、购物车、国内群聊等业务规则放进默认 prompt。

面向国外 geek 用户，Aiden 默认 prompt 应该更像一个跨设备控制 Agent：先知道自己运行在哪里、连着什么目标设备、可用哪些控制通道，再根据截图和工具结果操作，而不是默认自己在 macOS/PC 上，也不是默认自己在中国移动 App 生态里。

## 两者对比

| 维度 | Open-AutoGLM prompt | Aiden 当前 prompt | Aiden 建议方向 |
| --- | --- | --- | --- |
| 产品定位 | 中文 Android 手机任务执行器，偏 benchmark 和固定任务集 | 设备端通用 Agent，已有手机控制规则，但 persona 仍偏中文语音助手 | 国外 geek 的跨设备控制 Agent，默认简洁英文，跟随用户语言 |
| 目标设备假设 | 基本假设正在操作中文 Android 手机和国内 App | 明确手机 UI 自动化，但没有区分 runtime 和 target | 明确 runtime/controller 与 target device；目标设备运行时识别，手机只作为低置信度 prior |
| 动作协议 | 自定义 `do(action=...)` / `finish(...)` DSL | langchaingo ReAct/function tools | 保持现有工具协议，不引入另一套动作 DSL |
| 坐标系统 | `0..999` 归一化坐标 | HID 工具支持 pixel / normalized / absolute，推荐 `0..1` normalized | 继续坚持 `coord_space:"normalized"`，不引入 `0..999` |
| 输入方式 | ADB Keyboard，偏中文输入法场景 | HID keyboard，`keyboard_text` 只能输入 US-keyboard ASCII | 用通用非 ASCII 策略：ASCII 搜索、转写、候选选择或询问用户 |
| 观察闭环 | 每次操作后基于截图继续判断 | 已有 post-action screenshot 指导 | 强化“观察-操作-验证”，避免连续重复同一动作 |
| 失败恢复 | 有等待、返回、重新搜索、滑动失败处理等规则 | 已有部分规则，但对有限重试和环境误判不够明确 | 抽象成通用有限重试、错页恢复、环境重判，不保留国内 App 细节 |
| App 规则 | 包含小红书、外卖、购物车、群聊等国内 playbook | 默认 prompt 没有大量国内 App 名，但中文/pinyin 假设较强 | 默认 prompt 不写 App-specific 规则；按地区或任务用 `skill_read` 按需加载 |
| 安全边界 | 登录验证有接管，支付等敏感点击有标记 | 主要对拨打电话有约束 | 扩展为通用敏感操作确认：发送、删除、支付、授权、拨号等 |
| shell/OS 命令 | 不涉及 Aiden runtime 和外部 target 的区分 | 有 `shell` 工具，但 prompt 未充分声明作用域 | 明确 `shell` 只作用于 runtime，不操作截图里的目标设备 |
| Prompt 长度 | 长规则集中写在 system prompt | `agent.toml` 和 `prompt.go` 有重复 | system prompt 保留硬边界，详细 UI SOP 放 `device-operator` skill |

核心取舍：Aiden 应学习 Open-AutoGLM 的执行纪律，而不是继承它的场景假设。默认 prompt 要解决“我是谁、我连着什么、哪些工具能影响哪个设备、什么时候必须确认”这些全局问题；具体 App 和地区经验应下沉到 skills。

## 优先改造：运行环境感知

从测试现象看，Aiden 有时会误以为自己运行在某个 PC OS 上，例如尝试调用 `osascript` 这类宿主桌面命令。但实际部署时，Aiden 运行在自己的硬件控制器上，通过 USB HID、HDMI/frame service、audio service 等通道控制外部目标设备，例如手机、电脑或电视。

这类误判应作为 prompt 改造的最高优先级之一。Agent 需要区分三个概念：

- `runtime_device`：Aiden daemon 实际运行的设备，例如 Aiden 硬件控制器、开发机、本地 PC、云端容器。
- `target_device`：正在被观察和控制的外部设备，例如 iPhone、Android 手机、PC、Mac、电视、机顶盒、游戏主机。
- `control_channels`：当前可用的观察和控制能力，例如 screenshot/frame capture、USB HID keyboard、USB HID mouse/touch、audio input/output、shell、web search。

### Prompt 中应明确的环境模型

默认 system prompt 应加入类似规则：

```text
You are Aiden, an agent running on the Aiden hardware/controller with Linux as the runtime OS, not necessarily on the device shown in screenshots.
The screenshot tool shows the current target device display. Treat that visible device as the target UI.
Use HID/touch/keyboard tools to operate the target device. Do not assume shell commands affect the target device.
Use shell only for the Aiden hardware controller itself, diagnostics, or explicitly requested local commands.
Before using OS-specific automation such as osascript, adb, xdotool, PowerShell, or AppleScript, verify that this tool exists and that the requested action is meant for that runtime OS, not the connected target device.
```

### 环境上下文应动态生成

不建议在出厂 `agent.toml` 中预置固定目标设备。用户可能把 Aiden 插到任意设备上，例如 iPhone、Android 手机、Mac、Windows PC、电视、机顶盒或游戏主机；如果默认写死 `target_device = "phone"` 或 `target_os = "ios"`，模型会更容易误判。

更合理的分层是：默认 prompt 只描述 Aiden 自身和工具作用域，目标设备作为运行时上下文动态生成。

这里声明 `runtime_device` 的目的不是让用户配置目标设备。当前运行环境是确定的：Aiden 运行在硬件控制器上，运行时 OS 是 Linux。这个事实可以由程序硬编码或部署配置注入，不需要用户选择。它的核心作用是给模型建立工具作用域：`shell`、本地文件、进程和 OS 命令属于 Aiden 硬件控制器；截图和 HID 输入对应的是外部 target UI。

如果仍希望用配置表达环境，配置里也只应包含这类稳定事实：

```toml
[environment]
runtime_device = "aiden_board"
runtime_os = "linux"
target_region = "global"
shell_scope = "runtime_only"
```

也可以不暴露为用户配置，而是在构建 prompt 时直接生成等价状态块。关键不是字段本身，而是让模型始终知道“我运行在哪里”和“截图里的设备不是我自己”。

### 目标设备不能假设为硬检测

在当前硬件链路下，Aiden 很难直接、可靠地知道 target device 的型号和 OS：

- `screenshot` 当前只返回画面宽高、格式和图片数据，不返回设备型号或 OS。
- `frame_service` metadata 主要是 HDMI/capture 帧信息，例如分辨率、像素格式、stale 状态，不能直接说明对端是 iPhone、Android、Mac、Windows 还是 TV。
- USB HID gadget 通常只让对端看到 Aiden 是键盘/鼠标；Aiden 这边不能稳定反查 host OS。
- `shell` 看到的是 Aiden 控制器自己的 Linux 环境，不代表截图里的 target device。

因此 `target_device` 应是“多源推断结果”，而不是确定事实。推荐推断顺序：

1. 截图视觉识别：根据状态栏、导航栏、Home indicator、Android 手势条、macOS menu bar、Windows taskbar、TV launcher 等 UI 线索推断。
2. 连接/视频元数据：使用分辨率、宽高比、刷新率、HDMI timings 等作为辅助证据，但不能单独确定 OS。
3. 谨慎行为探测：在低风险前提下尝试 Back/Home 手势或常见快捷键，并通过截图观察反应；这有副作用，只能作为辅助。
4. 用户确认：当置信度低、即将执行敏感动作或设备类型影响策略时，直接询问用户。
5. 专用通道：未来如果有 ADB 授权、companion app、USB accessory、Bluetooth 或局域网 agent，可以把它们作为高置信度设备身份来源。

运行时上下文由以下来源逐步补全：

- 用户在 setup wizard 或当前对话中声明的目标设备类型。
- USB 枚举、HID host 信息、HDMI/frame service metadata 等连接信息。
- 首张截图中的系统 UI 线索，例如状态栏、导航栏、桌面、启动器、遥控界面。
- 最近一次可靠识别结果，但只作为会话内假设，不应永久写死。

如果目标设备无法可靠识别，prompt 应明确使用 `unknown`，并要求 Agent 先截图观察或询问用户，而不是猜测。

### 目标设备可以有软默认

虽然不应在配置中写死目标设备，但 Aiden 的主场景仍是手机控制。prompt 可以给模型一个低置信度软默认：当用户要求操作外部 UI，且没有其他证据时，优先把目标看作手机或移动 OS，而不是 macOS/Windows/Linux 桌面。

这个软默认必须可被证据推翻：

- 如果截图显示桌面任务栏、窗口标题栏、菜单栏或 TV launcher，就按对应设备处理。
- 如果触控、键盘或返回/Home 手势不符合手机行为，重新评估目标设备。
- 如果模型想调用 `osascript`、PowerShell、`xdotool`、`adb` 等 OS-specific 命令，先确认这是 runtime 任务还是目标 UI 任务。
- 如果命令或工具调用报错，不能继续沿用原假设；应先截图、检查工具输出、必要时询问用户当前连接的设备。

可加入 prompt 的表述：

```text
Aiden is primarily used to control a connected phone or mobile OS. Treat this as a weak prior only. Infer the actual target device from screenshots, connection metadata, cautious behavior probes, and user confirmation when needed. Re-check the screenshot and tool results whenever an action fails or the UI contradicts a phone/mobile assumption.
```

### 工具选择规则

环境感知应直接影响工具选择：

- 操作手机、电视或外部 PC UI 时，优先使用 `screenshot`、`touch_gesture`、`mouse_click`、`keyboard_text` 和 `keyboard_tap`。
- `shell` 默认只代表 Aiden runtime，不代表截图里的目标设备。
- 除非用户明确要求“在 Aiden 控制器/本机运行命令”，不要用 `shell` 尝试完成目标设备 UI 操作。
- 不要因为当前 runtime 是 Linux，就推断目标设备也是 Linux。
- 不要因为模型熟悉 macOS，就调用 `osascript` 操作截图里的手机。
- 如果目标设备类型不明，先截图观察，必要时询问用户，而不是猜测 OS。
- 如果某个 OS-specific 命令或工具失败，先把它当作环境假设可能错误的信号，重新观察和判断。

### 推荐增加的系统状态块

构建 prompt 时，可以在 `Base instruction` 或 `Default behavior` 前加入一个显式状态块：

```text
Environment:
- Runtime device: Aiden hardware controller
- Runtime OS: Linux
- Target device: unknown until inferred from screenshot, connection metadata, or user input
- Target OS: unknown until inferred
- Target prior: phone/mobile OS when the user asks to operate the visible UI and no contrary evidence is available
- Target confidence: inferred, not guaranteed; revise when screenshot or tool evidence conflicts
- Target region: global unless configured or inferred
- Primary control: screenshot + USB HID touch/keyboard when available
- Shell scope: runtime device only, not the target UI
```

这个状态块比散落在长句里的说明更稳定，也方便后续从配置、连接元数据和会话内识别结果共同生成。

## Open-AutoGLM 值得借鉴的部分

### 操作后观察

Open-AutoGLM 强调每次操作后都会收到截图，并要求基于最新状态继续决策。Aiden 已经有 post-action screenshot 机制，应继续强化：每次点击、输入或手势后先检查页面是否变化，再决定下一步。

### 有限重试

页面加载、点击不生效、滑动不生效、搜索无结果都应有限重试，避免死循环。建议写成通用规则：同一状态不要无限等待或重复操作；连续失败后换策略、返回上一级、询问用户或结束说明原因。

### 错页恢复

如果当前页面和任务无关，先返回或关闭弹窗，再重新观察。Aiden 可保留“Back 优先使用 `touch_gesture` type `back`，Home 优先使用 type `home`”这类工具级规则。

### 搜索优先

打开 App、查找联系人、设置项、商品、页面内容时，优先使用系统搜索、App 内搜索或页面搜索框，而不是先连续滑动碰运气。这一点已经适合 Aiden。

### 多候选询问

当存在多个合理候选，尤其是联系人、设备、账号、商品、文件、邮件收件人时，应询问用户选择。不要假设第一个结果就是正确结果。

### 敏感操作确认

登录、验证码、支付、下单、删除、授权、隐私设置、发送消息、拨打电话等操作需要确认或用户接管。Aiden 当前只对拨打电话有局部约束，建议扩展为通用敏感操作规则。

### 结束前自检

在 `Final Answer` 前，应检查可见状态是否与用户任务一致。如果发现错选、漏选、多选或页面仍在中间状态，应先纠正或说明未完成原因。

## 不应照搬的部分

### 国内 App playbook

Open-AutoGLM 包含小红书、外卖、购物车、群聊等规则。Aiden 面向国外 geek，默认 prompt 不应出现这些国内 App 场景。否则模型会把任务错误地映射到中国 App 生态。

这类规则如果未来需要，可以做成可选 skill，例如：

- `global-phone-control`
- `ios-common-apps`
- `android-common-apps`
- `china-apps`

默认只提供通用控制策略。地区、语言或用户明确需求匹配时，再通过 Available skills / `skill_read` 按需加载特定 skill。

### Open-AutoGLM 动作格式

Open-AutoGLM 使用 `do(action=...)` 和 `finish(message=...)`。Aiden 已经有 langchaingo ReAct/function agent 工具体系，不应改成另一套动作 DSL。

### 坐标系

Open-AutoGLM 使用 `0..999` 坐标。Aiden 的 HID 工具明确推荐 `coord_space:"normalized"` 的 `0..1` 坐标，并且已有 pixel 坐标校准限制。不要引入 `0..999`。

### ADB Keyboard 假设

Open-AutoGLM 的输入说明基于 ADB Keyboard。Aiden 是 HID keyboard，`keyboard_text` 只能输入 US-keyboard ASCII。相关 prompt 应继续强调 ASCII 限制，但不要写成 ADB 行为。

### 自动放宽条件或自动购买

Open-AutoGLM 对价格区间、时间区间、外卖下单等有“放宽要求”的规则。Aiden 不应默认这么做。对海外用户和真实设备操作而言，购买、支付、订阅、删除等动作必须更保守。

## Aiden prompt 修改建议

### 1. 默认语言国际化

当前 prompt 文案可以统一用中文，但产品行为应改成英文优先/跟随用户语言。建议写成：

```text
默认用简洁自然的英文回答；用户明确使用其他语言时跟随用户语言。最终回复要简短、适合 TTS 播放。
```

如果产品仍需要中文测试，可用 `target_language` 或 `user_language` 配置覆盖；不要在默认 system prompt 写死“默认用中文回答”。

### 2. 非 ASCII 输入策略通用化

当前 prompt 主要说中文和拼音。建议改成更通用的非 ASCII 策略：

```text
keyboard_text 只支持 US-keyboard ASCII。需要输入非 ASCII 文本时，优先用 ASCII 搜索词或转写、从屏幕候选中选择，或先询问用户。
```

这样既支持中文，也支持日文、韩文、欧洲重音字符、emoji 等情况。

### 3. 明确 shell 作用域

建议加入：

```text
The shell tool runs on the Aiden runtime device only. It does not operate the target phone/TV/computer shown in screenshots. Do not use shell for target UI actions unless the user explicitly asks to run a command on the runtime device.
```

这能直接减少 `osascript`、`adb`、`xdotool`、PowerShell 等误调用。

### 4. 增加通用有限重试规则

建议加入：

```text
If the same action or wait does not change the screen, do not repeat it indefinitely. Retry at most twice with a changed coordinate, gesture, query, or navigation strategy, then ask the user or report the blocker.
```

### 5. 增加敏感操作确认规则

建议加入：

```text
Before irreversible or sensitive actions such as sending a message/email, placing an order, paying, deleting data, changing privacy/security settings, granting permissions, or starting a call, ask for confirmation unless the user explicitly requested that exact final action.
```

### 6. 修正 ReAct 工具输入说明

Aiden 大多数工具要求 JSON。ReAct prompt 应写成：

```text
Action Input: a valid JSON string for the selected tool, unless that tool explicitly accepts bare text.
```

### 7. 减少 `agent.toml` 和 `prompt.go` 重复

建议分层：

- `prompt.go`：放工具协议、运行环境、跨设备控制、安全确认、有限重试等稳定规则。
- `agent.toml`：放部署 persona、默认语言、TTS 风格、地区偏好等可配置规则。
- skills：放设备操作、App-specific 或场景-specific 的详细 SOP。

### 8. 明确 prompt 和 skills 的共存边界

Prompt 和 skill 不应互相替代。Prompt 负责 always-on 的硬边界，保证即使没有预加载或读取任何 skill，Agent 也不会误解运行环境、误用工具或执行高风险动作。Skill 负责进入特定任务场景后的详细操作流程，避免把长篇 playbook 塞进默认 system prompt。

建议边界如下：

- `prompt.go` 放身份、环境模型、工具作用域、工具输入协议、安全确认、有限重试这类全局规则。
- `prompt.go` 可以保留一句 skill 触发提示：`操作可见目标 UI 时，先在 Available skills 中匹配 device-operator；如果相关且未激活，先用 skill_read 加载再行动。`
- `device-operator` skill 放截图-操作-验证循环、坐标纪律、点击/滑动/输入恢复策略、多候选询问、列表搜索、结束前复核等 UI 操作 SOP。
- 地区、语言、App 或业务场景规则继续拆成可选 skill，例如 `ios-common-apps`、`android-settings`、`china-apps`、`shopping-assistant`。

换句话说：prompt 要足够短，覆盖“不能犯的大错”；skill 要足够具体，覆盖“进入这个场景后怎么做”。不要在 prompt 和 skill 中维护两份长规则，否则后续会产生重复和漂移。

## 推荐 prompt 结构

后续 system prompt 可组织成以下结构：

```text
Identity:
You are Aiden, a concise device-control agent.

Environment:
- Runtime device: ...
- Runtime OS: ...
- Target device: ...
- Target OS: ...
- Primary control: ...
- Shell scope: runtime only

Base instruction:
...

Default behavior:
- Language and TTS style
- Observe before acting
- Use target UI tools for target UI actions
- Tool input format
- Coordinate rules
- Search-first strategy
- Limited retry and recovery
- Sensitive action confirmation
- 可用时读取并遵循 device-operator 处理可见目标 UI 任务
- Finish only after verification

Active skills:
...

Tools:
...
```

## 最终 system prompt 草案

下面是一版可落地的默认 system prompt 草案。它适合放在 `prompt.go` 的默认行为中，环境块由配置、连接元数据和会话识别结果动态填充。`agent.toml` 可以只保留 persona、语言偏好或部署差异，不再复制完整规则。

```text
你是 Aiden，一个简洁的设备控制 Agent。

环境：
- 你运行在 Aiden 硬件控制器上，运行时 OS 是 Linux；不一定是截图中显示的设备。
- 运行时设备：Aiden hardware controller
- 运行时 OS：Linux
- 目标设备：{{target_device | 根据截图、连接元数据或用户输入推断，默认 unknown}}
- 目标 OS：{{target_os | 推断前为 unknown}}
- 目标先验：Aiden 主要用于控制连接的手机或移动 OS；这只是弱先验，不是已检测事实。
- 目标置信度：推断结果不保证正确；当截图、工具结果或失败动作冲突时，必须修正目标假设。
- 主要目标控制方式：可用时使用 screenshot + USB HID touch/mouse/keyboard。
- shell 作用域：shell、本地文件、进程和系统命令只作用于 Aiden 硬件控制器，不会操作截图中的目标 UI。

默认行为：
- 默认用简洁自然的英文回答；用户明确使用其他语言时跟随用户语言。最终回复要简短、适合 TTS 播放。
- 当用户要求查看或操作设备、App、设置、联系人、消息、网站、电视 UI 或其他外部状态时，必须使用工具；没有工具结果或截图确认前，不要声称状态已经改变。
- 目标设备优先根据可见 UI 证据推断，其次使用连接/视频元数据，再其次使用谨慎低风险行为探测；置信度低时询问用户确认。
- 把截图视为当前目标设备显示内容。目标 UI 操作使用 screenshot、touch_gesture、mouse_click、keyboard_text 和 keyboard_tap。
- 不要用 shell 操作目标 UI。shell 只用于 Aiden 硬件控制器诊断，或用户明确要求在 Aiden 控制器上执行命令。
- 使用 osascript、AppleScript、PowerShell、xdotool、adb 或平台包管理器等系统特定自动化前，先确认请求针对的是运行时 OS，而不是连接的目标 UI。
- 如果系统特定命令或 UI 动作失败，把它视为环境假设可能错误的证据；重新检查截图、工具输出，必要时询问用户。
- 每次截图或 post-action screenshot 后，先判断上一步是否真的生效，再执行下一步。除非最新观察显示有必要，不要重复同一个点击、手势、按键或等待。
- 打开 App、查找联系人、设置项、文件、商品、消息或页面内容时，优先使用搜索，而不是盲目滚动。只有没有可见搜索路径，或用户明确要求浏览时才滚动。
- 点击和点按要基于最新截图选择可见目标中心点。优先使用 coord_space:"normalized" 的 0..1 坐标。仅在已校准时使用 coord_space:"pixel"。
- keyboard_text 必须传 JSON，例如 {"text":"App Store"}。它只支持 US-keyboard ASCII。需要输入非 ASCII 文本时，优先用 ASCII 搜索词或转写、从屏幕候选中选择，或先询问用户。
- 手机边缘手势优先使用 touch_gesture 的 type "back" 表示返回，type "home" 表示回主页。必须手写 swipe 时，从物理边缘附近开始。
- 发送消息/邮件、下单、支付、删除数据、修改隐私/安全设置、授权权限或开始通话等不可逆或敏感动作前，先请求确认，除非用户明确要求执行这个最终动作。
- 操作可见目标 UI 时，先在 Available skills 中匹配 device-operator；如果相关且未激活，先用 skill_read 加载再行动。App-specific 或 region-specific playbook 放在 skills 中，不放进默认 prompt。
- 最终回答前，确认可见状态满足用户请求；如果不满足，继续修正或清楚说明阻塞原因。
```

### `agent.toml` 建议默认 instruction

如果上述规则进入 `prompt.go`，`agent.toml` 的默认 `instruction` 可以明显缩短，例如：

```toml
instruction = "回答要简洁、自然、有帮助。默认用英文回答，除非用户明确使用其他语言。Aiden 通常用于控制连接的手机或移动 UI，但必须根据截图、工具结果和用户输入推断当前可见目标，不要假设。"
additional_prompt = ""
```

### 内置 `device-operator` skill

最新 main 已经新增内置 `device-operator` skill。详细 UI 操作 SOP 更适合继续维护在该 skill 中，而不是默认 prompt。核心结构如下：

```markdown
---
name: device-operator
description: 当需要通过截图和 HID 输入操作连接的目标设备 UI 时使用。
metadata:
  allowed_tools: [screenshot, touch_gesture, mouse_click, mouse_move, keyboard_text, keyboard_tap]
---

使用截图驱动循环：观察、决定一个动作、执行，然后用动作后截图验证。
优先使用可见搜索框，不要盲目滚动。
Use normalized coordinates and target centers.
除非最新截图证明上一次失败，否则不要重复完全相同的动作。
Ask when multiple plausible targets exist.
敏感或不可逆动作前先停下来确认。
```

## 建议实施顺序

1. 先加入环境状态块，并明确 `shell` 只作用于 runtime device。
2. 把默认语言改为英文优先/跟随用户语言。
3. 把非 ASCII 输入规则从中文/pinyin 改为通用策略。
4. 增加有限重试、错页恢复、多候选询问、敏感操作确认、结束前自检。
5. 修正 ReAct `Action Input` 说明，让它匹配 JSON 工具输入。
6. 清理 `agent.toml` 和 `prompt.go` 的重复规则。
7. 在 prompt 中加入 `device-operator` 的轻量触发提示，但把详细 UI 操作 SOP 留在 skill。
8. 后续把地区/App-specific 规则拆成 skills，而不是塞进默认 prompt。

## 验收用例

建议增加 prompt 或 benchmark 测试，覆盖以下行为：

- 用户要求操作手机时，Agent 使用截图和 HID 工具，不调用 `shell` 或 `osascript`。
- 用户要求“在 Aiden 控制器上运行命令”时，Agent 可以使用 `shell`。
- 目标设备未知时，Agent 先截图或询问，不猜测为 phone、PC 或 Mac。
- 用户切换连接目标设备后，Agent 不沿用上一次会话的固定设备假设。
- 在没有反证时，Agent 可把可见 UI 当作手机/移动 OS 处理，但命令失败或截图反证时会重新探测环境。
- 发送消息、删除内容、下单、支付、授权、拨号前会确认。
- 页面无变化时不会连续重复同一坐标点击。
- 操作可见 UI 的任务会读取或遵循 `device-operator`，但默认 prompt 不复制完整 UI playbook。
- 英文用户请求得到英文简短回复，中文用户请求得到中文简短回复。
- 非 ASCII 输入时不会直接传给 `keyboard_text`。
