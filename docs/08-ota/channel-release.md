# 三通道发布

正式发布共用 **Aiden Channel Release**（`.github/workflows/release.yml`）流程，
可直接手动触发，也可通过 **Debian Build (backup / self-hosted-02)** 入口调用。
`dev`、`staging`、`prod` 全部手动触发，不随提交或定时任务自动发布。
原主构建、fallback 和独立业务包工作流只保留构建产物功能。

## 发布决策

每个通道以自己的上次成功发布为比较基线，比较 Git 树中的文件内容、路径、模式和
SDK gitlink 提交。草稿、失败构建和 workflow artifact 不推进基线。

| 对比结果 | 构建和发布 | 基础契约 |
| --- | --- | --- |
| 本通道首次发布 | 完整 OTA、刷机镜像及配套 `.deb` | 分配新的契约 |
| 只有业务或管理范围内配置变动（包括新增文件） | `.deb` | 继承本通道最近 OTA 的契约和底座标识 |
| 系统变动或业务与系统混合变动 | 完整 OTA、刷机镜像及配套 `.deb` | 分配新的契约 |
| 只有文档、测试或没有变动 | 不构建、不发布 | 不变 |
| 勾选 `force_ota` | 完整 OTA，即使源码未变 | 分配新的契约 |

先按 `scripts/debian-system/config-package.json` 将管理范围内的 overlay 分类为
`config`（指纹计入 business），再按 `scripts/release/policy.json` 的 ignore、system、business
顺序匹配。首次配置接管强制新 OTA；此后范围内新增文件无需扩大契约。详见
[业务包管理运行配置](system-config-package.md)。
`src/` 和 `assets/business/` 通常属于业务；OTA 实现、Go 依赖声明、CMake 文件、
工厂 Agent 配置属于系统。范围外的 overlay、SDK、内核、分区、基础依赖、构建与发布脚本等
未列入业务范围的文件默认属于系统。业务技能的 `SKILL.md` 属于业务资源。
删除和重命名也参与比较。源码必须包含本通道前一次发布提交，拒绝倒退或分叉发布。

外部 Debian 仓库、签名密钥、Actions secret 和浮动下载内容不在 Git 比较范围内。
这些输入更新时使用 `force_ota`；无法通过源码差异自动识别它们。

## 版本与契约

发布标签为 `dev-v0.0.2`、`staging-v0.0.3`、`prod-v0.0.4` 等。
三个通道共享版本分配序列，默认在所有已发布版本的最大值上增加 patch，首个托管
发布为 `0.0.2`，高于已有本地包 `0.0.1-2`。可手动填写更高的 `MAJOR.MINOR.PATCH`。
正式包版本为 `${version}-1`，不再靠修改 `version.sh` 分配正式版本。

契约也全局递增：首个托管 OTA 为 `1.0.0`，之后每个 OTA 分别分配 `2.0.0`、
`3.0.0` 等。通道之间不会出现同号却不同底座的契约。每个通道首次发布都会构建
自己的底座，所以发布相同提交到另一个新通道也会产生一个 OTA 和新契约。

例如 dev 的 OTA 契约为 `4.0.0`，之后两个业务包仍依赖 `[4.0.0, 5.0.0)`，并绑定
相同 `base_release`、通道和系统源码指纹。dev 再发系统 OTA 后，后续 dev 业务包
使用新契约；staging/prod 仍按各自已发布的底座计算。

`scripts/release/contract.py` 根据发布计划同时生成：

- rootfs 的 `/usr/lib/aiden/platform/contract.json`。
- `.deb` 内的 `release-manifest.json` 和安装前检查。
- 发布附件 `platform-contract.json`。

安装托管业务包时，`preinst` 在解包前核对平台、契约、通道、底座标签和源码指纹；
不匹配则退出，要求先安装匹配的 OTA。dpkg 原有服务停止、恢复及失败回调逻辑保留。
初始 rootfs 安装 `.deb` 前先放入契约，最终镜像审计再次核对契约。
契约是兼容性标识，不代替签名；`.deb` 包含管理范围内的配置和 systemd unit，
不内含平台库或分区镜像。平台契约、业务 manifest 和发布记录以 `runtime_config: 1`
声明配置归属；旧记录缺少该字段按 0 处理。

## Actions 操作

工作流合入 GitHub 默认分支后：

1. 进入 **Aiden Channel Release**，选 channel 和 source_ref（默认 main，可填提交 SHA）。
2. 保持 `plan_only=true`，查看 Summary 和 `release-plan` artifact 中的计划、契约和变更列表。
3. 正式构建设 `plan_only=false`；`publish=false` 只生成可下载的验证产物。
4. 正式发布再设 `publish=true`。发布任务经过对应 channel 的 GitHub Environment。

