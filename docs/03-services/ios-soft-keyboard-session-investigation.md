# iOS 软键盘与 USB HID 会话调查记录

## 文档状态

- 调查日期：2026-08-05
- 适用范围：`hid.pointer_mode = "absolute"`、iOS AssistiveTouch、USB HID 键盘/指针与 CDC ECM 复合设备
- 当前结论：尚未找到满足产品要求的固件侧稳定修复；保持 PR #470 的单次启动枚举作为生产基线
- 本文性质：问题复盘、失败实验记录和后续试验约束，不代表可部署方案

## 用户痛点

用户已经开启以下 iOS 选项：

```text
设置 > 辅助功能 > 触控 > 辅助触控
设置 > 辅助功能 > 触控 > 辅助触控 > 显示屏幕键盘
```

但 Aiden 插入 iPhone 后仍会间歇性出现以下问题：

- 指针可以工作，但聚焦输入框后软键盘不弹出。
- 同一套固件、同一台手机、同一次冷插入，结果仍可能时好时坏。
- 关闭再开启“显示屏幕键盘”可以立即恢复，证明 iOS 设置本身已经开启，但当前 HID/AssistiveTouch 会话中的策略状态没有正确应用。
- 要求用户进入系统设置重新开关选项不是可接受的产品体验。

产品修复必须同时满足：

1. 软键盘在需要输入时可靠显示。
2. 不能依赖 Eject 类按键或要求用户手动恢复。
3. 不能让软键盘反复收起、弹出。
4. 不能显著拖慢板子启动、Wi-Fi HTTP 服务或 ECM 建链。
5. 必须保留现有 iOS 修饰键隔离机制。
6. 不能因为修复软键盘而破坏 Android、电脑连接、Agent HID 设备路径或 ECM watchdog。

## 当前硬件与固件约束

Luckfox Pico Zero 当前只有一个 USB Device Controller（UDC）。正常配置包含：

```text
hid.usb0  键盘
hid.usb1  absolute pointer
hid.usb2  Consumer Control
ecm.usb0  CDC ECM
```

这四个 function 属于同一个 USB 复合设备。因此：

- 解绑 UDC 会同时断开键盘、指针、Consumer Control 和 ECM。
- 绑定期间不能安全地向 configfs configuration 新增或移除 HID function。
- 真正实现“键盘已经枚举完成以后，再把鼠标作为新接口插入”必然需要一次新的主机可见 USB 配置过程。
- 在当前单 UDC 拓扑下，无法只重新建立 HID、同时让同一复合设备中的 ECM 保持不断。

## 已确认的生产基线

PR #470（`b03e8efd`，`fix(hid): enumerate startup composite only once`）已经部署并验证：

- `/etc/init.d/S49usbhid` 创建完整复合设备后只绑定 UDC 一次。
- 启动阶段不再执行同身份的自动解绑/重绑。
- 正常 function 顺序保持：键盘、pointer、Consumer Control、ECM。
- keyboard 保持 boot protocol/subclass `1/1`。

PR #470 只解决启动脚本自身的重复枚举。它不能阻止：

- iOS 主机发起 USB bus reset。
- 锁屏、解锁或主机策略变化导致接口重新建立。
- Agent 为修饰键执行的动态 pointer-free profile 切换。

## “旧会话”的暂时解释

这里的“旧会话”不一定表示线缆从未断开，更准确的描述是 iOS 内部出现了残留或错配的 HID 状态：

- 外接键盘抑制软键盘的状态由键盘子系统管理。
- AssistiveTouch 的 pointer 与“显示屏幕键盘”策略由另一套子系统管理。
- 两套状态在复合设备枚举、USB bus reset 或 profile restore 时异步建立。
- 同一 VID/PID/serial 会让 iOS 复用已记住的设备属性或策略。
- bus reset 可以重建接口，但不一定产生完整的物理 detach 生命周期。

一次原始基线日志中观察到：

