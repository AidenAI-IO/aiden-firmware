# iOS BLE 配对无弹窗排查记录（2026-08-07）

## 1. 摘要

当前故障不是 App 缺少一个“显示系统配对弹窗”的调用。iOS CoreBluetooth 没有主动显示该弹窗的公开 API；系统弹窗应由 App 访问需要加密的 GATT Characteristic 时，由 iOS 自动触发。

2026-08-10 的继续排查最终确认了四个按顺序暴露的问题：

1. Firmware 在配对窗口切换时反复删除并重建 Advertisement，导致 Wake Service UUID / manufacturer data 偶发丢失，App 的 UUID 过滤扫描收不到设备。
2. 板端 HCI 启动顺序不满足 Linux LE SMP 注册条件：AES/CMAC 必须先可用，而且不能由 `hciconfig hci0 up` 通过 legacy ioctl 抢先上电；必须让 `bluetoothd` 通过 management `Set Powered` 完成上电。只有这样 debugfs 中才会出现 LE SMP `0x0006` 和 BR/EDR SMP `0x0007` fixed channel。
3. BlueZ 5.65 在本板上即使 bond 成功，也会拒绝 `encrypt-notify` 对应的 CCCD 写入。Wake Characteristic 保留 `encrypt-read` 作为配对触发点，通知改为普通 `notify`；Wake payload 不含通知正文或命令，只用于提示 App 轮询已认证的 HTTP 通道。
4. iOS 与板端可以保留相反的旧 bond。真机已确认：iOS 设置中忽略设备后，板端仍保留 `Paired/Bonded=yes`，App 的安全读取反复返回 `Encryption is insufficient`。旧 App 把 CoreBluetooth ACL 连接当作“蓝牙已连接”，并每约 2 秒自动重连，因此表现为假连接和系统配对框循环弹出。

广告故障的原始结果链路是：

```text
App 打开板端配对窗口
  -> Firmware UnregisterAdvertisement/RegisterAdvertisement
  -> Controller/BlueZ 广告状态漂移或广告数据丢失
  -> App 的 Wake Service UUID 过滤扫描收不到 Peripheral
  -> App pairing_timeout
  -> 没有连接和 encrypted read
  -> iOS 没有机会显示系统配对弹窗
```

成功扫描后，原始 SMP 故障链路为：

```text
App encrypted read
  -> hci0 没有 SMP fixed channel 0x0006
  -> Bluetooth: hci0: security requested but not available
  -> iOS 无法建立 bond
```

当前 Firmware 已修复 Advertisement、SMP 上电顺序、Wake notify 兼容性，并增加“断开物理链路”和“删除板端 stale bond”接口。App 修复把“安全读取成功且 Wake 订阅成功”作为真实连接，检测到 bond 不一致后立即停止重试，避免继续弹框；用户下一次明确点击“重新配对”时才删除板端 bond 并开始一次全新配对。Firmware 已部署，App 改动需进入线上构建后才能真机验收。

## 2. 调查范围

审查和排查范围：

- App PR: `AidenAI-IO/aiden-app#41`
  - 调查时 HEAD: `a5f585e3`
- Firmware PR: `AidenAI-IO/aiden-firmware#486`
  - 调查时 HEAD: `ec559916`
- 关联 SDK PR: `AidenAI-IO/aiden-sdk#28`
  - 调查时 HEAD: `955b9ae3`
- 手机控制台日志：
  - `/Users/qing/Documents/project/aiden-hardware-demo/log`
  - 共约 18,050 行，时间范围约为 `18:00:23–18:00:36`
- Luckfox 板端实时状态、服务日志、内核日志、rfkill、内核配置和 crypto 状态
- 使用 Mac CoreBluetooth 作为独立 Central 的 BLE 扫描结果

本记录包含诊断结论和 2026-08-10 的修复验证。继续排查阶段执行了配对窗口切换、BLE 服务/HCI 重启、rfkill block/unblock 和板卡重启等状态变更实验；对应代码修复位于 Firmware PR #486 的 `feat/bluetooth-hardware-integration` worktree。

## 3. 用户现象

在 iOS App 中点击蓝牙“配对/连接”后：

- 没有出现 iOS 系统蓝牙配对弹窗；
- 视觉上没有完成配对；
- 板端最终没有保存 bond；
- 多版 App/Firmware 调整后现象仍然存在。

## 4. 设计上的预期配对链路

当前代码的预期流程如下：

1. App 调用板端 `POST /api/bluetooth/pairing/start`；
2. 板端只把 Adapter 设置为 Pairable / Discoverable，保持 Wake 广告实例不变；
3. App 的 `AidenBLEManager` 按 Wake Service UUID 扫描；
4. App 连接 Peripheral，发现 Wake Service 和 Wake Characteristic；
5. App 对带 `encrypt-read` 标志的 Wake Characteristic 执行 `readValue`；
6. iOS 发现链路未加密，自动启动 LE SMP 并显示系统配对弹窗；
7. 配对成功后 App 订阅普通 `notify`；
8. 板端记录 bond，App 保存 Peripheral identifier，后续自动重连。

关键代码：

- `aiden-app/src/services/BluetoothPairing.ts`
  - 先打开板端配对窗口，再调用 Native `startPairing`；
- `aiden-app/ios/AidenBridge/AidenBLEManager.swift`
  - `scanForPeripherals(withServices: [wakeServiceUUID])`；
  - 发现特征后调用 `peripheral.readValue(for: characteristic)`；
- `aiden-firmware/src/agent/internal/ble/bluez.go`
  - Wake Characteristic Flags 为 `encrypt-read`、`notify`。

上述触发方式本身符合 CoreBluetooth 的常见配对模型。App 无法直接命令 iOS 显示系统配对框。

