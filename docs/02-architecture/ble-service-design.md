# Aiden BLE 基础服务设计

> 状态：Draft v5
>
> 对应任务：`【Aiden】蓝牙基建与接入系统通知`
>（`3590beba-a111-496e-ba6e-909e734ffc59`）。

## 1. 设计范围

本任务建设 Aiden 的 BLE 公共底座，使 iPhone 在 Aiden App 的引导下与硬件建立系统
Bluetooth bond、向板端提供 ANCS 系统通知，并为 App 后台工具提供轻量唤醒能力。

首次配对采用“App 编排、iOS 系统确认、板端持久化”的单一路径：App 通过 USB ECM 调用
Agent 开放限时配对窗口，再用 CoreBluetooth 连接加密 Wake Characteristic；真正的 bond
仍由 iOS 系统弹窗确认。App 不能静默绕过系统确认，也不能通过公开 API 删除 iPhone 侧
bond。

HOGP 继续作为 iOS 系统可识别的标准 Profile，使 Aiden 在系统蓝牙设置中具有可管理的
设备条目、自动重连和“忽略此设备”入口，但不再要求用户直接从系统设置发起首次配对。
首次绑定需要 App；绑定完成后，即使 App 未运行或被卸载，HOGP 自动重连和板端 ANCS
消费仍由 iOS 与 `ble_service` 维持，不把持续运行能力绑定到 App 前台生命周期。

## 2. 目标与非目标

### 2.1 本版目标

- 为 Pico Zero 启用 Bluetooth 内核能力并初始化 AIC8800 `hci0`；
- 默认启动 BlueZ，并将配对信息持久化到 `/userdata`；
- 新增独立 `ble_service`，统一管理 GATT、配对、连接和 ANCS；
- 发布最小 BLE HID/HOGP，使系统能够管理 bond、自动重连并提供“忽略此设备”入口；
- 发布始终可用的 Aiden Control/Wake Service；
- 作为 ANCS Consumer 接收 iOS 系统通知并转换为统一事件；
- 通过 UDS 向 Agent 提供状态、Wake 和通知事件读取接口；
- Agent 提供 BLE 状态、开始配对和清除板端 bond 的 HTTP API；
- App 提供“蓝牙与系统通知”UI，负责打开配对窗口、展示状态和发起解绑；
- App 通过 CoreBluetooth 触发系统配对、状态恢复、自动连接并订阅 Wake；
- App 打开时复用已有系统 bond，不要求用户再次配对；
- BLE 故障不影响 USB HID、USB ECM、Wi-Fi 或 Phone Bridge HTTP Queue。

### 2.2 不在本版完成

- 不将 ANCS 通知写入 Agent memory；
- 不根据通知自动触发 Agent 动作；
- 不通过 BLE 传输 Phone Bridge 命令、结果、截图或音频；
- 不在本任务重构联系人、日历、剪贴板等 App 工具；
- 不实现 Android Notification Listener；
- 不把 BLE HID 扩展为新的完整键盘、鼠标或触控主通道。
- 不尝试通过私有 API 静默确认配对或删除 iPhone 侧系统 bond。

后续任务只消费本任务提供的稳定接口：

- “通过蓝牙获取通知，更新 memory”消费 `events_since`；
- “重构 App 工具，适配蓝牙唤醒后台 App 工具执行”消费 Wake，并继续通过 HTTP Queue
  获取命令和提交结果。

## 3. 核心设计

### 3.1 单一 BLE 服务

`ble_service` 是板端唯一 BLE 业务进程，负责：

- 向 BlueZ 注册 HOGP 和 Aiden Control/Wake GATT；
- 管理可发现、配对、bond 和已连接设备；
- 发现并订阅 iPhone 暴露的 ANCS；
- 解析通知并保存在有界内存队列；
- 通过 UDS 向 Agent 暴露能力。

Agent 不直接操作 BlueZ。这样 BLE 生命周期与 Agent 解耦，后续通知、App Wake 或其他
低带宽 BLE 能力都建立在同一底座上。

### 3.2 系统架构

