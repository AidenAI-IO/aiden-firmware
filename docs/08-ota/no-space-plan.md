# OTA eMMC 空间不足处理方案

## Background

`/userdata`（3GB，ext4）是共享分区，OTA 下载缓存与 swap、audio、日志争用空间。
实测报错：

```
write /userdata/ota/downloads/oem.img.tar.gz.part: no space left on device
```

### Root causes

1. **下载缓存从不清理**：升级成功后旧包（如 `oem.img.tar.gz`，~26M）永久残留在
   `/userdata/ota/downloads/`。`internal/ota/` 内无任何删除下载缓存的逻辑。
2. **文件名固定、不带版本号**：下新版本时旧包仍占位，需等新包下完 `os.Rename`
   才被覆盖，瞬时峰值需求翻倍（~52M）。
3. **下载前无空间预检**：`.img.tar.gz` 连分区大小校验都被 `validateAssetFitsPartition`
   跳过（`updater.go:796`）；全流程从不调用 statfs。
4. **ENOSPC 后 `.part` 残留 + 续传死循环**：下载失败刻意保留 `.part` 供续传
   （`download.go` 无 `os.Remove(part)`），但若失败原因是 ENOSPC，续传会带 Range 头
   append 续写，在同一处再次撞满，半包白占空间。

### 关键事实

- 每个包的精确大小在下载前已知且可信：来自已 ed25519 验签的 manifest 的 `asset.Size`，
  且校验强制 `Size > 0`（`manifest.go:168`）。无需 HEAD 探测。
- 下载流程是「所有 part 先下完 + 校验，再统一写分区」（`updater.go:285-357` → `383-398`），
  写分区为流式解压直写块设备，不落地未压缩 `.img`。故峰值占用 = 本次所有待下载压缩包之和。
- `ota update` 为独立 CLI、手动触发；错误只落 `state.json.LastError`（`recordError`,
  `updater.go:874`）+ stderr（`main.go:45`）。daemon 不参与，无语音链路。
- `updater.go` 已 import `syscall`、`errors`，已有 `formatBytes`（`updater.go:945`）可复用。

## Scope

- 聚焦 eMMC（`/userdata/ota/downloads/`）。Step 1/2/3 逻辑对 `DownloadDir` 通用，
  SD 卡路径（`<mount>/aiden/ota-cache`）将来自动受益，本次不专门处理。
- 不做静态空间预留（与 storage_monitor watermark 迁移冲突，且会被日志/录音吃掉）。
- 不接 daemon、不做语音播报（语音提示留二期）。

## Design

### Step 1 — 下载前清理无关旧缓存

进入下载循环前（`updater.go:285` 之前）扫描 `DownloadDir`：

- 判定依据：当前 manifest 各 asset 的 `Name` 名单。
- **删除**：名字不在当前 manifest 中的旧包，及其对应的旧版本 `.part`。
- **保留**：名字匹配当前 manifest 的 `.part` 残包（有效断点续传数据，不误伤续传）。
- 效果：下新包前腾出旧包，瞬时峰值从 ~52M 压回 ~26M。
- 目录不存在时静默跳过；删除失败仅记日志，不阻断（尽力而为）。

### Step 2 — 下载前空间预检

Step 1 之后：

- `syscall.Statfs(DownloadDir)` 取可用空间（复用 `storage_manager.go:1201` /
  `storage_monitor.go:63` 模式：`Bavail * Bsize`）。
- 需求 = `Σ(本次真正需下载的 asset.Size) + safety margin`。
  「真正需下载」排除已 hash 命中跳过的 part（`updater.go:296` slot hash 命中、
  `330` 缓存文件命中）。
- safety margin：固定余量（建议 2~4 MB，覆盖 ext4 元数据 / 目录项 / fsync 波动）。
- 不足 → `recordError("space", err)` 返回，**不开始下载**（见 时机 A 提示）。
- 顺带修复 `validateAssetFitsPartition` 对 `.img.tar.gz` 跳过校验的问题。

### Step 3 — ENOSPC 失败时删残包

`download.go` copyErr 判定处（`110-123`）：

- `errors.Is(copyErr, syscall.ENOSPC)` 为真 → 删掉本次 `.part` 再返回。
- 其他错误（网断 / 超时 / 服务端断连）→ 保留 `.part` 续传，语义不变
  （含 `download_test.go` 现有断言）。
- 理由：空间不足下续传注定再撞墙，半包白占空间，删除是止损。

## 提示（触发时机与文案）

「提示」= 写入 `state.LastError` + 抛到 stderr 的错误信息，无语音、无弹窗。

### 时机 A — 下载前预检未过（主路径）

Step 2 算出可用 < 需求，当场拦截、不下载。信息最完整，带精确数字：

```
no space for OTA: need 26.4 MB, free 18.1 MB after cleanup, short 8.3 MB.
Free up /userdata (logs/audio) or insert an SD card, then retry `ota update`.
```

- 数字用 `formatBytes` 格式化。
- `need` = Step 2 的需求总量；`free` = statfs 可用（清理后）；`short` = need - free。
- 通过 `recordError("space", ...)` 落 `state.json`，同时作为 `CheckOnce` 返回 err 抛到 stderr。

### 时机 B — 下载中途撞 ENOSPC（兜底，少见）

预检通过但下载期间被别的进程（日志/录音）吃满余量。Step 3 删 `.part` 后如实抛出，
信息较简（无「清理后剩余」完整账），但明确指出空间问题、`.part` 已清、建议腾空间后重试：

