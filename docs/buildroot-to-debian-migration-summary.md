# Luckfox Pico Zero 从 Buildroot 迁移到 Debian 13 总结

本文总结 `Falcom/debian` 分支将 Aiden Luckfox Pico Zero 固件从 Buildroot/uClibc
迁移到 Debian 13 armhf/glibc 的实施过程、关键改动、问题处理、验证结果和遗留事项。

本文以当前提交 `fe773f61` 以及 2026-08-21 完成的 USB HID/ECM 回归修复为准。
该 USB 修复当前仍在工作区，需重新构建并刷写后才会永久进入固件。早期 Stage 2、Stage 3 验收文档记录的是
2026-08-17 的中间状态；此后又完成了 RKNN mini runtime、无 HDMI 重试、`sudo`、
SSH 身份初始化和本地完整固件产物等修复。因此，本文的“当前状态”优先于旧验收文档
中的历史阻塞结论。

## 1. 迁移目标与最终结论

此次迁移不是替换整套 Rockchip BSP，而是在保留厂商启动链和硬件支持的基础上，
替换用户空间并重建应用、服务和 OTA 链路：

- 保留 Rockchip/Luckfox 的 U-Boot、Linux 5.10.160、DTB、内核模块、固件和镜像封装工具。
- 将 Buildroot/uClibc rootfs 替换为 Debian 13 armhf/glibc/systemd rootfs。
- 将原有 C/C++ 和 Go 应用重新构建为 Debian armhf 可执行文件。
- 将 Buildroot SysV init 脚本迁移为 Debian systemd 服务。
- 保留并增强 A/B 分区、OTA、设备身份、USB 恢复和硬件服务能力。
- 建立可重复构建、ELF/ABI 审计、文件系统审计、BSP 审计和最终镜像审计。
- 增加本地一键构建入口，能够生成可直接刷写的 `update.img` 和本地 OTA 产物，
  不依赖 GitHub Release。

当前已经可以在 Linux x86_64 主机上，从应用到 rootfs、BSP、A/B 镜像和
`update.img` 完整构建 Debian 固件。最终镜像审计通过，历史日志中也有多次
`Upgrade firmware ok` 的刷写记录。

迁移尚不能等同于生产发布批准。HDMI 摄像头、RKNN 新 runtime 的完整板端推理、
72 小时稳定性、OTA 断电矩阵和生产密钥治理仍需补充验证。

## 2. 迁移前后架构对照

| 项目 | Buildroot 固件 | Debian 固件 |
|---|---|---|
| 用户空间 | Buildroot | Debian 13 trixie armhf |
| C 运行库 | uClibc | glibc |
| init 系统 | BusyBox/SysV `rcS` 和 `S*` 脚本 | systemd |
| 软件包管理 | 固件构建期集成 | APT，使用固定 Debian snapshot 构建 |
| 网络 | `ifconfig`、dhcpcd、直接管理进程 | `ip`、wpa_supplicant、systemd-networkd |
| DHCP 客户端 | dhcpcd | 仅 systemd-networkd |
| SSH | Buildroot sshd 启动脚本 | Debian OpenSSH 和持久化 host key |
| 日志 | 文件和脚本管理 | journald 与服务独立日志并存 |
| 应用构建 | SDK uClibc 工具链 | Debian armhf GCC 14 工具链 |
| RKNN | uClibc mini runtime | 静态 mini runtime 2.3.2 加 glibc 兼容层 |
| 服务环境 | shell 直接读取环境文件 | 白名单解析后生成 `/run/aiden/system.env` |
| OTA | Buildroot 路径和服务语义 | Debian A/B、流式写入、个性化和健康标记 |
| 固件构建 | SDK/CI 分散步骤 | `debian_build.sh` 本地完整流水线 |

## 3. 分阶段实施过程

### 3.1 Stage 1：建立可启动的 Debian 基础系统

第一阶段先验证“不替换 BSP，只替换 rootfs”是否可行，完成了以下工作：

- 新增 Debian 13 armhf rootfs 构建脚本、包清单、overlay 和镜像组装脚本。
- 继续使用 Luckfox/Rockchip 的内核、DTB、模块、固件、U-Boot 和刷写格式。
- 提供 APT、OpenSSH、串口登录、systemd-networkd、wpa_supplicant、BlueZ 和 zram。
- 挂载 `/oem`、`/userdata`，并实现首次启动时的文件系统扩容。
- 建立 e2fsprogs、厂商共享库、ELF 架构和镜像内容审计。
- 新增 Stage 1 刷写和板端验收脚本，以 UART、SSH 和系统报告验证启动结果。

Stage 1 实机验收完成了首次启动、热重启和两次冷启动，结果为 23 项通过、
0 项失败、1 项可选跳过。Wi-Fi、DNS、APT、SSH、蓝牙和基础存储功能通过。

这一阶段证明 RV1106 的厂商 BSP 可以继续承载 Debian 13 armhf 用户空间，
也确认了 ext4 镜像属性、armhf hard-float ABI 和所需内核能力。

### 3.2 Stage 2：迁移应用与厂商用户态依赖

第二阶段将 Aiden 应用从 Buildroot/uClibc 构建路径迁移到 Debian/glibc：

- 新增 `cmake/toolchains/armhf-debian.cmake`。
- 新增 `cmake/platforms/rv1106-debian-glibc.cmake`。
- 将原平台参数整理到 `rv1106-buildroot-uclibc.cmake`，保留 Buildroot 构建路径。
- C/C++ 应用使用 Debian armhf GCC 14 交叉编译。
- 固定使用 Go 1.26.0 构建 ARMv7 静态程序：
  - `agent`
  - `ble_service`
  - `ota`
  - `abctl`