```text
                              iPhone
        +------------------------------------------------+
        | iOS Bluetooth System                           |
        | - 系统配对确认 / bond / HID 自动重连           |
        | - Settings “忽略此设备”                       |
        | - ANCS Notification Provider                   |
        +-----------------------+------------------------+
                                | 同一条 BLE 连接
        +-----------------------+------------------------+
        | Aiden App                                      |
        | - 通过 USB ECM 请求板端开放配对窗口            |
        | - CoreBluetooth 发起连接并触发系统配对         |
        | - 订阅 Wake                                    |
        | - 展示配对状态和解绑引导                       |
        | - Wake 后通过 USB ECM 拉取 HTTP Queue          |
        +-----------------------+------------------------+
                                |
                                v
+----------------------------------------------------------------+
| Luckfox Pico Zero                                              |
|                                                                |
| AIC8800 -> hci0 -> bluetoothd -> ble_service                   |
|                                  - HOGP server                 |
|                                  - Control/Wake GATT server    |
|                                  - ANCS consumer               |
|                                  - event ring                  |
|                                  - UDS server                  |
|                                        |                       |
|                                        v                       |
|                                      Agent                     |
|                                        |                       |
|                              USB ECM HTTP Queue                |
+----------------------------------------------------------------+
```

BLE 同时承担两个 GATT 方向：Aiden 作为 Peripheral 向 iPhone 提供 HOGP 和 Wake；在同一
连接上，Aiden 又作为 GATT Client 读取 iPhone 的 ANCS。这需要使用 AIC8800、BlueZ 和
iPhone 真机验证。

### 3.3 启动分层

```text
S35wifidrv -> S39hciinit -> S40bluetoothd -> S41ble_service
```

| 组件 | 职责 |
| --- | --- |
| `S39hciinit` | 将 `/dev/ttyS1` 接入 HCI，创建并启动 `hci0` |
| `S40bluetoothd` | 启动 BlueZ，挂载持久化 bond 目录 |
| `S41ble_service` | 启动并守护 `ble_service` |
| `ble_service` | GATT、HOGP、配对、Wake、ANCS 和 UDS |
| Agent | 消费 UDS；提供 App 配对 HTTP API；Phone Bridge 命令仍进入 HTTP Queue |

内核必须同时包含现有 zram 配置和 Bluetooth 配置。板子首次启用蓝牙需要刷入包含相关
内核配置的新镜像；完成后，普通 `ble_service` 迭代只需交叉编译、部署二进制并重启服务。

## 4. GATT 与连接设计

### 4.1 最小 HOGP

系统需要一个可管理的标准 Profile，否则只有自定义 GATT 的 bond 可能缺少清晰的系统设备
条目，用户无法可靠找到“忽略此设备”。

本版实现最小 HID over GATT Profile：

- 使用标准 HID Service `0x1812`；
- 首选副作用较小的 Consumer Control 报告；
- HOGP 本版主要用于系统 bond 管理、系统设置设备条目和连接保活，不替代现有 USB HID；
- 默认不通过 BLE 发送键盘输入，避免 USB HID 与 BLE HID 重复执行；
- 如果 Consumer Control 无法在目标 iOS 版本形成稳定配对和重连，再真机评估 Keyboard
  Profile；不能为了配对而默认引入额外软键盘或密码输入体验。

HOGP 类型和 Report Map 一旦与手机建立 bond，不得静默变更；修改 Profile 时需要清除
旧 bond 后重新配对。

### 4.2 Aiden Control/Wake Service

Wake Service 无论是否安装 App 都始终注册：

| Item | UUID |
| --- | --- |
| Wake Service | `a1de0001-7c4b-4f52-8d9a-6b4f6e6f7469` |
| Wake Characteristic | `a1de0002-7c4b-4f52-8d9a-6b4f6e6f7469` |

Wake Characteristic 支持 `read` 和 `notify`，payload 为 12 字节小端格式：

```text
byte 0      protocol version，当前为 1
byte 1      reason：0 unknown / 1 Phone Bridge / 2 manual / 3 system
bytes 2-3   reserved
bytes 4-11  uint64 wake sequence
```

