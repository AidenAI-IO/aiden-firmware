# Wi-Fi 国家与地区（监管域）自动设置方案

> 文档状态：实施方案（v4，采用分支 C）<br>
> 发布门禁：真机启动、地区切换与回滚验收完成，且对外地区清单经产品认证范围确认<br>
> 目标硬件：Luckfox Pico Zero / RV1106G3 / AIC8800DC（SDIO）<br>
> 目标系统：Debian 13 armhf，内核 5.10.160 BSP<br>
> 生成日期：2026-09-28<br>
> 代码基线：`main` @ `c344a74d`（行号均以此检出为准）<br>
> 适用范围：`src/agent/internal/configweb` 的 Wi-Fi 配置面、AIC8800 驱动加载参数、配置门户前端、备份恢复与 OTA 数据清单

## 1. 结论

1. **真机已选定分支 C。** 出厂驱动启用 `REGULATORY_WIPHY_SELF_MANAGED`，`wpa_supplicant.conf` 的 `country=` 不控制本网卡的监管域。`custregd=0` 路径受系统 regdb 加载失败阻断；驱动 nl80211 vendor 命令已在真机完成 CN→US→CN 验证。详见[验证记录](wifi-regulatory-domain-validation.md)。
2. **配置文件是唯一权威状态。** sidecar 仅承载来源元数据，任何情况下不得据其重写配置文件；两者冲突时丢弃 sidecar。见 [§4.3](#43-权威状态与一致性)。
3. **地区选择是独立交互，不依附于连接流程。** 用户意图通过专用接口显式表达，服务端不从字段相等或前端事件推断。见 [§4.4](#44-用户意图的表达)。
4. **`00` 在本驱动上不是保守世界域**，界面不得以"自动 / 世界通用"呈现，新设备默认值改为 `00` 的原计划撤回。见 [§3.1](#31-关键发现一驱动默认自行管理监管域)。
5. Country IE 的处置在全文只有一条规则，按阶段启用；是否允许自动应用是待决产品项，见 [§4.2](#42-证据强度分级) 与 [§11 未决事项](#11-风险与未决事项)。

## 2. 现状

### 2.1 数据流

| 环节 | 位置 |
| --- | --- |
| 运行时配置文件 | `/userdata/debian/wifi/wpa_supplicant-wlan0.conf` |
| 交给 wpa_supplicant | [`wpa_supplicant@wlan0.service.d/20-aiden.conf`](../../overlay-debian/etc/systemd/system/wpa_supplicant@wlan0.service.d/20-aiden.conf) 经 `AIDEN_WPA_SUPPLICANT_CONFIG` |
| 驱动与固件加载 | [`aiden-wifi-driver`](../../overlay-debian/usr/lib/aiden/aiden-wifi-driver)，模块目录 `/usr/lib/aiden/platform/modules` |
| 首次生成默认值 | [`aiden-userdata-migrate`](../../overlay-debian/usr/lib/aiden/aiden-userdata-migrate) 写入 `country=CN` |
| 读取与规范化 | [`loadWiFiConfig`](../../src/agent/internal/configweb/wifi.go) / `normalizeWiFiCountry`（`wifi.go:117`） |
| 写回 | `renderWiFiConfig`（`wifi.go:143`） |
| 通过 API 修改 | `PUT /api/network/wifi/connection` 的可选字段 `country` |
| 生效 | `applyWiFi(force=true)` → `systemctl restart wpa_supplicant@wlan0` → `networkctl reconfigure wlan0` |

### 2.2 现有缺陷

- **静默 fallback**：`normalizeWiFiCountry` 对任何不满足 `^[A-Za-z]{2}$` 的输入一律返回 `CN`，无错误、无日志。
- **`00` 被判为非法**：现有校验只接受 `A-Z`。
- **前端不暴露 country**：[`wifi.js`](../../src/config_web/web/assets/js/config/wifi.js) 的请求体只有 `ssid / psk / proxy_*`。
- **Country IE 被丢弃**：`handleWiFiScan`（`wifi.go:431`）的 `iw dev wlan0 scan` 完整输出已返回前端，但 `parseWiFiScanOutput`（`wifi.go:360`）只抽取 SSID。
- **`renderWiFiConfig` 兜底位置错误**：它是最后一道写盘口，不应在此处做语义兜底。
- **首次配网失败不恢复运行时状态**：见 [§6.2](#62-修复首次配网失败的运行时回滚前置)。

## 3. 关键发现

### 3.1 关键发现一：驱动默认自行管理监管域

模块由 [`scripts/debian-system/build.sh`](../../scripts/debian-system/build.sh) 从 SDK 源码编译，读到的即出厂件。

```c
/* rwnx_mod_params.c:62 —— 结构体初始化器，FULLMAC 构建取第二个值 */
COMMON_PARAM(custregd, true, true)

/* rwnx_mod_params.c:194 —— 此描述与实际默认值不符，属过期文本 */
MODULE_PARM_DESC(custregd, "Use permissive custom regulatory rules (for testing ONLY) (Default: 0)");
```

编译期默认值为 `true`。[`aiden-wifi-driver`](../../overlay-debian/usr/lib/aiden/aiden-wifi-driver) 的 `insert_if_present aic8800_fdrv.ko he_on="${he_on}"` 未覆盖该参数。在 5.10 内核上 `rwnx_custregd()`（`rwnx_mod_params.c:1944`，由 `rwnx_main.c:6043` 在 `wiphy_register()` 之后调用）必然执行：

```c
wiphy->regulatory_flags |= REGULATORY_IGNORE_STALE_KICKOFF;
wiphy->regulatory_flags |= REGULATORY_WIPHY_SELF_MANAGED;   /* rwnx_mod_params.c:1953 */
regulatory_set_wiphy_regd_sync(wiphy, getRegdomainFromRwnxDB(wiphy, default_ccode));
```

`default_ccode` 由 `aic8800_fdrv/Makefile:9` 的 `CONFIG_COUNTRY_CODE = "00"` 决定，解析到 `regdb.c:19` 的 `regdom_00`：

```c
static const struct ieee80211_regdomain regdom_00 = {
    .n_reg_rules = 2,
    .alpha2 = "00",
    .reg_rules = {
        REG_RULE(2390 - 10, 2510 + 10, 40, 0, 20, 0),   /* flags = 0 */
        REG_RULE(5150 - 10, 5970 + 10, 80, 0, 20, 0),   /* flags = 0 */
    }
```

即 2380–2520 MHz 与 5140–5980 MHz，均为 20 dBm，**无 `NO_IR`、无 DFS 标记**。文件中带 `NO_IR` 的保守版本被 `#if 0` 禁用。

**两条推论：**

1. `00` 在本驱动上是最宽松的规则集，不是保守世界域。界面不得以"自动 / 世界通用"呈现该值；新设备默认值改为 `00` 的原计划撤回。
2. `REGULATORY_WIPHY_SELF_MANAGED` 意味着 cfg80211 不会把用户态 regulatory hint 应用到该 wiphy。`wpa_supplicant` 的 `country=`、`iw reg set` 只改全局 core regdomain。驱动另行提供 vendor 命令（`aic_vendor.c:339`）与 priv `SET COUNTRY`（`aic_priv_cmd.c:1511`）正是为此。

`rwnx_reg_notifier`（`rwnx_main.c:5448`，经 `rwnx_main.c:5934` 注册）仍然存在，在非 self-managed 路径下会把 alpha2 转发给固件。是否 self-managed 决定它是否被触发。

`custregd` 是 `S_IRUGO` 模块参数，可在 insmod 时覆盖为 `0`：`rwnx_custregd()` 将直接返回，`rwnx_set_wiphy_params()` 中的 `custregd` 分支亦跳过，permissive `regdom_00` 永不生效。**该修复必须经阶段 0 验证，不得据静态分析直接采用。**

### 3.2 关键发现二：内核侧具备标准监管域能力

- [`rv1106-sdiowifi.config`](../../pico-sdk/sysdrv/source/kernel/arch/arm/configs/rv1106-sdiowifi.config)：`CONFIG_CFG80211=m`、`CONFIG_CFG80211_REQUIRE_SIGNED_REGDB=y`、`CONFIG_CFG80211_USE_KERNEL_REGDB_KEYS=y`。
- [`scripts/debian-system/packages.list`](../../scripts/debian-system/packages.list) 含 `wireless-regdb`。

内核配置具备标准监管域能力，但真机上 `custregd=0` 时 `iw reg reload` 对 Debian 与 upstream 两套数据库均返回 `No data available (-61)`，`iw reg set CN/US` 未改变 global `00` 或 phy 规则。当前采用已验证的驱动 vendor 路径；regdb 加载失败的根因仍需独立排查，不影响分支 C 的生效路径。

## 4. 设计原则

### 4.1 provenance（来源）优先级

| 来源 | 优先级 | 语义 | 可被自动逻辑覆盖 |
| --- | --- | --- | --- |
| `user` | 1 | 用户经地区设置接口显式确认 | 否 |
| `existing` | 2 | 升级前配置文件已有、且无 sidecar 的合法值 | 否 |
| `beacon` | 3 | 802.11d Country IE 去重多数 | 可被更强的 beacon 结果更新 |
| `browser_tz` | 4 | 门户所在浏览器上报的 IANA 时区 | 是 |
| `timezone` | 5 | `agent.toml` 的 `timezone`（非 UTC） | 是 |
| `factory` | 6 | 首次开机由 migrate 脚本写入的默认值 | 是 |

`factory` 与 `existing` 必须区分：若不区分，新设备写入的合法默认值会被误判为 `existing` 并短路决策链，导致永不检测。migrate 脚本写默认值时同时写入 `{"source":"factory"}` 的 sidecar；**仅当「配置文件有合法值且 sidecar 缺失」时**才判定为 `existing`。

### 4.2 证据强度分级

| 信号 | 能证明什么 | 处置 |
| --- | --- | --- |
| Country IE | 周围射频环境**正在使用**的规则，不能证明设备所在地 | 见下方单一规则 |
| 浏览器时区 | 操作者设备的系统设置 | 仅作为候选值呈现，永不自动应用 |
| agent timezone | 设备显示时区，非位置 | 仅作为候选值呈现，永不自动应用，`UTC` 不参与 |

**Country IE 的单一规则（全文唯一定义，阶段 2 与阶段 3 均以此为准）：**

- 解析与计票在阶段 2 交付，**阶段 2 只呈现候选值，不写入任何配置**；
- 自动应用能力在阶段 3 交付，受配置项 `wifi.region.auto_apply_beacon` 控制，**默认关闭**；
- 开启后，自动应用的门槛为：按 BSSID 前 5 字节（/40）归并后票数 `>= 3`、唯一最高、且全部票值通过 allowlist；
- 任何时候都不输出 `confidence` 标签，只返回票数，由上层判断。

BSSID 归并只能降低同一台 AP 多 SSID 的重复计票，**不能证明 AP 之间彼此独立**（同一运营商批量部署、同一型号默认配置均会同向偏置）。是否启用自动应用是产品决策，见 [§11](#11-风险与未决事项)。

### 4.3 权威状态与一致性

**配置文件（`wpa_supplicant-wlan0.conf` 的 `country=`）是唯一权威状态。** sidecar 只承载来源元数据，用于界面展示与决策链输入。

- **sidecar 永不重写配置文件。** 两者的 `country` 不一致时，以配置文件为准，将 sidecar 的 `country` 校正为配置文件的值并把 `source` 降级为 `unknown`，记录一条日志。
- 因此不存在"启动对账改写生效配置"的路径，也就不依赖 config-web 是否启动。[`aiden-config-web.service`](../../overlay-debian/etc/systemd/system/aiden-config-web.service) 带 `ExecCondition=/usr/lib/aiden/aiden-feature-enabled ENABLE_CONFIG_WEB`，可被特性开关禁用；本设计不得依赖它在 Wi-Fi 服务之前运行。
- sidecar 损坏、缺失或与配置文件冲突，一律视为"来源未知"，功能降级为手动选择，不影响联网。
- 写入顺序固定为：先配置文件（经既有事务），成功后再写 sidecar。sidecar 写失败只记日志，不回滚配置文件，也不使连接失败。

这一约定的代价是：断电可能导致 sidecar 落后于配置文件，表现为界面显示"来源未知"。这是可接受的降级，换来的是不存在任何路径能让陈旧的 sidecar 改变射频行为。

### 4.4 用户意图的表达

用户意图**只能**通过专用接口显式表达，服务端不从以下任何信号推断：

- 提交值是否等于推荐值（用户完全可能主动选中推荐值）；
- 前端是否触发过 `change` 事件（客户端可伪造）；
- 连接请求是否携带 `country` 字段。

具体做法见 [§8.4](#84-api)：地区设置是 `PUT /api/network/wifi/region` 的独立交互，界面上是一次单独的确认动作。连接接口 `PUT /api/network/wifi/connection` 保留 `country` 字段供旧客户端确认服务端本次决策值；若它与服务端决策值不同，连接任务拒绝该请求并要求先调用地区专用接口。该路径**永远不会产生 `user`**，也不接受用户提交 `00`。

## 5. 阶段 0：真机验证（发布门禁）

目的是确定监管域的实际生效路径。真机记录见[验证报告](wifi-regulatory-domain-validation.md)。

### 5.1 准备：隔离干扰

[`aiden-wlan-guard`](../../overlay-debian/usr/lib/aiden/aiden-wlan-guard) 会在网关不可达时 `reassociate`、`ip link` 重置并 `systemctl restart wpa_supplicant@wlan0`，足以污染对比结果。验证全程必须停用，并在结束后恢复。

```bash
systemctl stop aiden-wlan-guard.service
cp -a /userdata/debian/wifi/wpa_supplicant-wlan0.conf /tmp/wpa-orig.conf
cat /sys/module/aic8800_fdrv/parameters/custregd > /tmp/custregd-orig
```

收尾（无论成败均须执行）：

```bash
cp -a /tmp/wpa-orig.conf /userdata/debian/wifi/wpa_supplicant-wlan0.conf
systemctl restart aiden-wifi-driver.service wpa_supplicant@wlan0.service
systemctl start aiden-wlan-guard.service
```

### 5.2 采集基线

```bash
cat /sys/module/aic8800_fdrv/parameters/custregd
iw reg get
iw phy phy0 info | sed -n '/Frequencies/,/valid interface/p'
dmesg | grep -iE 'regulatory|regdomain|CAUTION'
grep '^country=' /userdata/debian/wifi/wpa_supplicant-wlan0.conf
```

记录：`custregd` 实际值；`iw reg get` 是否出现 `phy#0 (self-managed)`；各信道的 `disabled` / `no IR` / `radar detection` 标记与 dBm 上限。

### 5.3 验证 `country=` 是否有效

```bash
sed -i 's/^country=.*/country=US/' /userdata/debian/wifi/wpa_supplicant-wlan0.conf
systemctl restart wpa_supplicant@wlan0.service && networkctl reconfigure wlan0
```

重新采集 5.2 全部输出。判据为 **ch12/ch13 的标记与 5 GHz 的 dBm 上限是否变化**。仅看 `iw reg get` 全局段、配置文件内容或 dmesg 均不足以证明。

### 5.4 验证 `custregd=0` 分支

模块目录为 `/usr/lib/aiden/platform/modules`，固件路径需与 [`aiden-wifi-driver`](../../overlay-debian/usr/lib/aiden/aiden-wifi-driver) 保持一致。

```bash
systemctl stop wpa_supplicant@wlan0.service
rmmod aic8800_fdrv
insmod /usr/lib/aiden/platform/modules/aic8800_fdrv.ko he_on=0 custregd=0
systemctl start wpa_supplicant@wlan0.service
```

重复 5.2 与 5.3，确认：self-managed 标记是否消失；`regulatory.db` 是否成功加载（`dmesg | grep -i 'regulatory.db'`，签名校验失败会在此暴露）；`country=` 是否能改变 phy 规则；2.4/5 GHz 可用信道与功率的变化是否符合 `wireless-regdb` 中对应国家的条目。

### 5.5 判定矩阵与三条分支

| 5.3 | 5.4 | 分支 | 生效路径 | 界面选项 | 对后续阶段的影响 |
| --- | --- | --- | --- | --- | --- |
| 有效 | — | **A** | `wpa_supplicant country=`，搭载既有连接事务 | 完整地区列表，不含 `00` | 阶段 1–3 按本文档执行 |
| 无效 | 有效 | **B** | 同 A，但需在 `aiden-wifi-driver` 的 `insert_if_present aic8800_fdrv.ko` 行追加 `custregd=0` | 完整地区列表，不含 `00` | 增加 5 GHz 行为变化回归项；驱动参数变更须过 BSP 审计 |
| 无效 | 无效 | **C** | 驱动 vendor 命令或 priv `SET COUNTRY`，不经 wpa_supplicant | 完整地区列表，不含 `00` | 阶段 3 的生效与回滚需重新设计；既有连接事务无法复用，工作量显著上升 |

三条分支共同点：`00` 均不作为用户可选项呈现。

真机判定为 **C**。`custregd=0` 已验证无效；`custregd=1` 下的驱动 vendor 命令使 phy 规则随 CN、US 双向变化。后续实现及验收以 C 路径为准。

### 5.6 交付物

[验证报告](wifi-regulatory-domain-validation.md)已记录 CN→US→CN、分支判定、地区设置失败回滚、首次配网失败回滚及完整重启后的持久化结果。有已连接网络时的关联与 DHCP 回归仍须在具备测试 AP 的环境完成。

## 6. 阶段 1：可选择、严格校验与回滚修复

与监管域生效机制无关，可在特性分支先行开发；合入发布分支仍受阶段 0 门禁约束。

### 6.1 新建 `internal/wifiregion`（本阶段仅校验部分）

```go
func Normalize(value string) (string, bool)  // 合法 → (大写码, true)；非法 → ("", false)
func IsSupported(code string) bool           // allowlist 判定
func Options(locale string) []Option         // 下拉框选项，服务端唯一事实源
```

allowlist 以 `aic8800_fdrv/regdb.c` 的 170 个 alpha2 为**上界**（建议脚本机械提取），实际对外开放的列表按已完成认证的地区收窄，由产品与认证方确认。`00` 在内部作为合法值被 `IsSupported` 接受（配置文件里可能存在），但**不进入 `Options`**，用户无法选择。

### 6.2 修复首次配网失败的运行时回滚（前置）

`runWiFiConnection`（`wifi.go:563`）的回滚分支当前为：

```go
if len(original.Networks) > 0 {
    rollback = s.applyWiFi(ctx, s.options.WiFiConfigPath, true)
} else if commandExists("wpa_cli") {
    rollback = runCommandContext(..., "wpa_cli", "-i", s.options.WiFiInterface, "disconnect")
}
```

原配置无网络（首次配网）时只执行 `disconnect`，不重新加载原配置；而 candidate 文件此时已被删除，运行中的 wpa_supplicant 仍持有 candidate 的 country。磁盘与运行时立即分歧，且无任何后续路径修复。

**改为失败路径始终以原配置重新加载 supplicant**，无论原配置是否含网络：

```go
rollback = s.applyWiFi(ctx, s.options.WiFiConfigPath, true)
```

这是既有缺陷，但本方案会让它从"无网络可连"放大为"射频规则与磁盘不符"，因此纳入本方案范围，并作为阶段 3 的前置条件。

### 6.3 去掉静默 fallback

| 位置 | 现状 | 改为 |
| --- | --- | --- |
| `loadWiFiConfig` | 非法 → `CN` | 非法 → 标记来源为 `invalid`，交决策链重算 |
| `renderWiFiConfig` | 非法 → `CN` | 只接受已解析值，非法返回 error；此处不再兜底 |
| `handleWiFiConnect` | 无校验 | **在创建后台任务之前**校验，非法 → HTTP 400 |

`handleWiFiConnect`（`wifi.go:479`）先同步校验 SSID 与代理参数，随后起 goroutine 并返回 202 Accepted。country 校验必须与 `wifiproxy.ValidateSSID` 并列；若放进 `runWiFiConnection`，HTTP 响应早已发出，400 无法送达。

### 6.4 前端下拉选择器

- [`index.html`](../../src/config_web/web/index.html)：Wi-Fi 卡片头部新增只读的"当前地区"展示，并在其旁提供一个独立的"设置地区"入口（打开地区确认对话框）。
- 选项由服务端 `Options` 下发填充，**不硬编码于 HTML**。
- [`i18n.js`](../../src/config_web/web/assets/js/config/i18n.js)：新增 `wifi.region.*` 系列键，`en-US` 与 `zh-CN` 各一份。
- **国家名称不进翻译表**：服务端只下发国家码，客户端用 `Intl.DisplayNames` 按当前 locale 本地化，不支持时回退显示原始码。169 个国家 × 2 种语言的硬编码字符串没有维护价值，平台本来就有这份数据。

**服务端必须独立校验**，下拉框只是交互约束，不构成安全边界。

## 7. 阶段 2：Country IE 解析（只读）

本阶段**不写入任何配置**，只解析与呈现，与阻断项解耦。

### 7.1 解析

`iw dev wlan0 scan` 每个 BSS 段输出形如：

```
	Country: DE	Environment: Indoor/Outdoor
```

改造 `parseWiFiScanOutput`（`wifi.go:360`）：

```go
type wifiScanResult struct {
    SSIDs     []string
    Countries map[string]int // alpha2 → 按 BSSID /40 归并后的计数
}
```

要点：

- 以 `BSS xx:xx:xx:xx:xx:xx` 行分段，按 BSSID 前 5 字节归并；
- 解析对象改为 `result.Output`，而非当前拼接了 `$ ip link set ...` 命令回显的合并字符串；
- 票值须通过 allowlist，`00` 与 `XX` 视为无效票丢弃；
- `iwlist` 回退路径不输出 country IE，此时 `Countries` 为空，属已知降级。

### 7.2 API

扫描响应保持 `networks` 为 `[]string` 不变（前端 `appState.networks` 依赖），并列新增：

```json
{"ok": true, "networks": ["..."], "country_votes": {"DE": 7, "AT": 1}}
```

不输出 `confidence` 字段。

## 8. 阶段 3：provenance、事务与恢复

本阶段采用已验证的分支 C。配置文件承载持久化国家码，驱动 vendor 命令负责应用到 self-managed wiphy。

### 8.1 sidecar

参照 [`internal/wifiproxy`](../../src/agent/internal/wifiproxy/config.go) 的既有模式，新增 `internal/wifiregion` 的持久化部分：

```
/userdata/system/wifi-region.json
{"version":1,"country":"DE","source":"beacon","detected_at":"2026-09-28T10:00:00Z","votes":{"DE":7,"AT":1}}
```

**配置文件的 `country=` 是持久化权威值**，实际射频规则由驱动当前 wiphy 监管域决定；sidecar 只记录来源与依据，按 [§4.3](#43-权威状态与一致性) 永不反向改写。启动时在 supplicant 上线之前用驱动 vendor 命令应用配置值，并读回 phy 国家码。

### 8.2 决策链

```go
func Resolve(in Inputs) Decision {
    if in.Saved.Source == SourceUser && IsSupported(in.Saved.Country) {
        return Decision{in.Saved.Country, SourceUser}
    }
    if in.Saved.Source == SourceExisting && IsSupported(in.Saved.Country) {
        return Decision{in.Saved.Country, SourceExisting}
    }
    if c, ok := VoteBeacon(in.ScanVotes); ok {           // /40 归并后 >=3 票且唯一最高
        return Decision{c, SourceBeacon}                 // 是否落盘由 auto_apply_beacon 决定
    }
    if c, ok := ianaTimezoneCountry[in.BrowserTimezone]; ok {
        return Decision{c, SourceBrowserTZ}              // 仅候选，永不自动落盘
    }
    if in.AgentTimezone != "UTC" {
        if c, ok := agentTimezoneCountry[in.AgentTimezone]; ok {
            return Decision{c, SourceTimezone}           // 仅候选，永不自动落盘
        }
    }
    return Decision{in.CurrentCountry, SourceUnknown}    // 保持现状，不做任何改变
}
```

纯函数，零 I/O。**兜底为"保持现状"**，不引入任何新值。

调用方据 `Decision.Source` 决定是否落盘：`SourceBeacon` 且 `auto_apply_beacon` 为真时落盘，其余一律仅作为候选值返回给界面。

### 8.3 时区映射表

- 小表：覆盖 [`timezone.go`](../../src/agent/internal/agent/timezone.go) 中 `supportedTimezones` 的 27 项，其中 26 项可 1:1 映射到唯一国家，`UTC` 不入表。须加测试断言两者键集合一致，防止上游增删后漂移。
- 大表：浏览器可上报任意 IANA 时区，由 tzdata 的 `zone.tab` 生成，附 `go:generate` 入口。查不到即查不到，不做前缀猜测。

configweb 已 import `internal/agent` 并调用 `agent.LoadRuntimeConfig`，直接读取 `cfg.TimezoneOrDefault()` 即可。

### 8.4 API

```
GET  /api/network/wifi/region                      读取当前地区与候选值
POST /api/network/wifi/region/resolve              {"browser_timezone": "Europe/Berlin"} → 候选值
PUT  /api/network/wifi/region                      {"country": "DE"} → 用户显式确认，source=user
```

`POST .../resolve` 采用 POST 而非 GET 携带 query，避免客户端环境信息进入访问日志。响应：

```json
{
  "current":    {"country": "CN", "source": "existing"},
  "candidates": [
    {"country": "DE", "source": "beacon", "votes": {"DE": 7}},
    {"country": "AT", "source": "browser_tz"}
  ]
}
```

`PUT .../region` 是唯一能产生 `source=user` 的入口，对应界面上一次独立的确认动作。它同样需要让新地区生效，因此复用 [§8.5](#85-事务) 的事务：以当前已保存的网络列表 + 新 country 构造 candidate，验证通过后落盘。若设备当前未连接任何网络，则仅写配置文件并重启 supplicant，不做连通性判定。

### 8.5 事务

连接事务顺序为：生成 candidate → 向驱动 vendor 命令设置 candidate 国家码并读回 phy → 重启 supplicant 使用 candidate → 验证目标 SSID 关联与 IP → 持久化配置文件与代理配置 → 写 sidecar。任何失败均以独立的有界 context 恢复原驱动国家码、原配置文件及原 supplicant 运行配置。原设备未连接时，独立地区修改只检查驱动设置和 supplicant 重启结果，不要求关联或 DHCP 成功。

地区专用事务同样先写 candidate，再应用驱动与 supplicant；成功后写权威配置文件和 sidecar。运行中的 supplicant 持有 candidate 路径，成功后保留该文件供后续 reconfigure 使用。启动路径由 `aiden-wifi-driver.service` 等待 userdata migration，驱动加载后执行 `agent wifi-region-apply`，在 supplicant 启动前设置并读回 phy 国家码。驱动或读回失败会使该服务失败，避免按未经确认的规则启动 Wi-Fi。

sidecar 写入排在配置文件事务成功之后，失败只记日志（见 [§4.3](#43-权威状态与一致性)）。

### 8.6 备份、恢复与 OTA

新增文件 `system/wifi-region.json` 须同时登记在**四处**，缺任意一处都会导致硬失败：

| 位置 | 作用 | 缺失后果 |
| --- | --- | --- |
| [`backup/components.go`](../../src/agent/internal/backup/components.go) 的 `ComponentNetwork` | 备份来源 | 文件不进备份 |
| [`configweb/restore_jobs.go`](../../src/agent/internal/configweb/restore_jobs.go) 的 `ComponentNetwork` 路径映射 | 恢复目标 | 该 `switch` 的 `default` 分支返回 `unexpected network path`，**整个网络组件恢复被拒绝** |
| [`ota/data_snapshot.go`](../../src/agent/internal/ota/data_snapshot.go) 的 `relativePaths` | OTA 快照来源 | 文件不进快照 |
| [`ota/data_snapshot.go`](../../src/agent/internal/ota/data_snapshot.go) 的 `allowed` 校验表 | OTA 恢复白名单 | 含该文件的快照恢复时报 `invalid protected snapshot path`，**快照整体无法恢复** |

恢复后若 sidecar 与配置文件冲突，按 [§4.3](#43-权威状态与一致性) 丢弃 sidecar 的 country 并降级来源，不改写配置文件。

## 9. 文件改动清单

| 文件 | 阶段 | 改动 |
| --- | --- | --- |
| `src/agent/internal/wifiregion/`（新建） | 1 / 3 | `Normalize`、`IsSupported`、`Options`、`Resolve`、`VoteBeacon`、allowlist、时区表、sidecar 读写 |
| `src/agent/internal/wifiregion/gen/`（新建） | 3 | 由 `zone.tab` 生成大表的 `go:generate` 工具（418 条） |
| `scripts/gen_wifi_countries.sh`（新建） | 1 | 由 `regdb.c` 生成 allowlist（170 条），避免手抄 |
| `src/agent/internal/configweb/wifi.go` | 1 / 2 / 3 | 移除 `normalizeWiFiCountry`；失败路径始终重载原配置；`handleWiFiConnect` 前段加 400 校验；`parseWiFiScanOutput` 返回 `wifiScanResult`；扫描响应加 `country_votes`；连接事务接入决策链 |
| `src/agent/internal/configweb/wifi_region.go`（新建） | 3 | 三个新 HTTP handler |
| `src/agent/internal/configweb/api.go` | 3 | 注册 3 条路由与 `apiEndpoint` 枚举 |
| `src/agent/internal/configweb/options.go` / `run.go` | 3 | 新增 `WiFiRegionStatePath`（默认 `/userdata/system/wifi-region.json`）与 `WiFiRegionAutoApplyBeacon`，对应 `--wifi-region-state` 和 `--wifi-region-auto-apply-beacon` 两个 flag |
| `src/agent/internal/configweb/restore_jobs.go` | 3 | `ComponentNetwork` 路径映射补 sidecar |
| `src/agent/internal/backup/components.go` | 3 | `ComponentNetwork` 备份来源补 sidecar |
| `src/agent/internal/ota/data_snapshot.go` | 3 | `relativePaths` 与 `allowed` 两张表均补 sidecar |
| `overlay-debian/etc/systemd/system/aiden-config-web.service` | 3 | ExecStart 追加 `--wifi-region-state=...` |
| `overlay-debian/usr/lib/aiden/aiden-wifi-driver` 与对应 systemd service | 0 → 3 | 分支 C 在 supplicant 启动前调用 agent，将配置国家码应用到 self-managed wiphy 并读回；等待 userdata migration |
| `src/agent/internal/wifiregion/driver_linux.go` 与 `cmd/daemon/wifi_region_command.go` | 3 | 构造嵌套 nl80211 vendor 命令、读取 phy 国家码，提供启动命令 |
| `overlay-debian/usr/lib/aiden/aiden-userdata-migrate` | 3 | 写默认值时同时写 `{"source":"factory"}` sidecar；默认值维持 `CN`，不改为 `00` |
| `src/config_web/web/index.html` | 1 | 当前地区展示 + 独立的地区设置入口与确认对话框 |
| `src/config_web/web/assets/js/config/wifi.js` | 1 / 3 | 地区读取、候选值展示、显式确认调用；`Intl.DisplayNames` 本地化 |
| `src/config_web/web/assets/js/config/app.js` | 1 | 地区动作分发与页面加载时拉取 |
| `src/config_web/web/assets/js/config/state.js` | 1 | `appState.wifiRegion` |
| `src/config_web/web/assets/css/config.css` | 1 | 地区展示行样式 |
| `src/config_web/web/assets/js/config/i18n.js` | 1 | `wifi.region.*` 双语文案 |

## 10. 测试计划

### 10.1 Go 单元测试

沿用 [`configweb_test.go`](../../src/agent/internal/configweb/configweb_test.go) 既有的 `t.Setenv("PATH", binDir)` + 伪造命令脚本模式。

- `wifiregion`：`Normalize` 边界（含 `00`、`us`、`USA`、换行注入串）；`Options` 不含 `00`；`VoteBeacon` 的并列 / 单票 / 无效票 / 同 AP 多 SSID 归并；`Resolve` 六级优先级，**重点断言 `user` 与 `existing` 不被覆盖、`factory` 不短路、兜底返回"保持现状"**；两张时区表与 `agent.SupportedTimezones()` 的键集合一致性。
- `parseWiFiScanOutput`：以真实 `iw scan` 输出为 fixture，验证 SSID 与 country 双路解析及 BSSID 归并。
- 回滚修复：构造原配置无网络的场景，断言失败路径**调用了 `applyWiFi` 而非仅 `disconnect`**。
- 权威状态：sidecar 与配置文件冲突时，断言配置文件未被改写、sidecar 来源降级为 `unknown`。
- 意图判定：断言 `PUT /connection` 携带 `country` 时**不产生 `source=user`**；仅 `PUT /region` 可产生。
- HTTP 层：非法 country 返回 400 且**未创建后台任务**。
- 备份恢复：端到端跑一次含 sidecar 的备份→恢复，断言网络组件未被拒绝；OTA 快照→恢复，断言未报 `invalid protected snapshot path`。
- 回归：`TestWiFiCountryValidation`（`configweb_test.go:1405`）需重写，它当前断言的正是待移除的"非法 → CN"行为。

### 10.2 真机验收

```bash
iw reg get | sed -n '/phy#/,$p'
iw dev wlan0 info | grep wiphy
iw phy "phy$(iw dev wlan0 info | awk '/wiphy/ {print $2; exit}')" info | sed -n '/Frequencies/,/valid interface/p'
cat /sys/module/aic8800_fdrv/parameters/custregd
cat /userdata/system/wifi-region.json
grep '^country=' /userdata/debian/wifi/wpa_supplicant-wlan0.conf
```

判据是 **phy 侧的信道标记与功率上限**。phy 编号会随驱动重载变化，须从 `iw dev wlan0 info` 动态读取。覆盖 CN→US、US→CN、连接失败回滚、首次配网失败回滚、断电后 sidecar 落后五个场景。验证期间须按 [§5.1](#51-准备隔离干扰) 停用 wlan guard。

## 11. 风险与未决事项

| 项 | 级别 | 说明 |
| --- | --- | --- |
| 监管域实际生效路径 | 已判定 | 真机采用分支 C；CN→US→CN、启动持久化和 API 回滚已验证，见[验证报告](wifi-regulatory-domain-validation.md) |
| **是否允许 Country IE 自动应用** | **待决** | 现设计默认关闭（`auto_apply_beacon=false`）。审查意见认为 IE 无法证明设备所在地、BSSID 归并不能证明 AP 独立，主张一律需用户确认；产品侧原始需求为"自动设置"。需产品拍板，默认值一行可改 |
| 对外开放的地区清单 | 待决 | 驱动表 170 项只是上界，实际清单须按已认证地区收窄 |
| `regulatory.db` 加载 | 已观察 | Debian 与 upstream 数据库的 `iw reg reload` 均返回 `No data available (-61)`；分支 C 不依赖系统 regdb。根因可另行排查 |
| 存量设备行为变化 | 高 | 靠 `source=existing` 短路保护，需一次 OTA 后的现场回归确认 country 未变 |
| 分支 B 的驱动参数变更 | 中 | `custregd=0` 影响所有射频行为，须过 BSP 审计与长稳测试，不能只看信道表 |
| 合规 | 中 | 向终端用户开放地区选择在部分市场（尤其 FCC 软件安全 KDB 594280）可能不被接受，或需置于工程模式之后。需与认证实验室确认。本文档不构成法律意见 |
| sidecar 落后于配置文件 | 低 | 断电场景下表现为界面"来源未知"，功能降级为手动选择，不影响联网 |
| `iwlist` 无 country IE | 低 | 已知降级，落到候选值为空 |

## 12. 变更记录

| 日期 | 变更 |
| --- | --- |
| 2026-09-28 | v1 初版。阶段 0 阻断项、provenance 模型、证据强度分级 |
| 2026-09-28 | v2。配置文件确立为唯一权威状态，移除启动对账；用户意图改为独立接口显式表达；首次配网失败的运行时回滚纳入范围；备份恢复补齐四处登记点；阶段 0 命令按 `main` 检出修正并增加 guard 隔离；阶段边界改为"可预研、不可发布"；`00` 移出用户可选项；Country IE 处置统一为单一规则并加配置开关 |
| 2026-09-28 | **阶段 0 部分判定（真机实测，Luckfox Pico Zero，固件含本方案改动）**：`custregd=Y`，`iw reg get` 显示 `phy#0 (self-managed)`，规则为 `2380-2520 @ 40 / 20 dBm` 与 `5140-5980 @ 80 / 20 dBm`，**无 PASSIVE-SCAN、无 DFS、无 NO-IR**，与 `regdb.c:19` 的 `regdom_00` 逐条吻合；ch1–14 全部可用且均为 20 dBm（含仅日本可用的 ch14）。全局 regdomain 是内核保守世界域，但网卡不使用它。**结论：不是分支 A —— `wpa_supplicant` 的 `country=` 不控制本网卡。** 分支 B 与 C 的区分待做（需 root 执行 `custregd=0` 重载模块） |
| 2026-09-28 | v3。阶段 1–3（分支 A/B 假设下）实现落地于 `feat/wifi-regulatory-domain`：`internal/wifiregion` 包、`configweb` 改造、四处备份恢复登记点、overlay 与前端。国家名称改用 `Intl.DisplayNames`，不再进翻译表。`--wifi-region-auto-apply-beacon` 默认关闭 |
| 2026-09-28 | v4。真机确认 `custregd=0` 路径无效，驱动 vendor 命令可双向更新 phy 规则，采用分支 C；§8.5 改为驱动与 supplicant 联合事务，启动阶段增加 phy 国家码应用与读回。 |
| 2026-09-28 | 完成 vendor 驱动应用与异步读回轮询；真机验证 API 双向切换、候选启动失败回滚、首次配网失败回滚和完整重启后恢复。 |
| 待补 | 已连接网络的关联与 DHCP 回归；产品认证地区清单。 |