## 5. 已确认事实

### 5.1 App 已成功打开板端配对窗口

板端状态 API 返回：

```json
{
  "device_name": "Aiden-A48B",
  "board_identity": "8c7ab3f4a48b",
  "backend_available": true,
  "gatt_registered": true,
  "hogp_registered": false,
  "advertising": true,
  "pairing_open": true,
  "bonded_device_count": 0,
  "connected": false,
  "paired": false,
  "wake_subscriber": false
}
```

结论：

- App 到板端 HTTP API 的路径是通的；
- 点击按钮后板端确实打开了配对窗口；
- 故障发生在 HTTP 请求之后；
- 配对没有完成，因为 bond 数量仍是 0，且没有 Wake subscriber。

### 5.2 板端基础服务均在运行

现场确认：

- `hci0` 为 `UP RUNNING`；
- `bluetoothd` 正在运行；
- `ble_service` 正在运行；
- Agent 正在运行；
- BlueZ 已接受 GATT Application 和 Advertisement 注册。

因此这不是简单的“服务没启动”或“hci0 不存在”。

### 5.3 每次加密访问都在内核 SMP 层失败

板端 `dmesg` 中，每次尝试附近都会出现：

```text
Bluetooth: hci0: security requested but not available
```

Linux 5.10 对应源码位于 `net/bluetooth/smp.c` 的 `smp_conn_security()`：

```c
chan = conn->smp;
if (!chan) {
    bt_dev_err(hcon->hdev, "security requested but not available");
    return 1;
}
```

这条日志的含义非常明确：

- ATT/GATT 层已经请求提升安全等级；
- HCI/LE 连接对象存在；
- 但该连接的 SMP L2CAP fixed channel 没有创建；
- 内核无法开始 LE 配对和密钥交换。

这是目前证据最强、已经确认的直接失败点。

### 5.4 内核不是简单漏开 `CONFIG_BT_LE`

运行中内核配置包含：

```text
CONFIG_BT=y
CONFIG_BT_BREDR=y
CONFIG_BT_LE=y
CONFIG_BT_HCIUART=y
CONFIG_BT_HCIUART_H4=y
CONFIG_CRYPTO_KPP=y
CONFIG_CRYPTO_ECC=y
CONFIG_CRYPTO_ECDH=y
CONFIG_CRYPTO_CMAC=y
CONFIG_CRYPTO_AES=m
```

运行时也可见：

- `aes_generic` 已加载；
- `/proc/crypto` 中存在 `aes-generic`；
- `/proc/crypto` 中存在 `ecdh-generic`。

因此不能把问题简单归结为“没有启用 BLE 配置”。真正的问题是 SMP fixed channel 在连接建立时不存在。

### 5.5 SMP 注册存在启动时依赖和错误处理缺口

Linux 在 `hci0` 上电时调用：

```c
smp_register(hdev);
```

`smp_register()` 会创建 LE SMP 的 root fixed channel，并依赖：

- `cmac(aes)`；
- ECDH；
- L2CAP channel 分配。

但当前内核调用点没有检查 `smp_register()` 返回值：

```c
int __hci_req_hci_power_on(struct hci_dev *hdev)
{
    smp_register(hdev);
    return __hci_req_sync(...);
}
```

如果 SMP 注册失败：

- `hci0` 仍可显示为 UP；
- bluetoothd 仍可运行；
- BlueZ 仍可注册 GATT 和 Advertisement；
- 普通未加密 GATT 仍可能工作；
- 直到访问加密特征时才出现 `security requested but not available`；
- 后续加载 crypto 模块也不会自动重试 SMP 注册，除非重新走 HCI power-on。

Advertisement 修复后的真机重试进一步确认：App 已连接并触发安全读取，但内核仍输出同一错误；同时 `/proc/modules` 中有 `aes_generic`，没有 `cmac`，而镜像实际包含 `/oem/usr/ko/cmac.ko`。现有 `/oem/usr/ko/insmod_wifi.sh` 只显式加载 AES，没有加载 CMAC。由于模块位于 `/oem/usr/ko` 而不是标准 `/lib/modules`，内核不能依靠通常的模块自动加载路径补齐它。

后续控制变量证明，仅补载 CMAC 并执行 legacy `hciconfig hci0 down/up` 仍不足以修复 SMP。真正决定 fixed channel 是否注册的是上电路径：`S39hciinit` 原来用 `hciconfig hci0 up` 通过旧 `HCIDEVUP` ioctl 抢先上电，绕过了本内核/BlueZ 组合所需的 management power-on 流程。最终修复为：

- 在 HCI transport 创建前加载并验证 AES、CMAC 和 ECDH；
- `S39hciinit` 只负责 `hciattach`，不再调用 `hciconfig hci0 up`；
- 由 `bluetoothd` 通过 management `Set Powered` 完成 HCI 上电；
- 上电后检查 debugfs 中存在 `0x0006` 和 `0x0007`。

修复后真机成功出现系统配对窗口并完成 bond，板端 debugfs 同时存在：

```text
0x0006  LE SMP fixed channel
0x0007  BR/EDR SMP fixed channel
```

### 5.6 AIC 平台 rfkill 处于 blocked

板端当前有两个 Bluetooth rfkill 节点：

```text
1: bluetooth
    Soft blocked: yes
    Hard blocked: no

2: hci0
    Soft blocked: no
    Hard blocked: no
```

对应 sysfs：

```text
/sys/class/rfkill/rfkill1
  name=bluetooth
  device=/sys/devices/platform/aic-bt
  state=0
  soft=1

/sys/class/rfkill/rfkill2
  name=hci0
  device=/sys/devices/platform/ff4b0000.serial/tty/ttyS1/hci0
  state=1
  soft=0
```

