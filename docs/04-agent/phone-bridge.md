# Phone Bridge：手机中转 App 方案

## 定位

在被控手机上安装一个中转 App，通过 USB ECM 网络通道连接硬件板子，接收板子的快捷指令，执行系统允许的本地操作，弥补纯硬件“找图标、滑屏、点击、等动画”慢的问题。

硬件负责看屏幕、决策和 HID 兜底；中转 App 负责快速执行公开系统能力。

## 第一期能力

| 能力 | iOS | Android | 解决的问题 |
| --- | --- | --- | --- |
| 打开指定 App | URL Scheme / Universal Link / `openURL` | 包名 / Intent | 省去找图标和点击流程 |
| 剪贴板读写 | UIPasteboard | Clipboard API | 作为跨 App 内容中转 |
| 日历 / 提醒 | EventKit | Calendar / Alarm / Reminder 能力 | 快速写入系统事项 |
| 通讯录 | Contacts | Contacts Provider | 查询 / 新增联系人 |
| 通知 | Local Notification | Notification | 提醒用户或拉回中转 App |
| 与板子通信 | WebSocket client | WebSocket client / foreground service | 接收板子快捷指令 |

## 通信方式

板子在 USB 网络中的固定地址是 `192.168.42.1`，所以推荐由手机中转 App 主动连接板子，而不是让板子反连手机 App。

推荐路径：

```text
手机中转 App -> ws://192.168.42.1:8080/api/phone-bridge
```

也可以单独开端口：

```text
手机中转 App -> ws://192.168.42.1:18080/phone-bridge
```

也就是：板子做 WebSocket server，App 做 WebSocket client。

这样不用猜手机 IP，不需要手机 App 开本地 HTTP 服务，重连和配网更简单。现有 `192.168.42.1:80` 配网页和 `192.168.42.1:8080` agent 测试页可以保留，只需新增一个 bridge path 或端口。

## 打开 App 流程

```text
板子 HDMI 看到屏幕 / 用户下达任务
        │
        ▼
AI 识别目标 App，比如“微信”
        │
        ▼
查映射表
iOS: weixin://
Android: com.tencent.mm
        │
        ▼
如果中转 App 已连接：
    发 open_app 指令
否则：
    先用 HID 打开 Aiden 中转 App
    等 App 自动连上板子
    再发 open_app 指令
        │
        ▼
中转 App 执行：
iOS: openURL("weixin://")
Android: Intent 启动包名
        │
        ├─ HDMI 验证成功 -> 继续下一步
        └─ 失败 / 超时 -> 回退硬件模拟：找图标坐标 + HID 点击
```

## 关键边界

iOS 上，中转 App 不是后台常驻系统代理。它更像一个前台快路径执行器：

```text
Aiden App 前台
-> 收到板子命令
-> openURL 打开微信
-> Aiden App 进入后台
-> 后续由硬件继续 HDMI 观察 + HID 操作
```

所以 iOS 不能承诺 App 在后台长期接收指令。Android 可以通过 foreground service 做得更稳定，后台长连能力更强。

## iOS 关键点

- 不要用 `canOpenURL` 做大规模预检。
- 直接调用 `openURL(url)` 尝试打开。
- `LSApplicationQueriesSchemes` 主要限制 `canOpenURL` 查询，不限制动态尝试 `openURL`。
- scheme 映射表放板子侧或云端维护，不打包进 App，方便更新。
- `openURL` 返回成功也不等于目标页面完全可用，最终以 HDMI 视觉验证为准。

## Android 关键点

- 优先用包名启动：`getLaunchIntentForPackage("com.tencent.mm")`。
- Android 11+ 有 package visibility 限制，需要配置 `<queries>`，或为特定场景评估 `QUERY_ALL_PACKAGES`。
- 后台启动 Activity 有系统限制，但 foreground service、通知和用户授权后的可用性比 iOS 强。

## 技术选型

React Native 可以作为 UI 和业务框架，但不能假设完全不需要 Native：

