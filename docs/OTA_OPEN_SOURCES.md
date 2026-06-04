# OTA 开放性改进

本次改进让 OTA 系统更加开放，外部开发者可以从自己的仓库或后端分发固件。

## 主要改进

### 1. Manifest 支持直接 URL

`ManifestAsset` 新增可选的 `url` 字段：

```json
{
  "name": "boot_a.img",
  "url": "https://firmware.example.com/v1.0.0/boot_a.img",
  "size": 12345678,
  "sha256": "abc..."
}
```

**优势**：
- 不再强制要求 GitHub Release API 格式
- 可以使用任何 Web 服务器（Nginx、Apache、S3、CDN 等）
- 更适合内网部署和私有分发

### 2. 新增 --manifest-url 参数

直接指定 manifest URL，跳过 Release API：

```bash
ota check-now --manifest-url https://example.com/firmware/manifest.json \
  --public-key /path/to/pubkey.pem
```

### 3. Manifest 生成脚本支持 --base-url

```bash
scripts/generate_ota_manifest.sh \
  --version v1.0.0 \
  --channel stable \
  --build-time 2026-06-04T12:00:00Z \
  --sign-key private_key.pem \
  --image-dir output/image \
  --output manifest.json \
  --base-url https://firmware.example.com/v1.0.0  # 新增
```

自动在每个 asset 中注入完整下载 URL。

### 4. 官方仓库区分 main 和非 main 分支

官方仓库的 CI/CD 现在根据分支自动区分发布渠道：

| 分支 | Channel | GitHub Release | 默认 OTA 行为 |
|------|---------|---------------|--------------|
| `main` | `stable` | 正常 Release | ✅ 自动升级 |
| 其他分支 | `dev-{分支名}` | Prerelease | ❌ 不会升级 |

**双重保护机制**：

1. **Channel 校验**：设备默认 `channel: stable`，OTA 会拒绝 channel 不匹配的 manifest
2. **Prerelease 标记**：`releases/latest` API 只返回正式 Release，不返回 prerelease

这样非 main 分支的固件即使发布也不会影响生产设备的正常 OTA 升级。

**测试非 main 分支固件**：

```bash
# 通过 manifest-url 手动指定 dev 分支的 release（标记为 Pre-release）
ota check-now \
  --manifest-url "https://github.com/AidenAI-IO/aiden-hardware-demo/releases/download/TAG/manifest.json" \
  --public-key /oem/etc/ota_pubkey.pem
```

详见 [ota-release-channels.md](ota-release-channels.md)。

## 向后兼容

所有改动都是**可选和增量**的：
- 现有不带 URL 的 manifest 继续正常工作
- 现有的 GitHub Release 流程不受影响
- 默认行为保持不变

## 使用场景

### 场景 1：开发者 Fork 仓库

```bash
# 开发者在自己的 GitHub 仓库构建并发布固件
# 生成带 GitHub 直接 URL 的 manifest
TAG="v1.0.0-custom"
REPO="developer/aiden-custom"
scripts/generate_ota_manifest.sh ... \
  --base-url "https://github.com/$REPO/releases/download/$TAG"

# 设备从开发者的 release 更新
ota check-now \
  --manifest-url "https://github.com/$REPO/releases/download/$TAG/manifest.json" \
  --public-key /path/to/developer_pubkey.pem
```

### 场景 2：自建服务器分发

```bash
# 生成带 URL 的 manifest
scripts/generate_ota_manifest.sh ... \
  --base-url https://firmware.mycompany.com/aiden/v1.0.0

# 上传到自己的服务器
rsync -avz output/image/*.img manifest.json user@server:/var/www/firmware/

# 设备直接从服务器更新
ota check-now --manifest-url https://firmware.mycompany.com/aiden/v1.0.0/manifest.json \
  --public-key /userdata/ota/company_pubkey.pem
```

### 场景 3：本地开发测试

```bash
# 生成本地测试 manifest
scripts/generate_ota_manifest.sh ... \
  --base-url http://192.168.1.100:8000

# 启动本地服务器
cd output/image && python3 -m http.server 8000

# 测试（不实际刷写）
ota check-now --manifest-url http://192.168.1.100:8000/manifest.json \
  --public-key /path/to/dev_pubkey.pem --dry-run
```

## 文件改动

### 代码改动
- `src/agent/internal/ota/manifest.go` - 添加 URL 字段和验证
- `src/agent/internal/ota/updater.go` - 支持直接 URL 和 manifest-url 参数
- `src/agent/cmd/ota/main.go` - 添加 --manifest-url CLI 参数
- `scripts/generate_ota_manifest.sh` - 添加 --base-url 选项
- `scripts/create_github_release.sh` - 添加 --prerelease 选项
- `.github/workflows/build.yml` - 区分 main 和非 main 分支的 channel 与发布策略

### 测试
- `src/agent/internal/ota/manifest_test.go` - 新增 URL 字段测试
- 所有现有测试通过 ✅

### 文档
- `docs/ota-external-developers.md` - 完整开发者指南
- `docs/ota-quick-examples.md` - 快速使用示例
- `docs/ota-release-channels.md` - 发布渠道与分支区分说明

## 安全性

**不变的安全保障**：
1. 签名验证仍然是强制的
2. 用户必须显式信任外部公钥
3. 版本降级保护依然有效
4. 所有现有安全检查保持不变

**新增的灵活性**：
- 允许 HTTP（仅测试环境建议，生产环境必须使用 HTTPS）
- URL 验证确保使用 HTTP 或 HTTPS 协议

## 测试验证

```bash
# 编译测试
cd src/agent && go build ./cmd/ota

# 运行测试
go test ./internal/ota -v

# 所有测试通过 ✅
```

## 下一步

建议的增强（未实现，可选）：
1. 更新源配置管理（支持配置多个源并切换）
2. 图形化配置界面
3. 更新源的发现和订阅机制

## 参考文档

- 详细指南：[ota-external-developers.md](ota-external-developers.md)
- 快速示例：[ota-quick-examples.md](ota-quick-examples.md)
- 实现计划：[../.plan/open-ota-sources.md](../.plan/open-ota-sources.md)