AIC 驱动 `aic8800_btlpm/rfkill.c` 明确把平台 rfkill 初始化为 blocked：

```c
rfkill_init_sw_state(bt_rfk, true);
```

解除 block 时才调用：

```c
aicbsp_set_subsys(AIC_BLUETOOTH, AIC_PWR_ON);
```

调查时 Firmware PR 的 `overlay/etc/init.d/S39hciinit` 只执行 `hciattach` 和 `hciconfig hci0 up`，没有执行：

```sh
rfkill unblock bluetooth
```

这表示当前启动流程没有显式完成 AIC 平台要求的蓝牙 power-on。

它可能导致射频、低功耗或控制器状态处于不一致状态。因为 `hci0` 仍能收发 HCI 数据，所以它是否直接导致 SMP channel 缺失仍需实验验证；但这是一个必须修复的明确启动缺口。

2026-08-10 补充：在平台 rfkill 仍显示 soft blocked 时，Mac 曾成功扫描到正确 Wake UUID 和 manufacturer identity，说明 soft blocked 本身不是“完全没有广播”的充分条件。运行时显式 block 会使后续 `hciattach` 无法创建 `hci0`，unblock 也可能造成 Controller/BlueZ 状态漂移。因此应只在完整启动顺序中修复和验证 rfkill，不能把它视为当前扫描失败的唯一根因。

### 5.7 BlueZ 的 `advertising=true` 不等于射频可发现

独立使用 Mac CoreBluetooth 扫描：

1. 按 Aiden Wake Service UUID 过滤扫描约 12 秒；
2. 不带过滤扫描约 12 秒。

两次均未发现：

- `Aiden-A48B`；
- Wake Service UUID；
- 板端 manufacturer identity。

与此同时 BlueZ 报告：

```text
ActiveInstances = 1
MaxAdvLen = 31
Discoverable = true
Pairable = true
```

因此 Firmware 中的 `advertising=true` 当前只证明 Advertisement 已向 BlueZ 注册，不能证明 Controller 正在通过射频发送、也不能证明 iPhone 能发现它。

该现象最初与 AIC 平台 rfkill 仍 blocked 的状态一致，但 2026-08-10 的对照实验已经证明 blocked 状态不是唯一解释。

2026-08-10 的继续排查进一步缩小了原因：`ble_service` 首次注册 Advertisement 后，Mac 能收到完整 Wake UUID 和 manufacturer identity；重复切换配对窗口后，过滤扫描连续失败。HCI 抓包同时显示 Firmware 删除并重建 advertising set，且部分重建流程没有重新下发完整 advertising data。因此先前的“BlueZ 显示已注册但外部扫不到”主要应归因于 Advertisement 重注册和 Controller 状态漂移，不能只归因于 rfkill。

### 5.8 手机日志不足以定位 App 状态机阶段

用户提供的手机日志中没有找到：

```text
[AidenBLE] Bluetooth powered on
[AidenBLE] Scanning for Aiden Wake service
[AidenBLE] Discovered matching Aiden peripheral ...
[AidenBLE] Connecting to ...
[AidenBLE] Connected to ...
[AidenBLE] Secure pairing probe failed: ...
```

日志中大量内容是 iOS `bluetoothd`、`sharingd`、AirPods/Continuity/Find My 等系统蓝牙噪声，无法映射到 App 自己的 CoreBluetooth 回调。

另外，手机日志时间段似乎早于部分板端配对窗口操作时间。因此这份日志不能证明 App 是否已扫描、发现、连接或读取特征，也不能用来否定板端内核错误。

后续采集应确保包含 App 进程日志，并按 `[AidenBLE]` 过滤。

## 6. 当前根因判断

### 6.1 2026-08-10 已确认的第一层直接根因：广告生命周期不稳定

Firmware 的 `setPairingMode()` 每次切换配对窗口都会：

1. 设置 Adapter `Pairable` / `Discoverable`；
2. `UnregisterAdvertisement`；
3. 修改 Advertisement properties；
4. 使用相同 D-Bus object path 再次 `RegisterAdvertisement`。

继续排查得到以下对照结果：

- `ble_service` 首次注册 Advertisement 后，Mac 按 Wake Service UUID 过滤能发现 `Aiden-A48B`；
- discovery callback 中同时包含 Wake Service UUID 和 `ffff8c7ab3f4a48b` manufacturer data；
- RSSI 约为 `-35` 到 `-45`，排除了单纯的弱信号解释；
- HCI 抓包可见 `LE Remove Advertising Set`、`LE Set Extended Advertising Parameters` 和 `LE Set Extended Advertising Enable` 均返回成功；
- 部分重新注册流程没有再次出现包含 Wake UUID 和 manufacturer identity 的 `LE Set Extended Advertising Data`；
- 连续调用配对窗口 API 后，6 次短时 Wake UUID 过滤扫描全部失败；
- 失败时 API 仍返回 `advertising=true`，说明该字段只代表 BlueZ 注册成功，不能代表真实射频广告有效。

这直接解释了为什么 App 大多数情况下没有系统配对弹窗：App 固定使用 Wake Service UUID 过滤扫描；没有 discovery callback，就不会连接、发现 GATT 或读取 `encrypt-read` Characteristic。

第一修复目标是让 Advertisement 在 `ble_service` / BlueZ / HCI 生命周期内只注册一次。配对窗口切换只修改 Adapter 的配对授权状态，不再删除和重建 Advertisement。

### 6.2 2026-08-07 已确认的第二层直接根因：SMP fixed channel 缺失

板端 Linux Bluetooth 内核栈没有为 LE 连接建立 SMP fixed channel，导致加密安全请求无法执行。

结果链路：