Wake 只表示“有后台工作可取”，不携带命令内容。App 收到 Notify 后通过 USB ECM 请求现有
HTTP Queue。没有 App 或没有订阅者时，`wake` 返回 `delivered=false`，Queue 中的命令仍然
保留。

App 先通过 USB ECM 获取板端 `device_name`，CoreBluetooth 扫描时同时匹配 Wake Service
UUID 和该稳定广播名称，不能永久绑定“扫描到的第一台同 UUID 设备”。后续仍可在同一
Control Service 增加只读设备身份 Characteristic，进一步校验当前 BLE 设备与 USB 连接
的板子一致。

### 4.3 ANCS Consumer

手机完成加密配对并暴露 ANCS 后，`ble_service`：

1. 发现 Notification Source、Control Point 和 Data Source；
2. 订阅 Notification Source 与 Data Source；
3. 收到 Added/Modified 后通过 Control Point 请求应用 ID、标题、副标题、正文和日期；
4. 将 Added、Modified、Removed 转换为统一事件；
5. 将事件放入默认 512 条的内存 ring，不持久化正文。

事件至少包含：

- 本地递增 `id`；
- ANCS notification UID、事件类型、分类和 flags；
- app identifier、title、subtitle、message、date；
- `metadata_complete` 和错误信息。

服务重启会清空事件 ring，但不会清除手机 bond。通知正文不得写入普通日志、`/userdata`
或 Agent memory。

## 5. App 编排的连接流程

### 5.1 首次配对

1. 用户在 App“设置 → 蓝牙与系统通知”中点击“连接 Aiden 蓝牙”；
2. App 通过 USB ECM 调用 Agent `POST /api/bluetooth/pairing/start`；
3. Agent 通过 UDS `pairing_start` 请求 `ble_service` 开放默认 5 分钟窗口；
4. App 从状态响应取得唯一 `device_name`，使用 Wake Service UUID 和该名称扫描当前 Aiden；
5. App 订阅要求加密的 Wake Characteristic，iOS 自动显示系统配对确认；
6. 用户点击系统“配对”后，BlueZ 保存 bond 并将首台 iPhone 标记为 Trusted；
7. `ble_service` 立即关闭 Discoverable/Pairable，App 订阅 Wake，板端继续建立 ANCS；
8. App 轮询 Agent 状态并展示蓝牙绑定、后台唤醒和系统通知三个阶段。

App 负责发起和展示流程，但配对确认属于 iOS。App 不得模拟确认、绕过系统弹窗或把
“CoreBluetooth 已连接”误判为系统 bond 已完成。

### 5.2 已经配对后打开 App

1. 如果 App 已保存 peripheral identifier，启动时创建支持 state restoration 的
   `CBCentralManager` 并自动恢复；
2. 如果板端已有 bond、但 App 尚未保存 identifier，用户点击“连接 Aiden App”；
3. App 读取板端状态，发现 `paired=true` 后跳过 `pairing_start`，按 `device_name` 扫描；
4. CoreBluetooth `connect` 附着到已有 peripheral；
5. App 发现并订阅 Wake Characteristic；
6. 不创建第二个 bond，也不再次要求用户配对；
7. 用户关闭 App 后，HOGP 和 ANCS 仍可继续工作，只有 Wake subscriber 消失。

这里的 `connect` 是 App 建立自己的 CoreBluetooth 会话，不代表再建立一条独立物理连接。

### 5.3 解除配对

1. 用户在 App 中点击“解除蓝牙配对”并确认；
2. App 调用 Agent `POST /api/bluetooth/pairing/forget`；
3. `ble_service` 调用 BlueZ `Adapter1.RemoveDevice` 删除板端 bond 和 Trusted 状态；
4. App 取消 CoreBluetooth 连接并清除保存的 peripheral identifier；
5. App 引导用户打开 iOS 蓝牙设置，对 Aiden 执行“忽略此设备”；
6. 需要再次连接时，用户重新从 App 发起首次配对。

iOS 没有公开 API 允许第三方 App 静默删除系统 bond。只删除板端 bond 会在 iPhone 上留下
旧密钥和设备记录，并可能导致后续重配失败，因此 UI 必须明确提示最后的系统设置步骤。

## 6. 板端 UDS 接口

默认 socket：