- 静态构建 OpenCV-Mobile，避免在目标系统携带动态 OpenCV 依赖。
- 对 Rockit、MPP、RGA、RKAIQ、RKRawStream 和 RKNN 逐项分析 ABI 与依赖。
- 新增 Stage 2 应用构建、ELF 审计、板端 G0 部署和硬件测试脚本。
- 增加音频、摄像头、NPU、CMA、DMA-BUF、模块和设备节点的采集能力。

当前 `output/debian-stage2/apps-audit/summary.txt` 记录：

```text
status=pass
elf_count=22
```

旧的 Stage 2/3 验收记录中曾出现 23 个 ELF。当前数量减少为 22，是因为
`librknnrt.so` 动态 full runtime 已被移除，`rknn_vad` 改为静态嵌入
`librknnmrt.a` mini runtime，并不是应用缺失。

### 3.3 Stage 3：systemd、A/B、OTA 和最终镜像

第三阶段将开发验证 rootfs 收敛为完整的 Debian A/B 固件：

- 建立生产形态的 Debian rootfs 包清单和 overlay。
- 将 31 个 Buildroot `S*`/`rcS` 启动项逐项分类为：
  - 迁移到 systemd；
  - 使用 Debian 原生服务替代；
  - 退休；
  - 无需重复执行。
- 新增 `aiden.target`，统一编排 Aiden 设备服务。
- 继续使用 A/B boot、OEM、rootfs 分区，并保留 userdata 和 OTA 数据分区。
- Slot A 作为工厂成功槽；Slot B 初始不可启动，等待 OTA 激活。
- 新增 A/B slot 解析、健康标记、失败恢复、inactive rootfs 个性化和状态管理。
- 将 `machine-id`、SSH host key 和持久配置放入 userdata，跨槽保持一致。
- 兼容旧 Buildroot userdata 布局并进行一次性迁移。
- 生成 rootfs、OEM、userdata、OTA、boot A/B 和最终 `update.img`。
- 对解包后的最终 `update.img` 再执行分区、文件系统、ELF、身份和配置审计。

当前 `output/debian-stage3-local/audit-report.txt` 的最终结果为：

```text
Audit passed
```

### 3.4 本地一键构建与本地 OTA 产物

新增根目录脚本 `debian_build.sh`，将原本分散的 Stage 2、Stage 3 和签名步骤
串成一个本地构建流程：

1. 检查 Docker、Git、OpenSSL、Python 等主机工具。
2. 检测在线 CPU 数量，`RK_JOBS` 默认 24，超过 CPU 数量时自动封顶。
3. 获取并校验固定的 Go 1.26.0 linux-amd64 工具链。
4. 校验 OTA PEM 公私钥是否匹配。
5. 校验 `pico-sdk` 子模块是否为指定提交且工作区干净。
6. 清理 Debian 构建输出目录。
7. 构建并审计 Stage 2 应用。
8. 构建 Stage 3 rootfs 和 BSP。
9. 组装 A/B 工厂镜像。
10. 生成本地签名 manifest 和与镜像匹配的设备 OTA 配置。
11. 重新打包 `update.img` 并执行最终审计。
12. 将可刷写镜像和本地发布产物复制到 `output/debian/image/`。

该流程使用外部 `agent.toml`，不会把它复制到项目源码中；也不会创建或发布
GitHub Release。当前完整构建支持的主机边界是 Linux x86_64。

## 4. 主要迁移工作

### 4.1 libc 和厂商库边界治理

Luckfox SDK 中同时包含 glibc、uClibc、Android/Bionic、armhf 和 aarch64
二进制，仅根据目录名无法可靠判断是否能在 Debian 中使用。迁移中建立了完整的
ELF/ABI 审计规则：

- 仅允许 ARM EABI5 hard-float、目标架构匹配的对象。
- 动态程序必须使用 `/lib/ld-linux-armhf.so.3`。
- 禁止依赖 `libc.so.0`、`ld-uClibc.so.1` 或 Android/Bionic。
- 禁止将 aarch64 库混入 armhf rootfs。
- 检查 `DT_NEEDED`、RPATH、RUNPATH、符号和链接闭包。
- 应用全部重新编译，不直接复制 Buildroot 可执行文件。

最终选型为：

- Rockit/MPP 使用可与 glibc 程序链接的静态库。
- RGA 使用 glibc 动态库，并保留准确的 soname 符号链接链。
- 使用经过审计的 RKAIQ/RKRawStream 组件。
- RVE/IVE 只有 uClibc 版本，因此不进入 Debian 固件。
- RKNN 采用静态 mini runtime 兼容方案，详见问题处理章节。

### 4.2 SysV init 到 systemd

迁移没有简单地按脚本编号复制启动顺序，而是根据依赖关系重新建模：

- `rcS` 由 systemd PID 1 替代。
- D-Bus、OpenSSH、BlueZ、networkd、wpa_supplicant、timesyncd 使用 Debian 原生单元。
- Wi-Fi 驱动、蓝牙 UART attach、USB gadget、frame、audio、agent、OTA 等板级功能
  使用 `aiden-*.service`。
- telnet、Samba、dhcpcd 等不需要或不适合生产的组件被移除。
- 可选硬件使用 `Wants=`、有界等待或持续重试，避免阻塞整个 `aiden.target`。
- 每个服务明确声明 OEM、userdata、媒体模块、网络和时间同步依赖。

统一环境生成器负责解析 `/userdata/system/env`，只接受白名单变量和合法格式，
输出 `/run/aiden/system.env`。非法配置不会直接注入 systemd 服务；涉及外部访问的
服务会退化到禁止外部访问或本地恢复模式。

### 4.3 网络、Wi-Fi 与本地恢复入口

网络控制面改为 Debian 原生组件：