- 约 13.5 秒首次完成 USB 配置。
- 约 106 秒出现第二次 `dwc3 ... device reset`。
- watchdog 没有观察到完整的 `configured -> not attached` 过程，也没有执行自动 reset。

因此，刚插入时正常、随后又不弹软键盘，并不与 PR #470 已生效矛盾。

## 必须保留的修饰键隔离机制

`/oem/usr/bin/aiden-dynamic-keyboard` 是为 iOS AssistiveTouch 修饰键问题引入的必要机制，不属于本次应删除或绕过的代码。

当键盘报告包含 Ctrl、Shift、Option/Alt 或 Cmd/Meta 时，Agent 会：

1. 切换到 pointer-free 的键盘 + Consumer Control + ECM profile。
2. 使用独立 PID/serial，避免 iOS 继续按 pointer-bearing 描述符路由修饰键。
3. 发送修饰键动作。
4. 在需要 pointer 或运行结束时恢复 normal profile。

大写字母和需要 Shift/AltGr 的符号也可能触发隔离。该机制经过较长时间调试，是当前修饰键可靠性的前提。软键盘修复不得改变它的触发条件、报告格式或语义。

由于只有一个 UDC，该机制现阶段仍会短暂影响 ECM。它可以单独优化，但不能以修复软键盘为理由直接移除。

## 标准 HID 能力调查

检查 USB HID Usage Tables 1.5 后，没有发现标准的“显示系统软键盘”或“重新应用屏幕键盘策略”usage。

相关但不等价的标准 usage 包括：

- `AL Keyboard Layout`
- `AC Next Keyboard Layout Select`
- Keyboard Input Assist 的候选项导航、接受和取消

这些 usage 用于键盘布局管理或输入候选导航，不能要求 iOS 显示系统软键盘。

公开的 Apple vendor keyboard usage 包含 Function、Spotlight、Launchpad、Language、Accessibility Toggle 等，也没有公开、稳定的“显示软键盘”报告。

暂时结论：不存在一个已公开的标准 USB HID 命令，可以让设备要求 iOS 清理当前键盘策略，同时保持原复合设备和 ECM 完全不变。

## 已尝试方法与结果

### 1. 同身份启动重复枚举

早期实现会在完整复合设备绑定后再次解绑/重绑相同 VID/PID/serial。

结果：

- iOS 可能保留物理 USB 会话，同时重建部分 HID 接口。
- pointer 可以工作，但外接键盘和 AssistiveTouch 的软键盘策略可能错配。

结论：不可用。PR #470 已移除该路径。

### 2. 启动后执行 normal -> pointer-free -> normal

相关提交：

- `0641b519 fix(hid): bootstrap iOS pointer sessions`
- `0d3c0224 fix(hid): keep startup enumeration minimal`（回滚）

该方案在正常复合设备启动后，又调用动态 profile controller：

1. 等待 initial normal profile 配置，最长 10 秒。
2. 切换到 pointer-free profile，ECM 仍包含在 profile 中。
3. 等待配置并 settle。
4. 恢复 normal profile，ECM 再次重建。
5. 再等待配置并 settle。

结果：

- 软键盘最终可能恢复。
- USB 设备身份和 ECM 多次重建。
- DHCP、ECM 和上层连接恢复明显变慢。
- 实测电脑侧访问板子可能慢约 30 秒。

结论：方向能影响 iOS 策略，但实现不可作为产品方案。

### 3. 单次枚举中把 pointer 放到 ECM 后面

实验顺序：

```text
keyboard -> Consumer Control -> ECM -> pointer
```

相关提交：

- `0246776c fix(hid): enumerate iOS pointer interface last`
- `e40f7b8c Revert "fix(hid): enumerate iOS pointer interface last"`

目标是在一次 UDC bind 中让 pointer 最后成为主机接口，避免任何额外 ECM 重建。

实际结果：

- iOS 不接受该 configuration。
- `dwc3 ... device reset` 约每 0.5 秒出现一次。
- 软键盘反复收起、弹出。
- ECM 无法稳定配置，网络服务长时间不可用。