```text
/run/ble_service/ble_service.sock
```

UDS 复用项目现有的 12 字节小端 envelope。本版提供五个业务操作：

| 操作 | 用途 |
| --- | --- |
| `status` | 查询 HCI、BlueZ、连接、配对、Wake subscriber 和 ANCS 状态 |
| `wake` | best-effort 发送 Wake Notify |
| `events_since` | 按 cursor 增量读取内存中的 ANCS 事件 |
| `pairing_start` | 在没有可信 bond 时打开配置时长的配对窗口 |
| `pairing_forget` | 删除板端 BlueZ bond、Trusted 状态和当前连接状态 |

示例：

```json
{"op":"status"}
{"op":"pairing_start"}
{"op":"pairing_forget"}
{"op":"wake","reason":"phone_bridge"}
{"op":"events_since","since":"42","limit":50}
```

`events_since` 返回 `truncated=true` 时表示 cursor 已早于 ring 的保留范围。持久消费、去重、
memory 更新和通知策略由后续任务负责，不进入 BLE 服务。

### 6.1 Agent HTTP API

App 不直接访问 UDS。Agent 将配对控制转换成手机可通过 USB ECM 调用的 HTTP API：

| Method | Path | 用途 |
| --- | --- | --- |
| `GET` | `/api/bluetooth/status` | 获取完整 BLE runtime status |
| `POST` | `/api/bluetooth/pairing/start` | 打开配对窗口；已有 Trusted bond 时返回冲突 |
| `POST` | `/api/bluetooth/pairing/forget` | 删除板端 bond 并返回删除数量和最新状态 |

HTTP API 只编排配对状态，不承载 BLE 密钥、通知正文或 Phone Bridge 命令。

## 7. 配对、持久化与隐私

- BlueZ 数据目录持久化为
  `/userdata/ble_service/bluetooth -> /var/lib/bluetooth`；
- 第一版只支持一个可信 iPhone；
- `ble_service` 启动时默认不开放新设备配对；
- 用户从 App 发起后才开放默认 5 分钟窗口，重复发起会刷新 deadline；
- 5 分钟是显式用户操作后的安全上限，用来覆盖蓝牙授权和 iOS 系统确认耗时；App 会立即
  开始扫描，成功后窗口立刻关闭，不会固定暴露满 5 分钟；
- 已有 Trusted bond 时拒绝打开新配对窗口，必须先执行解绑；
- 配对成功后立即关闭 Discoverable/Pairable；
- 清除或替换手机必须同时删除 BlueZ bond 和本地可信设备状态；
- App 清除本地 peripheral identifier，并引导用户在 iOS 设置中忽略设备；
- Wake 和 HOGP 的敏感 Characteristic 要求加密连接；
- ANCS 事件只保存在有界内存，不写入磁盘、Agent memory 或普通日志；
- App 被用户强制退出或蓝牙权限被关闭时，不承诺 Wake 能重新拉起 App。

## 8. 故障与恢复

| 故障 | 行为 |
| --- | --- |
| `hci0` 不存在 | HCI init 重试；BLE 不可用，但 Wi-Fi、USB 和 HTTP Queue 正常 |
| `bluetoothd` 退出 | init watchdog 重启；`ble_service` 标记 backend unavailable 后重连 |
| `ble_service` 退出 | `S41ble_service` 自动重启；bond 保留，事件 ring 清空 |
| iPhone 断开 | 已绑定设备继续等待系统或 App 重连；不会自动开放新配对 |
| 没有 Wake subscriber | `delivered=false`；HTTP Queue 命令不丢失 |
| ANCS 不完整或超时 | 当前事件标记 metadata incomplete，不能阻塞后续通知 |
| ring 溢出 | 丢弃最旧事件，旧 cursor 返回 `truncated=true` |

## 9. 本卡验收

### 9.1 固件与服务底座

- 新镜像启动后存在并启用 `hci0`；
- BlueZ、`ble_service` 默认启动且异常退出后能恢复；
- BlueZ bond 在重启和 rootfs 更新后仍保留；
- Wi-Fi、USB ECM 和 USB HID 不受影响；
- UDS `status`、`wake`、`events_since` 可用。

