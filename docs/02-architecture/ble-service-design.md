# Aiden BLE 基础服务设计

> 状态：Draft v4
>
> 对应任务：`【Aiden】将蓝牙模块接受系统通知接入aiden`
>（`3590beba-a111-496e-ba6e-909e734ffc59`）。

## 1. 设计范围

本任务建设 Aiden 的 BLE 公共底座，使硬件在安装或未安装 Aiden App 时都能与 iPhone
建立 BLE 链路并接收系统通知，同时为后续 App 后台工具提供轻量唤醒能力。

本版支持两种入口，但最终共用同一个 `ble_service`、同一个 iPhone bond 和同一条 BLE
物理连接：

| 模式 | 连接入口 | 本版能力 |
| --- | --- | --- |
| 无 App 模式 | 用户在 iOS“设置 → 蓝牙”中选择 Aiden，通过 BLE HID/HOGP 完成配对 | 系统自动重连、ANCS 系统通知 |
| App 增强模式 | App 通过 CoreBluetooth 发现并连接 Aiden；如果已经通过系统设置连接，则直接复用 | ANCS 系统通知、Wake Notify、后续通过 HTTP Queue 执行 App 工具 |

无 App 模式不是另一套服务。HOGP 只作为 iOS 系统可识别的配对和重连入口；Wake Service
始终存在，安装 App 后可以在已有连接上发现并订阅。

## 2. 目标与非目标

### 2.1 本版目标

- 为 Pico Zero 启用 Bluetooth 内核能力并初始化 AIC8800 `hci0`；
- 默认启动 BlueZ，并将配对信息持久化到 `/userdata`；
- 新增独立 `ble_service`，统一管理 GATT、配对、连接和 ANCS；
- 发布最小 BLE HID/HOGP，使无 App 用户能够从系统设置完成首次配对，并由 iOS 自动重连；
- 发布始终可用的 Aiden Control/Wake Service；
- 作为 ANCS Consumer 接收 iOS 系统通知并转换为统一事件；
- 通过 UDS 向 Agent 提供状态、Wake 和通知事件读取接口；
- App 通过 CoreBluetooth 状态恢复、自动连接并订阅 Wake；
- App 打开时复用已有系统配对和连接，不要求用户再次配对；
- BLE 故障不影响 USB HID、USB ECM、Wi-Fi 或 Phone Bridge HTTP Queue。

### 2.2 不在本版完成

- 不将 ANCS 通知写入 Agent memory；
- 不根据通知自动触发 Agent 动作；
- 不通过 BLE 传输 Phone Bridge 命令、结果、截图或音频；
- 不在本任务重构联系人、日历、剪贴板等 App 工具；
- 不实现 Android Notification Listener；
- 不把 BLE HID 扩展为新的完整键盘、鼠标或触控主通道。

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
        | - Settings 配对 / HID 自动重连                 |
        | - ANCS Notification Provider                   |
        +-----------------------+------------------------+
                                | 同一条 BLE 连接
        +-----------------------+------------------------+
        | 可选 Aiden App                                 |
        | - CoreBluetooth 连接或复用已有连接             |
        | - 订阅 Wake                                    |
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
| Agent | 消费 UDS；Phone Bridge 命令仍进入 HTTP Queue |

内核必须同时包含现有 zram 配置和 Bluetooth 配置。板子首次启用蓝牙需要刷入包含相关
内核配置的新镜像；完成后，普通 `ble_service` 迭代只需交叉编译、部署二进制并重启服务。

## 4. GATT 与连接设计

### 4.1 最小 HOGP

无 App 模式需要一个 iOS 系统能够管理的标准 Profile，否则普通自定义 GATT 外设不能
依赖“设置 → 蓝牙”获得稳定的系统配对和自动重连。

本版实现最小 HID over GATT Profile：

- 使用标准 HID Service `0x1812`；
- 首选副作用较小的 Consumer Control 报告；
- HOGP 本版主要用于配对和连接保活，不替代现有 USB HID；
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

后续可在同一 Control Service 增加只读设备身份 Characteristic，供 App 校验当前 BLE
设备与 USB 连接的板子一致；本版至少保证广播名称带稳定的设备后缀，避免多个 Aiden
同时出现时无法区分。

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

## 5. 两种连接流程

### 5.1 无 App 模式

1. Aiden 广播唯一名称、HOGP 和 Control/Wake Service；
2. 用户进入 iOS“设置 → 蓝牙”，选择 `Aiden-<设备后缀>`；
3. iOS 在访问加密 Profile 时显示系统配对/ANCS 授权；
4. 配对成功后 BlueZ 保存 bond，iOS 通过 HID Profile 负责后续自动重连；
5. `ble_service` 在同一连接上订阅 ANCS 并产生通知事件；
6. Wake Service 保持可发现，但没有 App 时没有 subscriber。