- 简单 `openURL`、WebSocket、UI：RN 可直接覆盖。
- Android 包名启动、前台服务、package visibility：需要 Kotlin / Java native module。
- iOS 本地网络权限、日历、联系人、HealthKit、App Intents：需要 Swift / 原生配置。
- 不建议 Expo Go；建议 bare React Native，或 Expo Prebuild + config plugin。长期看 bare RN 更稳。

### 是否找现成模板改

终态会演进到「聊天 + 配置 + Skill + 记忆管理 + 桥接执行」，是一个完整 App，不是一个简单 dispatcher。但**不建议直接 fork GitHub 上的 AI 对话模板**：

- 大多绑死 OpenAI / 直连 LLM，与「接板子 WebSocket」的定位冲突
- 常自带登录、订阅、云同步等无关功能，删除成本高于自建
- 多数基于 Expo Go，与本项目需要 native module 的方向冲突
- 教程级项目居多，不是生产级代码

可以**参考**结构和片段（如 `react-native-gifted-chat` 官方 example、chatbot-ui 类项目的消息流处理思路），但不直接作为代码基线。

### 推荐组合：bare RN + 成熟组件库

不找完整模板，站在轮子上拼：

| 需求 | 推荐 |
| --- | --- |
| 项目骨架 | `npx @react-native-community/cli init AidenBridge`（bare RN） |
| 导航 | `@react-navigation/native` + native-stack |
| 聊天 UI | `react-native-gifted-chat`，或自己用 `FlashList` 搭 |
| 状态管理 | `zustand`（轻量，不像 Redux 那么重） |
| 配置 / 记忆存储 | `react-native-mmkv`（比 AsyncStorage 快几十倍） |
| Markdown 渲染 | `react-native-markdown-display` |
| WebSocket | RN 内置 `WebSocket` API |

理由：

1. 第一期只要 WebSocket + `openURL`，几天就能上板子联调，不会被 UI 阻塞
2. 聊天界面后期增量加，不影响核心桥接链路
3. native module（包名启动、Calendar、Contacts、前台服务）必须自己写，模板帮不上忙
4. 维护自己懂的代码库 > 维护一个改过的别人代码库

## 为什么 USB 网络之外还要 WebSocket

USB ECM（`192.168.42.1`）和 WebSocket 是两层：

| 层 | 作用 |
| --- | --- |
| USB ECM | 网络链路——让手机和板子之间能通 IP 包 |
| WebSocket | 应用协议——在这条链路上传指令和回执 |

USB ECM 是"路修好了"，WebSocket 是"路上跑的车"。

现有 `192.168.42.1:80` 配网页是 HTTP 请求-响应模式，适合人操作网页。但板子需要**主动推指令给手机 App**（比如"现在打开微信"），HTTP 做不到——HTTP 是 client 发起的，server 不能主动找 client 说话。

WebSocket 的核心价值：

- **双向实时**：板子随时能推命令给 App，App 随时能回执
- **长连接**：不用每次重建连接，延迟低
- **状态感知**：连着即在线，断开立即可知，触发 HID 回退

## 第一期实现建议

1. 板子 agent 新增 `phone_bridge` WebSocket 通道。
2. 中转 App 启动后自动连接 `ws://192.168.42.1:8080/api/phone-bridge`。
3. App 定时发 heartbeat。
4. App 在连接成功、从后台回到前台时主动上报 `phone_environment`，包括系统版本、语言地区、时区、屏幕/电池、系统 App、第三方候选 App 可用性等。
5. 板子维护 `bridge_connected`、`platform`、`last_heartbeat_at`、`environment` 状态。完整环境通过 status API 暴露；Agent runtime context 只注入系统类型/版本、语言地区/时区、屏幕尺寸、已确认可打开第三方候选 App 等摘要。

### 命令协议

板子通过 WebSocket 向 App 发送 `BridgeCommand`，App 执行后回复 `BridgeCommandResponse`。

#### 通用字段

**BridgeCommand (板子 → App)**:
```json
{
  "id": "cmd_001",
  "type": "open_app | clipboard_read | clipboard_write | calendar_create | calendar_query | calendar_delete",
  "timeout_ms": 5000,
  "payload": { }  // 可选，命令相关的 JSON (剪贴板文本、日历事件等)
}
```