### 9.2 配对与重连验收

- `ble_service` 重启后没有 bond 时保持 `Pairable=false`；
- App 点击连接后，板端开放限时窗口并由 iOS 显示系统配对确认；
- 配对、锁屏、手机重启和板子重启后能够自动重连；
- HOGP 不造成不可接受的额外键盘或软键盘体验；
- 板端可以接收短信、邮件等 ANCS 通知并形成标准化事件；
- 系统蓝牙设置中存在可管理的 Aiden 条目和“忽略此设备”入口；
- App 解绑后板端 bond 被删除，并明确引导用户完成 iPhone 侧忽略设备。

### 9.3 App 增强模式验收

- 未预先配对时，App 可以打开板端窗口、扫描并发起连接，首次配对仍显示系统确认；
- 板端已有历史 bond、但 App 尚未保存 identifier 时，App 能复用该 bond，不出现第二次配对；
- 多台 Aiden 同时广播时，App 只连接 USB 当前板端返回的 `device_name`；
- App 前台、后台和锁屏时可订阅并收到 Wake Notify；
- App UI 分别显示 bond、Wake subscriber 和 ANCS subscription 状态；
- 收到 Wake 后仍通过 HTTP Queue 获取命令；BLE 中不出现工具 payload；
- BLE 不可用或 App 未订阅时，HTTP Queue 中的命令不会丢失。

### 9.4 ANCS 验收

- Added、Modified、Removed 事件和通知属性解析正确；
- Data Source 分片、断连和不完整响应不会阻塞后续通知；
- 事件 ring 有界，服务重启后正文不会从磁盘恢复；
- 通知不会自动写入 memory 或触发 Agent 动作。

## 10. 当前实现与后续边界

当前 firmware PR #486、iOS App PR #41 和 SDK PR #28 已提供公共 BLE 底座，包括：

- 内核/BlueZ 初始化、持久化 bond 和 HCI/BlueZ/业务服务恢复逻辑；
- 最小 Consumer Control HOGP GATT server；
- 稳定设备后缀、App 显式开启的限时配对窗口和单一可信设备策略；
- Agent BLE 状态、配对开始和板端解绑 HTTP API；
- App 蓝牙设置 UI、CoreBluetooth 配对编排和系统解绑引导；
- Wake、ANCS、UDS 和 Agent Queue Wake；
- ANCS 分片、超时、响应大小限制和有界队列处理。

代码完成后仍需要新 SDK 镜像和 iPhone 真机验证以下硬件行为：

- App 内发起、iOS 系统确认的首次配对、自动重连和 ANCS 授权；
- HOGP 不产生额外软键盘或不可接受的 Consumer Control 副作用；
- App 对已有 HOGP peripheral 的发现与复用，以及板端/iPhone 双侧解绑；
- iPhone 前台、后台、锁屏和重启后的 Wake/ANCS 恢复；
- HCI、BlueZ 和业务服务在真实 AIC8800 上的故障恢复时序。

本卡完成后，后续任务应基于这些接口继续开发，不再各自创建新的蓝牙进程、配对流程或
BLE 命令通道。

## 11. 参考

- `src/agent/cmd/ble_service/main.go`
- `src/agent/internal/ble/`
- `overlay/etc/init.d/S39hciinit`
- `overlay/etc/init.d/S40bluetoothd`
- `overlay/etc/init.d/S41ble_service`
- [BLE Service](../03-services/ble-service.md)
- [Unix Domain Socket Protocol](../06-protocols/uds-protocol.md)
- [Apple Notification Center Service Specification](https://developer.apple.com/library/archive/documentation/CoreBluetooth/Reference/AppleNotificationCenterServiceSpecification/Specification/Specification.html)
- [Core Bluetooth Background Processing](https://developer.apple.com/library/archive/documentation/NetworkingInternetWeb/Conceptual/CoreBluetooth_concepts/CoreBluetoothBackgroundProcessingForIOSApps/PerformingTasksWhileYourAppIsInTheBackground.html)
- [Bluetooth HID over GATT Profile](https://www.bluetooth.com/specifications/specs/hid-over-gatt-profile-1-0/)
