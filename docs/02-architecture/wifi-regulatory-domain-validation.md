# AIC8800 Wi-Fi 监管域真机验证记录

验证日期：2026-09-28。设备为 Luckfox Pico Zero / RV1106G3 / AIC8800DC，内核 `5.10.160`。经 `ssh llm` 从 USB 网段访问设备。开发板的系统时钟显示 2026-04-14，以下时间以执行主机为准。

## 生效路径判定

| 条件 | 操作与结果 |
| --- | --- |
| `custregd=N`，配置 `country=CN` | `iw reg get` 只有 global `country 00`；phy 的信道 12–14 带 `no IR`。执行 `iw reg set CN` 和 `iw reg set US` 后均无变化。 |
| `custregd=N`，两套系统 regdb | Debian 与 upstream 的 `regulatory.db` 分别执行 `iw reg reload`，均返回 `No data available (-61)`。恢复了 Debian 自动选择项。 |
| `custregd=Y`，驱动刚加载 | `iw reg get` 报告 `phy#2 (self-managed)`、`country 00`，规则为 `2380–2520 MHz @ 40 MHz / 20 dBm` 与 `5140–5980 MHz @ 80 MHz / 20 dBm`。 |
| `custregd=Y`，vendor 命令设 CN | `phy#2` 变为 `country CN`；信道 12/13 可用、20 dBm，信道 14 disabled。 |
| `custregd=Y`，vendor 命令设 US | `phy#2` 变为 `country US`；信道 12/13 disabled，信道 11 上限 30 dBm。 |
| vendor 命令设回 CN | `phy#2` 恢复 `country CN` 与相应规则。 |

据此采用方案 **C**：保留 AIC8800 的 self-managed 监管域，通过驱动的 nl80211 vendor 命令设置每个 wiphy 的国家码。`country=` 继续作为持久化权威值，启动和配置事务均需显式把该值应用到驱动。global `country 00` 在此路径下不能代表网卡当前实际规则。

## 命令细节

驱动 vendor ID 为 `0x001a11`，子命令为 `0x100e`。请求包含 `NL80211_ATTR_IFINDEX`、`NL80211_ATTR_VENDOR_ID`、`NL80211_ATTR_VENDOR_SUBCMD`，以及带 `NLA_F_NESTED` 标记的 `NL80211_ATTR_VENDOR_DATA`；内部属性 4 是以零结尾的两位国家码。直接使用 `iw dev wlan0 vendor send ...` 发送原始数据时，内核返回 `expected nested data`。Go 实现使用通用 netlink 构造嵌套属性，并通过 `NL80211_CMD_GET_INTERFACE` 与 `NL80211_CMD_GET_REG` 读回 phy 国家码。

真机上使用测试版 agent 执行 `wifi-region-apply --country=US` 与 `--country=CN`，命令分别读回 `Wi-Fi phy country: US` 和 `Wi-Fi phy country: CN`，同时核对了 `iw reg get` 的 phy 段。两次操作均在运行中完成，无须重启开发板。

## 集成验收

在开发板安装修复版 agent、驱动加载脚本及 systemd 单元后，完成以下验证：

| 场景 | 结果 |
| --- | --- |
| `PUT /api/network/wifi/region`：CN→US→CN | 两次请求均返回 HTTP 200。每次分别核对 `iw reg get` 的 phy 国家码、`wpa_supplicant-wlan0.conf` 的 `country=` 与 `wifi-region.json` 的 `source=user`，三者一致；期间没有重启。 |
| 地区候选配置启动失败 | 在 `/run/systemd/system/wpa_supplicant@wlan0.service.d/` 临时加入仅拒绝 `.region-candidate` 的 `ExecStartPre`，请求切换到 US 返回 HTTP 500 且 `wifi_rollback.ok=true`；phy、磁盘文件与 sidecar 均保持 CN，supplicant 为 `active`。测试覆盖文件和脚本已删除。 |
| 完整重启 | 重启后 `custregd=Y`，新建的 `phy#0 (self-managed)` 读回 CN；驱动、supplicant、配置服务均为 `active`。本次启动 journal 中，驱动于 `[24.418263]` 输出 `Wi-Fi phy country: CN`，supplicant 于 `[24.530302]` 启动。 |
| 重启后再次 CN→US→CN | 两次 API 请求均返回 HTTP 200；phy 与持久配置同步变化，最终恢复 CN。 |
| 无已保存网络时首次连接失败 | 使用不存在的 SSID 启动连接任务，返回 `status=failed` 且 `wifi_rollback.ok=true`；原配置仍无网络，phy 为 CN，supplicant 为 `active`，临时候选文件已删除。 |
| sidecar 落后于权威配置 | 临时把 sidecar 写为 `US/source=user`，保持配置文件 CN。调用地区 GET 接口后，响应和 sidecar 均为 `CN/source=unknown`；配置文件与 phy 仍为 CN。测试结束后恢复原 sidecar。 |