```text
App 读取 encrypt-read Characteristic
  -> BlueZ/ATT 请求提高链路安全等级
  -> 内核 smp_conn_security()
  -> conn->smp == NULL
  -> security requested but not available
  -> LE SMP 不启动
  -> iOS 不显示系统配对弹窗
  -> 不产生 bond
```

这个故障只有在 App 已经成功发现并连接 Peripheral、开始 encrypted read 后才会触发。修复广告生命周期后，必须继续检查它是否稳定复现。

### 6.3 已确认的 SMP 启动根因与其余因素

#### 已确认根因 A：crypto 加载与 HCI management 上电顺序错误

支持证据：

- 每个连接都稳定出现 `conn->smp == NULL`；
- `smp_register()` 只在 HCI power-on 时调用；
- 注册依赖 crypto；
- 运行时 AES 为模块；
- 镜像包含 `cmac.ko`，但故障现场 `/proc/modules` 中没有 `cmac`；
- 模块加载脚本只加载 AES，没有加载 CMAC；
- `S39hciinit` 使用 legacy `hciconfig hci0 up` 抢先上电；
- 仅补载 CMAC 后执行 legacy down/up，SMP fixed channel 仍然缺失；
- 删除 legacy power-on、让 `bluetoothd` 通过 management `Set Powered` 上电后，`0x0006` 和 `0x0007` 均出现；
- 内核忽略 `smp_register()` 返回值，不会把失败升级为 HCI 启动失败；
- 后续没有自动重试。

已完成的修复实验：

- 在 HCI power-on 前加载 `aes_generic.ko` 和 `cmac.ko`；
- 验证 AES 与 ECDH 算法存在；
- `S39hciinit` 不再执行 `hciconfig hci0 up`；
- 由 `bluetoothd` 完成 management power-on；
- 完整重建 HCI transport、BlueZ 和 `ble_service`；
- 将同一顺序固化进 `S39hciinit`，缺少模块或算法时直接让 HCI 启动失败；
- 真机已完成一次端到端配对，系统弹窗、bond、Wake 和 ANCS 均成功。

#### 原因 B：AIC 平台 rfkill 未解除

支持证据：

- AIC 驱动默认 blocked；
- 当前运行状态确实是 soft blocked；
- `S39hciinit` 没有 unblock；
- 独立 Mac 扫描完全看不到广播；
- BlueZ 注册状态与实际射频可发现性不一致。

2026-08-10 控制变量结果：

- rfkill 仍 blocked 时，冷启动后的完整广告曾被 Mac 正确收到；
- 运行时 unblock 后 AIC 驱动执行 Bluetooth power 回调，但不能单独修复重复广告注册故障；
- 显式 block 后 `hciattach` 无法重新创建 `hci0`；
- block/unblock 与 HCI/BlueZ 的顺序会影响 Controller 状态，因此必须在启动流程中验证，不能作为运行时修复手段。

#### 可能的复合情况

两个问题可能同时存在：

1. 平台 rfkill 导致 Controller power/radio 状态不完整；
2. HCI 在 crypto/SMP 条件不完整时上电，SMP 注册失败；
3. 后续服务看到一个表面可用的 `hci0`，继续注册 GATT；
4. 未加密阶段可能连接，但加密访问必然失败。

#### 已确认原因 C：iOS 与板端 stale bond

如果“解绑”只删除板端 BlueZ bond，而 iOS 仍保留旧 LTK：

- iOS 可能认为设备已经配对，不再显示首次配对弹窗；
- 板端没有对应密钥，链路加密恢复失败；
- App 会表现为已经发现或连接 Peripheral，但 encrypted read / notify 失败。

真机已确认相反方向的 stale bond：用户在 iOS 设置中忽略 `Aiden-A48B` 后，板端仍保留设备 `1C:0E:C2:A3:D7:75`，状态为 `Paired/Bonded=yes`。App 日志持续出现：

```text
[AidenBLE] Secure pairing probe failed: Encryption is insufficient.
[AidenBLE] Disconnected: nil
```

旧 App 会把该 ACL 连接显示为“蓝牙已连接”，随后按约 2 秒间隔重新连接，每次 encrypted read 都可能再次触发系统配对框。将 BlueZ `JustWorksRepairing` 临时改为 `always` 未能修复 BlueZ 5.65 的这一行为，已恢复为 `confirm`。

修复策略不是无限重试，也不是静默删除 bond：App 首次确认安全读取失败且任一侧存在旧配对记录时，立即停止重连并进入 `bond_reset_required`；用户再次明确点击连接后，App 先调用板端 `/api/bluetooth/pairing/reset` 删除旧 bond，再只发起一次新的系统配对。

App phase 可用于快速分层，但不能代替原始错误和板端日志：

- `pairing_timeout` 且没有 `peripheral_identifier`：优先检查广告和扫描；
- 已有 `peripheral_identifier`，进入 `connecting` / `authenticating` 后出现 `Encryption is insufficient`：优先检查双方 bond 是否一致。

## 7. App PR 审查发现

### 7.1 设备身份校验在缺少 manufacturer data 时被绕过

`AidenBLEManager.matchesTarget()` 中，如果 iOS 没返回 manufacturer data，只要 `pairingRequested == true` 就接受任何广播同一 Wake Service UUID 的设备：

```swift
return pairingRequested
```

这一分支还会在检查设备名之前直接返回。

风险：

- 多台 Aiden 同时存在时可能连接到错误板子；
- 任意实现相同私有 UUID 的设备都可能成为候选；
- 第一台错误候选被赋给 `peripheral` 后，正确设备的 callback 可能被 `isCurrentPeripheral()` 当作 stale callback 忽略。

这个问题不是本次“没有弹窗”的主要直接原因，但属于配对身份和多设备可靠性缺陷。

