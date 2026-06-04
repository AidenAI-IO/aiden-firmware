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

## 向后兼容

所有改动都是**可选和增量**的：
- 现有不带 URL 的 manifest 继续正常工作
- 现有的 GitHub Release 流程不受影响
- 默认行为保持不变

## 使用场景

### 场景 1：开发者 Fork 仓库

```bash
# 开发者在自己的 GitHub 仓库发布定制固件
ota check-now --repo developer/aiden-custom \
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

### 测试
- `src/agent/internal/ota/manifest_test.go` - 新增 URL 字段测试
- 所有现有测试通过 ✅

### 文档
- `docs/ota-external-developers.md` - 完整开发者指南
- `docs/ota-quick-examples.md` - 快速使用示例

## 安全性

**不变的安全保障**：
1. 签名验证仍然是强制的
2. 用户必须显式信任外部公钥
3. 版本降级保护依然有效
4. 所有现有安全检查保持不变

**新增的灵活性**：
- 允许 HTTP（用于本地测试，会记录警告）
- 推荐生产环境使用 HTTPS

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

- 详细指南：[docs/ota-external-developers.md](docs/ota-external-developers.md)
- 快速示例：[docs/ota-quick-examples.md](docs/ota-quick-examples.md)
- 实现计划：[.plan/open-ota-sources.md](.plan/open-ota-sources.md)