首次配对仍需要用户操作。普通硬件不能因为 USB 插入而强制 iOS 弹出蓝牙配对框或静默
完成配对。

### 5.2 已经配对后打开 App

1. App 创建支持 state restoration 的 `CBCentralManager`；
2. 使用 Wake Service/HID Service 查询系统当前已连接的 Aiden；
3. CoreBluetooth `connect` 附着到已有 peripheral；
4. App 发现并订阅 Wake Characteristic；
5. 不创建第二个 bond，也不再次要求用户配对；
6. 用户关闭 App 后，HOGP 和 ANCS 仍可继续工作，只有 Wake subscriber 消失。

这里的 `connect` 是 App 建立自己的 CoreBluetooth 会话，不代表再建立一条独立物理连接。

### 5.3 未配对时从 App 连接

1. 用户授予 App 蓝牙权限；
2. App 按 Wake Service UUID 和稳定设备后缀扫描目标 Aiden；
3. App 可以自动调用 `connect`；
4. 首次访问加密 Characteristic 时，配对确认仍由 iOS 显示并由用户完成；
5. 成功后保存 peripheral identifier，并订阅 Wake；
6. 后续优先恢复或复用该 peripheral，不重新配对。

App 可以自动发现和发起连接，但不能绕过 iOS 首次配对确认，也不能仅凭“扫描到第一个
相同 UUID 的设备”就永久绑定。

## 6. 板端 UDS 接口

默认 socket：

```text
/run/ble_service/ble_service.sock
```

UDS 复用项目现有的 12 字节小端 envelope。本版只提供三个业务操作：

| 操作 | 用途 |
| --- | --- |
| `status` | 查询 HCI、BlueZ、连接、配对、Wake subscriber 和 ANCS 状态 |
| `wake` | best-effort 发送 Wake Notify |
| `events_since` | 按 cursor 增量读取内存中的 ANCS 事件 |

示例：

```json
{"op":"status"}
{"op":"wake","reason":"phone_bridge"}
{"op":"events_since","since":"42","limit":50}
```

`events_since` 返回 `truncated=true` 时表示 cursor 已早于 ring 的保留范围。持久消费、去重、
memory 更新和通知策略由后续任务负责，不进入 BLE 服务。

## 7. 配对、持久化与隐私

- BlueZ 数据目录持久化为
  `/userdata/ble_service/bluetooth -> /var/lib/bluetooth`；
- 第一版只支持一个可信 iPhone；
- 首次没有 bond 时开放限时配对窗口，成功后关闭 Discoverable/Pairable；
- 清除或替换手机必须同时删除 BlueZ bond 和本地可信设备状态；
- Wake 和 HOGP 的敏感 Characteristic 要求加密连接；
- ANCS 事件只保存在有界内存，不写入磁盘、Agent memory 或普通日志；
- App 被用户强制退出或蓝牙权限被关闭时，不承诺 Wake 能重新拉起 App。

## 8. 故障与恢复

| 故障 | 行为 |
| --- | --- |
| `hci0` 不存在 | HCI init 重试；BLE 不可用，但 Wi-Fi、USB 和 HTTP Queue 正常 |
| `bluetoothd` 退出 | init watchdog 重启；`ble_service` 标记 backend unavailable 后重连 |
| `ble_service` 退出 | `S41ble_service` 自动重启；bond 保留，事件 ring 清空 |
| iPhone 断开 | 继续广播，等待系统或 App 重连 |
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

### 9.2 无 App 真机验收

- 未安装 Aiden App 时，可从 iOS 设置中找到并配对 Aiden；
- 配对、锁屏、手机重启和板子重启后能够自动重连；
- HOGP 不造成不可接受的额外键盘或软键盘体验；
- 板端可以接收短信、邮件等 ANCS 通知并形成标准化事件；
- Wake Service 同时存在，但 `wake_subscriber=false` 属于正常状态。

### 9.3 App 增强模式验收

- 未预先配对时，App 获得权限后可以扫描并发起连接，首次配对仍显示系统确认；
- 已通过设置配对时，App 能复用已有 peripheral/bond，不出现第二次配对；
- App 前台、后台和锁屏时可订阅并收到 Wake Notify；
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
- 稳定设备后缀、限时首配窗口和单一可信设备策略；
- Wake、ANCS、UDS 和 Agent Queue Wake；
- ANCS 分片、超时、响应大小限制和有界队列处理。

代码完成后仍需要新 SDK 镜像和 iPhone 真机验证以下硬件行为：

- 无 App 的系统设置配对、自动重连和 ANCS 授权；
- HOGP 不产生额外软键盘或不可接受的 Consumer Control 副作用；
- App 对已有 HOGP peripheral 的发现与复用；
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
