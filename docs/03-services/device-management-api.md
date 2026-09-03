---
sidebar_position: 3
---

# Config Web 管理 API

Config Web 是 Go Agent 二进制的 `config-web` 子命令。它与 Agent 运行时
分别监听 80 和 8080，分别管理自己的 PID、日志和重启周期。两者只在配置
保存后通过 `127.0.0.1` 的内部 reload 接口交互；Config Web 不代理 Agent
的聊天、会话或 Phone Bridge 业务。

## 资源接口

公开接口统一使用 `/api` 根路径，不增加版本前缀：

| 资源 | 方法与路径 | 说明 |
| --- | --- | --- |
| 配置 | `GET /api/config` | 读取 `agent.toml` 配置内容 |
| 配置 | `PATCH /api/config` | 合并补丁 `{ "config": { ... } }` |
| 配置 | `GET /api/config/schema` | 字段类型、默认值、可选值、敏感标记和重启提示 |
| 配置 | `PUT /api/config/locale` | 页面语言快捷更新 |
| 配置 | `POST /api/config/test` | 配置和设备环境校验，不保存 |
| 模型 | `GET /api/models?provider=...&locale=...` | 模型目录查询，由 Config Web 代理至 Agent 运行时 |
| STT 测试 | `POST /api/config/test/stt/start` | 启动麦克风录音测试 |
| STT 测试 | `POST /api/config/test/stt/stop` | 结束录音并返回识别结果 |
| 设备 | `GET /api/device/snapshot` | 首屏聚合读模型（配置、Wi-Fi、设备、固件、存储摘要） |
| 设备 | `GET /api/device/status` | 型号、固件、进程、USB/HID 和能力摘要 |
| 设备 | `POST /api/device/reboot` | 重启设备 |
| 设备 | `POST /api/device/usb/reenumerate` | 重新枚举 USB HID/ECM |
| 网络 | `POST /api/network/wifi/scan` | 扫描附近 Wi-Fi |
| 网络 | `PUT /api/network/wifi/connection` | 连接并保存网络，失败回滚 |
| 网络 | `DELETE /api/network/wifi/connection?ssid=...` | 忘记网络，不依赖 DELETE 请求体 |
| 系统 | `GET/PUT /api/system/environment` | 读取或原子替换系统环境变量 |
| OTA | `GET /api/ota/status` | 当前状态、进度和日志摘要 |
| OTA | `POST /api/ota/updates` | 创建 OTA 任务，返回 `task_id` |
| 日志 | `GET /api/logs/agent` | Agent 日志摘要 |
| 日志 | `GET/PUT /api/logs/llm/{name}` | 查看、导入 LLM HTTP 日志 |
| 日志 | `GET /api/logs/support` | 导出诊断支持日志包 |
| 存储 | `GET /api/storage/status` | SD/eMMC 状态和格式化任务 |
| 存储 | `POST /api/storage/format` | 异步格式化 SD 卡 |
| 存储 | `POST /api/storage/eject` | 同步并安全弹出 SD 卡 |

上述接口的公开入口全部由 Config Web 80 端口提供。Config Web 可在本机通过
受控 HTTP facade 调用 Agent 运行时实现，但前端和外部客户端不应直接访问
Agent 8080 的模型、STT 或存储路径。

存储状态响应由 `GET /api/storage/status` 和
`GET /api/device/snapshot` 中的 `storage` 字段共用：

```json
{
  "effective_mode": 1,
  "card": {
    "present": false,
    "mounted": false,
    "device": "",
    "total_bytes": 0,
    "free_bytes": 0,
    "reason": ""
  },
  "mount_point": "/mnt/sdcard",
  "format_job": {"status": "idle"},
  "migration": {"status": "idle"}
}
```

`format_job` 是异步任务，状态可以是 `idle`、`running`、`success` 或
`failed`；`migration` 表示 eMMC 到 SD 的后台迁移并使用相同的状态值。
存储卡可能已插入但尚未挂载，客户端应分别使用 `card.present` 和
`card.mounted` 决定格式化和弹出按钮是否可用。

## 配置保存响应

`PATCH /api/config` 成功响应包含：

```json
{
  "ok": true,
  "persisted": true,
  "applied": true,
  "revision": 123,
  "changed_paths": ["model.model"],
  "restart_required": false,
  "restart_reasons": []
}
```

`persisted` 表示配置文件已经原子落盘，`applied` 表示 Agent 已接受并加载
该 revision；两者必须分别展示。若 reload 失败，接口返回 HTTP 503，同时
保留 `persisted=true`、`applied=false` 和错误信息，页面应提示“配置已保存但
当前 Agent 未生效”。需要完整重启的字段会设置 `restart_required` 和
`restart_reasons`。

## API 边界

旧的 `/api/wifi/*`、`/api/system/env`、`/api/ota/update`、`/api/reboot`、
`/api/agent/*`、`/api/llm-logs/*` 等兼容入口已删除，不再返回
`Deprecation` 适配响应。客户端必须使用上表中的 canonical 资源；`GET
/api/config` 是纯配置语义，聚合首屏使用 `/api/device/snapshot`。

## Agent 内部 reload

`POST /api/internal/config/reload` 仅接受 loopback 请求，请求可携带
`revision`。Agent 会校验 revision，并在所有受影响的运行时依赖成功重建后
才报告 `applied=true`；过期 revision 返回 HTTP 409。此接口不是对外的管理 API。
