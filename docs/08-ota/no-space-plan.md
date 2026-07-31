# OTA 预留空间设计

## 背景

`/userdata` 是 3 GiB 的共享 ext4 分区。OTA 下载缓存与 swap、录音、日志等运行时数据共同占用该分区，曾出现：

```text
write /userdata/ota/downloads/oem.img.tar.gz.part: no space left on device
```

旧方案在每次 OTA 前清理缓存并通过 `statfs` 检查当时的剩余空间。这样能提前报错，但不能保证用户真正开始升级时仍有空间：预检通过后，日志或录音仍可能继续写入并抢占下载额度。

现在改为常驻预留空间。设备在正常运行期间用一个实际写满、占用磁盘块的文件锁住 OTA 预算；只有 OTA 持有更新锁且即将下载时才释放这部分空间。

## 默认预算

- 预留预算：200 MiB。
- 下载安全余量：4 MiB，覆盖 ext4 元数据、目录项和 `fsync` 波动。
- 预留文件：`<download_dir>/.ota-reserve`。
- eMMC 默认路径：`/userdata/ota/downloads/.ota-reserve`。

对最近 20 个正式发布的 manifest 按目标槽统计，最大下载组合出现在
`20260730-032204-49de84e`：

| 资源 | 大小 |
| --- | ---: |
| `boot_b.img.tar.gz`（A/B 二选一） | 3.45 MiB |
| `oem.img.tar.gz` | 25.26 MiB |
| `rootfs.img.tar.gz`（由 manifest 引用前一个 Release） | 97.90 MiB |
| 合计 | 126.61 MiB |

加上 4 MiB 安全余量后需要 130.61 MiB。200 MiB 预算还剩约 69.39 MiB
增长空间。设备只下载 manifest 为目标槽选择且哈希不匹配的资源；不会下载另一个
boot 槽位，也不会下载 Release 中约 254 MiB 的 `update.img.tar.gz`。

## 空间模型

预留不是一个永远固定为 200 MiB 的单文件，而是一笔固定 OTA 预算：

```text
有效下载缓存 + .ota-reserve = reserve_size_bytes
```

例如已有 126 MiB 的已验证缓存时，预留文件补到 74 MiB。这样既能复用缓存，又不会把未使用的 74 MiB 暴露给普通运行时数据。

预留文件通过实际写零并 `fsync` 创建，不使用会产生稀疏文件的 `truncate` 扩容。因此文件的逻辑大小对应真实占用的文件系统块。

### 技术原理

文件系统没有“只允许 OTA 使用”的原生容量配额。预留文件用最简单可靠的方式模拟
这一配额：正常运行时先把预算对应的数据块真实分配给 `.ota-reserve`，其他进程看到的
可用空间已经扣除了这笔预算，因此日志、录音和会话数据无法占用它。OTA 在持有
`update.lock` 后删除预留文件，文件系统立即归还这些数据块；下载完成或失败后，再按
“缓存 + reserve = 200 MiB”补齐。

`.tar.gz` 不会解压到 `/userdata` 临时文件。写分区时流式解压并直接写 inactive block
device，所以持久存储峰值只计算压缩包、断点文件和预留文件。

## 生命周期

### 1. 开机补齐

`S54ota` 每次开机执行 `ota health`。健康处理和预留补齐共用 OTA 更新锁：

1. 处理待确认升级、成功提交或回滚。
2. 如果 SD/eMMC 路由发生变化，清空另一侧专用 OTA 缓存目录，保证只有一份预算。
3. 统计当前下载目录内已有缓存和断点文件。
4. 将 `.ota-reserve` 补到“配置预算减缓存大小”。

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
  "reserve_size_bytes": 209715200,
  "reserve_safety_margin_bytes": 4194304
}
```

| 字段 | 默认值 | 说明 |
| --- | ---: | --- |
| `reserve_size_bytes` | 209715200 | 下载缓存和预留文件的总预算，默认 200 MiB |
| `reserve_safety_margin_bytes` | 4194304 | 释放预留后必须额外留下的下载余量，默认 4 MiB |

如果将来的剩余下载量超过当前预留文件，OTA 会在发起资源请求前失败：

```text
OTA reserve too small: need 204.0 MiB for remaining downloads and safety margin,
reserved 200.0 MiB; increase reserve_size_bytes
```

这表示发布包已经超过设备预留策略，应调整配置或减小发布资源，而不是让设备临时清理用户数据。

## SD 卡行为

预留文件跟随 `download_dir`。OTA CLI 只有在 SD 已挂载，且“文件系统实际可用空间 +
已有 OTA 缓存/reserve”至少能承载完整 200 MiB 预算时，才把下载目录切换到
`<sd-mount>/aiden/ota-cache`；否则使用 eMMC。OTA 状态文件始终保留在
`/userdata/ota`。

StorageManager 挂载 SD 时会把卡上已有 OTA 缓存/reserve 计入可恢复容量，避免重启后
因为 reserve 自己占空间而错误卸载 SD；但只有 SD 的实际剩余空间仍高于通用写入下限时，
才会继续把音频从 eMMC 迁移到 SD。

OTA 和 eMMC→SD 迁移共用 `/userdata/ota/update.lock` 作为协调信号。OTA 持锁期间不启动
迁移；已经运行的迁移会在当前文件结束后停止，避免抢占 reserve 释放出的 SD 空间。

## StorageMonitor、清理和提示

预留替代的是“下载前临时 `statfs` 预检”，不是存储治理：

- StorageMonitor 继续监控 `/userdata`，默认 Warning/Critical/Emergency 阈值仍为
  50/10/5 MiB。reserve 在 eMMC 时会被当作已使用空间，这是预期行为；reserve 在 SD
  时不会影响 `/userdata` 的监控结果。
- StorageMonitor 的日志、音频和会话归档清理继续保留。cleaner 不包含 OTA 目录，不会
  删除 `.ota-reserve`、已验证缓存或 `.part`。
- OTA 下载前继续删除失效缓存；SD/eMMC 切换时清理另一侧专用 OTA 缓存。这些清理用于
  保持单一预算和避免错误断点，不能由 reserve 替代。
- `ENOSPC` 时删除本次 `.part` 的兜底继续保留，防止每次重试都在同一位置撞满；普通网络
  错误仍保留 `.part` 续传。
- reserve 建立失败、预算过小和下载失败继续写入 `state.json.LastError` 并输出到 stderr。
  当前没有新增独立语音或手机弹窗；StorageMonitor 原有告警和降级提示继续生效。

## 发布约束

`generate_ota_manifest.sh` 默认检查 A/B 两个目标槽的最坏下载组合。200 MiB 预算扣除
4 MiB 安全余量后，manifest 资源总量上限为 196 MiB；超过时 CI 在签名和发布前失败。

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
- OTA 释放预留到恢复预留之间，非 StorageManager 管理的系统日志仍可能并发写盘；4 MiB
  安全余量、当前约 69 MiB 的包体增长空间和 `ENOSPC` 清残包共同作为兜底。
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
- SD 容量不足时回退 eMMC，已有 OTA 缓存/reserve 会计入 SD 容量。
- SD/eMMC 切换时另一侧 OTA 缓存被清空，不会留下双份预算。
- OTA 更新锁持有期间不启动 eMMC→SD 音频迁移。
- 发布 manifest 任一目标槽超过 196 MiB 时生成失败。
