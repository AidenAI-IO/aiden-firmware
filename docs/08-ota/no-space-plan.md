# OTA 预留空间设计

## 背景

`/userdata` 是 3 GiB 的共享 ext4 分区。OTA 下载缓存与 swap、录音、日志等运行时数据共同占用该分区，曾出现：

```text
write /userdata/ota/downloads/oem.img.tar.gz.part: no space left on device
```

旧方案在每次 OTA 前清理缓存并通过 `statfs` 检查当时的剩余空间。这样能提前报错，但不能保证用户真正开始升级时仍有空间：预检通过后，日志或录音仍可能继续写入并抢占下载额度。

现在改为常驻预留空间。设备在正常运行期间用一个实际写满、占用磁盘块的文件锁住 OTA 预算；只有 OTA 持有更新锁且即将下载时才释放这部分空间。

## 默认预算

- 预留预算：64 MiB。
- 下载安全余量：4 MiB，覆盖 ext4 元数据、目录项和 `fsync` 波动。
- 预留文件：`<download_dir>/.ota-reserve`。
- eMMC 默认路径：`/userdata/ota/downloads/.ota-reserve`。

2026-07-30 的最新正式发布需要为目标槽下载：

| 资源 | 大小 |
| --- | ---: |
| `boot_b.img.tar.gz`（A/B 二选一） | 约 3.6 MiB |
| `oem.img.tar.gz` | 约 25.3 MiB |
| 合计 | 约 28.9 MiB |

64 MiB 能覆盖当前约 29 MiB 的下载量和 4 MiB 安全余量，并为包体增长留出空间。OTA 不下载发布中的 `update.img.tar.gz`。

## 空间模型

预留不是一个永远固定为 64 MiB 的单文件，而是一笔固定 OTA 预算：

```text
有效下载缓存 + .ota-reserve = reserve_size_bytes
```

例如已有 20 MiB 的可续传 `.part` 文件时，预留文件补到 44 MiB。这样既保留断点续传，又不会把未使用的 44 MiB 暴露给普通运行时数据。

预留文件通过实际写零并 `fsync` 创建，不使用会产生稀疏文件的 `truncate` 扩容。因此文件的逻辑大小对应真实占用的文件系统块。

## 生命周期

### 1. 开机补齐

`S54ota` 每次开机执行 `ota health`。健康处理和预留补齐共用 OTA 更新锁：

1. 处理待确认升级、成功提交或回滚。
2. 统计下载目录内已有缓存和断点文件。
3. 将 `.ota-reserve` 补到“配置预算减缓存大小”。

如果另一个 `ota update` 已持有锁，`ota health` 不会补建预留文件，避免把下载刚释放的空间重新占回去。

### 2. OTA 下载前

签名验证、版本策略和分区大小检查通过后：

1. 删除不属于当前目标槽的旧缓存。
2. 删除名字相同但大小或 SHA256 不匹配的旧完整包。
3. 保留当前目标资源的有效完整包和大小合法的 `.part` 文件。
4. 补齐预留预算。
5. 根据已验签 manifest 的 `asset.size` 计算剩余下载字节数。
6. 确认“剩余下载 + 4 MiB 安全余量”不超过当前预留文件。
7. 删除 `.ota-reserve`，开始网络下载。

这里不再用 `statfs` 决定能否升级。OTA 只消费自己预先建立的预算。

### 3. 下载结束或失败

- 下载成功并完成校验后，在写分区前恢复预留预算。
- 网络失败时保留 `.part`，并按 `.part` 已占大小补回剩余预留。
- 发生 `ENOSPC` 时仍删除本次 `.part` 作为兜底，避免无效续传循环，然后尝试恢复预留。
- 已验证的完整缓存可以保留；它与缩小后的预留文件合计仍为固定预算，下次相同资源可直接复用。

## 配置

`/userdata/ota/config.json` 支持：

```json
{
  "reserve_size_bytes": 67108864,
  "reserve_safety_margin_bytes": 4194304
}
```

| 字段 | 默认值 | 说明 |
| --- | ---: | --- |
| `reserve_size_bytes` | 67108864 | 下载缓存和预留文件的总预算，默认 64 MiB |
| `reserve_safety_margin_bytes` | 4194304 | 释放预留后必须额外留下的下载余量，默认 4 MiB |

如果将来的剩余下载量超过当前预留文件，OTA 会在发起资源请求前失败：

```text
OTA reserve too small: need 72.0 MiB for remaining downloads and safety margin,
reserved 64.0 MiB; increase reserve_size_bytes
```

这表示发布包已经超过设备预留策略，应调整配置或减小发布资源，而不是让设备临时清理用户数据。

## SD 卡行为

预留文件跟随 `download_dir`。当 OTA CLI 根据 StorageManager 状态把下载缓存切换到 `<sd-mount>/aiden/ota-cache` 时，预留也位于该目录；OTA 状态文件仍保留在 eMMC 的 `/userdata/ota`。

## 运维检查

```bash
# 查看预留和缓存
ls -lh /userdata/ota/downloads
du -h /userdata/ota/downloads/* /userdata/ota/downloads/.ota-reserve 2>/dev/null

# 手动补齐预留（同时处理 pending health）
/oem/usr/bin/ota health

# 查看预留相关日志
grep 'ota reserve' /var/log/ota/ota.log
```

## 已知边界

- 预留机制只能保护预留成功建立后的升级。旧固件首次升级到包含本机制的版本时，仍由旧 OTA 客户端负责下载。
- OTA 释放预留到恢复预留之间，其他进程理论上仍可并发写盘；64 MiB 预算中的 4 MiB 安全余量用于降低这类短时波动风险，`ENOSPC` 清残包仍作为最后兜底。
- 如果开机时磁盘已经过满，预留文件可能无法首次补齐。`ota health` 会返回错误并记录日志，需要先清理空间一次。

## 验证范围

- `ota health` 创建真实大小的预留文件。
- 已有 `.part` 文件时只补齐剩余预算。
- 未取得 OTA 更新锁时不创建预留文件。
- 下载请求发生时预留文件已释放。
- 下载完成后“缓存 + 预留”恢复为配置预算。
- 旧缓存会在下载前删除。
- 下载需求超过预留时，在资源请求前拒绝并写入 `state.json` 的 `reserve` 阶段。
- `ENOSPC` 删除 `.part`，其他网络错误保留 `.part`。