**BridgeCommandResponse (App → 板子)**:
```json
{
  "id": "cmd_001",
  "ok": true,
  "method": "open_url | clipboard | calendar",
  "error": "...",  // ok=false 时填充
  "data": { }      // 可选，返回的 JSON (读剪贴板内容、日历事件列表等)
}
```

**App 主动事件 (App → 板子)**:
```json
{
  "id": "phone_environment",
  "ok": true,
  "method": "phone_environment",
  "data": {
    "platform": "ios",
    "system_name": "iOS",
    "system_version": "18.5",
    "locale": "zh-Hans-CN",
    "time_zone": "Asia/Shanghai",
    "utc_offset": "+08:00",
    "manufacturer": "Apple",
    "model": "iPhone16,2",
    "screen": {"width_pixels": 1179, "height_pixels": 2556, "scale": 3},
    "battery": {"level": 0.87, "charging": true},
    "system_apps": [{"name": "Camera", "available": true, "category": "system", "availability_source": "builtin"}],
    "third_party_apps": [{"name": "WeChat", "available": true, "category": "third_party", "availability_source": "can_open_url"}],
    "available_apps": [{"name": "WeChat", "available": true, "category": "third_party"}]
  }
}
```

`phone_environment` 不对应板子命令 ID，板子只更新 bridge status，不会把它当作工具调用回执。
`system_apps` 是系统内置 App/能力清单，`third_party_apps` 才是安装/可打开性探测结果。`available_apps` 仅保留为旧板子兼容字段。

#### 命令类型

##### 1. `open_app` — 打开 App

```json
{
  "id": "open_001",
  "type": "open_app",
  "ios_urls": ["weixin://"],
  "android_packages": ["com.tencent.mm"],
  "timeout_ms": 10000
}
```

回复:
```json
{
  "id": "open_001",
  "ok": true,
  "method": "open_url"
}
```

浏览器语义和固定网页语义分开：打开浏览器入口时使用浏览器 URL scheme / Android browser intent；打开固定网页时传具体 `http`/`https` URL（iOS 放入 `ios_urls`，Android 使用 `android.intent.action.VIEW:<url>`）。

##### 2. `clipboard_read` — 读剪贴板

```json
{
  "id": "clip_read_001",
  "type": "clipboard_read",
  "timeout_ms": 5000
}
```

回复:
```json
{
  "id": "clip_read_001",
  "ok": true,
  "data": {
    "text": "剪贴板内容"
  }
}
```

##### 3. `clipboard_write` — 写剪贴板

```json
{
  "id": "clip_write_001",
  "type": "clipboard_write",
  "payload": {
    "text": "要复制的内容"
  },
  "timeout_ms": 5000
}
```

回复:
```json
{
  "id": "clip_write_001",
  "ok": true
}
```

##### 4. `calendar_create` — 创建日历事件

```json
{
  "id": "cal_create_001",
  "type": "calendar_create",
  "payload": {
    "title": "牙医预约",
    "start": "2026-06-02T15:00:00+08:00",
    "end": "2026-06-02T16:00:00+08:00",
    "all_day": false,
    "location": "诊所",
    "notes": "带医保卡",
    "alarm_minutes_before": 30
  },
  "timeout_ms": 8000
}
```

回复:
```json
{
  "id": "cal_create_001",
  "ok": true,
  "data": {
    "event_id": "ios_calendar_id_123"
  }
}
```

##### 5. `calendar_query` — 查询日历事件

```json
{
  "id": "cal_query_001",
  "type": "calendar_query",
  "payload": {
    "start": "2026-06-02T00:00:00+08:00",
    "end": "2026-06-03T00:00:00+08:00"
  },
  "timeout_ms": 8000
}
```

回复:
```json
{
  "id": "cal_query_001",
  "ok": true,
  "data": {
    "events": [
      {
        "event_id": "...",
        "title": "牙医预约",
        "start": "2026-06-02T15:00:00+08:00",
        "end": "2026-06-02T16:00:00+08:00",
        "location": "诊所"
      }
    ]
  }
}
```

##### 6. `calendar_delete` — 删除日历事件

