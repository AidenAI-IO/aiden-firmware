# Phone Bridge Protocol Contract

**Version**: 1.1
**Date**: 2026-06-10

本文档定义硬件板子 (aiden-hardware-demo) 与手机 App (aiden-app) 之间的 WebSocket 命令协议。

## 连接

- **URL**: `ws://192.168.42.1:8080/api/phone-bridge?platform=ios` (或 `platform=android`)
- **方向**: App 作为 WebSocket client 主动连接板子 (WebSocket server)
- **网络**: 通过 USB ECM 建立的 `192.168.42.0/24` 子网，板子固定 IP `192.168.42.1`

## 心跳

App 应每隔 10-15 秒发送一次心跳消息 (id 为 `"heartbeat"` 或 `"ping"` 的 JSON)，板子会原样回显，App 以此检测连接活性。

示例:
```json
{"id": "heartbeat", "ok": true}
```

板子会记录 `last_heartbeat_at` 时间戳，超过 60 秒未收到心跳视为连接不健康。

## 消息格式

### BridgeCommand (板子 → App)

```typescript
{
  id: string;              // 唯一命令 ID
  type: string;            // 命令类型 (见下方)
  timeout_ms?: number;     // 超时毫秒 (可选，默认 5000)
  
  // 以下字段根据 type 使用
  ios_urls?: string[];           // open_app 专用
  android_packages?: string[];   // open_app 专用
  payload?: object;              // 其他命令类型的 JSON 负载
}
```

### BridgeCommandResponse (App → 板子)

```typescript
{
  id: string;       // 与 BridgeCommand.id 一致
  ok: boolean;      // 执行成功 true，失败 false
  method?: string;  // 执行方式 (可选)
  error?: string;   // ok=false 时填充错误信息
  data?: object;    // 返回数据 (可选，读类命令使用)
}
```

### AppEvent (App → 板子)

App 也可以主动发送事件消息。事件复用 `BridgeCommandResponse` 外层字段，但 `id`/`method` 不对应任何板子下发的命令，板子不会把它当作 pending command 回执。

当前事件:

- `phone_environment`: App 在 WebSocket 连接成功、从后台回到前台时上报手机环境快照。

示例:

```json
{
  "id": "phone_environment",
  "ok": true,
  "method": "phone_environment",
  "data": {
    "captured_at": "2026-06-10T03:20:00Z",
    "source": "aiden-app",
    "platform": "ios",
    "system_name": "iOS",
    "system_version": "18.5",
    "is_tablet": false,
    "locale": "zh-Hans-CN",
    "language": "zh",
    "region": "CN",
    "time_zone": "Asia/Shanghai",
    "utc_offset_minutes": 480,
    "utc_offset": "+08:00",
    "uses_24_hour_clock": true,
    "manufacturer": "Apple",
    "brand": "Apple",
    "model": "iPhone16,2",
    "screen": {
      "width": 393,
      "height": 852,
      "width_pixels": 1179,
      "height_pixels": 2556,
      "scale": 3
    },
    "battery": {
      "level": 0.87,
      "charging": true,
      "state": "charging"
    },
    "system_apps": [
      {"name": "Camera", "available": true, "category": "system", "availability_source": "builtin"},
      {"name": "Contacts", "available": true, "category": "system", "availability_source": "builtin"}
    ],
    "third_party_apps": [
      {"name": "WeChat", "available": true, "category": "third_party", "availability_source": "can_open_url", "ios_url": "weixin://"},
      {"name": "Douyin", "available": false, "category": "third_party", "availability_source": "can_open_url", "ios_url": "snssdk1128://"}
    ],
    "available_apps": [
      {"name": "WeChat", "available": true, "category": "third_party", "ios_url": "weixin://"},
      {"name": "Douyin", "available": false, "category": "third_party", "ios_url": "snssdk1128://"}
    ]
  }
}
```

板子会把最新完整环境写入 `GET /api/phone-bridge/status` 的 `environment` 字段。每轮 Agent runtime context 只注入精简摘要：连接状态、系统类型/版本、语言地区/时区、屏幕尺寸、已确认可打开的第三方候选 App。断开连接时清理环境，避免使用旧信息。

`system_apps` 表示系统内置 App/能力，iOS 上不依赖 `canOpenURL` 判断是否存在；`third_party_apps` 表示第三方候选 App，iOS 通过 `canOpenURL`、Android 通过 package launchability 探测。`available_apps` 是兼容旧板子的第三方候选摘要，新实现应优先读取拆分后的字段。

## 命令类型

### 1. `open_app`

