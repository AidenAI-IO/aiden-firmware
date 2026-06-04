# OTA 架构与运行时

OTA 由三层配合完成：`pico-sdk` 生成 A/B 镜像和 factory `misc.img`，GitHub Actions 发布签名 Release，设备端 `ota` daemon 在运行时完成下载、写入、切换和健康提交。

## 分区布局

生产镜像使用 A/B 布局：

```text
32K(env),512K@32K(idblock),256K(uboot),4M(misc),32M(boot_a),32M(boot_b),256M(oem_a),256M(oem_b),1536M(rootfs_a),1536M(rootfs_b),3G(userdata)
```

| 分区 | A/B | OTA 行为 |
| --- | --- | --- |
| `env` | 否 | 不 OTA 更新；只走工厂或 USB recovery |
| `idblock` | 否 | 不 OTA 更新；避免 brick 风险 |
| `uboot` | 否 | 不 OTA 更新；旧 bootloader 需完整刷机更新 |
| `misc` | 否 | 保存 A/B metadata，OTA 只修改 slot 状态 |
| `boot` | 是 | 写入 inactive `boot_a` 或 `boot_b` |
| `oem` | 是 | 写入 inactive `oem_a` 或 `oem_b` |
| `rootfs` | 是 | 写入 inactive `rootfs_a` 或 `rootfs_b` |
| `userdata` | 否 | 跨升级保留，保存配置和运行时状态 |

## 启动流程

1. ROM 加载 SPL。
2. SPL 从 `misc` 分区 byte offset `2048` 读取 Android AVB A/B metadata。
3. SPL 选择 priority 最高且 bootable 的 slot，并加载 `boot_a` 或 `boot_b`。
4. slot-specific FIT boot image 提供 `root=PARTLABEL=rootfs_a|rootfs_b` 和 `aiden.slot_suffix=_a|_b`。
5. Linux 挂载匹配的 `rootfs_*`。
6. `S20oemslot` 根据 `aiden.slot_suffix` 挂载 `/dev/block/by-name/oem_a|oem_b` 到 `/oem`。
7. `S54ota` 启动 OTA daemon，先处理 pending health，再进行网络检查。

## 更新流程

1. `ota` 读取 `/userdata/ota/config.json` 和 `/oem/etc/ota_pubkey.pem`。
2. 获取 manifest：配置了 `manifest_url` 时直接拉取该 URL；否则查询 GitHub Release `releases/latest` 端点（即 `DefaultReleaseURL`，可由 config 的 release URL 覆盖），从 release assets 里取 `manifest.json`。
3. 下载 `manifest.json`，删除 `signature.value` 后做 canonical JSON Ed25519 验签。
4. 拒绝旧 `build_time` 或同 build time 不同 version 的 downgrade。
5. 选择 inactive slot，解析 manifest 中对应 slot 的 asset。
6. 下载镜像到 `/userdata/ota/downloads`，校验 size 和 SHA256。
7. 写入 inactive `boot_*`、`oem_*`、`rootfs_*`，并 fsync。
8. 删除旧 `health.ok`，写入 `/userdata/ota/pending_boot.json`。
9. 修改 `misc`，把目标 slot 设为 active trial slot，默认 tries 为 3。
10. 重启进入目标 slot。

## 健康确认与回滚

新 slot 启动后，Go daemon 在 runtime init 完成后调用 OTA health 写入逻辑。只有同时满足以下条件才写 `/userdata/ota/health.ok`：

- `pending_boot.json` 存在。
- 当前 `aiden.slot_suffix` 等于 pending target slot。
- 当前 rootfs slot 等于 pending target slot。
- health marker 写入时包含 pending 中的 version、build time、nonce 和当前 boot ID。

`ota` 看到匹配 marker 后：

1. 调用 `abctl`/slot logic mark successful。
2. 更新 `/userdata/ota/state.json` 的 committed version/build time 和 per-slot partition hashes。
3. 删除 `pending_boot.json` 和 `health.ok`。

如果健康窗口超时，`ota` 会主动 reboot，让 SPL 消耗 tries。tries 用尽且目标 slot 未 successful 时，SPL 回退到上一成功 slot。daemon 在旧 slot 观察到已经回滚后，会清理 pending 状态并把 state phase 标记为 `rolled-back`。

## Manifest 约定

Manifest 中 `parts[].name` 只能是 `boot`、`oem`、`rootfs`。每个 part 使用以下 asset 形式之一：

- `asset`：slot-neutral `{name,size,sha256}`，只适用于两边字节完全一致的镜像。
- `asset_a` 和 `asset_b`：slot-specific `{name,size,sha256}`。

`boot` 必须使用 `asset_a` 和 `asset_b`，因为 boot image 内包含 slot-specific DTB bootargs。`oem` 和 `rootfs` 可以使用 slot-specific assets，也可以在确认为 byte-identical 时使用 slot-neutral asset。

## Factory baseline

发布版 `update.img` 必须内置 `/userdata/ota/config.json`。CI 在生成签名 manifest 后调用 `scripts/generate_ota_device_config.sh` 生成该文件，再通过 `scripts/repack_ota_update_image.sh` 只重建 `userdata.img` 和 `update.img`。

`config.json` 至少包含：

- `factory_version` - 首刷版本号，用于防止降级和选择性更新验证
- `factory_build_time` - 首刷构建时间
- `factory_partition_hashes.a.boot|oem|rootfs` - slot A 各分区的 SHA256
- `factory_partition_hashes.b.boot|oem|rootfs` - slot B 各分区的 SHA256

可选配置字段：

- `manifest_url` - 直接指定 manifest URL（跳过 GitHub Release API）
- `public_key_path` - 覆盖默认公钥路径（默认 `/oem/etc/ota_pubkey.pem`）
- `interval_seconds` - OTA 检查间隔（默认 3600 秒）
- `github_token_path` - GitHub token 文件路径（私有仓库需要）

Factory baseline 必须 slot-aware，因为 `boot_a.img` 和 `boot_b.img` hash 不同。缺失 baseline 时 OTA 初始化必须失败，不应猜测当前分区版本。

注：`generate_ota_device_config.sh` 生成的 config.json 还包含 `repo` 和 `channel` 字段，但这些字段仅用于人类可读性，不被 OTA 代码读取。实际的 channel 验证来自 manifest 本身。

## 私有仓库 token

Public GitHub Release 不需要 device token。私有仓库可以把只读 token 放在：

```text
/userdata/ota/gh_token
```

存在 token 时，`ota` 会对 GitHub Release metadata、manifest 和 image 下载请求加 bearer token。OTA signing key 与 GitHub token 是两套独立凭据。