- wpa_supplicant 负责无线关联。
- systemd-networkd 是唯一 DHCP 客户端。
- `config_web` 新增 `systemd-networkd` 后端。
- 配置程序使用 `ip`、`networkctl` 和 `systemctl`，不再依赖 `ifconfig`/dhcpcd。
- Wi-Fi 配置写入失败时恢复旧配置。
- USB ECM 使用 networkd 配置地址，并由 dnsmasq 只在 `usb0` 上提供 DHCP。
- USB ECM watchdog 支持检查和重建 composite gadget。
- Debian 的 USB gadget 使用 POSIX shell 可移植的八进制 HID report descriptor，避免
  Buildroot BusyBox 支持而 Debian `dash` 不支持的 `printf '\\xNN'` 差异。
- `usb0` 的 networkd 配置允许在无 carrier 时继续配置静态地址；gadget、watchdog、
  动态键盘和等待 helper 不再通过重复 `networkctl reconfigure` 删除刚设置的地址。

Agent 原先在 HID 恢复时直接调用 Buildroot 路径
`/etc/init.d/S60usb_ecm_watchdog`。现在通过
`AIDEN_USB_COMPOSITE_REFRESH_COMMAND` 注入平台实现：Buildroot 保留旧默认值，
Debian 使用 `/usr/lib/aiden/aiden-usb-ecm-watchdog`。

### 4.4 用户、SSH 与设备身份

Debian 固件新增普通管理用户：

- 用户名：`aiden`
- UID/GID：1000
- 默认密码：`luckfox`
- shell：`/bin/bash`
- 用户组：`sudo`、`audio`、`video`、`dialout`、`plugdev`、`netdev`

固件安装 `sudo`，保留需要输入密码的 sudo 行为，不配置 `NOPASSWD`。
SSH 允许普通用户密码登录；root 禁止密码登录，仅允许公钥认证。

`machine-id` 和 SSH host key 不固化在只读镜像中，而是在首次启动时生成并持久化到
userdata。这样切换 A/B 槽或升级 rootfs 后，设备身份和 SSH 指纹不会变化。

默认密码只适用于开发和首次接入，进入生产前必须由设备初始化或安全配置流程更换。

### 4.5 A/B OTA

OTA 代码和系统服务针对 Debian 进行了重构：

- 使用 Debian 独立的配置路径和 systemd 健康服务。
- 校验 manifest 签名、镜像 hash、渠道和目标版本。
- 检查可用存储空间，避免下载或解包过程中耗尽 userdata。
- 使用流式方式写入 inactive 分区，减少临时空间占用。
- 在切槽前对 inactive rootfs 执行机器身份、SSH 和配置个性化。
- 健康标记绑定当前 OTA transaction，避免旧标记错误确认新升级。
- 处理 pending、success、rollback 等槽状态。
- 保留 userdata，并兼容 Buildroot 到 Debian 的首次迁移。

当前本地构建使用项目提供的 PEM 公私钥生成自洽的 manifest 和设备配置，目的是
完成本地构建和开发验证，不代表已经实现生产发布身份、密钥托管和 GitHub Release。

### 4.6 可重复构建与供应链记录

为降低 Debian、BSP 和镜像构建受时间、网络和主机环境影响的程度，完成了以下约束：

- Debian snapshot 固定为 `20260803T000000Z`。
- Docker 基础镜像固定 digest。
- `pico-sdk` 固定为提交 `5246f9c461d7a1a1e9ee6f7228b1cd1fb129ee4a`。
- BSP 在 `output/` 下的隔离 checkout 构建，不修改源子模块。
- 固定 `SOURCE_DATE_EPOCH`。
- rootfs 使用固定 UUID 和目录 hash seed。
- 规范化 inode、superblock 和归档时间。
- 对 rootfs 内容、属主、硬链接、ACL、xattr、capability 和符号链接进行比较。
- 生成包清单、文件清单、capability/xattr 清单和 SPDX SBOM。

Rockchip BSP 中存在三类非确定性，分别通过 SDK 补丁和规范化脚本处理：

- FIT 中的 host mmap 地址。
- proprietary loader 的构建时间戳及 vendor CRC。
- Rockchip resource index 中未初始化的数据。

Stage 3 历史验收记录表明，两次独立 rootfs 构建以及两次独立 BSP 构建曾达到
字节一致。

## 5. 遇到的问题及解决方法

### 5.1 uClibc 与 glibc ABI 不兼容

**表现**

Buildroot 应用和部分厂商库依赖 `libc.so.0`、uClibc loader 或 uClibc 特有符号，
无法直接放入 Debian/glibc rootfs。

**原因**

armhf 和 hard-float 兼容并不等于 libc ABI 兼容。相同架构的共享库仍可能绑定
不同动态 loader、libc soname 和内部符号。

**解决**

- 建立 SDK 全量 ELF/ABI 清单和构建门禁。
- 所有项目应用用 Debian 工具链重新编译。
- 只选择经过验证的 glibc 动态库或 ABI 可兼容的静态对象。
- 排除所有 uClibc、Bionic 和错误架构共享库。
- 对每个最终 ELF 检查 interpreter、依赖、RPATH/RUNPATH 和符号。

### 5.2 RKNN full runtime 无法加载现有 VAD 模型

**表现**

官方 glibc armhf full runtime 2.3.2 能打开 NPU，但加载现有模型时失败：

```text
Verify ModelBuffer failed!
Invalid RKNN format
Import rknn model failed!
```

**原因**

现有 encoder/decoder VAD 模型是 RV1106 mini runtime split 格式。Rockchip
2.3.2 对 armhf/glibc 只提供 full runtime，对 RV1106 mini runtime 只提供
`armhf-uclibc` 版本。因此不是模型文件损坏，而是模型格式与所选 runtime 类型不匹配。

**解决**

- 使用官方 `armhf-uclibc/librknnmrt.a` 2.3.2 静态库，而不是动态 uClibc `.so`。
- 对静态库的目标 ABI 和未定义符号进行分析，确认其 ARM EABI5、hard-float
  属性可与 Debian armhf 程序链接。
