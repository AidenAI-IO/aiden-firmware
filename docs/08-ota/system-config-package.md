# 系统配置包与业务包

`aiden-system-config` 管理经过审核、可在当前基础契约内热更新的系统集成文件。
`aiden-business` 管理程序和资源。二者使用同一发布版本，APT 一起升级，内核、驱动、
分区、系统库、启动底座和平台契约继续通过完整 A/B OTA 更新。

## 文件归属

唯一配置文件清单是 `scripts/debian-system/config-package.json`。首批包含：

| 文件 | 权限 | 用途 |
| --- | --- | --- |
| `/etc/profile.d/aiden-env.sh` | 0644 | 登录终端的受管理环境 |
| `/etc/sudoers.d/20-aiden-proxy` | 0440 | sudo 保留代理变量 |
| `/usr/lib/aiden/aiden-managed-env-run` | 0755 | 业务服务的环境加载 |

文件所有者均为 root:root；`/etc` 下文件登记为 dpkg conffiles，保留管理员修改并遵循
dpkg 的冲突处理，不强制覆盖。`/userdata`、密钥、`/run` 生成文件和平台契约不在包内。
配置包是 `Architecture: all`，但其安装脚本和 manifest 仍检查 Luckfox armhf 平台绑定。

清单同时驱动打包、发布分类、rootfs overlay 排除和镜像审计。rootfs 先安装两个包，
再复制其余 overlay；配置包文件不能由 rsync 再覆盖。源码仍保留在 `overlay-debian`。
不会把整个 overlay 自动划入配置包，也不会按目录通配接管未来新增文件。

增加或删除清单条目属于文件所有权及平台边界变化，当前要求通过新 OTA 生效。此后
条目内容的兼容修改可以通过 APT 发布。修改如果依赖新系统库、驱动或改变底座要求，
维护人员必须选择 `force_ota`；文件分类无法推断所有语义上的兼容性变化。

## 两个包的关系

同一发布例如 `dev-v0.0.8` 会同时产生：

```text
aiden-business_0.0.8-1_armhf.deb
aiden-system-config_0.0.8-1_all.deb
```

业务包 `Depends: aiden-system-config (= 0.0.8-1)`；配置包使用 `Breaks` 拒绝更老或
更新的业务版本。没有循环 Depends。APT 负责解包排序、必要的暂时反配置和配置排序；
不能仅把配置升级到新版本而保留不匹配的业务包。两个包的版本独立冻结/hold 会阻止
配套升级。降级需要同时指定两个版本。

每个包都嵌入平台契约、通道、底座标签、系统指纹及另一包的精确版本。在解包前校验
平台，失败不会覆盖文件。两包的 manifest 必须匹配；发布附件和 APT 索引生成器均
检查此约束及真实 Debian Depends/Breaks，不能发布或索引只有一半的配套版本。

即使只有业务代码或只有配置内容变化，也构建、发布同版本的两个包。第一版优先保证
依赖和回滚简单可验证，不复用其他发布的业务包，也不分配独立配置版本。

## 安装与失败恢复

两个包共用 `/var/lib/aiden-business/service-transition`。第一次进入维护脚本时保存
正在运行的服务，先停代理变更监听器及任务，再停业务；后续 hook 保留最初的快照。
只有配置完成且另一个包已经以相同版本配置成功，才 daemon-reload 并恢复原先运行的
服务，最后恢复监听器。配置包还会运行 `visudo -c` 校验实际保留下来的 sudo 配置。
新旧包混合或只解包一个时保持停服，恢复记录不删除。

APT 提供依赖约束，但两包升级不是文件系统原子事务。断电、磁盘错误或配置失败后：

```bash
sudo apt --fix-broken install
sudo dpkg --configure -a
sudo /usr/lib/aiden/ota --config /userdata/debian/ota/config.json self-check
```

修复前不要删除服务快照；这些命令不会自动回退到旧版本。若需要回退，选择当前
契约源仍保留的同一版本，例如：

```bash
sudo apt install --allow-downgrades \
  aiden-business=0.0.8-1 aiden-system-config=0.0.8-1
```

正常升级保持：

```bash
sudo apt update && sudo apt upgrade
dpkg-query -W aiden-business aiden-system-config
```

离线安装须同时提供两个 `.deb`。只显式移除配置包时，APT 可能同时移除依赖它的业务包；
不要将其作为独立可卸载组件。两个包只修改活动 rootfs，不同步 B 槽，回切旧 OTA 槽会
回到该槽自身的包版本和配置。用户数据保留，原先停止的服务不会主动启动。

## 发布流程与首次上线

三个通道仍全部手动发布；`release.yml` 和 `build-backup.yml` 共用决策与发布实现。

| 比较本通道上次发布的结果 | 构建内容 | 基础契约 |
| --- | --- | --- |
| 业务或清单内配置内容变化 | 两个配套 `.deb` | 继承 |
| 清单边界、未列入清单的 overlay、内核等平台变化 | OTA 镜像和两个 `.deb` | 递增 |
| 文档/测试变化或无变化 | 不发布 | 不变 |
| 首次启用双包机制或 `force_ota` | OTA 镜像和两个 `.deb` | 递增 |

发布记录保留 `format: 1`，新增 `package_set: 2`；缺少该字段的历史记录按单包处理。
历史 Release 不改写，旧契约 APT 源继续提供原来的业务包。首次双包机制必须发一次
新 OTA，建立文件归属、共享维护脚本和双包 APT pin；不能把新双包版本直接塞入旧契约。
这次提交不手工修改契约数字，由手动发布时的 planner 分配新契约。

先使用 `plan_only=true` 检查比较基线、`config` 变更列表和契约；构建验证后才手动选择
发布。Release 包含两包、各自 manifest、平台契约和校验和，发布成功后刷新 GitHub
Pages APT 源。每个通道/契约保留最新三个发布的完整配套包，索引没有 `Valid-Until`。

构建入口不变：`scripts/debian-package/release.sh build` 产生配套包；`debian_build.sh`
把两包一起安装到新 rootfs。独立配置打包可在 Linux 上使用：

```bash
AIDEN_BUSINESS_VERSION=0.0.8 AIDEN_BUSINESS_REVISION=1 \
  python3 scripts/debian-package/system_config.py build output/system-config
```

正式发布仍须使用发布计划提供完整环境绑定并通过 staging/verify；上述开发打包不构成
发布。Linux 集成检查入口为 `bash scripts/test_system_config_package.sh`，它在可丢弃
容器内以 root 执行真实 dpkg 升降级及恢复测试，不安装到构建机系统。