驱动 vendor 命令返回后，phy 监管域更新存在短暂异步延迟。初版集成测试曾在立即读回时误报失败；当前实现采用最长 3 秒的轮询，修复后上述真机验收通过。

Linux 测试环境中，`wifiregion`、`backup` 和 `ota` 包测试通过；`configweb` 包测试包含首次配网期间取消 context 后的独立回滚验证，以及无网络时 supplicant 配置选择器清理失败的错误上报验证。最终部署的 agent 与本地 ARM 构建 SHA-256 一致。

## 2026-09-29 首次固件刷机回归

用户从 llm 上的独立仓库构建并刷入新固件后，只读检查得到：配置文件为 `country=CN`，`custregd=Y`，`wlan0` 属于 `phy#0 (self-managed)`，但该 phy 的监管域仍为 `country 00`。驱动、userdata migration、supplicant 与配置服务均显示 `active`；驱动日志出现 `CAUTION: USING PERMISSIVE CUSTOM REGULATORY RULES`，没有 `Wi-Fi phy country: CN`。本次固件的启动日志中，驱动服务于 `[15.503024]` 开始，migration 于 `[16.465476]` 开始并于 `[18.978780]` 完成，supplicant 于 `[24.129157]` 启动。

设备上的 `/usr/lib/aiden/aiden-wifi-driver` 和 `aiden-wifi-driver.service` 与 llm 构建仓库中的旧版文件 SHA-256 一致；旧版服务没有等待 userdata migration，加载脚本没有调用 `agent wifi-region-apply`。设备 agent 的 SHA-256 `2f2b5f2b071d2fd07e66637a5a86ca70d08eaa30f31f838b04f48476efba622a` 与 llm 的 `output/debian-apps/apps/bin/agent` 一致。llm 仓库缺少 `cmd/daemon/wifi_region_command.go` 和 `internal/wifiregion/driver_linux.go`，其 `main.go` 也未分派 `wifi-region-apply`。该固件虽提供地区 API，实际驱动监管域应用路径没有进入镜像。本次只进行了只读检查，未修改开发板国家码。

结论：**这次刷入的固件未通过监管域生效验收**。需先将本地完整实现同步到 llm 构建仓库，重新构建并核对镜像内 agent、驱动脚本与 systemd 单元；刷机后再检查启动日志出现 `Wi-Fi phy country: CN` 且早于 supplicant 启动，并核对 `iw reg get` 的 phy 国家码与配置文件一致。

## 2026-09-29 重新构建后的固件回归

同步 Mac 与 llm 两处工作区后重新构建并刷机。设备 agent 的 SHA-256 为 `9299dfa03330ea7f38361c1b880637ee8db82b1a92442103b303cf9852a24b5c`，与 llm 的构建产物一致；驱动加载脚本和 systemd 单元也与 llm 源码哈希一致。`custregd=Y`，`wlan0` 属于 `phy#0 (self-managed)`，配置文件、sidecar 和 phy 的国家码均为 CN。启动时序如下：

| 单调时间 | 事件 |
| --- | --- |
| `[16.411073]` | userdata migration 开始 |
| `[19.179385]` | userdata migration 完成 |
| `[19.673284]` | Wi-Fi 驱动服务开始 |
| `[32.808760]` | 驱动服务输出 `Wi-Fi phy country: CN` |
| `[32.905467]` | supplicant 启动 |

CN 下信道 12/13 可用、14 禁用。设备没有已保存网络，`wpa_state=DISCONNECTED`；通过地区 API 运行中切换到 US 和切回 CN 均返回 HTTP 200。US 下 phy 读回 US，信道 12/13 禁用、11 的上限为 30 dBm，配置文件与 sidecar 同步为 US。切回 CN 后，phy 和配置文件恢复 CN，并把测试前的 `source=factory` sidecar 恢复。

通过临时 systemd 覆盖文件仅拒绝 `.region-candidate` 启动，请求切换 US 返回 HTTP 500 且 `wifi_rollback.ok=true`；phy、配置文件与 sidecar 均保持 CN，supplicant 为 `active`。覆盖文件和测试脚本已删除。显式请求 `country=00` 返回 HTTP 400，国家码未变化。当前固件通过启动生效、无网络时运行中切换及失败回滚验收。

内核仍报告 `regulatory.db` 签名或格式无效，驱动初始加载时也输出宽松规则提示；随后 per-phy 监管域已应用并读回 CN。这两条启动日志应继续作为独立问题跟踪，当前采用的驱动 vendor 路径不依赖系统 regdb。

## 尚需发布前确认

- 对外开放的国家列表须按产品认证范围收窄；当前代码表仅表示驱动识别范围。
- 有真实已连接 Wi-Fi 的环境中仍需执行地区切换后的关联与 DHCP 回归；本次开发板没有已保存网络。
- 设备系统时钟落后于执行主机，需单独检查时间同步配置。此问题未影响上述国家码读回。