- 新增 `src/rknn_glibc_compat.c`，为 runtime 使用的 uClibc ctype 数据符号提供
  glibc 兼容实现：
  - `__ctype_b`
  - `__ctype_tolower`
- 从 glibc locale 表生成 uClibc 所需的表，并处理两者 `tolower` 表元素宽度不同的问题。
- 将 mini runtime 静态嵌入 `rknn_vad`，移除动态 `librknnrt.so`。
- 审计 runtime 2.3.2 版本字符串、RKNN 静态符号和不存在动态 RKNN 依赖。

**当前状态**

代码、交叉编译和静态审计已经通过，原先使用 full runtime 导致的格式不匹配路径已被
替换。不过仓库现有硬件日志中没有找到新静态 mini runtime 成功加载两份 VAD 模型并
完成持续推理的明确记录。因此还应在板端补做模型初始化、推理正确性、内存/CMA 和
长时间稳定性测试，才能将 RKNN 实机验收标记为完全通过。

### 5.3 NPU/媒体设备权限和 DMA heap

**表现**

Debian 初始创建的 `/dev/rknpu` 和 `/dev/mpi/*` 为 `0600 root:root`，普通用户
无法访问。测试 full runtime 时还要求 `/dev/dma_heap/system`，而旧 rootfs 未正确
coldplug Rockchip heap 节点。

**解决**

- 增加 udev 规则，将设备设置为 `0660 root:video`。
- 将 `aiden` 加入 `video`、`audio` 等硬件组。
- 媒体模块 helper 加载 Rockchip 媒体/NPU 模块，并兼容创建 DMA heap 节点和链接。
- 最终采用 mini runtime 后，RKNN 运行期只依赖 `/dev/rknpu`，降低了依赖面。

### 5.4 rootfs 只读导致服务连锁失败

**表现**

启动日志曾出现：

```text
Read-only file system
systemd-timesyncd failed
aiden-rootfs-grow failed
aiden-oem-ldconfig failed
```

**原因**

Rockchip boot args 没有明确提供 `rw`，rootfs 也没有对应的 fstab 项使 systemd
自动重挂为可写，导致首次启动初始化和服务写入失败。

**解决**

`aiden-rootfs-grow` 在每次启动时先执行 `mount -o remount,rw /`，然后再进行
rootfs 扩容、挂载和运行时目录初始化；systemd 单元保证它发生在 OEM、userdata、
ldconfig 和 timesyncd 之前。

### 5.5 未连接 HDMI 时 frame.service 的历史恢复行为（已由 2026-08-21 修复替代）

**表现**

测试板没有连接 HDMI 输入，系统也没有 probe 到 `rk628-csi` 或 `tc358743` bridge。
frame service 找不到可用 bridge 后退出，早期 systemd 配置达到启动频率限制后停止重试。

**解决**

- `aiden-frame-start` 最多等待 20 秒寻找 HDMI bridge。
- `aiden-frame.service` 使用 `Restart=on-failure` 和 `RestartSec=2s`。
- 设置 `StartLimitIntervalSec=0`，使其在无 HDMI 时持续有界重试。
- 插入并 probe HDMI bridge 后，服务无需重启系统即可恢复。

这套逻辑是早期迁移版本的行为；它会让 systemd 反复重启服务，并且在失败期间没有
可用的 IPC socket。2026-08-21 的稳定常驻修复已替代该行为，详见下节。

#### 2026-08-21 稳定常驻修复

此前 Debian 启动 helper 在 TC358743 的 EDID/HPD 操作失败，或启动后暂时找不到
HDMI bridge 时，会直接退出，导致 systemd 反复重启服务且 socket 不存在。现已调整为：

- `frame_service` 先创建 `/run/frame_service/frame_service.sock`，再启动捕获线程；
- HDMI bridge、`/dev/video0`、I2C、EDID、DV timings 或视频流暂时不可用时，捕获管理器
  保持 `RECOVERING` 并以 1--5 秒有界退避重试；
- `--auto-subdev` 每次恢复时重新发现 `rk628-csi`/`tc358743`，不依赖固定的
  `v4l-subdevX` 编号；
- TC358743 的 shell 侧 EDID/HPD 预处理失败只记录警告，不再阻止 IPC 服务启动；
- 设备锁从进程入口移到可恢复的捕获源，视频节点暂时不存在或被占用时不会终止
  `frame_service`；
- 没有 HDMI 时 health socket 仍可用并返回 `STARTING`/`RECOVERING`，接入 HDMI 后无需
  重启 systemd 服务即可自动切换到 `RUNNING`。

因此，今后应将“socket 不存在”视为服务启动故障；将 `state=RECOVERING` 视为服务正常
常驻但当前没有可用视频帧。截图在后者状态下应等待 HDMI 信号恢复，而不是反复重启
`aiden-frame.service`。

### 5.6 EDID 工具版本和设备类型不兼容

**表现**

- 部分 `v4l2-ctl` 不支持 `--fix-edid-checksums`。
- 对非 HDMI bridge 的 V4L2 subdevice 设置 EDID 会返回 `ENOTTY`。

**解决**

- 应用内部按 128 字节 EDID block 重新计算 checksum。
- helper 仅在 `v4l2-ctl --help-edid` 明确支持时传入该选项。
- 只对识别出的 `rk628-csi`/`tc358743` bridge 执行 EDID 流程。

当前未连接 HDMI bridge，所以完整摄像头链路仍待后续硬件验证。

### 5.7 可选设备拖慢 systemd 启动

**表现**

外部 RTC 不存在，或 Wi-Fi 设备/驱动出现延迟时，`.device` 单元可能让依赖服务
长时间等待，`wlan0` 曾触发启动超时。

