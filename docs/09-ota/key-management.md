# OTA 密钥管理

生产 OTA manifest 使用 Ed25519 签名。设备通过 `/oem/etc/ota_pubkey.pem` 验证 `manifest.json`，只有签名和镜像 hash 都通过时才会写 inactive slot。

## 生成密钥

离线生成生产 Ed25519 私钥和公钥：

```bash
openssl genpkey -algorithm ed25519 -out ota_ed25519_private_key.pem
openssl pkey -in ota_ed25519_private_key.pem -pubout -out ota_pubkey.pem
```

私钥 `ota_ed25519_private_key.pem` 不得提交到仓库。把它视为 release signing infrastructure。

验证公钥格式：

```bash
scripts/validate_ota_pubkey.sh ota_pubkey.pem
```

脚本只接受 Ed25519 public key。RSA、ECDSA、格式错误或缺失文件都会被拒绝。

## 公钥部署

生产镜像构建必须提供生产公钥。`_build_image.sh` 支持两个来源：

```bash
OTA_PUBLIC_KEY_PATH=/path/to/ota_pubkey.pem ./_build_image.sh
```

或提交 `keys/ota_pubkey.pem`，前提是文件没有 dev/test/placeholder 标记。

构建脚本会把公钥复制到 overlay，并最终打包到：

```text
/oem/etc/ota_pubkey.pem
```

## GitHub Secret

CI release workflow 使用名为 `OTA_ED25519_PRIVATE_KEY` 的 GitHub secret，内容是完整 PEM 私钥：

```text
-----BEGIN PRIVATE KEY-----
...
-----END PRIVATE KEY-----
```

workflow 使用该 secret：

- derive 对应 public key，供 image build 打包到 `/oem/etc/ota_pubkey.pem`。
- 在 A/B images 构建完成后签名 `pico-sdk/output/image/manifest.json`。

缺少该 secret 时 release workflow 应失败。

## 本地签名

本地生成 manifest 示例：

```bash
scripts/generate_ota_manifest.sh \
  --version 20260521-120000-abcdef0 \
  --channel stable \
  --build-time 2026-05-21T12:00:00Z \
  --sign-key ota_ed25519_private_key.pem \
  --image-dir pico-sdk/output/image \
  --output pico-sdk/output/image/manifest.json
```

脚本要求存在 `boot_a.img`、`boot_b.img`，以及 slot-specific 或 slot-neutral 的 `oem` 和 `rootfs` images。CI 上传 `pico-sdk/output/image/*`，包括 `manifest.json` 和 USB 首刷用 `update.img`。

## 密钥轮换

V1 设备信任 `/oem/etc/ota_pubkey.pem`。不要在 fleet 接受新公钥前直接切换 GitHub `OTA_ED25519_PRIVATE_KEY`，否则旧设备会拒绝新私钥签名的 manifest。

安全轮换流程：

1. 离线生成新 Ed25519 key pair。
2. GitHub `OTA_ED25519_PRIVATE_KEY` 暂时保持旧私钥。
3. 构建 transition OTA，内容包含新的 `/oem/etc/ota_pubkey.pem` 或同时信任新旧公钥的 keyring。
4. 用旧私钥签名并发布 transition release。
5. 通过 `ota status`、release telemetry 或现场检查确认目标设备已 boot 并 mark successful。
6. 确认 fleet 已信任新公钥后，把 GitHub secret 切换为新私钥。
7. 后续 release 使用新私钥签名。

USB 或工厂换钥是另一条路径：

1. 构建包含新 public key 的完整镜像。
2. 通过 USB recovery 或工厂流程刷机。
3. 设备确认使用新 public key 后，再为对应 channel 切换 GitHub signing secret。

## 私钥泄露处理

如果 OTA 私钥可能泄露：

1. 删除或禁用包含泄露 key 的 GitHub Actions secrets。
2. 删除或隔离可能由泄露 key 签名的不可信 release 和 assets。
3. 离线生成新的 Ed25519 key pair。
4. 构建包含新 public key 的 recovery image。
5. 优先使用 USB 或受控物理 recovery 处理无法安全信任 OTA 的设备。
6. 如果必须 OTA，发布经过人工审计的 transition release，并用 `ota status` 和 `abctl read` 监控。
7. 如果仓库访问 token 也可能泄露，单独轮换 `/userdata/ota/gh_token`。该 token 只用于私有仓库 Release 下载，不参与 manifest 签名；public Release 不需要配置 token。更多运行时路径见 [architecture.md](architecture.md#私有仓库-token)。
