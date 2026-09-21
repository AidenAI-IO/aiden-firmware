# GitHub APT 软件源

业务源地址：`https://aidenai-io.github.io/aiden-firmware/apt`。
GitHub Pages 托管签名的 `InRelease`、`Release.gpg`、包索引和 `.deb`；包只取自已经
正式发布的三通道 Release，Actions 中未发布的构建产物不会进入源。

## 板子升级

包含此功能的新基础镜像会从 `/usr/lib/aiden/platform/contract.json` 自动选择源。
例如 dev、契约 `1.0.0` 使用 `dev-c1`；staging/prod 各用自己的契约源。

```bash
sudo apt update && sudo apt upgrade
dpkg-query -W aiden-business aiden-system-config
sudo /usr/lib/aiden/ota --config /userdata/debian/ota/config.json self-check
```

APT/dpkg 自动停止并恢复原先运行的业务服务。用户配置和 userdata 保留，无需重启。
新契约的软件包不会出现在旧契约的候选版本中；先安装新 OTA 后，设备才会切换到
新基础契约的软件源。包的 preinst 仍会校验通道、契约、底座标签和系统指纹。

`/etc/apt/preferences.d/aiden-business` 将匹配源中的业务包和配置包优先级设为 990，Debian
源的包设为 1，低于已安装包的 100。因此普通 `apt upgrade` 升级这两个配套包，Debian
系统包随 OTA 更新，避免基础契约未变而系统库已经改变。显式安装此前未安装的
Debian 工具仍然可用。自行添加第三方系统源时需另行设置其 pin。维护人员有意改变系统库时，可显式选择版本或调整 pin，随后
应通过新的 OTA 建立基础契约。

已安装相同版本时 `apt upgrade` 不会重装。同版本构建包需要换成正式发布包时：

```bash
sudo apt install --reinstall aiden-business aiden-system-config
```

## 现有托管设备首次接入

从经过审核的源码检出复制公钥和配置工具。不要从未验证的网络响应直接执行脚本。

```bash
scp overlay-debian/usr/share/keyrings/aiden-archive-keyring.asc \
    overlay-debian/usr/lib/aiden/aiden-apt-source luckfox:/tmp/
ssh -t luckfox 'sudo python3 /tmp/aiden-apt-source --public-key /tmp/aiden-archive-keyring.asc'
ssh -t luckfox 'sudo apt update && sudo apt upgrade'
```

公钥指纹：`D40CD0F29C3A65449439857F53CFFB430DBD167D`。
仅信任该源的 `Signed-By` 公钥，不使用 `apt-key` 或 `trusted=yes`。
工具拒绝无通道或无完整契约的旧镜像；这些设备需先强刷本通道的托管基础镜像。
源配置、公钥与系统包 pin 属于平台，随 OTA 提供，不由业务包修改。

## 发布与维护

首次设置：

1. GitHub 仓库 Settings → Pages → Source 选择 **GitHub Actions**。
2. 仓库 Actions secret `APT_SIGNING_PRIVATE_KEY` 存入与已提交公钥对应的 ASCII-armored
   OpenPGP 签名私钥。使用独立的 APT 密钥，不能复用 OTA Ed25519 PEM。
3. 推送并合入此流程，手动执行 **Aiden APT Repository**，用已发布版本建立索引。
   合入前也可以在 **Debian Build (backup / self-hosted-02)** 选择功能分支，勾选
   `apt_only` 并保持 `dry_run=false`，只刷新 APT 索引，忽略构建和新版本发布参数。

主发布和 backup 发布都在 GitHub Release 成功公开后调用该流程。它不会重新编译
业务包，也不会分配版本或改变发布比较基线。正式发布前会检查签名 secret 是否存在。
APT 刷新失败不会撤销已公开的 Release，修复后单独重跑 APT 流程即可。

索引不设置 `Valid-Until`，无需定时刷新；仅在正式发布后或手动触发时更新。
APT 仍会验证索引签名和包校验和，三个通道的新版本仍全部手动发布。
Pages 的 `github-pages` Environment 必须允许运行发布流程的分支。

每个“通道 + 契约”保留最新三个正式发布的完整配套包，旧契约的源继续可用。索引使用 APT
by-hash 下载，包路径按契约和发布标签固定；过旧缓存遇到已清理的包时重新执行
`apt update`。站点超过 900 MiB 会停止部署，保留旧站点；届时应将不再支持的
契约归档到其他静态存储并调整生成策略，不能直接删除仍有设备使用的契约源。

源码记录、SHA256、包架构/版本与嵌入契约均通过验证后才签名并原子部署整个站点。
私钥仅进入签名步骤的临时 GnuPG 目录，清理后只上传公开站点内容。

双包机制首次启用须先升级到新的 OTA 基座；旧契约源继续保留单业务包，不会混入新机制。
依赖关系、降级和失败恢复见 [系统配置包](system-config-package.md)。

Linux 上本地验证：

```bash
python3 scripts/test_apt_repository.py
GNUPGHOME=/path/to/private-gnupg python3 scripts/apt/repository.py \
  --repo AidenAI-IO/aiden-firmware --output output/apt/site
```

测试运行真实的 `apt update` 和模拟 `apt upgrade`，覆盖契约隔离、系统包 pin、
错误公钥、篡改索引、无到期限制的签名元数据和发布包校验失败。签名私钥应另存安全备份；轮换
公钥时先通过 OTA 向设备分发新旧公钥的过渡信任，再切换仓库签名。
