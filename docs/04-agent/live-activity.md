# Live Activity / Dynamic Island

`【Aiden】灵动岛交互` 的定位是任务状态面板和回到 Aiden app 的入口，不是 iOS 后台执行能力。agent 继续执行任务，iOS app 负责展示状态。

Live Activity 有两条独立链路：

- 前台链路：app 轮询 agent 的 `live_activity` 状态，并用 ActivityKit 本地创建、更新、结束 Live Activity；不申请 push token，不注册 APNs，不需要后端。
- 后台链路：app 退后台、锁屏或未打开时，远程更新只能走 APNs。生产形态应由 Aiden 后端/relay 保存 Apple 凭证和 token，再把 agent 上报的状态推给 APNs。

## 状态模型

异步 chat 请求提供 `request_id` 时，agent 会为该请求维护一份 `live_activity` 快照：

```json
{
  "request_id": "chat_...",
  "status": "running",
  "task_title": "Open Settings",
  "current_step": "Checking the current screen",
  "last_tool_name": "screenshot",
  "progress": 0.21,
  "can_stop": true,
  "started_at": "2026-06-12T07:00:00Z",
  "updated_at": "2026-06-12T07:00:02Z"
}
```

状态值：

- `running`: 任务执行中，可展示步骤和进度。
- `completed`: 任务完成。
- `failed`: 任务失败，`last_error` 可展示失败原因。
- `canceled`: 用户取消或请求被中断。

`GET /api/chat/result?request_id=<id>&offset=<n>` 保持原响应兼容，并额外返回 `live_activity` 字段。iOS app 前台轮询时使用该字段本地更新 Live Activity。这条前台链路不使用 APNs。

## API

### 查询状态

```http
GET /api/live-activity/status?request_id=<id>
```

响应：

```json
{
  "status": "ok",
  "live_activity": {
    "request_id": "chat_...",
    "status": "running",
    "task_title": "Open Settings",
    "current_step": "Checking the current screen",
    "progress": 0.21,
    "can_stop": true
  }
}
```

### 后台远程更新 token 注册

前台本地 Live Activity 不注册 push token。只有后台远程更新链路需要 token，例如闭源 iOS app 或 Aiden 后端准备把任务状态通过 APNs 推到锁屏/灵动岛时，才注册 ActivityKit/APNs token：

```http
POST /api/live-activity/registrations
Content-Type: application/json

{
  "request_id": "chat_...",
  "activity_id": "activitykit-id",
  "push_token": "hex-token",
  "platform": "ios"
}
```

如果 APNs 已配置，agent 会在后续状态变化时发 `apns-push-type: liveactivity` 更新。未配置 APNs 时，注册仍会成功，但只表示 agent 收到了 token；前台本地更新不依赖这个接口。

## 配置

agent 侧状态快照默认启用。APNs 配置只针对后台/锁屏/灵动岛远程更新；app 前台本地更新不读取这些字段。

开发期可以让板子直接连 APNs 联调：

```toml
[live_activity]
enabled = true
bundle_id = "com.qing.aidenbridgedaily"
environment = "sandbox" # production for TestFlight/App Store
team_id = "APPLE_TEAM_ID"
key_id = "APNS_AUTH_KEY_ID"
private_key_path = "/userdata/agent/AuthKey_APNS.p8"
timeout_sec = 10
```

`topic` 默认是 `<bundle_id>.push-type.liveactivity`，只有特殊 bundle/topic 需求时才需要显式设置。

生产环境不要把 APNs `.p8` 放到开源仓库或用户板子。推荐形态是：

```text
agent -> Aiden 后端/relay -> APNs -> iPhone Live Activity
```

后端保存 APNs Auth Key、Team ID、Key ID、token 和设备绑定关系；agent 只上报任务状态。

## Apple 开发者前置项

需要在 Apple Developer / Xcode signing 中完成：

- App ID 启用 Push Notifications；这是后台 APNs 远程更新需要的能力，前台本地更新不依赖它。
- App 支持 Live Activities。
- 使用包含 Push Notifications 能力的 provisioning profile。
- 准备 APNs Auth Key `.p8`，记录 Team ID 和 Key ID。
- 开发调试使用 `environment = "sandbox"`；TestFlight/App Store 使用 `production`。

## App 端行为

iOS app 包含 `AidenLiveActivityExtension` Widget Extension 和 `AidenLiveActivityModule` native module。

- 前台 polling 到 `live_activity.status=running` 时，app 调用 ActivityKit `startOrUpdate`，使用本地 Live Activity，不申请 push token。
- polling 到 `completed`、`failed` 或 `canceled` 时，app 结束本地 Live Activity。
- 后台远程更新由 APNs/后端链路负责，不复用前台轮询路径。
- 用户点击灵动岛/锁屏 Live Activity 时，系统会回到 Aiden app。

## 联调顺序

1. 用 iOS 真机安装 app，确认 Widget Extension 被嵌入。
2. 前台发起 chat，确认 Live Activity 能本地出现并更新；这一步不需要 APNs，也不应出现 token 注册请求。
3. 准备后台链路：配置 Aiden 后端/relay 或开发期 agent 直连 APNs。
4. app 退后台后执行长任务，确认 APNs 能更新灵动岛/锁屏状态。