建议：

- manufacturer identity 存在时必须严格匹配；
- identity 缺失时至少要求稳定设备名匹配，并继续等待可能包含 scan response 的重复 callback；
- 不要在身份和名称都缺失时立即锁定第一台设备；
- 板端配对窗口不能替代 App 侧目标设备识别。

### 7.2 App 把所有安全读取错误都显示为“用户取消”

`didUpdateValueFor` 中，安全探测失败统一进入：

```text
phase = pairing_cancelled
系统蓝牙配对未完成，请重新发起配对。
```

但本次板端是内核 SMP 不可用，不是用户取消。

影响：

- 用户看到的错误原因不准确；
- 多次点击只会重复触发同一板端错误；
- 现场容易误判为 iOS 没执行请求。

建议保留并展示底层 `CBError` / ATT error 的 domain、code、description，同时区分：

- 用户取消；
- 连接失败；
- 不支持加密/认证；
- 超时；
- GATT 权限错误。

### 7.3 手机日志采集缺少可观测性闭环

Native 代码虽然使用了 `[AidenBLE]` 日志，但当前用户采集结果没有这些内容。应在 UI 或 JS 状态中至少展示：

- 当前 phase；
- 最近错误；
- Peripheral identifier/name；
- 扫描是否开始；
- 是否发现设备；
- 是否连接；
- 是否找到 Service/Characteristic；
- 安全读取的原始错误。

## 8. Firmware PR 审查发现

### 8.1 文档声称实现 HOGP，但运行代码没有导出 HOGP

设计和服务文档多处声称：

- 已实现最小 Consumer Control HOGP；
- iOS 系统蓝牙设置中会出现可管理的 Aiden 条目；
- 状态会返回 `HOGPRegistered=true`。

实际代码 `bluez.go` 只导出 Wake Service，广告 UUID 也只有 Wake UUID。运行状态明确为：

```json
"hogp_registered": false
```

`hogp.go` 中的实现基本未被注册流程使用。

这会导致产品预期和实际行为不一致，尤其是：

- 是否能在 iOS 设置中看到设备；
- 是否能通过设置页“忽略此设备”；
- 是否由系统持有长期 HID/Accessory 连接；
- App 卸载后的行为。

需要在合并前二选一：

1. 真正完成并注册 HOGP；或
2. 删除/改写所有 HOGP 已实现的文档和状态承诺。

本次最终选择第 2 项：删除未接入运行流程的 `hogp.go`、相关 UUID/状态字段和测试，并修正文档。当前产品的 HID 能力仍由 USB HID 提供；BLE 只承担安全 Wake 与 ANCS，系统设置中的设备条目来自真实系统 bond，不依赖伪 HOGP 服务。

### 8.2 配对/可信设备策略并不是严格的单手机策略

配对窗口打开时，`deviceAllowed()` 接受任意设备。

`refreshTrustedDevice()` 会优先选择任意当前连接的 bonded device，并把它标记为 trusted，但不会删除其他 bond。

风险：

- 板端可能保留多个 bond；
- “可信设备”可能随着当前连接设备变化；
- 与文档中的单一可信 iPhone 语义不一致；
- 多手机环境下可能发生 Wake/ANCS 归属切换。

建议明确产品策略：

- 单 bond：新配对成功时删除旧 bond；或
- 多 bond：状态/API/文档都显式支持列表，并明确当前 active device 的选举规则。

### 8.3 Advertisement 状态报告过度乐观

`status.Advertising=true` 在 `RegisterAdvertisement` 成功后立即设置。它没有验证：

- Controller advertising enable 状态；
- rfkill/power 状态；
- 外部设备是否能扫描到；
- 广播是否因 Controller/firmware 错误停止。

建议至少把状态拆分为：

- `advertisement_registered`；
- `controller_powered`；
- `rfkill_blocked`；
- `advertising_active`（如果底层可查询）。

### 8.4 HCI 初始化没有满足 AIC 驱动的 power-on 契约

`S39hciinit` 应在 `hciattach` 前显式解除平台 Bluetooth rfkill，并验证结果。

当前脚本只检查：

- UART 设备存在；
- AIC Wi-Fi/Bluetooth firmware driver module 存在；
- `hci0` 能被 `hciconfig` 查询和置为 up。

这些检查不足以证明 AIC Bluetooth subsystem 已经 power-on。

### 8.5 SMP 注册失败没有健康检查

当前服务健康状态只覆盖 GATT/Advertisement 的 D-Bus 注册。即使内核 SMP 不可用，API 仍会返回：

```text
backend_available=true
gatt_registered=true
advertising=true
```

建议在板端加入启动自检或诊断字段，至少能识别：

- crypto 依赖是否存在；
- rfkill 是否解除；
- 加密 GATT 是否能真正触发 SMP；
- 最近一次 pairing/security kernel failure。

## 9. 已执行验证与测试状态

已通过：

- App/Firmware PR `git diff --check`；
- Firmware `go test -race ./internal/ble`；
- Firmware `go test ./...`；
- App BluetoothPairing Jest 10/10；
- App ESLint；
- App iOS native `xcodebuild`（`CODE_SIGNING_ALLOWED=NO`）；
- 板端服务状态检查；
- 板端 Bluetooth HTTP 状态检查；
- 板端 rfkill、内核配置、crypto、模块和 dmesg 检查；
- Mac CoreBluetooth 独立扫描。
- `ble_service` 首次注册后 Wake UUID 过滤扫描；
- 重复打开配对窗口后的过滤扫描对照；
- HCI advertising set 删除、创建、数据和 enable 命令抓包；
- 过滤扫描与无过滤扫描对照；
- 运行时 rfkill block/unblock 与 HCI 重启行为检查。
- HCI management power-on 后 debugfs `0x0006` / `0x0007` 检查；
- iPhone 系统配对弹窗、bond、Wake 订阅和 ANCS 端到端连接；
- stale board bond、`Encryption is insufficient` 和旧 App 重连弹框循环复现；
- 新 Firmware `/api/bluetooth/pairing/reset` 与 `/api/bluetooth/disconnect` 板端部署验证。