```
no space for OTA during download: disk full while writing oem.img.tar.gz, partial file removed.
Free up /userdata (logs/audio) or insert an SD card, then retry `ota update`.
```

- 经 `recordError("download", ...)` 落 `state.json` + stderr。

## 改动落点

| 步骤                      | 文件                                      | 位置                                  |
| ------------------------- | ----------------------------------------- | ------------------------------------- |
| 清无关旧缓存              | `internal/ota/updater.go`                 | 下载循环前（`285` 之前）              |
| 空间预检 + statfs helper  | `internal/ota/updater.go`                 | 同上                                  |
| 补 `.img.tar.gz` 大小校验 | `internal/ota/updater.go`                 | `validateAssetFitsPartition`（`796`） |
| ENOSPC 删 `.part`         | `internal/ota/download.go`                | `copyErr` 判定处（`110-123`）         |
| 时机 A 文案               | `internal/ota/updater.go`                 | 预检失败分支                          |
| 时机 B 文案               | `internal/ota/download.go` / `updater.go` | ENOSPC 分支                           |

## Test plan (TDD)

- 清理：旧包被删、当前 manifest 的包与其 `.part` 保留、目录不存在不报错。
- 预检：可用 < 需求时不下载并返回带数字的错误；排除已命中跳过的 part。
- `.img.tar.gz` 大小校验：超分区大小时拒绝。
- ENOSPC：模拟 `syscall.ENOSPC` → `.part` 被删；非 ENOSPC → `.part` 保留（保留现有断言）。
- 提示文案：`state.LastError` 含 need/free/short 数字。

## 二期增强（可选，暂不实现）

### 2.1 — 手机端通知

当前提示只落 `state.json.LastError` + stderr，用户需手动查看。二期可接入 phone bridge，将空间不足推送到手机 App。

**接入点**：在空间不足分支，best-effort `POST http://localhost:8080/api/phone-bridge/commands`，body：

```json
{
  "command": {
    "id": "ota-nospace-<timestamp>",
    "type": "notification_send",
    "payload": {
      "title": "存储空间不足",
      "body": "OTA 需要 26.4MB，可用 18.1MB。清理日志/录音或插入 SD 卡后重试。",
      "sound": true
    }
  }
}
```

**注意事项**：

- 该端点目前无鉴权且绑 `0.0.0.0`，需收口安全问题（限制到 `127.0.0.1` 或加本地 token）。
- 依赖 daemon 在运行且端口监听，需 best-effort 处理连接失败（降级到仅 log/state）。
- 通道支持 WebSocket（App 在线）+ HTTP 队列（5min TTL，App 后台可领取）。

详见 phone bridge 调研：`internal/agent/phone_bridge.go:458`（SendCommand）、`phone_bridge_http.go:52`（入队端点）、`tools_phone_bridge_data.go:579`（notification_send 协议）。

### 2.2 — 自动清理日志/录音

当前 Step 2 预检不足时立即报错。二期可先 best-effort `POST /api/storage/cleanup` 触发日志/录音清理，清完重新 statfs 预检，仍不足才提示用户。

**端点**：`POST http://localhost:8080/api/storage/cleanup`（`storage_monitor_http.go:87`），body：

```json
{
  "force": false,
  "targets": ["llm_http_log", "audio_archive", "session_archive"]
}
```

**返回**：`{"success": true, "freed_mb": 15, "available_mb": 33.1, ...}`

**注意事项**：

- 同样需处理 daemon 不在线、安全收口问题。
- 现有 cleaner 只覆盖日志/录音三类（`storage_monitor.go:521`），不清理 OTA 缓存本身（Step 1 已覆盖）。

### 2.3 — HTTP 空间预检接口

daemon 新增 `GET /api/ota/check-space`，供 App 或 config web UI 在用户点"开始升级"前检查空间。

**请求**：`GET /api/ota/check-space?manifest_url=<url>`（可选，缺省用 `config.OTA.ManifestURL`）

**返回**：

```json
{
  "sufficient": false,
  "need_mb": 26.4,
  "available_mb": 18.1,
  "short_mb": 8.3,
  "after_cleanup_estimate_mb": 33.1,
  "download_dir": "/userdata/ota/downloads",
  "suggestions": ["insert_sd_card", "cleanup_logs"]
}
```

**实现**：

1. 读 manifest URL（请求参数或 `config.OTA`）
2. HTTP GET manifest（几 KB JSON，在内存里验签 + 解析，不落盘）
3. 遍历 `manifest.Parts`，对每个 `asset.Size` 求和 → `need_mb`
4. `syscall.Statfs(DownloadDir)` → `available_mb`
5. 可选：调各 cleaner 的 `EstimateReclaimable` → `after_cleanup_estimate_mb`
6. 返回 JSON

**说明**：

- Manifest 在内存中处理，不持久化。类比浏览器查询网页 JSON，用完即丢，不占 `/userdata`。
- 与 Step 2 预检复用同一逻辑，只是通过 HTTP 暴露给前端。

**路由注册**：`internal/agent/server.go:484` 后加 `mux.HandleFunc("/api/ota/check-space", s.handleOTACheckSpace)`。

---

**二期三项共享基础设施**：daemon HTTP 端点、best-effort 降级、安全收口（`0.0.0.0` → `127.0.0.1` / token）。建议统一处理。