打开指定 App 或 URL。

**请求**:
```json
{
  "id": "open_001",
  "type": "open_app",
  "ios_urls": ["weixin://"],
  "android_packages": ["com.tencent.mm"],
  "timeout_ms": 10000
}
```

**iOS 实现**: 调用 `UIApplication.shared.open(URL(string: ios_urls[0])!)` 尝试打开第一个 URL。  
**Android 实现**: 调用 `packageManager.getLaunchIntentForPackage(android_packages[0])` 或解析 deeplink。

浏览器入口不要绑定固定网站：打开浏览器本身时传浏览器 URL scheme / Android browser intent；打开指定网页时传具体 `http`/`https` URL（Android 使用 `android.intent.action.VIEW:<url>`）。

**响应**:
```json
{
  "id": "open_001",
  "ok": true,
  "method": "ios_url_scheme"
}
```

`method` 表示 App 侧采用的底层机制，常见值包括 `ios_url_scheme`、`ios_shortcut`、`android_intent`、`android_deeplink`、`launch_package`、`dial`、`open_url`。其中 `open_url` 只表示显式网页 URL。
Agent 暴露的 `open_app` 工具会把这些底层机制归一化为面向任务的 `method`（例如打开 App 返回 `open_app`，打开网页返回 `open_url`），并把底层值放在 `mechanism` 字段中。

失败时:
```json
{
  "id": "open_001",
  "ok": false,
  "error": "App not installed"
}
```

---

### 2. `clipboard_read`

读取系统剪贴板内容。

**请求**:
```json
{
  "id": "clip_read_001",
  "type": "clipboard_read",
  "timeout_ms": 5000
}
```

**iOS 实现**: `UIPasteboard.general.string`  
**Android 实现**: `ClipboardManager.getPrimaryClip()`

**响应**:
```json
{
  "id": "clip_read_001",
  "ok": true,
  "data": {
    "text": "剪贴板内容"
  }
}
```

空剪贴板返回 `"text": ""`。

**权限**:
- iOS 16+ 会显示粘贴横幅，频繁读会打扰用户
- Android 10+ 只有前台 app 可读剪贴板，后台需要 foreground service

---

### 3. `clipboard_write`

写入系统剪贴板。

**请求**:
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

**iOS 实现**: `UIPasteboard.general.string = payload.text`  
**Android 实现**: `ClipboardManager.setPrimaryClip(ClipData.newPlainText("label", text))`

**响应**:
```json
{
  "id": "clip_write_001",
  "ok": true
}
```

---

### 4. `calendar_create`

创建日历事件。

**请求**:
```json
{
  "id": "cal_create_001",
  "type": "calendar_create",
  "payload": {
    "title": "牙医预约",
    "start_at": "2026-06-02T15:00:00+08:00",
    "end_at": "2026-06-02T16:00:00+08:00",
    "all_day": false,
    "location": "诊所",
    "notes": "带医保卡",
    "alarm_minutes_before": 30
  },
  "timeout_ms": 8000
}
```

**字段说明**:
- `title` (必需): 事件标题
- `start_at` (必需): 开始时间 (RFC3339 格式，带时区)
- `end_at` (可选): 结束时间，未提供时默认 start_at + 1 小时
- `all_day` (可选): 是否全天事件，默认 false
- `location` (可选): 地点
- `notes` (可选): 备注
- `alarm_minutes_before` (可选): 提前多少分钟提醒，默认不设提醒

**iOS 实现**: 使用 `EventKit` 框架，需要 `NSCalendarsUsageDescription` 或 `NSCalendarsWriteOnlyAccessUsageDescription` 权限。  
**Android 实现**: 使用 `CalendarContract` API，需要 `WRITE_CALENDAR` 权限。

**响应**:
```json
{
  "id": "cal_create_001",
  "ok": true,
  "data": {
    "event_id": "ios_calendar_id_123"
  }
}
```

`event_id` 是平台返回的事件唯一标识，用于后续删除。

---

### 5. `calendar_query`

查询指定时间范围内的日历事件。

**请求**:
```json
{
  "id": "cal_query_001",
  "type": "calendar_query",
  "payload": {
    "start_at": "2026-06-02T00:00:00+08:00",
    "end_at": "2026-06-03T00:00:00+08:00"
  },
  "timeout_ms": 8000
}
```

**iOS 实现**: `EKEventStore.events(matching:)` 查询 `start_at` 到 `end_at` 范围。  
**Android 实现**: 查询 `CalendarContract.Instances` 表，需要 `READ_CALENDAR` 权限。