仍待完成：

- Shell 测试：临时 clone 中 submodule 未完整检出，失败不代表测试逻辑失败；
- App stale-bond 状态机改动进入线上构建后的最终真机回归；
- “用户忽略 iOS bond、板端保留 bond”与“板端删除 bond、iOS 保留 bond”两个方向的产品化恢复流程完整验收。

## 10. 推荐修复和验证顺序

### 阶段一：稳定 Advertisement 生命周期

1. `ble_service` 启动时只注册一次 Advertisement；
2. 配对窗口打开/关闭时不再执行 `UnregisterAdvertisement/RegisterAdvertisement`；
3. 配对窗口只修改 Adapter `Pairable` / `Discoverable` 和 pairing agent 授权状态；
4. 保持 Wake Service UUID 和 manufacturer identity 的 advertising data 不变；
5. BlueZ 或 HCI 重启后由 `ble_service` 的正常重连流程重新注册一次；
6. 不再把 `RegisterAdvertisement` 成功直接等同于射频广告健康。

回归验证至少连续打开配对窗口 20 次，每次都应满足：

- Mac/iPhone 使用 Wake UUID 过滤能在预期时间内发现设备；
- discovery callback 包含正确 Wake UUID 和 board identity；
- HCI 抓包不再因窗口切换出现 advertising set 删除/重建；
- `ble_service`、BlueZ 和 HCI 重启后能重新注册并恢复广播。

### 阶段二：修复板端启动前置条件

1. 在 `S39hciinit` 中、`hciattach` 之前执行并验证 Bluetooth rfkill unblock；
2. 明确保证 SMP crypto 依赖在 `hci0 up` 之前已经可用；
3. 如果 AES/CMAC 是模块，显式加载并检查 `/proc/crypto`；
4. 前置条件失败时，不要继续把 `hci0` 报告为健康；
5. 再执行 HCI power-on，使内核重新调用 `smp_register()`。

注意：只在当前已经运行的系统中加载 crypto，不重新 power-cycle HCI，可能无法补建 SMP root channel。

注意：2026-08-10 的运行时实验确认，显式 `rfkill block bluetooth` 会使后续 `hciattach` 无法创建 `hci0`；运行中的 block/unblock 还可能让 Controller 和 BlueZ 状态失配。因此 rfkill 修复必须放在启动脚本中验证，不能把生产运行时切换当作无害操作。

### 阶段三：最小 SMP 控制变量实验

该实验会改变板端蓝牙状态，应在确认可中断当前连接后执行：

1. 记录当前 `rfkill list`、`lsmod`、`/proc/crypto` 和 `dmesg`；
2. 解除平台 Bluetooth rfkill；
3. 确认 AES/CMAC/ECDH 可用；
4. 重新初始化 `hci0`，再重启 bluetoothd 和 ble_service；
5. 用 Mac 扫描 Wake UUID；
6. 用 App 发起一次配对；
7. 观察是否仍出现 `security requested but not available`。

结果解释：

- 广播可见且配对成功：启动前置条件就是根因；
- 广播可见但仍无弹窗，且仍有 SMP 日志：继续修内核 SMP 注册；
- 广播仍不可见：继续检查 AIC Controller firmware、HCI advertising command 和天线/射频；
- 不再有 SMP 日志但 iOS 仍不弹框：再回到 App callback 和 ATT error 分析。

### 阶段四：增强内核/SDK 错误处理

建议修改 SDK 内核：

- 检查并记录 `smp_register(hdev)` 返回值；
- SMP 注册失败时阻止蓝牙设备进入“健康可用”状态，或至少输出明确错误；
- 在 crypto 模块后加载场景中支持明确的 HCI restart/retry；
- 保留足够的 boot dmesg，避免真正的初始化错误被后续日志覆盖。

### 阶段五：修复 App 的身份和错误展示

- 不再接受身份、名称都未匹配的第一台同 UUID 设备；
- 把原始 CoreBluetooth/ATT 错误带到 JS/UI；
- 在 UI 显示配对阶段，而不是只有“点击后等待”；
- 收集 `[AidenBLE]` 日志并与板端时间戳对齐。

### 阶段六：设计双方一致的解绑策略并修正文档

- 区分“断开 App BLE 连接”和“双方忘记系统 bond”；
- 不允许只删除板端 bond 后把操作显示成完整解绑；
- iOS CoreBluetooth 没有删除系统 bond 的公开 API，需要明确用户操作、系统设置入口或可控的 repairing 策略；
- 通过只删除板端 bond 的真机实验验证 stale bond 行为；
- 决定是否真正支持 HOGP；
- 让设计文档、运行状态和实际 GATT 导出一致；
- 明确单 bond 或多 bond 策略。

## 11. 修复后的验收条件

完整通过应同时满足：

1. `rfkill list` 中所有 Bluetooth 节点均不是 soft/hard blocked；
2. Mac 和 iPhone 能稳定扫描到 `Aiden-A48B` 及 Wake Service UUID；
3. App 日志依次出现扫描、发现、连接、服务发现、特征发现、安全读取；
4. 首次安全读取触发 iOS 系统蓝牙配对弹窗；
5. 用户确认后，板端不再输出 `security requested but not available`；
6. 板端状态满足：

   ```text
   bonded_device_count=1
   paired=true
   connected=true
   wake_subscriber=true
   ```