**解决**

- RTC device job 设置 5 秒超时。
- wlan0 device job 设置 30 秒超时。
- 对非关键硬件使用 `Wants=` 和有界恢复逻辑。
- 本地 agent、USB 和配置网页不再因精确时间或无线网络缺失而永久阻塞。

### 5.8 SSH 端口拒绝连接

**表现**

浏览器可以访问 `192.168.42.1`，但 SSH 返回：

```text
ssh: connect to host 192.168.42.1 port 22: Connection refused
```

**原因**

`ssh.service` 依赖 SSH 身份初始化；早期脚本没有创建 `/run/sshd`，并优先生成耗时较长
的 RSA key。在资源有限的板上，identity service 可能超过 30 秒超时，导致 SSH
依赖失败。

**解决**

- 创建 `/run/sshd` 并设置为 `0755`。
- 按 `ed25519`、`ecdsa`、`rsa` 顺序生成 host key。
- identity service 超时提高到 180 秒。
- host key 持久化到 `/userdata/system/ssh`。
- 启动 SSH 前执行 `sshd -t` 校验配置和密钥。

### 5.9 普通用户无法 sudo

**表现**

root 已禁止密码 SSH 登录，但初始 Debian rootfs 没有 `sudo`，普通用户无法执行管理操作。

**解决**

- 安装 `sudo` 包。
- 创建 `aiden` 用户并加入 `sudo` 及硬件访问组。
- 为 `aiden` 设置默认密码 `luckfox`。
- 允许普通用户 SSH 密码认证。
- root 保持仅公钥认证，且不配置免密 sudo。

### 5.10 Wi-Fi 控制面与 Buildroot 行为不一致

**表现**

原 `config_web` 依赖 `ifconfig`、dhcpcd 和直接拉起/终止进程。在 Debian 中继续这样做
会与 networkd 争夺地址和 DHCP 状态。

**解决**

- 为 `config_web` 新增 `--wifi-backend=systemd-networkd`。
- 使用 `ip`、`networkctl` 和 `systemctl restart wpa_supplicant@wlan0`。
- 只允许 networkd 获取 DHCP 地址。
- 写配置失败时回滚原配置。
- 保留 wlan guard，但只负责有界恢复，不接管 DHCP。

### 5.11 Buildroot 路径残留

**表现**

Agent 的 USB HID 恢复逻辑仍调用 Buildroot init 脚本路径，Debian 中该路径不存在。

**解决**

增加平台可配置命令 `AIDEN_USB_COMPOSITE_REFRESH_COMMAND`。Buildroot 继续使用原路径，
Debian systemd 服务注入新的 USB ECM watchdog helper，支持常驻 watchdog 和一次性
`refresh`。

### 5.12 生产签名参数阻塞本地构建

**表现**

原 Stage 3 设计要求生产 `OTA_PUBLIC_KEY_PATH` 和已签名、与镜像 hash 完全匹配的
OTA 配置。没有生产身份时，最终镜像步骤会在安全门禁处停止。

**解决**

- 保留生产门禁，不在底层 Stage 3 脚本中静默绕过签名校验。
- 在 `debian_build.sh` 中使用本地 PEM 公私钥生成开发 manifest。
- 校验公私钥匹配、manifest 签名和镜像 hash。
- 自动生成与本次镜像匹配的设备 OTA 配置。
- 仅输出本地产物，不发布 GitHub Release。

这解决的是开发构建问题，不替代生产密钥托管、发布身份、审批和轮换机制。

### 5.13 Debian USB HID 描述符和 ECM 地址回归

**表现**

某次新 Debian 固件启动后，主机曾无法稳定看到 `Aiden HID+ECM`，浏览器也无法访问
`192.168.42.1`。更换 USB 线缆和主机端口后问题仍可复现；主机日志包含：

```text
unknown main item tag
item fetching failed
hid-generic probe failed with error -22
```

在更早的失败阶段还出现过 USB 控制传输错误：

```text
Device not responding to setup address
device not accepting address ..., error -71
unable to enumerate USB device
```

**原因**

该问题不是最近提交修改了 DWC3、设备树或 USB peripheral 模式。回归审计确认，
Debian 迁移提交 `03dffcf4` 将原 Buildroot 的 `S49usbhid` 直接安装为 Debian
helper，并将 `ifconfig` 替换为 `ip`/`networkctl`：

1. `S49usbhid` 使用 `printf '\\x05\\x01...'` 生成 HID report descriptor。Buildroot
   BusyBox 支持 `\\xNN`，但 Debian `/bin/sh` 是 `dash`，会把它写成 ASCII 文本。
   板端实测描述符长度为 `180/232/188` 字节，而正确长度应为 `45/58/47` 字节。
2. `usb0` 先由脚本设置 `192.168.42.1/24`，随后 helper 调用
   `networkctl reconfigure`。USB 尚未完成枚举时接口没有 carrier，networkd 可能移除
   该静态地址，进而导致 dnsmasq 等待地址失败，浏览器无法访问配置页面。

**解决**

- 将 5 份 HID descriptor 改为 POSIX 八进制转义，保持 descriptor 内容不变，只修复
  shell 解释差异。
- 在 `overlay-debian/etc/systemd/network/30-usb0.network` 中加入：

  ```ini
  [Link]
  RequiredForOnline=no

  [Network]
  ConfigureWithoutCarrier=yes
  ```

- 删除 gadget 启动、ECM watchdog、动态键盘和等待 IP helper 中会撤销刚设置地址的
  重复 `networkctl reconfigure` 调用。
- 增加测试，使用 `/bin/sh` 实际解释 descriptor 并校验 5 份二进制内容和长度；同时
  检查 networkd 配置和相关 helper 不再进行重复重配置。

**板端回归结果**

修复文件临时部署到开发板并冷重启后，结果如下：

