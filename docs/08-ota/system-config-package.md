# 业务包管理运行配置

运行配置直接进入 `aiden-business`，不另发 `aiden-system-config`。每个版本只有一个
`.deb`，程序、资源、配置和维护脚本一起升级或降级。配置即使只有几行，也可随业务包
通过 APT 发布；是否需要重启与是否需要 OTA 是两个独立判断。

## 一次定义范围，后续自动纳入新文件

`scripts/debian-system/config-package.json` 定义包与平台的边界，采用命名空间规则和
平台排除项，不是逐个文件的白名单。`scripts/debian-package/system_config.py` 从
`overlay-debian/` 自动生成本版本的实际文件清单，并供打包、rootfs 排除复制和镜像审计
共同使用；发布分类使用同一规则。

| 纳入业务包的范围 | 示例 / 用途 |
| --- | --- |
| `/etc/aiden/**`、`/etc/aiden_*.conf`、`/etc/default/aiden-*` | 新功能优先放入专属目录；兼容已有音频、BLE、启动、swap 配置 |
| `/etc/locale.conf` | 默认语言环境；Debian 提供 `/etc/default/locale → ../locale.conf` 兼容链接 |
| `/etc/profile.d/aiden-*.sh`、`/etc/sudoers.d/*-aiden-*` | root/aiden 终端环境、sudo 代理保留规则 |
| `/etc/ssh/sshd_config.d/*-aiden.conf` | Aiden SSH 设置 |
| `/etc/systemd/system/aiden*.{service,path,timer}`、`aiden.target` | 业务和设备辅助服务，受平台排除项约束 |
| `*.service.d/*-aiden.conf`、`*.device.d/*-aiden-optional.conf` | Aiden 服务与设备 drop-in |
| `/etc/systemd/network/{20-wlan0,30-usb0}.network`、`*-aiden-*.network` | 网络配置 |
| `/etc/dnsmasq.d/usb0.conf`、`aiden*.conf` | USB DHCP 设置 |
| `/etc/systemd/journald.conf.d/*-aiden.conf` | 日志设置 |
| `/etc/tmpfiles.d/aiden*.conf`、`/etc/udev/rules.d/*-aiden-*.rules` | 目录、设备权限规则 |
| `/usr/lib/aiden/aiden-*` | Aiden 辅助脚本，受平台排除项约束 |

例如以后新增 `overlay-debian/etc/aiden/camera.conf` 或
`overlay-debian/usr/lib/aiden/aiden-new-helper`，无需改清单或契约，自动进入 `.deb`；
新增服务应由 `aiden.target` 的依赖或已有服务依赖拉起。APT 不执行 `preset-all`，
不会擅自改变管理员的启用状态。仅有 `[Install] WantedBy=` 不会自动启用新服务。

平台排除项包括 OTA、分区扩容/槽位、userdata 迁移、机器身份、SSH 身份、用户目录、
环境准备、Wi-Fi 驱动、媒体模块等基础脚本和 unit。平台库、内核、驱动、挂载 unit、
APT 源及公钥、OTA 信任根、EDID、VQE 基础配置也继续归平台。
`/etc/bluetooth/main.conf` 已归 Debian `bluez` 包所有，不直接抢占；厂商配置应优先
使用服务支持的 Aiden drop-in。目录属于 `/etc` 不代表任意文件都能被接管。

修改边界规则本身或修改范围外的文件，仍按系统变更走 OTA；范围内的变更若实际依赖
新的库、驱动、启动前提或 ABI，也必须选择 `force_ota`。自动路径分类不能证明语义兼容。

## 安装、配置与生效

Debian 13 的 locale 规范路径是 `/etc/locale.conf`；业务包仅拥有这个普通 conffile，
保留系统创建的 `/etc/default/locale` 链接，避免 dpkg 在已有链接上遗留 `.dpkg-new`。
rootfs 在压缩、镜像打包前和最终镜像审计中均检查配置的内容、权限和归属。

文件作为真实 dpkg payload 安装，hook 不以 `cp` 方式覆盖系统文件。包内
`/usr/lib/aiden/runtime-config.json` 记录路径、权限、哈希和生效方式：

