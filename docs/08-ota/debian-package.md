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

驱动、内核模块、Rockchip 运行库、EDID、启动集成脚本和 OTA 信任根仍由 OEM/BSP
基座提供。`/userdata/agent`、`/userdata/system` 和用户 Skill 不在包内。

包内的 `release-manifest.json` 记录业务版本、架构、平台契约范围、配置 schema 和
`business_epoch`。发布或升级前，更新器必须验证这些字段、包签名、磁盘空间和当前
platform contract；契约主版本不匹配时必须转为完整 A/B slot OTA。

构建命令（在 Linux 主机执行）：

```bash
scripts/debian-apps/build-apps.sh all
scripts/debian-system/build.sh builder
AIDEN_BUSINESS_VERSION=5.2.1 \
  DEBIAN_PACKAGE_OUTPUT_DIR=output/debian-system \
  scripts/debian-package/build.sh
scripts/debian-system/build.sh rootfs
```

完整镜像命令会自动生成包并安装它（`AIDEN_BUSINESS_VERSION` 设置 Debian 包版本）：

```bash
AIDEN_BUSINESS_VERSION=5.2.1 ./debian_build.sh
```

macOS 开发机不直接执行 Debian/armhf 构建。将仓库同步到 Linux 构建机
（例如 `ssh luhaodev`）后执行上述命令，再把
`output/debian-system` 产物同步回开发机。