```text
lsusb: 1d6b:0104 Linux Foundation Multifunction Composite Gadget
hid.usb0/report_desc: 45 bytes，识别为 Keyboard
hid.usb1/report_desc: 58 bytes，识别为 Mouse
hid.usb2/report_desc: 47 bytes，识别为 Consumer Control Device
UDC: configured
usb0: 192.168.42.1/24
主机 ECM: 192.168.42.152/24
http://192.168.42.1/: HTTP 200
```

随后执行 UDC 解绑、无 carrier 状态下 networkd 重配、重新绑定测试，静态地址仍然
保留，主机再次完成 HID+ECM 枚举。修复后主机未再出现 `unknown main item tag`、
`item fetching failed` 或 HID `-22`。这次验证证明软件回归已解决；早期 `error -71`
属于更底层的 USB 控制传输失败，仍应在出现时结合线缆、供电、接口和 UART/内核日志
单独排查，不能仅由 HID descriptor 修复解释。

## 6. 验证与测试结果

### 6.1 已确认通过

| 范围 | 结果 | 证据或说明 |
|---|---|---|
| Debian 基础启动 | 通过 | 首次启动、热重启、两次冷启动通过 |
| Stage 1 实机验收 | 通过 | 23 pass、0 fail、1 optional skip |
| Wi-Fi、DNS、APT、SSH、蓝牙基础能力 | 通过 | Stage 1 板端验收记录 |
| Stage 2 应用 ELF 审计 | 通过 | 当前 `status=pass`，22 个 ELF |
| glibc loader/依赖闭包 | 通过 | 无 uClibc loader/依赖混入 |
| rootfs 构建与导入审计 | 通过 | e2fsck、内容和属性审计通过 |
| BSP 审计 | 通过 | 固定 SDK 提交、模块、固件和 A/B boot 检查通过 |
| 可重复构建 | 通过两次独立验证 | rootfs 和 BSP 历史验收达到字节一致 |
| 最终镜像审计 | 通过 | `output/debian-stage3-local/audit-report.txt` 为 `Audit passed` |
| 刷写工具链 | 通过 | 多份日志记录 `Upgrade firmware ok` |
| 媒体/NPU 模块加载 | 通过 | 相关模块和 `/dev` 节点创建成功 |
| 普通用户设备权限 | 通过修复 | `/dev/rknpu` 和 `/dev/mpi/*` 使用 `root:video` 0660 |
| 音频采集与播放 | 通过 | 10 秒采集和播放测试成功 |
| USB HID/ECM 恢复路径 | 通过代码和测试闭环 | Debian helper 替换 Buildroot 固定路径 |
| USB HID+ECM 冷启动和重新枚举 | 通过临时部署验证 | `1d6b:0104`、3 个 HID 接口、ECM、`192.168.42.1` 和 HTTP 200 均通过；正式镜像需重新构建后再刷写 |
| 本地完整固件构建 | 通过 | 能生成并审计 `update.img` 和 OTA 产物 |

### 6.2 已实现但仍需补充板端闭环

| 范围 | 当前状态 | 待补验证 |
|---|---|---|
| RKNN VAD | 静态 mini runtime 2.3.2 和 glibc shim 已构建、审计通过 | 两份模型加载、推理结果、资源和持续运行 |
| SSH 身份可靠性 | `/run/sshd`、生成顺序和超时已修复 | 用最新镜像重新确认首次启动和密码登录时延 |
| frame.service 无 HDMI 行为 | 已改为持续重试 | 接入 HDMI bridge 后确认自动恢复 |
| Wi-Fi 自动配置 | networkd 后端已实现，手动连接可工作 | 配置网页写入、回滚和重连完整测试 |
| USB HID/ECM 修复进入正式镜像 | 源码修复和板端临时部署已验证 | 重新运行 `./debian_build.sh`，刷写新 `update.img` 后做一次冷启动验收 |
| A/B OTA | 写入、个性化、健康标记和状态代码已实现 | 真机升级、失败回滚和断电矩阵 |

### 6.3 尚未完成的发布门禁

- HDMI 摄像头完整采集链路。
- RKNN VAD 板端推理准确性、吞吐、内存/CMA 和长时间稳定性。
- 摄像头、RKNN、音频并发压力测试。
- 72 小时稳定性测试和正式资源预算。
- A/B 下载、写入、切槽、启动确认、失败回滚各阶段的断电测试。
- 生产 OTA 私钥托管、公钥注入、发布审批、密钥轮换和撤销策略。
- 生产默认密码和首次初始化安全策略。
- 最终许可证复核，尤其是静态嵌入 proprietary RKNN runtime 的发布边界。
- 面向生产批次的 G1-G5 完整验收报告。

## 7. 当前构建和刷写方式

### 7.1 从零构建

默认构建命令：

```bash
./debian_build.sh
```

常用输入可以通过环境变量覆盖：

```bash
RK_JOBS=24 \
AGENT_CONFIG_PATH=/path/to/agent.toml \
OTA_PRIVATE_KEY_PATH=/path/to/id_25519.pem \
OTA_PUBLIC_KEY_PATH=/path/to/id_25519.pub.pem \
./debian_build.sh
```

默认 `RK_JOBS=24`；如果主机在线 CPU 少于 24，会使用主机可用的最大并行数。

主要主机依赖包括：

- Linux x86_64。
- Docker daemon 和当前用户的 Docker 访问权限。
- Git 和子模块支持。
- curl、OpenSSL、Python 3、tar、sha256sum 等基础工具；manifest 所需的 `jq`
  由固定的 Stage 3 构建容器提供。
- 可访问 Debian snapshot、Docker registry 和 Go 下载源，或已经具有对应缓存。
- 外部 `agent.toml`。
- 匹配的 Ed25519 PEM 公私钥。

### 7.2 构建产物

