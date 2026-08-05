# Aiden 板 Linux 发行版适配调研

> 调研日期：2026-08-05
>
> 目标硬件：Aiden Hardware Demo 使用的 Luckfox Pico Zero（RV1106G3）
>
> 方法：真机只读检查 + Luckfox、Linux、U-Boot、Buildroot、OpenWrt、Debian、Ubuntu、Yocto/OpenEmbedded 官方资料和源码树核对。

## 结论摘要

短期生产固件应继续使用 **Luckfox 厂商 BSP 中的 Buildroot**。它是目前唯一同时覆盖 Pico Zero 启动链、RV1106 设备树、摄像/媒体/NPU、音频、Wi-Fi 和 USB gadget 的官方方案。Aiden 当前的 HDMI 采集、音频、RKNN、HID/ECM 和 A/B OTA 也都建立在这套 BSP 和 uClibc ABI 上。

其他方案的合理定位如下：

1. **Buildroot（Luckfox BSP）**：当前最适合，继续作为生产底座；需要补上旧版本安全维护和 SDK 升级策略。
2. **Yocto/OpenEmbedded**：若未来需要多年量产维护、SBOM、严格可复现构建和更系统的包生命周期，可作为长期 BSP 项目；目前没有 RV1106/Luckfox machine，移植成本最高。
3. **Debian armhf**：CPU ABI 可用，但没有 Pico Zero 官方镜像或板级支持；只适合保留 Luckfox U-Boot、内核、DTB 和驱动后的实验性 rootfs。
4. **Ubuntu 22.04 / Ubuntu Base armhf**：Luckfox 提供第三方镜像入口，Canonical 也提供 armhf 用户态，但不是 Canonical 的 RV1106 板级支持；资源和 ABI 风险与 Debian 相同或更高，不建议作为 Aiden 产品底座。
5. **OpenWrt**：官方 Rockchip target 只有 ARMv8，而 RV1106 是 32 位 ARMv7；需要新建 target 并移植完整厂商 BSP，不是可直接刷写的方案。
6. **Alpine armv7**：官方提供通用 armv7 rootfs/U-Boot tarball，但没有 Luckfox/RV110x 板型；网络上的 RV1103 移植属于社区方案，不能视为 RV1106/Pico Zero 官方支持。
7. **纯主线 Linux + 任意发行版**：当前仍不具备 Aiden 所需的完整 RV1106/Pico Zero 支持。主线已经出现 RV1103B/Onion Omega4 的早期支持，但不能替代 RV1106 厂商 BSP。

## 当前板上系统（2026-08-05 真机只读检查）

对 `192.168.42.1` 的只读检查结果：

| 项目 | 当前值 |
| --- | --- |
| 发行系统 | Buildroot 2023.02.6 |
| 内核 | Linux 5.10.160，`armv7l` |
| 板型 / compatible | `Luckfox Pico Zero`；`rockchip,rv1103g-38x38-ipc-v10`、`rockchip,rv1106g3` |
| CPU | 单核 Cortex-A7，ARMv7，硬浮点 |
| 物理内存 | 256MB DDR3L |
| Linux 可见内存 | `MemTotal: 149640 kB` |
| CMA | `CmaTotal: 102400 kB`（用于相机/媒体连续内存） |
| 检查时可用内存 | 约 96MB |
| Swap | 约 320MB：64MB zram + 256MB swapfile |
| 存储 | 8GB eMMC，约 7.65GB 可见 |
| 当前 rootfs | 1.4GB 分区，约 300MB 已用 |
| init / libc | BusyBox 1.36.1 init，uClibc 1.0.31 |
| 包管理 / systemd | 无 `apt`、`apk`、`opkg`、`rpm`，无 systemd |
| 关键内核能力 | 未启用 `CONFIG_NAMESPACES`、`CONFIG_OVERLAY_FS`、`CONFIG_SMP`、`CONFIG_PREEMPT` |
| 进程内存样本 | Node/WeTTY RSS 约 46MB；Go Agent RSS 约 22MB |