**响应**:
```json
{
  "id": "cal_query_001",
  "ok": true,
  "data": {
    "events": [
      {
        "event_id": "ios_calendar_id_123",
        "title": "牙医预约",
        "start_at": "2026-06-02T15:00:00+08:00",
        "end_at": "2026-06-02T16:00:00+08:00",
        "location": "诊所"
      }
    ]
  }
}
```

无事件时返回空数组 `"events": []`。

---

### 6. `calendar_delete`

删除指定日历事件。

**请求**:
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

**iOS 实现**: `EKEventStore.remove(event:, span:, commit:)`  
**Android 实现**: `ContentResolver.delete(CalendarContract.Events.CONTENT_URI, ...)`

**响应**:
```json
{
  "id": "cal_delete_001",
  "ok": true
}
```

如果事件不存在，可返回 `ok: false, error: "Event not found"`，也可返回 `ok: true` (幂等删除)。

---

### 7. `contacts_query`

查询通讯录联系人。

**请求**:
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

**字段说明**:
- `query` (可选): 搜索关键词，匹配姓名或电话号码
- `limit` (可选): 最多返回数量，默认 20

**iOS 实现**: 使用 `CNContactStore` 查询，需要 `NSContactsUsageDescription` 权限。  
**Android 实现**: 查询 `ContactsContract` API，需要 `READ_CONTACTS` 权限。

**响应**:
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

无匹配联系人时返回空数组 `"contacts": []`。

---

### 8. `contacts_create`

新增联系人。

**请求**:
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

**字段说明**:
- `name` (必需): 联系人姓名
- `phone_numbers` (可选): 电话号码数组
- `emails` (可选): 邮箱地址数组
- `organization` (可选): 公司/组织名称
- `notes` (可选): 备注信息

**iOS 实现**: 使用 `CNContactStore.add(CNSaveRequest)` 创建，需要 `NSContactsUsageDescription` 权限。  
**Android 实现**: 使用 `ContentResolver.insert(ContactsContract.RawContacts.CONTENT_URI)`，需要 `WRITE_CONTACTS` 权限。

**响应**:
```json
{
  "id": "contacts_create_001",
  "ok": true,
  "data": {
    "contact_id": "new_contact_id_123"
  }
}
```

`contact_id` 是平台返回的联系人唯一标识，用于后续更新。

---

### 9. `contacts_update`

修改已有联系人。

**请求**:
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

**字段说明**:
- `contact_id` (必需): 要更新的联系人 ID
- 其他字段同 `contacts_create`，提供的字段会覆盖原有值

**iOS 实现**: 使用 `CNContactStore.execute(CNSaveRequest)` 更新联系人。  
**Android 实现**: 使用 `ContentResolver.update()` 更新 `ContactsContract.Data` 表。

**响应**:
```json
{
  "id": "contacts_update_001",
  "ok": true
}
```

如果联系人不存在，返回 `ok: false, error: "Contact not found"`。

---

### 10. `notification_send`

发送本地通知。

**请求**:
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

**字段说明**:
- `title` (必需): 通知标题
- `body` (可选): 通知正文
- `schedule_at` (可选): 定时发送时间 (RFC3339)，不填则立即发送
- `sound` (可选): 是否播放声音，默认 true
- `badge` (可选): 应用角标数字 (iOS)

**iOS 实现**: 使用 `UNUserNotificationCenter` 发送本地通知，需要用户授权。  
**Android 实现**: 使用 `NotificationManager` 和 `AlarmManager` (定时)，Android 13+ 需要 `POST_NOTIFICATIONS` 权限。

**响应**:
```json
{
  "id": "notification_001",
  "ok": true,
  "data": {
    "notification_id": "notification_123"
  }
}
```

`notification_id` 可用于后续取消通知（未来扩展）。

---

## 错误处理

当 App 无法执行命令时，应返回 `ok: false` 和 `error` 字段:

```json
{
  "id": "...",
  "ok": false,
  "error": "需要日历权限"
}
```

常见错误场景:
- 权限未授予: `"需要日历权限"` / `"需要剪贴板权限"` / `"需要通讯录权限"` / `"需要通知权限"`
- App 未安装: `"App not installed"`
- 无效参数: `"Invalid start time format"`
- 系统 API 失败: `"Calendar API error: ..."` / `"Contacts API error: ..."`
- 联系人不存在: `"Contact not found"`

## 超时与重连