本地输出目录为 `output/debian/image/`，包含：

```text
update.img
update.img.sha256
boot_a.img.tar.gz
boot_b.img.tar.gz
oem.img.tar.gz
rootfs.img.tar.gz
update.img.tar.gz
manifest.json
```

其中可直接刷写的固件是：

```text
output/debian/image/update.img
```

### 7.3 刷写

开发板进入 Loader 或 Maskrom 刷写模式后执行：

```bash
./upgrade_tool/upgrade_tool uf output/debian/image/update.img
```

刷写后应至少检查 UART 启动日志、systemd failed units、USB 网络、SSH、存储挂载、
媒体模块、音频和 NPU。未接 HDMI 时可以暂时忽略 frame service 的失败重试。

USB 回归修复在重新刷写前也可以仅通过临时替换 helper 做诊断，但正式验证必须使用
重新构建后的 `output/debian/image/update.img`，否则修复不会保留在下一次启动中。

## 8. 关键提交索引

| 提交 | 内容 |
|---|---|
| `a709faf6` | 新增 Debian 13 armhf Stage 1 rootfs、BSP、镜像、审计、刷写和实机验证链路 |
| `03dffcf4` | 完成应用交叉编译、systemd overlay、A/B OTA、设备身份、Stage 2/3 构建和测试主体 |
| `4e528c6b` | 使用静态 RKNN mini runtime 2.3.2 和 glibc 兼容层替换 full runtime |
| `de785b84` | 修复无 HDMI 时 frame service 达到启动频率限制后不再重试的问题 |
| `407ee8dc` | 安装 sudo，创建可密码登录和执行 sudo 的 `aiden` 用户 |
| `d2a05d66` | 更新设备操作 skill 文档，与固件迁移主体关联较弱 |
| `cd3be07e` | 修复 SSH runtime 目录、host key 生成顺序和身份初始化超时 |
| `fe773f61` | 新增 `debian_build.sh` 和本地完整固件/OTA 发布产物 |
| 工作区修复（2026-08-21，尚未提交） | 修复 Debian `dash` 下 HID descriptor 生成错误、usb0 无 carrier 地址丢失，并增加 USB 回归测试 |

相对 `origin/main`，当前迁移分支共涉及约 218 个文件，新增约 14,601 行、删除
211 行，改动主要集中在 `scripts/`、`overlay-debian/` 和 `src/`。

## 9. 当前状态判断

从工程实现角度，此次迁移已经完成了以下主干闭环：

- Debian 13 armhf 可以在原 Luckfox/Rockchip BSP 上启动。
- Aiden 应用能够以 glibc armhf 形式构建并通过依赖审计。
- Buildroot 的启动职责已经迁移为 systemd 服务或 Debian 原生服务。
- A/B 分区、OTA 状态、设备身份和 userdata 迁移已纳入 Debian 设计。
- 可以本地生成、审计和刷写完整 `update.img`。
- 音频、网络、蓝牙、USB、媒体模块和基础 NPU 设备访问已经取得实机证据。
- USB HID+ECM 已完成 Debian descriptor、networkd 无 carrier 和冷启动/解绑重绑回归验证；
  修复代码尚未进入新的正式镜像。

因此，该分支已经达到“可继续进行 Debian 固件开发和集成测试”的状态。

但若目标是“生产发布”，当前仍应视为条件通过而不是最终通过。最重要的剩余工作是：

1. 使用静态 mini runtime 在板端重新执行两份 VAD 模型的加载和推理回归。
2. 重新构建并刷写包含 USB 回归修复的新镜像，完成一次正式冷启动验收。
3. 接入 HDMI bridge 后完成摄像头和 frame service 验收。
4. 完成 RKNN、摄像头、音频并发及 72 小时稳定性测试。
5. 完成 A/B OTA 回滚和断电矩阵。
6. 替换开发默认密码和本地签名身份，完成生产安全与发布治理。

## 10. 资料来源

本文根据以下仓库资料整理：

- `debian-13-armhf-migration-plan.md`
- `docs/debian-stage1.md`
- `docs/debian-stage1-acceptance-20260813.md`
- `docs/debian-stage2.md`
- `docs/debian-stage2-host-acceptance-20260817.md`
- `docs/debian-stage3.md`
- `docs/debian-stage3-host-acceptance-20260817.md`
- `scripts/debian/init-script-map.tsv`
- `scripts/debian/environment-service-map.tsv`
- `output/debian-stage2/apps-audit/summary.txt`
- `output/debian-stage3-local/audit-report.txt`
- `Falcom/debian` 相对 `origin/main` 的提交历史和代码差异

## 11. Buildroot 残留审计与后续清理建议（2026-08-21）

本次审计确认，仓库中仍保留 Buildroot 相关文件和代码，但它们不是单一意义上的
“误残留”。当前分支仍是 Buildroot 回退链与 Debian 新链并存的双平台结构。审计期间
没有删除任何文件或代码。

### 11.1 仍在使用的旧构建链

以下文件仍构成旧 Buildroot 固件的构建入口或平台配置，不能在保持旧构建能力的前提下
直接删除：

```text
build.sh
build_image.sh
_build.sh
_build_image.sh
cmake/toolchain-arm-rockchip830.cmake
cmake/platforms/rv1106-buildroot-uclibc.cmake
```

`CMakeLists.txt` 当前默认平台仍为 `rv1106-buildroot-uclibc`，同时提供
`rv1106-debian-glibc`。GitHub Actions、发布脚本和回归测试仍调用
`build_image.sh`/`_build_image.sh`，并检查 SDK 中的 Buildroot 可重复构建配置。
删除这些入口会同时影响旧固件构建、CI 和现有对比验证。

### 11.2 `overlay/` 是共享资产，不应整体删除

`overlay/` 原本是 Buildroot overlay，但 Debian stage2/stage3 仍有意复用其中的部分
文件，包括：