```json
{
  "id": "cal_delete_001",
  "type": "calendar_delete",
  "payload": {
    "event_id": "ios_calendar_id_123"
  },
  "timeout_ms": 8000
}
```

回复:
```json
{
  "id": "cal_delete_001",
  "ok": true
}
```

##### 7. `contacts_query` — 查询通讯录

```json
{
  "id": "contacts_query_001",
  "type": "contacts_query",
  "payload": {
    "query": "张三",
    "limit": 20
  },
  "timeout_ms": 8000
}
```

回复:
```json
{
  "id": "contacts_query_001",
  "ok": true,
  "data": {
    "contacts": [
      {
        "contact_id": "contact_123",
        "name": "张三",
        "phone_numbers": ["+86 138 1234 5678"],
        "emails": ["zhangsan@example.com"]
      }
    ]
  }
}
```

##### 8. `contacts_create` — 新增联系人

```json
{
  "id": "contacts_create_001",
  "type": "contacts_create",
  "payload": {
    "name": "李四",
    "phone_numbers": ["+86 139 8765 4321"],
    "emails": ["lisi@example.com"],
    "organization": "公司名",
    "notes": "备注信息"
  },
  "timeout_ms": 8000
}
```

回复:
```json
{
  "id": "contacts_create_001",
  "ok": true,
  "data": {
    "contact_id": "new_contact_id_123"
  }
}
```

##### 9. `contacts_update` — 修改联系人

```json
{
  "id": "contacts_update_001",
  "type": "contacts_update",
  "payload": {
    "contact_id": "contact_123",
    "name": "李四（更新）",
    "phone_numbers": ["+86 139 8765 4321", "+86 010 1234 5678"],
    "emails": ["lisi_new@example.com"]
  },
  "timeout_ms": 8000
}
```

回复:
```json
{
  "id": "contacts_update_001",
  "ok": true
}
```

##### 10. `notification_send` — 发送通知

```json
{
  "id": "notification_001",
  "type": "notification_send",
  "payload": {
    "title": "提醒",
    "body": "该吃药了",
    "schedule_at": "2026-06-04T18:00:00+08:00",
    "sound": true,
    "badge": 1
  },
  "timeout_ms": 5000
}
```

回复:
```json
{
  "id": "notification_001",
  "ok": true,
  "data": {
    "notification_id": "notification_123"
  }
}
```

### 时间格式

所有时间字段统一用 **RFC3339 with offset**，例如：
- `2026-06-02T15:00:00+08:00` (东八区下午3点)
- `2026-06-02T07:00:00Z` (UTC 早上7点)

板子侧的 `current_time` 工具可以给模型提供当前时区基准。

### 权限与隐私

- **剪贴板读取**: iOS 16+ 会弹一次性授权横幅，频繁读会影响体验。Android 10+ 要求前台 app 或 foreground service。
- **日历读写**: iOS 和 Android 都需要运行时权限，首次调用时弹授权框。App 收到命令时如果权限未授予，应返回 `ok:false, error:"需要日历权限"`，超时由板子侧 `timeout_ms` 控制。
- **通讯录读写**: iOS 需要 `NSContactsUsageDescription` 权限，Android 需要 `READ_CONTACTS` 和 `WRITE_CONTACTS` 权限。未授权时返回 `ok:false, error:"需要通讯录权限"`。
- **通知权限**: iOS 需要通过 `UNUserNotificationCenter` 请求授权，Android 13+ 需要 `POST_NOTIFICATIONS` 权限。未授权时返回 `ok:false, error:"需要通知权限"`。

### 实施注意

7. 板子再通过 HDMI 验证目标 App 是否打开（仅 `open_app`）。
8. 验证失败自动回退 HID（仅 `open_app`；剪贴板/日历无 HID 兜底，失败直接告诉用户）。

## 最终定位

这套方案不是替代硬件控制，而是增加一条软件快路径：

```text
能软件快速完成的，走中转 App；
软件做不到或不稳定的，继续走 HDMI + HID。
```

iOS 以“前台快路径 + 硬件兜底”为主。Android 可以逐步增强为“后台常驻中转 + 硬件兜底”。
