---
sidebar_position: 4
---

# Debian business package

Debian 构建链使用 `aiden-business` 作为业务基线包。应用构建阶段先生成并审计 armhf
业务程序，`scripts/debian-package/build.sh` 在 Linux 构建机的 Debian 容器中调用
`dpkg-deb`。独立打包输出 `output/debian-package/`，完整镜像构建输出
`output/debian-system/aiden-business.deb`。系统构建阶段在 debootstrap
生成的 rootfs 中执行 `dpkg -i`，随后再制作 `rootfs.ext4` 和 A/B 镜像。

包内容位于标准 Debian 路径：

```text
/usr/lib/aiden/                 # Agent、frame/audio/BLE、OTA CLI、VAD helper 和运行辅助脚本
/usr/lib/aiden/models/          # RV1106 VAD 模型
/usr/share/aiden/config-web/    # Config Web 静态资源
/usr/share/aiden/skills/        # bundled skills
/usr/share/aiden/audio/         # 业务音频资源
/etc/aiden/ 等                  # 规则范围内的运行配置，登记为 conffiles
```

驱动、内核模块、Rockchip 运行库、EDID、平台排除项中的基础集成脚本和 OTA 信任根由 base rootfs
提供。取消 OEM 分区和 `/oem` 目录，不提供兼容路径；旧版本设备必须完整强刷。`/userdata/agent`、`/userdata/system` 和用户 Skill 不在包内。

运行配置直接纳入同一个业务包，覆盖范围、生效时机和首次接管见
[业务包管理运行配置](system-config-package.md)。

包内的 `release-manifest.json` 记录业务版本、架构、平台契约范围、配置 schema 和
`business_epoch`。三通道发布包的 `preinst` 会核对当前平台契约、通道和底座标识。
包签名、磁盘空间等完整升级策略仍需独立业务升级编排器；当前通过 apt 安装下载的包。
新布局从契约 `1` 开始，包要求为 `[1, 2)`。
基座通过 `/usr/lib/aiden/platform/contract.json` 声明契约，该文件不属于业务包。
旧布局不能通过
slot OTA 跨越此变更，必须重新强刷；新布局内后续平台更新使用完整 boot/rootfs OTA。

构建命令（在 Linux 主机执行）：

```bash
scripts/debian-apps/build-apps.sh all
scripts/debian-system/build.sh builder
DEBIAN_PACKAGE_OUTPUT_DIR=output/debian-system \
  scripts/debian-package/build.sh
scripts/debian-system/build.sh bsp
OTA_PUBLIC_KEY_PATH=keys/ota_pubkey.pem scripts/debian-system/build.sh rootfs
```

完整镜像命令会自动生成包并安装它：

```bash
./debian_build.sh
```

macOS 开发机不直接执行 Debian/armhf 构建。将仓库同步到 Linux 构建机
（例如 `ssh luhaodev`）后执行上述命令，再把
`output/debian-system` 产物同步回开发机。

新平台路径与包所有权：

| 内容 | 路径 | 所有者 |
| --- | --- | --- |
| 业务程序和自检 CLI | `/usr/lib/aiden/` | aiden-business |
| 业务模型、通知音频 | `/usr/lib/aiden/models/`、`/usr/share/aiden/audio/voice-notifications/` | aiden-business |
| 运行配置和 Aiden 服务辅助脚本 | `config-package.json` 定义的命名范围（排除平台项） | aiden-business |
| RGA、VQE 动态库 | `/usr/lib/aiden/platform/lib/` | base rootfs |
| 模块、Wi-Fi/MCU 固件 | `/usr/lib/aiden/platform/modules/` | base rootfs |
| EDID、VQE 配置 | `/usr/share/aiden/edid/`、`/usr/share/aiden/audio/config_aivqe.json` | base rootfs |
| OTA Ed25519 公钥 | `/usr/share/keyrings/aiden-ota.pem` | base rootfs |

rootfs A/B 各 1792 MiB，分区节点为 p7/p8；userdata 为 p9，ota 为 p10。
原 OEM 两槽合计 512 MiB 平分给 rootfs，userdata 3 GiB 和 OTA 300 MiB 保持原容量。
新 manifest 使用 schema 2，必须完整包含 boot 和 rootfs。发布附件为
`boot_a.img.tar.gz`、`boot_b.img.tar.gz`、`rootfs.img.tar.gz`、`update.img.tar.gz`、
`manifest.json`，另可单独发布 `.deb`。

更换平台文件或信任公钥后必须重新生成 rootfs；镜像组装会比对公钥输入与 rootfs
构建记录，拒绝混用旧 rootfs。构建顺序为 apps → BSP → package/rootfs → images →
manifest/config → audit。A/B 写入、校验、个性化、健康确认与失败回滚仍按槽处理。

rootfs 同时记录 boot/env 输入校验和；重新构建 BSP 后，旧 rootfs 不可直接用于组装。
rootfs 阶段每次重新打包当前 apps，避免复用带旧路径的缓存业务包。

## 版本管理