结论：HID function 不能放到当前 ECM/IAD 后面；该顺序明确禁止再次部署。

### 4. 纯键盘 bootstrap，最终 composite 才加入 ECM

实验设计：

1. 使用独立 PID/serial 枚举纯键盘，不包含 pointer、Consumer Control 或 ECM。
2. 等待主机配置，最长 1 秒。
3. settle 0.5 秒，detach 0.3 秒。
4. 切换到原始正常顺序的 keyboard + pointer + Consumer Control + ECM。
5. ECM 只在最终 composite 中出现一次。

该方案试图保留“先键盘、后鼠标”的有效意图，同时去掉旧方案中 ECM 的多次建立和三个 10 秒等待窗口。

实际结果：

- 插入后观察到至少 8 次枚举，且仍持续枚举。
- iOS 进入 reset 循环，未稳定进入 final configured 状态。
- 即使 bootstrap 不包含 ECM，快速切换设备身份/拓扑仍不被当前 iOS 会话可靠接受。

结论：不可用。该候选仅存在于未提交实验中，未推送为有效修复。

### 5. 只更换 final normal PID/serial

在保留原始 function 顺序的前提下，运行时将 normal identity 临时改为：

```text
idProduct = 0x0105
bcdDevice = 0x0105
serial = AIDENNORMALRECOVERY1
```

结果：

- 已进入 reset 循环的当前物理连接没有恢复。
- iOS 仍约每 0.5 秒 reset 设备，UDC 保持 `default`。

结论：在 host controller 已卡住时，短暂板端解绑加新身份不足以恢复当前连接。

### 6. 延长板端 UDC 断开时间

曾尝试保持 UDC 断开约 5 秒后再恢复原配置。

结果：

- iPhone 停止向板子供电。
- 板子断电，Wi-Fi SSH 同时消失。

结论：长时间板端断开不是可接受的自动恢复方法。

### 7. 手动重新开关“显示屏幕键盘”

结果：

- 当前坏会话可以立即恢复。
- 不需要改变 USB descriptor 或 ECM。

结论：这是有效的诊断和人工恢复手段，但不是可交付的用户体验。

## 历史实验索引

以下历史提交提供了描述符实验参考，但本次调查没有重新证明其产品可用性：

- `40e3a3a2 test(hid): prewarm pointer before keyboard`
- `fd1b9eb5 test(hid): enumerate pointer before keyboard`
- `09a3ffee test(hid): merge keyboard and pointer reports`
- `42bba7e2 test(hid): delay initial USB enumeration`
- `66e3f573 fix: use report keyboard protocol for iOS modifiers`
- `bd4f1245 Revert "fix: use report keyboard protocol for iOS modifiers"`

不要仅根据提交标题重新启用这些实验；必须先查明当时的实机结果和回滚原因。

## 暂时结论

1. PR #470 的启动单次枚举仍是当前最安全的生产基线。
2. iOS 软键盘问题更像键盘策略与 AssistiveTouch pointer 策略的异步状态竞争，而不是单纯的设置错误。
3. 在一个 UDC、一个复合设备内，无法真正做到 pointer 后加入而 ECM 完全不断。
4. 把 HID interface 移到 ECM 后面会造成持续 USB reset，已被实机否定。
5. 即使 bootstrap 阶段不包含 ECM，快速的纯键盘 -> final composite 身份切换仍会造成持续枚举。
6. 新 PID/serial 不能保证恢复一个已经卡在 reset 循环中的当前物理连接。
7. 标准 HID 和公开 Apple vendor usage 中没有可靠的“显示 iOS 软键盘”命令。
8. 修饰键 pointer-free 隔离机制必须保留，不应与软键盘问题混为一谈。
9. 当前没有同时满足“软键盘必现、ECM 无感、无用户操作、单 UDC”的已验证固件方案。

## 当前生产建议

在找到新的、可测量的机制前：