7. 断开后能使用已保存 bond 自动重连；
8. 清除配对后能重新触发系统弹窗；
9. 多台 Aiden 同场时 App 不会连接错设备；
10. 文档中的 HOGP 和 bond 行为与实际代码一致。

## 12. 当前最终结论

本次“点击配对没有任何系统弹窗”的核心不是 App 没调用弹窗，而是多个串联故障依次遮蔽了后续阶段。

第一层根因是 Advertisement 生命周期不稳定：配对窗口切换反复删除和重建广告，使 Wake UUID / manufacturer data 丢失或 Controller 实际停止广播。App 的 UUID 过滤扫描收不到 Peripheral，因此不会进入连接和 encrypted read。

第二层根因是 HCI/SMP 启动顺序错误：crypto 未先准备好，并且 `hciconfig hci0 up` 绕过了所需的 BlueZ management power-on。结果是 LE SMP fixed channel `0x0006` 不存在，encrypted read 无法启动系统配对。

第三层是 BlueZ 5.65 的 `encrypt-notify` CCCD 兼容问题。配对触发保留 `encrypt-read`，Wake 订阅改为普通 `notify` 后已完成端到端连接。

第四层是 stale bond 与 App 状态机问题。用户在 iOS 忽略设备但板端保留 bond 时，安全读取会返回 `Encryption is insufficient`。旧 App 将未认证 ACL 当作成功连接，并持续重试，造成“蓝牙已连接”假状态和配对框循环。新实现仅在加密读取和 Wake 订阅成功后报告连接；检测到 bond 不一致即停止重试，下一次用户点击才执行一次受控的板端 bond reset 和重新配对。

最终修复链路为：

```text
保持单一稳定 Advertisement
  -> 连续验证 Wake UUID 过滤扫描
  -> HCI 前加载 AES/CMAC，交由 bluetoothd management power-on
  -> 验证 SMP 0x0006/0x0007
  -> encrypt-read 触发配对，普通 notify 承载 Wake 提示
  -> App 区分 ACL 与安全会话
  -> stale bond 停止重试并由用户明确触发 reset/re-pair
```

截至 2026-08-10，Firmware 已部署上述修复；App 源码修复已通过测试但尚未安装到手机，需要线上构建包完成最后的 stale-bond 和重复弹框验收。

## 13. 2026-08-10 修复、部署与验证

### 13.1 Firmware 修复

PR #486 worktree 已修改：

- `src/agent/internal/ble/pairing.go`
  - `setPairingMode()` 只设置 Adapter `Pairable` / `Discoverable`；
  - 删除配对窗口切换中的 `UnregisterAdvertisement/RegisterAdvertisement`；
  - 删除对 Advertisement properties 的动态修改；
- `src/agent/internal/ble/bluez.go`
  - Advertisement contents 改为 backend 生命周期内固定；
  - Wake Service UUID、manufacturer identity 和本地名称只在启动注册时导出；
  - Advertisement `Discoverable` 保持固定，配对授权由 Adapter 和 pairing agent 控制；
- `src/agent/internal/ble/pairing_test.go`
  - 新增 pairing mode 只修改 Adapter properties 的回归测试；
  - 新增 property 更新失败即停止的测试；
- 更新 BLE 服务和架构文档，明确 Advertisement 生命周期约束。

### 13.2 测试与编译

已通过：

```text
go test ./internal/ble
go test -race ./internal/ble
go test ./internal/agent -run 'Bluetooth|PhoneBridge'
go test ./...
sh scripts/test_ble_init.sh
git diff --check
```

使用本机 Go 1.26.3 直接交叉编译：

```text
GOOS=linux GOARCH=arm GOARM=7 GOTOOLCHAIN=local \
  go build -buildvcs=false -o ../../build/bin/ble_service ./cmd/ble_service
```

产物：

```text
ELF 32-bit LSB ARM EABI5, statically linked
SHA-256 c2e95967a60c3d75be3be54f045f0a76a557f6e0047491190af48000a1617a77
```

### 13.3 板端部署与广告回归

已部署到：

```text
/oem/usr/bin/ble_service
```

并重启：

```text
/etc/init.d/S41ble_service
```

部署后状态：

```text
backend_available=true
gatt_registered=true
advertising=true
pairing_open=false
bonded_device_count=0
```

连续执行 20 次 `POST /api/bluetooth/pairing/start`：

- 20 次均成功；
- HCI 抓包中 `LE Remove Advertising Set (0x203c)` 为 0 次；
- 第一次 closed -> open 时 BlueZ 原地更新参数，并重新下发包含 Wake UUID 和 manufacturer identity 的完整 advertising data；
- 后续重复刷新窗口不再删除或重建 advertising set。

20 次窗口刷新后，Mac CoreBluetooth 按 Wake Service UUID 过滤仍成功发现：

```text
name=Aiden-A48B
rssi=-76
services=[A1DE0001-7C4B-4F52-8D9A-6B4F6E6F7469]
manufacturer=ffff8c7ab3f4a48b
```

因此第一层 Advertisement/UUID 过滤扫描故障已经修复。下一步由 iPhone App 真机验证系统配对弹窗；如果 App 已发现/连接但 encrypted read 仍失败，则继续按本记录的 SMP 和 stale bond 分层排查。

### 13.4 App 重试后的 SMP 证据与启动修复

用户在 Advertisement 修复部署后再次点击 App 连接，板端最新 `dmesg` 记录：

```text
Bluetooth: hci0: security requested but not available
```

同一时段 BlueZ 看到未配对的 iPhone Device。这次已经不是 UUID 过滤扫描超时，而是 App 连接后触发 encrypted read 时，内核没有可用的 SMP fixed channel。

现场 crypto/module 状态：