备用入口 **Debian Build (backup / self-hosted-02)**（`build-backup.yml`）提供同样的
`channel`、`source_ref`、`version`、`force_ota`、`plan_only`、`publish` 参数，
OTA 固定在 `aiden-hosted-02` 构建；业务包仍在 GitHub 托管 Ubuntu 构建。
默认 `plan_only=true`、`publish=false`。要发布，设置 `dry_run=false`、
`plan_only=false`、`publish=true`。`dry_run=true` 优先，仅检查备用机环境、
签名密钥和工具链，忽略其余发布选项，不生成计划、不构建、不发布。

两个入口复用相同的变更分类、契约分配、通道 Environment、草稿上传和下载校验，
并由被调用的发布工作流持有同一个全局发布锁。备用入口自身的 `backup-build`
并发组只负责串行化备用入口，不能改成相同的发布锁名称，否则嵌套调用会互相等待。

需要仓库 secret `OTA_ED25519_PRIVATE_KEY`（Ed25519 PEM，仅 OTA 构建使用），可选
`AGENT_CONFIG_TOML`。业务包构建不需要这两个 secret。发布使用 workflow 自带的
`GITHUB_TOKEN`，发布流程中只有发布 job 使用 `contents: write`。备用入口的调用 job
也需声明该权限上限，才能传给被调用的发布 job；规划和构建 job 仍为只读。
仓库的 Actions 权限必须允许该权限。建议为 `staging`、`prod` Environment 配置审核人和允许发布的分支。

固件可选择 `aiden-hosted-01`、`aiden-hosted-02` 或 `ubuntu-24.04`；业务包在 GitHub
托管 Ubuntu 构建。固件复用 `build.yml` 的 SDK 清理、工具链、缓存、签名和最终审计。
传入的 source_ref 只解析一次，后续步骤固定到该 SHA；该提交须包含本发布工具。

全通道共享一个 workflow concurrency group，并在发布前重新核对发布历史。
GitHub concurrency 不是无限队列：同组只保留一个运行和一个等待任务，后来的等待
任务可能替换前一个。不要并行启动多个发布；已运行的发布不会被新任务取消。

## 本地脚本

规划仅需 Git、Python 3.11+、已认证 GitHub CLI。编译在 Linux amd64（如 luhaodev）
上执行，需要 Docker 和现有固件构建环境；macOS 可查看计划和产物。

```bash
python3 scripts/release/release.py plan \
  --repo AidenAI-IO/aiden-firmware --channel dev

# 干净工作区、HEAD 与 plan 中的 source_commit 一致。
# OTA 构建还需配置已有 debian_build.sh 的签名/信任公钥变量。
python3 scripts/release/release.py build --plan output/release/plan.json
python3 scripts/release/release.py verify output/release/assets

# 仅在明确准备对外发布时执行。
python3 scripts/release/release.py publish output/release/assets
```

`plan --history history.json` 使用离线发布记录数组进行预览/测试。空数组表示没有
托管发布，不表示读取了线上历史。发布器始终重新读取 GitHub，不接受离线历史作为
发布依据。`--force-ota` 强制刷新底座；`--version` 指定高于全局已发布版本的版本号。

发布目录包含 `.deb`、业务 manifest、平台契约、自动变更说明、`release.json` 和
`SHA256SUMS`。OTA 还包含签名 manifest、boot A/B、rootfs、刷机镜像压缩包、刷机镜像
校验文件和 OTA 验签公钥。userdata 不作为单独 OTA 分区发布。

## 发布完整性与失败重试

`release.json` 记录源码 SHA、Git 树、通道、版本、前次发布、平台基线、系统/业务
指纹、变更列表和每个附件的大小/哈希。已发布的托管标签必须有有效记录，读取失败
会中止规划，不会误当作首次发布。旧 `debian-*`/`business-v*` 标签不作为新流程基线。
不要删除或修改已发布的托管标签、附件或记录；它们是后续版本与契约分配的依据。

发布器核对标签指向、创建草稿、上传、完整重新下载并校验后才公开。OTA 还验证
Ed25519 签名、镜像哈希和固定到标签的下载 URL。发布过程中历史变化会中止公开。
已公开版本拒绝覆盖。上传失败后保留原始 `channel-release-assets` artifact，使用
同一批文件重新运行 `publish`；草稿已有不同构建内容时不会覆盖。重新构建会改变
构建时间和签名，因此不能替代原始附件来续传同一草稿。

dev/staging 是 prerelease。只有 prod 的 OTA 可以更新 GitHub Latest；prod 业务包
不会抢占 Latest。新 OTA 客户端读取设备配置的 `repo`、`channel`，分页寻找该通道
版本最高的 OTA，跳过业务包，并验证已签名 manifest 的通道。空 channel 和旧配置
继续使用旧 Latest 入口；旧设备需要先强刷本通道的新基础镜像，才能获得通道选择。

正式发布后自动刷新 GitHub Pages 上的签名 APT 源。设备运行 `apt update && apt upgrade`
即可升级本通道、当前基础契约内的业务包；系统更新继续走 A/B OTA。首次配置、签名
密钥、系统包 pin 和索引维护见 [GitHub APT 软件源](apt-repository.md)。
