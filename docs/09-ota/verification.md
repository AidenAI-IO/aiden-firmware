# A/B 与 `abctl` 验证

`abctl` 是 OTA A/B metadata 的诊断和工厂测试工具，可操作普通文件，也可操作设备上的 `/dev/block/by-name/misc`。

## Metadata 约定

- A/B metadata 位于 `misc` 分区 byte offset `2048`。
- 数据结构为 32 bytes，兼容 Android AVB A/B layout version `1.0`。
- CRC32 使用 big-endian IEEE，覆盖 metadata bytes `0..27`。
- Reserved bytes 保持 reserved。应用 OTA 状态只保存在 `/userdata/ota`，不写入 `misc` reserved bytes。
- Factory `misc.img` 初始状态：slot A priority 15、successful，slot B disabled。

## Host image 检查

使用普通文件做安全测试：

```bash
build/bin/abctl init /tmp/misc.img --size 4M
build/bin/abctl read /tmp/misc.img
```

期望输出包含 slot A successful、slot B priority 0、`last_boot=A`。

手动状态切换：

```bash
build/bin/abctl set-active /tmp/misc.img b --tries 3
build/bin/abctl read /tmp/misc.img
build/bin/abctl mark-successful /tmp/misc.img b
build/bin/abctl read /tmp/misc.img
```

显式写测试状态：

```bash
build/bin/abctl write /tmp/misc.img \
  --a-priority 14 --a-tries 0 --a-successful 1 \
  --b-priority 15 --b-tries 3 --b-successful 0
build/bin/abctl read /tmp/misc.img
```

`abctl write` 会拒绝非法状态，例如 successful slot 带非零 tries。

## 设备检查

设备上读取真实 `misc`：

```bash
/oem/usr/bin/abctl read /dev/block/by-name/misc
```

手动切换 slot：

```bash
/oem/usr/bin/abctl set-active /dev/block/by-name/misc b --tries 3
sync
reboot
```

启动后确认 active slot：

```bash
cat /proc/cmdline
mount | grep ' /oem '
/oem/usr/bin/abctl read /dev/block/by-name/misc
```

确认设备健康后提交 slot：

```bash
/oem/usr/bin/abctl mark-successful /dev/block/by-name/misc b
sync
```

## Rollback 测试

设置一次 trial boot，并且不要 mark successful：

```bash
/oem/usr/bin/abctl set-active /dev/block/by-name/misc b --tries 1
sync
reboot
```

不要提交 B。继续重启直到 SPL 消耗 tries 并回到之前的 successful slot。用 `cat /proc/cmdline`、`mount | grep ' /oem '` 和 `abctl read` 确认。

## 期望诊断信号

- `/proc/cmdline` 包含 `aiden.slot_suffix=_a` 或 `_b`。
- `/proc/cmdline` 的 `root=PARTLABEL=rootfs_a|rootfs_b` 与 slot suffix 匹配。
- `/oem` 挂载自 `/dev/block/by-name/oem_a` 或 `oem_b`，与 slot suffix 匹配。
- `abctl read` 能解析 AVB A/B metadata，不报 CRC 或 layout 错误。
- `ota status` 能展示 OTA state、active slot、pending boot 和原始 A/B data。