- 保持 PR #470 的 `S49usbhid`。
- 保持现有 `aiden-dynamic-keyboard` 修饰键隔离。
- 不增加自动启动 profile cycle。
- 不增加同身份启动 rebind。
- 不把任何 HID function 放到 ECM 后面。
- 不执行长时间 UDC disconnect。
- 不把人工开关系统选项包装成产品修复。

## 后续研究方向

### 1. 先定位 reset 的主机控制过程

下一轮描述符试验前，应先拿到 USB control transfer 级别证据：

- 使用硬件 USB protocol analyzer，或开启可用的 DWC3 gadget tracepoint/debug 信息。
- 区分 reset 发生在 device descriptor、configuration descriptor、SET_ADDRESS 还是 SET_CONFIGURATION 阶段。
- 记录每次 reset 前最后一个成功的 control request。

没有该证据时继续排列 function 顺序，试错成本和污染主机缓存的风险都很高。

### 2. 仅在隔离测试设备上验证 HID 内部顺序

仍可研究以下单次枚举顺序：

```text
keyboard -> Consumer Control -> pointer -> ECM
```

它保持 ECM/IAD 最后，只让 pointer 成为最后一个 HID interface。本次没有验证该顺序是否能影响软键盘策略。

该试验必须使用：

- 专用测试板和专用 iPhone。
- 新的测试 PID/serial。
- 独立供电，避免 UDC 断开导致板子掉电。
- 自动 reset 计数和一键恢复脚本。

不得直接部署到正在使用的板子。

### 3. 研究自定义 gadget function

如果必须在一个物理连接内独立控制 pointer 可见性，需要评估：

- 自定义 HID gadget function 与 alternate setting。
- 一个接口内的多个 top-level collection 和 Report ID。
- 内核侧是否能在不解绑整个 UDC 的情况下改变 pointer collection 的可用状态。

这属于内核/USB gadget 级改造，风险和工作量明显高于 init 脚本调整。

### 4. 改变硬件或网络拓扑

如果产品硬约束是“真正先键盘后 pointer，并且 ECM 绝对连续”，可行方向只剩：

- 独立 UDC。
- 独立 USB 设备或 hub 拓扑。
- 将 ECM 移出当前会被 HID profile 切换影响的物理设备。

## 下一轮试验准入标准

新的方案在进入生产板前至少需要满足：

1. 30 次冷插入中软键盘全部按预期显示。
2. 每次插入完成后 UDC 稳定为 `configured` 至少 60 秒。
3. 正常插入阶段不得出现持续或周期性 `dwc3 ... device reset`。
4. ECM 主机接口每次物理插入只建立一次。
5. 网络和 HTTP 可用时间相对 PR #470 基线增加不超过 2 秒。
6. 不出现用户可见的多次软键盘收起/弹出。
7. 锁屏/解锁、重新聚焦输入框后仍稳定。
8. 完成一次 modifier isolation/restore 后软键盘仍稳定。
9. Cmd、Ctrl、Shift、Option、大小写和符号输入全部回归通过。
10. Android touchscreen mode 和电脑连接回归通过。

## 本次事故后的恢复状态

失败实验结束时，板上文件已恢复为实验前备份：

```text
/etc/init.d/S49usbhid
SHA256 8d8ec09be7fa6b38b611fa57897944fb63abaf9ebd6c42e5a45521e1591622ac

/oem/usr/bin/aiden-dynamic-keyboard
SHA256 237a939198aa11f3193202c84390fb51908213659c218b743aa93290904e830d

/etc/aiden_dynamic_keyboard.conf
SHA256 ad02acd900df2cb6dc6514d2eee6be9b9d40684399a87fb81dae04e336c7d5d5
```

为停止当时的持续枚举，最后还显式解绑了 UDC。下一次板子重新上电时，应由恢复后的原始 `S49usbhid` 重新创建正常配置。

失败的 interface-order 提交 `0246776c` 已由 `e40f7b8c` 反向撤销，远端分支最终净代码改动为零。纯键盘 bootstrap 候选未提交、未作为有效修复推送。
