# OTA 设备验收流程

启用生产 OTA rollout 前，应在代表性硬件上跑完以下验收。建议记录设备序列号、硬件版本、起始 slot、目标 release version、每一步的 `abctl read` 和 `ota status` 输出。

## 前置条件

- 生产镜像使用生产 Ed25519 public key 构建。
- GitHub Release 包含 `manifest.json`、`boot_a.img`、`boot_b.img`、`oem.img`、`rootfs.img`、`userdata.img` 和 `update.img`（自 PR #112 起使用中性资源）。
- 发布版 `update.img` 已内置 `/userdata/ota/config.json`。
- 有 UART 时建议同时记录 SPL rollback 日志。

`ota` 在启动时必须先处理 `/userdata/ota/pending_boot.json` health，再做网络或 GitHub update check。不要在 pending health 处理前加入网络等待。

## 1. USB 首刷验收

按正常 USB recovery 流程刷入发布版 `update.img`：

```bash
./upgrade_tool/upgrade_tool uf pico-sdk/output/image/update.img
```

设备启动后检查：

```bash
cat /proc/cmdline
mount | grep ' /oem '
/oem/usr/bin/abctl read /dev/block/by-name/misc
/oem/usr/bin/ota status
```

期望：

- factory boot 在 slot A。
- `/proc/cmdline` 包含 `aiden.slot_suffix=_a` 和 `root=PARTLABEL=rootfs_a`。
- `/oem` 挂载自 `/dev/block/by-name/oem_a`。
- `misc` metadata 能从 byte offset `2048` 正常解析，slot A successful。
- `/userdata/ota/config.json` 存在，`ota status` 不报 missing factory baseline。

## 2. 手动 slot 切换

切到 inactive slot：

```bash
/oem/usr/bin/abctl set-active /dev/block/by-name/misc b --tries 3
sync
reboot
```

重启后检查：

```bash
cat /proc/cmdline
mount | grep ' /oem '
/oem/usr/bin/abctl read /dev/block/by-name/misc
```

期望：

- `/proc/cmdline` 包含 `aiden.slot_suffix=_b` 和 `root=PARTLABEL=rootfs_b`。
- `/oem` 挂载自 `/dev/block/by-name/oem_b`。
- slot B 在 mark successful 前有 remaining tries。

## 3. Mark successful

确认新 slot 可用后提交：

```bash
/oem/usr/bin/abctl mark-successful /dev/block/by-name/misc b
sync
/oem/usr/bin/abctl read /dev/block/by-name/misc
```

期望：slot B successful，tries 为 0；上一 slot 仍保留为可回退 slot。

## 4. Rollback 试启动

强制试启动另一个 slot，但不要 mark successful：

```bash
/oem/usr/bin/abctl set-active /dev/block/by-name/misc a --tries 1
sync
reboot
```

如果需要阻止 health success，可在试启动期间停止 `ota` 或阻止应用 readiness。tries 消耗完后再次 reboot。

期望：SPL 回到上一 successful slot。用 UART、`cat /proc/cmdline` 和 `abctl read` 确认。

## 5. OTA happy path

确认配置：

```bash
cat /userdata/ota/config.json
```

配置应指向目标 repo/channel，并包含 `factory_partition_hashes.a` 和 `factory_partition_hashes.b` 的 `boot`、`oem`、`rootfs` hash。

执行一次 OTA：

```bash
/oem/usr/bin/ota check-now
```

期望流程：

1. 下载并验证签名 manifest。
2. 选择 inactive slot 的 assets。
3. 下载、校验并写入 inactive partitions。
4. 写入 `/userdata/ota/pending_boot.json`。
5. 切换 `misc` 并重启。
6. 新 slot 启动后写入匹配的 `/userdata/ota/health.ok`。
7. `ota` mark successful 并删除 pending files。

成功后检查：

```bash
/oem/usr/bin/ota status
/oem/usr/bin/abctl read /dev/block/by-name/misc
ls -l /userdata/ota/pending_boot.json /userdata/ota/health.ok 2>&1 || true
```

期望：

- `ota status` 显示 committed version/build time。
- active slot successful 且 tries 为 0。
- `pending_boot.json` 和 `health.ok` 已清理。

## 6. 失败场景

至少覆盖以下失败场景：

| 场景 | 期望 |
| --- | --- |
| 无效签名或无效 manifest | 拒绝更新，不写分区，不切 slot |
| SHA256 或 size 不匹配 | 拒绝更新，不写目标分区 |
| 降级 release | 拒绝更新，不切 slot |
| health marker 缺失或不匹配 | 目标 slot 不 mark successful，tries 消耗后回滚 |
| inactive boot image 损坏 | SPL 不应卡死，应回退到 previous successful slot |
| 下载中断 | 不切 slot；恢复网络后可重试或重新下载 |

断电中断写分区应通过可控电源或 HIL rig 做，不建议手工随机拔电。