- 所有者为 root；普通配置 `0644`，可执行辅助脚本 `0755`，sudoers `0440`。
- `/etc` 文件登记为 conffiles：上游和本地同时修改时，dpkg 按交互选择处理。无人值守
  安装可明确使用 `--force-confold`，但这意味着部分新设置不会覆盖本地修改。
- 不打包 `/userdata`、`/run` 的运行状态、凭据或用户设置。
- 删除源码文件会停止在新包中携带它；dpkg 可能保留 obsolete conffile。需要彻底删除或
  改名时，应在该版本增加可回滚的 `dpkg-maintscript-helper` 迁移，并测试升降级，不能
  把“从清单消失”当作删除管理员配置。

`preinst/prerm` 保存原先运行的业务服务并停止它们，同时记录安装前的实际配置哈希和权限。
`postinst` 检查实际 sudo 配置、应用 Aiden tmpfiles、daemon-reload，然后恢复原先运行
的业务服务，最后恢复代理重启监听器。原先停止的业务服务不会主动启动。
校验或启动失败保留恢复记录；修复后执行 `sudo dpkg --configure -a` 可继续。
离线构建 rootfs 时跳过在线停启和重启标记。

规则中的 `live` 表示不要求整机重启：业务配置随业务服务恢复生效，终端 profile 和
locale 在下一次登录/新建终端时生效。其余范围内文件默认要求稍后重启，包括新加入
且尚未审核为 live 的配置。APT 不自动重启 SSH、systemd-networkd、USB 或整机。
只有实际安装后的延迟生效文件内容或权限发生变化，才写入：

```text
/run/reboot-required
/run/reboot-required.pkgs
/run/aiden-business-reboot-required.json   # 需要重启生效的具体路径
```

只升级业务代码、只改 live 配置、相同配置重装、dpkg 保留本地配置均不会凭空要求重启。
标记会保留到重启，不因后续安装覆盖。对已经运行的脚本进程，文件替换不等于立刻
切换其执行内容；默认重启策略避免在远程安装中主动打断设备连接。

## 发布与首次接管

三个通道仍全部手动发布，主入口和 `build-backup.yml` 使用相同实现。

| 对比本通道上次发布 | 产物 | 契约 |
| --- | --- | --- |
| 业务或范围内配置新增/修改/删除 | 一个 `aiden-business` 包 | 继承 |
| 平台或边界变化、`force_ota` | OTA 镜像和配套业务包 | 递增 |
| 首次采用配置归属机制 | OTA 镜像和配套业务包 | 递增一次 |
| 仅文档/测试或无变化 | 不发布 | 不变 |

首次 OTA 将这些配置从 overlay 直接复制转为业务包所有，并在平台契约中声明
`runtime_config: 1`。业务 manifest 和发布记录也记录该版本；缺少字段的旧发布按
`0` 兼容读取。preinst 拒绝把新包装到没有该声明的旧平台，历史 Release 不改写。
planner 自动分配新基础契约，后续范围内配置变化不再递增契约。

发布器和 APT 索引生成器验证包清单、文件内容/模式、conffiles、通道和契约。发布成功
后刷新 GitHub Pages 签名源；索引仍不设置 `Valid-Until`，每个通道/契约保留最新三个
正式版本。业务更新只修改当前活动 rootfs，切回旧 OTA 槽会回到该槽自己的包与配置。

```bash
sudo apt update && sudo apt upgrade
dpkg-query -W aiden-business
cat /run/aiden-business-reboot-required.json  # 无需重启时可能不存在

# 同契约内降级（示例版本必须仍在源中）
sudo apt install --allow-downgrades aiden-business=0.0.8-1
```

本地查看实际范围无需构建：

```bash
python3 scripts/debian-package/system_config.py inventory /tmp/runtime-config.json
```

Linux 验证：`bash scripts/test_system_config_package.sh` 在可丢弃容器内执行真实 dpkg
升降级、conffile 保留、失败恢复和生产打包流程；其中程序为测试替身，不作为可发布的
板端二进制。正式包仍通过 `scripts/debian-package/release.sh build` 或三通道流程构建。