- **超时**: 板子侧每个命令都有 `timeout_ms`，超时后放弃等待响应。App 应尽量在超时前响应。
- **重连**: WebSocket 断开后，App 应自动重连，间隔 3-5 秒重试。板子侧没有主动重连机制。
- **幂等**: 板子可能因超时重发命令，App 应尽量做到幂等 (如删除不存在的事件返回成功)。

## 时间格式

所有时间字段必须是 **RFC3339 格式且带时区偏移**，例如:
- `2026-06-02T15:00:00+08:00` (东八区下午3点)
- `2026-06-02T07:00:00Z` (UTC 早上7点)

解析时使用标准库 (iOS `ISO8601DateFormatter`, Android `Instant.parse`)。

## 权限管理

### iOS

- **剪贴板读取**: iOS 14+ 会显示横幅，无需声明权限
- **日历**: 必须在 `Info.plist` 添加:
  ```xml
  <key>NSCalendarsUsageDescription</key>
  <string>用于快速创建和管理日历事件</string>
  ```
  iOS 17+ 细分为 `NSCalendarsFullAccessUsageDescription` (读写) 和 `NSCalendarsWriteOnlyAccessUsageDescription` (只写)。
- **通讯录**: 必须在 `Info.plist` 添加:
  ```xml
  <key>NSContactsUsageDescription</key>
  <string>用于查询和管理联系人</string>
  ```
- **通知**: 需要通过 `UNUserNotificationCenter.requestAuthorization` 请求用户授权。

### Android

- **剪贴板**: Android 10+ 后台无法读剪贴板，需要 foreground service 或确保 App 在前台。
- **日历**: 需要运行时权限:
  ```xml
  <uses-permission android:name="android.permission.READ_CALENDAR" />
  <uses-permission android:name="android.permission.WRITE_CALENDAR" />
  ```
  首次调用时弹授权框，拒绝后返回 `ok: false, error: "需要日历权限"`。
- **通讯录**: 需要运行时权限:
  ```xml
  <uses-permission android:name="android.permission.READ_CONTACTS" />
  <uses-permission android:name="android.permission.WRITE_CONTACTS" />
  ```
- **通知**: Android 13+ 需要运行时权限:
  ```xml
  <uses-permission android:name="android.permission.POST_NOTIFICATIONS" />
  ```

## 测试建议

1. **Mock 测试**: App 可在 WebSocket 连接失败时进入 mock 模式，本地模拟命令响应，便于开发调试。
2. **超时场景**: 测试权限弹框期间超时，确认 App 正确处理用户授权后的后续命令。
3. **边界情况**: 空剪贴板、无日历事件、无效 `event_id`、全天事件、跨时区查询、空通讯录查询、重复联系人、无效 `contact_id`、定时通知取消等。

## 版本兼容

当前协议版本 1.1，后续扩展新命令时:
- 新增字段向后兼容 (旧版 App 忽略未知字段)
- 新增命令类型，旧版 App 返回 `ok: false, error: "Unknown command type"`
- 修改已有字段语义需升级版本号

---

## 附录: 完整示例

### 心跳
**App → 板子**:
```json
{"id": "heartbeat", "ok": true}
```

**板子 → App (回显)**:
```json
{"id": "heartbeat", "ok": true}
```

### 打开微信
**板子 → App**:
```json
{
  "id": "open_1717667890123_1",
  "type": "open_app",
  "ios_urls": ["weixin://"],
  "android_packages": ["com.tencent.mm"],
  "timeout_ms": 10000
}
```

**App → 板子**:
```json
{
  "id": "open_1717667890123_1",
  "ok": true,
  "method": "ios_url_scheme"
}
```

### 读剪贴板
**板子 → App**:
```json
{
  "id": "clip_read_1717667890234_2",
  "type": "clipboard_read",
  "timeout_ms": 5000
}
```

**App → 板子**:
```json
{
  "id": "clip_read_1717667890234_2",
  "ok": true,
  "data": {
    "text": "https://example.com"
  }
}
```

### 创建日历事件
**板子 → App**:
```json
{
  "id": "cal_create_1717667890345_3",
  "type": "calendar_create",
  "payload": {
    "title": "团队会议",
    "start_at": "2026-06-05T10:00:00+08:00",
    "end_at": "2026-06-05T11:00:00+08:00",
    "location": "会议室 A",
    "notes": "讨论 Q2 规划",
    "alarm_minutes_before": 15
  },
  "timeout_ms": 8000
}
```

**App → 板子**:
```json
{
  "id": "cal_create_1717667890345_3",
  "ok": true,
  "data": {
    "event_id": "12345678-ABCD-1234-5678-1234567890AB"
  }
}
```