这些结果与 Luckfox 官方资料一致：Pico Zero 为单核 Cortex-A7、256MB DDR3L、8GB eMMC；官方 Zero BoardConfig 使用 ARMv7 uClibc 工具链、Buildroot rootfs、RV1106 专用 DTS，并配置 100MB CMA。参见 [Luckfox Pico Zero 产品规格](https://wiki.luckfox.com/Luckfox-Pico-Zero) 和 [官方 Pico Zero BoardConfig](https://github.com/LuckfoxTECH/luckfox-pico/blob/main/project/cfg/BoardConfig_IPC/BoardConfig-EMMC-Buildroot-RV1106_Luckfox_Pico_Zero-IPC.mk)。

真机上 `frame_service`、`audio_service`、`config_web` 是以 `/lib/ld-uClibc.so.0` 为解释器的 ARM EABI5 动态 ELF；Go Agent 是静态 ELF。仓库构建也明确使用 `arm-rockchip830-linux-uclibcgnueabihf`，并链接 RV1106 的 MPP、RGA、Rockit 和 RKNN 库。Luckfox 官方 SDK 只提供 RV1106 的 uClibc 工具链和对应媒体库目录，参见 [SDK 工具链目录](https://github.com/LuckfoxTECH/luckfox-pico/tree/main/tools/linux/toolchain)、[RV1106 MPP 发布目录](https://github.com/LuckfoxTECH/luckfox-pico/tree/main/media/mpp/release_mpp_rv1106_arm-rockchip830-linux-uclibcgnueabihf) 和 [RV1106 RGA 发布目录](https://github.com/LuckfoxTECH/luckfox-pico/tree/main/media/rga/release_rga_rv1106_arm-rockchip830-linux-uclibcgnueabihf)。

## 无法绕过的板级约束

### 1. 用户态架构兼容不等于板子可启动

RV1106 的 Cortex-A7 符合 ARMv7 hard-float 用户态要求，因此 Debian/Ubuntu 的 `armhf` 程序在指令集层面是匹配的。但 ARM SoC 的启动、时钟、中断、eMMC、USB、摄像、音频和媒体硬件依赖具体的 bootloader、DTB 和内核驱动。Debian 官方安装指南也明确说明 ARM 平台高度异构，能否安装取决于内核组件支持和设备树，而不仅是 CPU 架构；参见 [Debian armhf 支持硬件说明](https://www.debian.org/releases/stable/armhf/ch02s01.en.html)。

Luckfox 官方启动链由 Rockchip loader/idblock、厂商 U-Boot、FIT/boot image、RV1106 DTB、厂商 5.10 内核和 rootfs 组成；设备树由 U-Boot 加载、再由内核据此初始化设备。参见 [Luckfox SDK 目录和镜像说明](https://wiki.luckfox.com/Luckfox-Pico-Zero/SDK) 与 [Luckfox 设备树说明](https://wiki.luckfox.com/Luckfox-Pico-Zero/Device-Tree)。

### 2. 256MB 是物理容量，不是普通用户态的可用容量

官方 Zero BoardConfig 配置 `RK_BOOTARGS_CMA_SIZE="100M"`。真机因此只报告约 146MB `MemTotal`，而不是完整 256MB；检查时 `MemAvailable` 约 96MB。CMA 页面可在需要时承载可迁移内存，但摄像/媒体链工作时会占用这部分连续内存，不能把它当成始终可供通用进程使用的余量。官方 SDK 也把 CMA 定义为相机内存，参见 [Luckfox SDK 编译配置说明](https://wiki.luckfox.com/Luckfox-Pico-Zero/SDK)。

因此，发行版选择的首要瓶颈是内存和媒体连续内存，而不是 8GB eMMC 容量。

### 3. 当前硬件服务受 uClibc 用户态 ABI 约束

Luckfox 官方明确说明 RV1106 SDK 使用 uClibc，加入第三方包时不能假定 glibc 依赖可用，参见 [Luckfox Buildroot 配置说明](https://wiki.luckfox.com/Luckfox-Pico-Zero/Buildroot-Configuration)。Debian 和 Ubuntu armhf 默认使用 glibc；把它们的 rootfs 换上去并不会自动让现有 uClibc 媒体库和 Aiden C/C++ 服务兼容。

可行的混合方案必须二选一：

- 为 glibc 重新构建 Aiden C/C++ 服务及其所有 Rockchip 媒体依赖；或
- 在 glibc rootfs 内完整保留 uClibc loader、库搜索路径和厂商动态库，并逐项验证双 libc 共存。

两者都需要完整的 HDMI capture、音频、RKNN、Wi-Fi、USB HID/ECM 和 OTA 回归测试。

## 候选方案比较

| 方案 | 官方 RV1106 / Pico Zero 板级支持 | 运行资源 | 包管理 | Aiden 硬件能力风险 | 维护成本 | 建议 |
| --- | --- | --- | --- | --- | --- | --- |
| Luckfox Buildroot | 完整厂商 BSP | 最低 | 无在线包管理；整镜像更新 | 最低 | 中；版本偏旧需自行维护 | **近期生产首选** |
| Yocto/OpenEmbedded | 无现成 machine | 可裁剪到较低 | 可生成 RPM/DEB/IPK | 初始移植高，完成后可控 | 最高；适合长期产品化 | **长期候选** |
| Debian 13 armhf | 只有 CPU 用户态支持 | 官方建议高于本板；依赖 swap 才可能安装 | apt/dpkg | glibc ABI、板级启动和媒体栈风险高 | 高 | **仅 PoC** |
| Ubuntu 22.04 armhf | Luckfox 有第三方镜像；Canonical 无 RV1106 BSP | 偏高 | apt/dpkg | 与 Debian 相同，且无官方 Server 板型 | 高 | 不建议生产 |
| OpenWrt | 官方 Rockchip 只有 ARMv8 | 基础系统可较小 | opkg | 必须新建 ARMv7 target 和移植 BSP | 很高 | 除非产品转为网络设备 |
| Alpine armv7 | 通用 armv7 rootfs；无 Luckfox BSP | 较低 | apk | musl 与现有 uClibc ABI 不兼容，仍需厂商启动链 | 高 | 社区实验，不作生产依据 |
| 主线内核 + 自选 rootfs | 无 RV1106/Pico Zero 完整支持 | 取决于 rootfs | 取决于发行版 | 相机/ISP/NPU/媒体/启动链缺口最大 | 很高 | 暂不采用 |

## 各方案详细判断

### Luckfox Buildroot：最适合当前 Aiden

Luckfox 官方 RV1106 镜像页把 Buildroot 标为推荐系统；官方 SDK 的 Pico Zero 配置明确选择 `LF_TARGET_ROOTFS=buildroot`，构建菜单把 Buildroot 描述为支持 Rockchip 官方功能的系统。参见 [Luckfox RV1106 系统镜像概览](https://wiki.luckfox.com/Luckfox-Pico-RV1106/Getting-Started/overview)、[Luckfox SDK README](https://github.com/LuckfoxTECH/luckfox-pico/blob/main/README.md) 和 [Pico Zero BoardConfig](https://github.com/LuckfoxTECH/luckfox-pico/blob/main/project/cfg/BoardConfig_IPC/BoardConfig-EMMC-Buildroot-RV1106_Luckfox_Pico_Zero-IPC.mk)。

Buildroot 本身是生成嵌入式 toolchain、rootfs、内核和 bootloader 的构建系统，而不是传统通用发行版。它有意不提供设备端二进制包仓库，推荐以整张、已验证的 rootfs 镜像更新，参见 [Buildroot 官方手册](https://buildroot.org/downloads/manual/manual.html#_about_buildroot) 和 [Buildroot 关于二进制包的说明](https://buildroot.org/downloads/manual/manual.html#faq-no-binary-packages)。这与 Aiden 现有 A/B 整分区 OTA 模型一致；当前分区布局见 [Aiden 固件文档](./firmware.md#partition-reference)。

主要问题不是运行能力，而是维护周期：当前 rootfs 是 Buildroot 2023.02.6，厂商内核是 5.10.160。Buildroot 官方说明每年 `.02` 为 LTS，但维护期只持续到下一 LTS 的首个 bugfix；2023.02 已超出上游维护窗口，参见 [Buildroot 发布工程规则](https://buildroot.org/downloads/manual/manual.html#release-engineering)。Linux 官方当前把 5.10 LTS 维护期列到 2026 年 12 月，参见 [Linux kernel 长期支持版本](https://www.kernel.org/category/releases.html)。项目需要把 Buildroot 包 CVE backport、厂商 SDK 更新和后续内核迁移列入维护计划。

### Debian armhf：可做混合 rootfs 实验，不是直接安装

Debian 13 `armhf` 要求 ARMv7 + VFPv3，RV1106 的 Cortex-A7 满足 CPU 级要求；但 Debian 的支持说明没有 Luckfox Pico 或 RV1106，且官方强调未列出的 ARM 平台仍需目标内核和 DTB 支持，安装器也可能无法自动让系统可启动。参见 [Debian armhf 支持硬件说明](https://www.debian.org/releases/stable/armhf/ch02s01.en.html) 和 [Debian armhf 安装文件说明](https://www.debian.org/releases/stable/armhf/ch04s02.en.html)。

资源方面，Debian 无桌面安装的官方建议最低为 512MB RAM 和 4GB 磁盘；启用 swap、由有经验的用户裁剪时可低至约 140MB 安装，但官方不建议把这种低配当成常规配置，参见 [Debian 最低硬件建议](https://www.debian.org/releases/stable/armhf/ch03s04.en.html)。Aiden 的 eMMC 空间基本够用，但媒体 CMA 后约 146MB 的可见内存、已有 Node/Agent/硬件服务和 swap 抖动会让 apt、升级和峰值负载非常紧张。

若要验证 Debian，应保留 Luckfox loader、U-Boot、5.10 内核、DTB、模块和分区/OTA协议，只替换一个测试槽的 rootfs；不要先尝试纯 Debian 内核或官方安装器。

### Ubuntu：有第三方镜像和 armhf 用户态，但不等于官方板级支持

Luckfox RV1106 下载页列有 Ubuntu 22.04 eMMC/TF 卡第三方镜像，但官方推荐项仍是 Buildroot，参见 [Luckfox RV1106 系统镜像概览](https://wiki.luckfox.com/Luckfox-Pico-RV1106/Getting-Started/overview)。这说明 Ubuntu 用户态在厂商/社区启动链上可以被做出来，但不能推导出 Aiden 依赖的全部媒体和 USB 能力已经得到同等验证。

Canonical 的 Ubuntu Server for ARM 下载页面向认证的 64 位 ARM 服务器，而 RV1106 是 32 位 ARMv7，参见 [Ubuntu Server for ARM](https://ubuntu.com/download/server/arm)。Canonical 仍发布 Ubuntu Base 24.04 armhf tarball并保留 `armhf` 软件仓库，参见 [Ubuntu Base 24.04 发布目录](https://cdimage.ubuntu.com/ubuntu-base/releases/24.04/release/) 和 [Ubuntu 24.04 armhf archive](https://ports.ubuntu.com/ubuntu-ports/dists/noble/main/binary-armhf/Release)。所以自定义 rootfs 在用户态上可行，板级、glibc ABI 和资源问题则与 Debian 相同。

### OpenWrt：资源不是核心问题，官方 target 架构不匹配

OpenWrt 适合需要可写系统和设备端包管理的网络设备；但其当前官方 Rockchip target 只定义 `SUBTARGETS:=armv8`，设备列表覆盖 RK3308、RK3328、RK3399、RK35xx 等 64 位平台，没有 RV1103/RV1106 或 Luckfox Pico。参见 [OpenWrt Rockchip target](https://github.com/openwrt/openwrt/blob/main/target/linux/rockchip/Makefile) 和 [OpenWrt Rockchip ARMv8 设备列表](https://github.com/openwrt/openwrt/blob/main/target/linux/rockchip/image/armv8.mk)。

因此 OpenWrt 不是换 rootfs 即可完成：需要新增 32 位 ARMv7 target，接入厂商 U-Boot、5.10 内核、DTB、Wi-Fi、USB gadget 和媒体驱动，再解决 Aiden 的 uClibc ABI。除非 Aiden 的产品重点转为路由/网关功能，否则投入产出比低。

### Alpine armv7：通用用户态存在，RV1103 社区移植不代表 Pico Zero 支持

Alpine 官方下载页提供 `armv7` Standard、Mini root filesystem 和 Generic U-Boot 镜像，说明其轻量 musl/apk 用户态适用于 ARMv7，参见 [Alpine 官方下载页](https://alpinelinux.org/downloads/)。但 Alpine 官方资料没有 Luckfox Pico、RV1103 或 RV1106 板型；Generic U-Boot 镜像也不能替代 Luckfox 的 Rockchip loader、专用 U-Boot、DTB 和媒体内核。

网上可见的 RV1103 Alpine 启动方案应归类为社区 BSP/混合 rootfs 实验，不能外推为 RV1106G3 Pico Zero 的官方支持。即使保留 Luckfox 内核，Alpine 的 musl 与当前 Aiden/Rockchip 二进制所需的 uClibc 仍是不同 ABI，因此它只是在资源上比 Debian/Ubuntu 更轻，并没有消除驱动和动态库迁移成本。

### Yocto/OpenEmbedded：长期产品工程候选

Yocto 的目标是生成针对嵌入式产品裁剪的 Linux 系统，可只加入需要的组件，并能产出 RPM、DEB 或 IPK；代价是学习曲线和首次构建成本较高，参见 [Yocto 项目介绍](https://docs.yoctoproject.org/overview-manual/yp-intro.html)。

Yocto 官方托管的 `meta-rockchip` 层列出了多种 Rockchip machine，但当前没有 RV1103、RV1106 或 Luckfox Pico；其 README 也说明该层尽量使用上游源，并列出当前能构建/启动的板卡清单。参见 [`meta-rockchip` machine 目录](https://git.yoctoproject.org/meta-rockchip/tree/conf/machine?h=master) 和 [`meta-rockchip` README](https://git.yoctoproject.org/meta-rockchip/tree/README?h=master)。

采用 Yocto 意味着自建 `meta-luckfox`/`meta-aiden` BSP 层，封装厂商 U-Boot、5.10 内核、DTB、模块、媒体二进制、Aiden 服务、A/B OTA 和设备配置。运行时可以像 Buildroot 一样小，但首次移植和持续维护工作量显著更大。它适合有明确多年生命周期、供应链合规、SBOM 和多硬件平台复用需求的阶段，而不是当前的快速发行版替换。

### 主线 Linux：已出现 RV1103B 支持，但 RV1106/Pico Zero 仍缺位

Linux 主线源码当前包含 `rockchip,rv1103b` 的 SoC 描述和 Onion Omega4 EVB 设备树，参见 [主线 `rv1103b.dtsi`](https://github.com/torvalds/linux/blob/master/arch/arm/boot/dts/rockchip/rv1103b.dtsi) 和 [主线 Omega4 EVB DTS](https://github.com/torvalds/linux/blob/master/arch/arm/boot/dts/rockchip/rv1103b-omega4-evb.dts)。这表明 RV1103B 上游化已经开始，但主线 Rockchip ARM DTS 目录仍没有 RV1106 或 Luckfox Pico Zero 板级 DTS，参见 [Linux 主线 Rockchip ARM DTS 目录](https://github.com/torvalds/linux/tree/master/arch/arm/boot/dts/rockchip)。

主线 U-Boot 源码同样没有 RV1103/RV1106 板级实现，而 Luckfox SDK 有专用 `luckfox_rv1106_uboot_defconfig` 和 `rv1106g-luckfox-pico-zero.dts`。参见 [U-Boot 主线 ARM DTS](https://source.denx.de/u-boot/u-boot/-/tree/master/arch/arm/dts) 和 [Luckfox Pico Zero BoardConfig](https://github.com/LuckfoxTECH/luckfox-pico/blob/main/project/cfg/BoardConfig_IPC/BoardConfig-EMMC-Buildroot-RV1106_Luckfox_Pico_Zero-IPC.mk)。在相机/ISP/NPU、音频和 USB gadget 路径完成上游化前，切换任意“主线发行版”都会丢失 Aiden 的核心硬件能力。

## 建议路线

### 近期：继续 Buildroot，并改善维护方式

- 保留 Luckfox loader、U-Boot、5.10 内核、DTB、媒体库和 uClibc ABI。
- 新工具优先通过 Buildroot defconfig、overlay 或静态单文件程序进入镜像，不在设备端引入通用在线包管理。
- 为 Buildroot 2023.02.6 和厂商 5.10 内核建立 CVE/补丁清单；评估升级到更新 Luckfox SDK 时，不把 Buildroot、内核和媒体库拆开升级。
- 继续使用 A/B 整分区 OTA，避免产生大量未经组合验证的包版本状态。
- 做内存预算时以真机 `MemTotal`/`MemAvailable` 为基准，并在 HDMI capture、音频、Node/WeTTY、Agent 同时运行的峰值场景验证。

### 中期：做一个受控 Debian armhf PoC

仅当 apt、标准 glibc 软件生态或现场调试便利性有明确业务价值时，才建议做 PoC：

1. 在独立测试分区/设备上保留现有 bootloader、boot image、内核、DTB 和模块。
2. 用最小 Debian armhf rootfs，不安装桌面或完整 systemd 服务集合。
3. 先验证 uClibc/glibc 双运行时或重新构建 Rockchip 媒体依赖，再移植 Aiden 服务。
4. 测试 HDMI→CSI capture、音频录放、RKNN、Wi-Fi、HID/ECM、A/B 健康确认和 24 小时内存压力。
5. 若常态 swap、媒体分配失败或 OTA 镜像明显膨胀，则停止该路线。

### 长期：满足产品化条件后再启动 Yocto BSP

当项目需要多个板型共享基础层、长期 CVE 策略、SBOM/许可证清单、可复现供应链和标准化 BSP 测试时，再立项 Yocto。首个里程碑不是“启动 shell”，而是完整复现当前 Buildroot 的硬件能力和 A/B OTA 验收矩阵。

## 型号识别注意事项

Luckfox 当前官网的 Pico Zero 是 RV1106G3、256MB、8GB eMMC；旧资料中的 `Luckfox Pico` 常指 RV1103 和更小内存型号。发行版判断不能只看商品名，应以以下真机信息为准：

```sh
cat /proc/device-tree/model
tr '\0' '\n' </proc/device-tree/compatible
uname -a
cat /proc/meminfo
cat /etc/os-release
```

本报告已用这些信息确认当前 Aiden 板为 Pico Zero / RV1106G3 兼容平台。若未来硬件实际换成 RV1103 或 64MB 型号，Debian、Ubuntu 和 Yocto 用户态的可行性会进一步下降。