```text
overlay/etc/init.d/S49usbhid
overlay/etc/aiden_boot_timeline.sh
overlay/oem/usr/bin/aiden-dynamic-keyboard
overlay/oem/usr/lib/aiden-log.sh
overlay/oem/usr/model/*.rknn
overlay/oem/usr/model/*.bin
overlay/oem/usr/share/aiden/audio/
overlay/oem/usr/share/aiden/edid/
```

因此不能简单删除整个 `overlay/`。如果以后决定 Debian-only，应先将仍需使用的内容
迁移到职责明确的目录（例如 `assets/oem/`、`assets/models/`、`assets/audio/`、
`assets/edid/` 和 `assets/usb/`），再修改 Debian 构建脚本、审计脚本和测试。

### 11.3 可作为 Debian-only 阶段的清理候选

在完成迁移和引用审查后，以下内容可以考虑清理：

```text
cmake/platforms/rv1106-buildroot-uclibc.cmake
cmake/toolchain-arm-rockchip830.cmake
overlay/oem/usr/lib/librknnmrt.so
```

`librknnmrt.so` 是旧 uClibc mini runtime；当前 Debian RKNN 方案使用
`third_party/rknpu2/v2.3.2/lib/librknnmrt.a` 静态库和 glibc 兼容层。不过该 `.so`
仍被旧 Buildroot CMake/镜像流程引用，所以当前不能直接删除。

`overlay/etc/init.d/` 中部分脚本（例如 `S30dbus`、`S35wifidrv`、`S40network`、
`S50telnet`、`S50usbdevice`、`S57wetty`、`S91smb` 和 `S99usb0config`）不会进入
当前 Debian rootfs，但仍属于旧 Buildroot overlay。删除它们前必须先移除或重写
旧镜像流程、相关测试和发布策略。

### 11.4 双平台兼容代码和 SDK 子模块

Agent 的 USB HID 恢复逻辑保留 Buildroot 默认命令
`/etc/init.d/S60usb_ecm_watchdog`，Debian 通过环境变量切换到
`/usr/lib/aiden/aiden-usb-ecm-watchdog`。这是有意的双平台兼容设计，不应当作无效代码
直接删除。

`pico-sdk` 子模块内部仍包含 Buildroot defconfig、构建规则和 uClibc 工具链。Debian
stage3 仍使用该 SDK 构建 U-Boot、驱动、环境和 A/B 镜像，当前测试也会读取 SDK 的
Buildroot 配置。因此不能直接修改或删除子模块内部的 Buildroot 文件；若以后切换到
Debian-only，应维护独立的裁剪 SDK fork。

### 11.5 当前可安全处理的对象

目前仅建议按需清理生成物，而不是源码：

```text
build/
build-host/
build-debian-g0/
output/
.cache/
.toolchains/
benchmark/.venv/
pico-sdk/output/
```

这些目录通常被 `.gitignore` 忽略，但 `output/` 可能包含固件、审计报告和刷写证据，
清理前应确认不再需要。当前本次审计没有执行清理。

### 11.6 若未来选择 Debian-only 的建议顺序

Debian-only 清理应作为一次架构变更执行，而不是逐个删除文件：

1. 迁移 Debian 仍使用的 overlay 共享资产。
2. 修改 Debian stage1/stage2/stage3 脚本及所有相关审计和回归测试。
3. 将 CMake 默认平台切换为 Debian，并移除 Buildroot 平台选择。
4. 移除 Buildroot 构建入口、旧 overlay 和旧 runtime。
5. 使用裁剪后的独立 SDK fork 完成 BSP、镜像、USB、RKNN、音频、摄像头、OTA 和
   回滚回归。
6. GitHub Actions 构建和 GitHub Release 自动发布另行规划，不作为本轮本地 Debian
   构建收敛的完成条件。

在上述工作完成前，保留 Buildroot 文件是为了支持旧固件回退、问题对比和现有 CI，
不表示 Debian 迁移未完成。

### 11.7 Debian-only 决策更新（2026-08-31）

项目后续部署目标已确定为 Debian-only。该决定覆盖本节早期“双平台长期并存”的假设。
当前已完成：

1. CMake 默认及公开平台选项切换为 `rv1106-debian-glibc`。
2. Debian rootfs helper 由 `overlay-debian/` 自身持有。
3. Debian OEM 脚本、模型、音频与 EDID 资源迁至 `overlay-debian-oem/`。
4. Debian Stage 2/3 构建和镜像审计不再读取根目录 `overlay/`。
5. Agent、Frame Service、Config Web 和持久 Python 环境使用 systemd 原生控制链。
6. `fq`、`yq`、`rg` 由 Debian Stage 2 构建，Stage 3 按 manifest 安装并对
   最终 rootfs 逐文件复核校验和。
7. Debian BSP 明确继承 `aiden-rk628.config`，避免切断旧 userspace 后丢失
   RK628D/TC358743 内核能力；OTA CLI 默认配置路径也切换为
   `/userdata/debian/ota/config.json`。
8. GitHub Actions 构建和 GitHub Release 自动发布暂不纳入本轮范围，现有 workflow
   保持不变；这不改变本地固件生产路径只支持 Debian 的决定。

旧 `build.sh`、根目录 Buildroot overlay、uClibc 平台文件及相关测试文件在本次 rebase
收尾中仍作为历史对比材料保留，但不再属于本地 Debian 构建兼容承诺，也不得被活动文档
或本地生产入口调用。后续可以独立删除这些遗留内容，而不需要再维持可构建性。`pico-sdk` 内部的
Buildroot 目录和 uClibc 工具链仍可作为厂商 BSP 构建 U-Boot、kernel、modules 和打包
镜像的实现细节存在；这不代表设备用户空间继续支持 Buildroot。
