---
sidebar_position: 4
---

# Debian business package

Debian 构建链使用 `aiden-business` 作为业务基线包。应用构建阶段先生成并审计 armhf
业务程序，`scripts/debian-package/build.sh` 在 Linux 构建机的 Debian 容器中调用
`dpkg-deb`，输出 `output/debian-system/aiden-business.deb`。系统构建阶段在 debootstrap
生成的 rootfs 中执行 `dpkg -i`，随后再制作 `rootfs.ext4` 和 A/B 镜像。

包内容位于标准 Debian 路径：

```text
/usr/lib/aiden/                 # Agent、frame/audio/BLE、OTA CLI 和 VAD helper
/usr/lib/aiden/models/          # RV1106 VAD 模型
/usr/share/aiden/config-web/    # Config Web 静态资源
/usr/share/aiden/skills/        # bundled skills
/usr/share/aiden/audio/         # 业务音频资源
```

驱动、内核模块、Rockchip 运行库、EDID、启动集成脚本和 OTA 信任根由 base rootfs
提供。取消 OEM 分区和 `/oem` 目录，不提供兼容路径；旧版本设备必须完整强刷。`/userdata/agent`、`/userdata/system` 和用户 Skill 不在包内。

包内的 `release-manifest.json` 记录业务版本、架构、平台契约范围、配置 schema 和
`business_epoch`。发布或升级前，更新器必须验证这些字段、包签名、磁盘空间和当前
platform contract。目前这些检查是发布契约要求，独立业务升级编排器尚未实现。
本次路径和分区 ABI 变更将包要求提升至 `[3.0.0, 4.0.0)`。旧布局不能通过
slot OTA 跨越此变更，必须重新强刷；新布局内后续平台更新使用完整 boot/rootfs OTA。

构建命令（在 Linux 主机执行）：

```bash
scripts/debian-apps/build-apps.sh all
scripts/debian-system/build.sh builder
AIDEN_BUSINESS_VERSION=5.2.1 \
  DEBIAN_PACKAGE_OUTPUT_DIR=output/debian-system \
  scripts/debian-package/build.sh
scripts/debian-system/build.sh bsp
OTA_PUBLIC_KEY_PATH=keys/ota_pubkey.pem scripts/debian-system/build.sh rootfs
```

完整镜像命令会自动生成包并安装它（`AIDEN_BUSINESS_VERSION` 设置 Debian 包版本）：

```bash
AIDEN_BUSINESS_VERSION=5.2.1 ./debian_build.sh
```

macOS 开发机不直接执行 Debian/armhf 构建。将仓库同步到 Linux 构建机
（例如 `ssh luhaodev`）后执行上述命令，再把
`output/debian-system` 产物同步回开发机。

新平台路径与包所有权：

| 内容 | 路径 | 所有者 |
| --- | --- | --- |
| 业务程序和自检 CLI | `/usr/lib/aiden/` | aiden-business |
| 业务模型、通知音频 | `/usr/lib/aiden/models/`、`/usr/share/aiden/audio/voice-notifications/` | aiden-business |
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