`scripts/debian-package/version.sh` 是业务版本默认值的唯一来源：业务版本 `0.0.1`、
Debian revision `2`，完整本地包版本为 `0.0.1-2`。命令行可用 `AIDEN_BUSINESS_VERSION` 和
`AIDEN_BUSINESS_REVISION` 覆盖。正式三通道发布由发布计划自动分配版本和基础契约，
详见 [三通道发布](channel-release.md)。

未使用发布计划时，平台契约默认为 `1`。正式发布每次系统 OTA 都分配新契约，
后续业务包继承本通道的最新底座。OTA 清单仍使用 schema 2。

## 独立构建和 GitHub Release

Linux amd64 构建机需要 Docker、Git、curl、Python 3.11+ 和 dpkg-deb。
工作区必须干净，SDK 提交必须与应用构建记录一致。

```bash
scripts/debian-package/release.sh build
```

该命令准备固定版本 Go 工具链，构建 OpenCV 和业务程序，执行应用 ELF 审计，生成 `.deb`。
使用独立的精简打包容器；不会编译 BSP、构建 rootfs 或要求 OTA 私钥和 Agent 凭据。
Go/OpenCV 缓存环境变量与 `debian_build.sh` 相同。
若当前提交的应用已经构建并审计，可用 `release.sh stage` 只重新打包；来自其他提交或
有未提交修改的产物会被拒绝。

产物目录 `output/debian-package/release/` 包含：

```text
aiden-business_0.0.1-2_armhf.deb
release-manifest.json
build-metadata.json
RELEASE-NOTES.md
SHA256SUMS
```

**Debian Business Package (artifacts only)** 工作流只构建产物。
正式发布使用 **Aiden Channel Release**，全部手动触发，由计划判断该发业务包还是
完整 OTA。旧 `scripts/debian-package/release.sh publish` 入口已关闭。
发布操作、自动变更对比、失败重试和所需仓库设置见 [三通道发布](channel-release.md)。

正式发布同时更新 [GitHub APT 软件源](apt-repository.md)，设备通过 `Signed-By`
验证签名索引及包校验和，并使用 `apt update && apt upgrade` 升级业务。
手动下载的 `SHA256SUMS` 只用于传输完整性，不等同包签名。

## 在当前设备安装

仅在新布局且契约兼容的设备上安装。先核对 `contract.json` 和包内 manifest，
保留上一版本 `.deb` 和用户配置备份。

```bash
cd /path/to/downloaded-release
sha256sum -c SHA256SUMS
cat /usr/lib/aiden/platform/contract.json
sudo apt install ./aiden-business_0.0.1-2_armhf.deb
sudo /usr/lib/aiden/ota --config /userdata/debian/ota/config.json self-check
dpkg-query -W aiden-business
```

从 revision 2 开始，`preinst/prerm/postinst/postrm` 维护脚本随 apt/dpkg 自动停启业务。
升级和重装时记录正在运行的服务，先停止 proxy 重启监听器及其任务，再停止 Agent、
Config Web、Wi-Fi proxy、frame、audio、BLE 和 ttyd。解包和配置成功后执行 daemon-reload，
恢复原先运行的服务，最后恢复监听器。启用状态不变；原先停止的服务不会主动启动，
原先手动启动但未 enable 的服务也会恢复。systemd 的正常依赖关系仍然适用。

首次从旧包升级也会由新包 preinst 接管停服。同版本演练用：

```bash
sudo apt install --reinstall ./aiden-business_0.0.1-2_armhf.deb
```

制作 rootfs 时的 chroot、`SYSTEMD_OFFLINE=1`、非空 `DPKG_ROOT` 和无 systemd 环境
跳过服务操作，并遵守 `policy-rc.d`。首次安装时没有运行的业务不会自动启动，镜像首次
开机由镜像中安装的 systemd units 启动。移除包会停服；conffile 按 dpkg 规则保留，
辅助脚本被移除，用户数据不删除。

服务快照保存在 `/var/lib/aiden-business/service-transition/`。dpkg 的 abort 回调会尝试
恢复先前服务；恢复失败时保留快照且维护脚本返回失败，可修复原因后执行
`sudo dpkg --configure -a` 重试。不要在修复前删除快照。
维护脚本不自动回退包文件、迁移配置或运行硬件健康自检；安装成功后仍应运行上面的
self-check，失败时根据备份恢复旧包。自检将 Agent HTTP 不可用记录为告警，但要求
Config Web 可访问，以便 Agent 配置损坏时仍能通过恢复门户修复。托管包由 preinst
自动核对契约。

从此前测试包 `5.2.1-2` 重新编号到 `0.0.1-2` 属于一次明确降级；自动安装时额外使用
`apt install -y --allow-downgrades ./aiden-business_0.0.1-2_armhf.deb`。
业务包只修改当前活动 rootfs，B 槽不会同步升级，也不更新固件 OTA 的出厂版本记录。
`/etc/locale.conf` 的 `LANG=C.UTF-8` 由业务包管理，Debian 的 `/etc/default/locale`
链接指向它，下一次登录生效；平台契约
文件仍属于基座。首次配置接管必须建立新 OTA 基线，不能仅手动修改契约文件。
