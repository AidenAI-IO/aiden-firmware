# OTA 概览

本项目的生产 OTA 使用 A/B 分区、签名 manifest 和启动健康确认机制。设备运行中只写入 inactive slot，重启后由 Rockchip SPL 选择新 slot；如果新系统没有在健康窗口内确认成功，SPL 会自动回退到上一成功 slot。

## 适用范围

- 目标硬件：Luckfox Pico Zero / RV1106 + eMMC。
- Distribution: GitHub Releases. Published assets contain `manifest.json` plus compressed image archives: `boot_a.img.tar.gz`, `boot_b.img.tar.gz`, `oem.img.tar.gz`, `rootfs.img.tar.gz`, and `update.img.tar.gz`. The extracted images still use the slot-neutral `oem.img` and `rootfs.img` layout introduced in PR #112; older releases used `oem_a.img`, `oem_b.img`, `rootfs_a.img`, and `rootfs_b.img`.
- 更新方式：设备端 `/oem/usr/bin/ota` 拉取 manifest、验签、校验 SHA256、写 inactive slot、切换 `misc` 并重启。
- 回滚方式：Rockchip SPL A/B metadata 控制 boot tries；应用健康确认后才 mark successful。

## 文档索引

### 核心文档

- [OTA 架构与运行时](architecture.md)
- [OTA 密钥管理](key-management.md)
- [设备验收流程](device-acceptance.md)
- [A/B 与 `abctl` 验证](verification.md)

### 开放性与外部开发者

- [OTA 开放性改进](OTA_OPEN_SOURCES.md) - manifest 支持直接 URL、外部开发者分发固件
- [外部开发者指南](ota-external-developers.md) - 如何使用自定义源分发固件
- [快速示例](ota-quick-examples.md) - GitHub Releases、自建后端、混合模式示例
- [发布渠道策略](ota-release-channels.md) - 分支与渠道隔离机制

### 技术分析

- [中性资源兼容性分析](OTA_COMPATIBILITY_ANALYSIS.md) - PR #112 向后兼容性评估

## 核心约束

- OTA 不更新 `env`、`idblock`、`uboot`；这些只通过工厂或 USB recovery 更新。
- OTA 只写 inactive slot 的 `boot_*`、`oem_*`、`rootfs_*`。
- `/userdata` 跨升级保留，并保存 OTA 配置、状态、下载缓存和健康标记。
- `boot_a.img` 与 `boot_b.img` 包含不同 slot bootargs，manifest 必须使用 slot-specific boot assets。
- 缺少 factory baseline 或 manifest 签名/hash 校验失败时，设备必须 fail closed。

## 常用命令

```bash
# 查看 OTA 状态
/oem/usr/bin/ota status

# 立即检查并执行一次 OTA
/oem/usr/bin/ota update

# 查看 A/B metadata
/oem/usr/bin/abctl read /dev/block/by-name/misc

# 查看当前 slot 与 rootfs
cat /proc/cmdline
mount | grep ' /oem '
```

`check-now` 仍作为兼容别名保留；新脚本和文档应使用 `update`。

## 相关源码

| 路径 | 说明 |
| --- | --- |
| `src/agent/cmd/ota` | OTA CLI 入口，包含手动更新和 health 处理 |
| `src/agent/cmd/abctl` | A/B metadata 诊断工具 |
| `src/agent/internal/ota` | manifest、下载、状态机、slot、health 等 OTA 核心逻辑 |
| `overlay/etc/init.d/S20oemslot` | 根据 `aiden.slot_suffix` 挂载 `/oem` |
| `overlay/etc/init.d/S54ota` | 开机一次性 OTA health 处理 |
| `scripts/generate_ota_manifest.sh` | 生成签名 OTA manifest |
| `scripts/generate_ota_device_config.sh` | 从 manifest 生成首刷配置 |
| `scripts/repack_ota_update_image.sh` | 把首刷 OTA 配置重新打入 `userdata.img` 和 `update.img` |
| `pico-sdk/project/scripts/mk-ab-misc.py` | 生成 factory `misc.img` A/B metadata |