```text
aes_generic: loaded
ecdh-generic: available
cmac.ko: present at /oem/usr/ko/cmac.ko but not loaded
```

已修改 `overlay/etc/init.d/S39hciinit`：

- 在创建或拉起 `hci0` 前显式加载 `aes_generic.ko` 和 `cmac.ko`；
- 检查 AES 和 ECDH 算法可用；
- 模块或算法缺失时拒绝继续报告 HCI 健康；
- 若在已有 HCI 上首次补载 crypto，执行 HCI down/up，让内核重新运行 `smp_register()`。

已通过：

```text
sh -n overlay/etc/init.d/S39hciinit
sh scripts/test_ble_init.sh
git diff --check
```

修复脚本已部署至板端 `/etc/init.d/S39hciinit`，并在 CMAC 已加载的条件下完整重启：

```text
S39hciinit
S40bluetoothd
S41ble_service
```

部署后 `hci0` 为 `UP RUNNING`，`bluetoothd` 和 `ble_service` 均正常。但仅加载 CMAC 并继续使用 legacy `hciconfig hci0 up` 仍未生成 SMP fixed channel，说明 CMAC 是必要前置条件，不是完整根因。

### 13.5 最终 SMP 根因：legacy HCI power-on

进一步控制变量确认，`S39hciinit` 中的：

```text
hciconfig hci0 up
```

会通过 legacy `HCIDEVUP` 路径抢先把设备标记为 powered，绕过本板 Linux 5.10 / BlueZ 5.65 组合所需的 management power-on 流程。最终修改为：

- `S39hciinit` 先加载 AES/CMAC 并创建 HCI transport；
- 不再执行 `hciconfig hci0 up`；
- 由 `bluetoothd` 通过 management `Set Powered` 上电；
- HCI、BlueZ、`ble_service` 按顺序启动。

修复后 debugfs 明确存在：

```text
0x0006  LE SMP
0x0007  BR/EDR SMP
```

真机随后成功出现系统配对窗口并建立 bond，证明该启动顺序是原始 `security requested but not available` 的直接修复。

### 13.6 Wake CCCD 写权限兼容性

首次成功配对后，iOS 安全读取已经完成，但 BlueZ 5.65 对 `encrypt-notify` 的 CCCD 写仍返回写权限错误。该问题没有通过绕过配对解决，而是按数据边界拆分权限：

- Wake Characteristic 的 `encrypt-read` 保留，用于显式触发并验证系统 bond；
- notification flag 从 `encrypt-notify` 改为 `notify`；
- Wake payload 只包含序号与唤醒原因，不包含通知正文、命令或凭据；
- 实际命令仍通过 USB ECM 上已认证的 HTTP/Phone Bridge 通道获取。

真机验证中，修改后蓝牙、后台 Wake 订阅和 ANCS 系统通知曾同时连接成功。

### 13.7 物理断开与系统 bond

原 App 的“断开”只停止 App 自己的 CoreBluetooth 连接意图，板端 BlueZ/ANCS 链路仍可能保持，导致 iOS 设置中的 `Aiden-A48B` 继续显示已连接。

Firmware 新增：

```text
POST /api/bluetooth/disconnect
```

它调用 BlueZ `Device1.Disconnect` 断开真实物理链路，同时清除 Wake/ANCS 实时状态，但保留双方 bond。保留 bond 不会妨碍之后重连；它只让下一次连接可以复用已有密钥，不必再次弹出系统配对框。

### 13.8 stale bond、假连接与重复配对框

用户在 iOS 设置中忽略设备后，板端仍保留 `1C:0E:C2:A3:D7:75` 的 bond。手机日志反复出现：

```text
[AidenBLE] Secure pairing probe failed: Encryption is insufficient.
[AidenBLE] Disconnected: nil
```

旧 App 的两个行为放大了问题：

1. 只要 `CBPeripheral.state == .connected` 就把“蓝牙连接”显示为已连接，即使 encrypted read 尚未成功；
2. `didConnect` 时立即重置重连退避，安全读取失败后约每 2 秒重连一次，因而持续触发系统配对框。

App 修复：

- 新增 `secureSessionEstablished`，只有 encrypted read 成功后才允许 native `connected=true`；
- UI 的“蓝牙连接”要求板端和 native 都确认安全连接；
- 重连退避只在 Wake 订阅成功后重置；
- 对已有 App/板端 bond 的 `Encryption is insufficient` 进入 `bond_reset_required`；
- 立即停止扫描、连接和自动重试，避免继续弹框；
- 清除 App 保存的 Peripheral 缓存，但保留 `requires_bond_reset` 标记；
- 用户下一次明确点击“重新配对 Aiden 蓝牙”时，先调用 `POST /api/bluetooth/pairing/reset`，再打开配对窗口并只启动一次新尝试。

BlueZ `JustWorksRepairing=always` 已做真机实验，未能修复 BlueZ 5.65 的 stale-bond 行为，已恢复为 `confirm`。

### 13.9 2026-08-10 最新部署状态

使用本机 Go 直接交叉编译并部署：

```text
agent       sha256 3e95373e8ae41dc0b91062149b154a17024cf97648900a7b2c3b3fff84a3501e
ble_service sha256 f7c6f7b0184e1eb79c55e5c6749a1dce520db1dd70dd88f7949e2c8c99fd4ce1
```

板端已验证：

```text
agent=running
ble_service=running
backend_available=true
gatt_registered=true
advertising=true
paired=false
connected=false
pairing_open=false
pairing reset removed=0
SMP fixed channels=0x0006,0x0007
```

当前板端不存在旧 bond，并保持非 Pairable，等待 App 主动打开配对窗口。App 修复未安装本地包；需等线上构建包含本节改动后进行最终测试。
