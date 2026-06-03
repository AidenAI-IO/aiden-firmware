# Episode 遥测与 Langfuse 接入

Aiden Agent 在每次任务结束后，可将完整的 task episode（元数据、事件链、截图）异步上报到 [Langfuse](https://langfuse.com/)，用于 trace 浏览、数据集构建和评测。

## 功能开关

在 `agent.toml` 中配置 `[telemetry]` 段。默认关闭，开启后不影响任务执行（best-effort 异步上报）。

```toml
[telemetry]
enabled = true
provider = "langfuse"
base_url = "http://langfuse.example.com:3000"
public_key = "pk-lf-..."
secret_key = "sk-lf-..."
upload_screenshots = true
upload_timeout_sec = 30
max_retry = 2
environment = "prod"
tags = ["aiden-hardware"]
```

| 字段 | 说明 |
| --- | --- |
| `enabled` | 总开关，`false` 时零开销 |
| `base_url` | Langfuse Web 地址（不含路径） |
| `public_key` / `secret_key` | Langfuse API 密钥 |
| `upload_screenshots` | 是否上传 `artifacts/step_*.jpeg` 截图 |
| `upload_timeout_sec` | 单次上报超时 |
| `max_retry` | 失败后重试次数 |
| `environment` | Langfuse trace 环境标签 |
| `tags` | 附加到每条 trace 的标签 |

密钥直接写入 `agent.toml` 的 `[telemetry]` 段。

## 数据流

```text
Runtime.Run()
  → Planner / Executor / Verifier 循环
  → EpisodeRecorder 记录事件
  → CommitEpisode 落盘 (episode.yaml + events.jsonl + artifacts/)
  → exportEpisodeBestEffort 异步上报 Langfuse
```

### Langfuse 映射

| Aiden Episode | Langfuse |
| --- | --- |
| `TaskEpisode` | Trace (`aiden-episode`) |
| `planner_decision` | Span `planner`（嵌套在 `iteration_N` 下） |
| `tool_call` / `tool_result` | Span `tool/{name}` + `tool_result/{name}` |
| `verifier_decision` | Span `verifier` |
| `Outcome.Success` | Score `success=1` |
| `artifacts/*.jpeg` | Media upload + observation 引用 |
| `Extra` metrics | Trace metadata |

本地 episode 仍写入 `/userdata/agent/memory/episodes/`，Langfuse 为额外副本，用于集中分析和数据集管理。

## 自托管 Langfuse

项目提供 Docker Compose 配置：[`deploy/langfuse/docker-compose.yml`](../../deploy/langfuse/docker-compose.yml)

```bash
cd deploy/langfuse
cp .env.example .env
# 编辑 .env，设置 ENCRYPTION_KEY=$(openssl rand -hex 32)
docker compose up -d
```

启动后访问 `http://localhost:3000`，创建 Organization / Project，复制 Public Key 和 Secret Key 到设备环境变量。

组件：Langfuse Web + Worker、Postgres、ClickHouse、Redis、MinIO（截图与事件 blob 存储）。

## 验证

1. 启动 Langfuse（本地或远程）
2. 设备 `agent.toml` 设置 `telemetry.enabled = true`
3. 执行一次任务（Web UI 或 benchmark）
4. 在 Langfuse UI → Traces 中确认：
   - 存在 `aiden-episode` trace
   - 含 planner / tool / verifier spans
   - 截图可在 tool_result observation 中预览
   - metadata 含 `total_duration_ms`、token 统计

## Trace → Dataset → Benchmark 工作流

Langfuse 用于从生产 episode 筛选高质量样本，再转化为项目 benchmark suite。

### 1. 在 Langfuse 中筛选 Trace

- 打开 **Traces**，按 tag（`success` / `failure`）或 metadata 过滤
- 查看 iteration spans 和截图，确认任务质量
- 对合格 trace 添加 label 或 score

### 2. 创建 Dataset

1. Langfuse UI → **Datasets** → New Dataset（例如 `phone_control_candidates_v2`）
2. 从 trace 详情页 → **Add to dataset**
3. 填写 input（用户目标 `user_goal`）和 expected output（`final_answer` 或 rubric 描述）
4. 可选：将 screenshot media 作为 dataset item metadata 引用

### 3. 导出并转为 Benchmark Task

Langfuse 支持导出 dataset items（UI 或 [Public API](https://langfuse.com/docs/api-and-data-platform/features/public-api)）。

将选中 item 手工或脚本转换为 [`benchmark/suites/`](../../benchmark/suites/) 格式：

```json
{
  "id": "open_settings_from_prod_001",
  "category": "single_step",
  "description_for_judge": "Agent must open Settings from home screen.",
  "prompt": "请打开系统设置。",
  "rubric": [
    {
      "id": "in_settings",
      "check": "Post-screenshot shows the Settings app main page."
    }
  ],
  "hard_assertions": {
    "min_tool_calls": 1,
    "max_tool_calls": 8,
    "must_complete_within_sec": 90
  }
}
```

字段对应关系：

**Episode / trace metadata 附加字段：**

| 字段 | 说明 |
| --- | --- |
| `model` / `model_name` / `model_provider` | 本次 run 使用的 LLM（来自 `agent.toml` 的 `[model]`） |
| `agent_commit` | Agent 二进制构建时的 git commit（`_build.sh` ldflags 注入） |
| `agent_build` | Agent 构建版本号（`YYYYMMDD-HHMMSS-<commit>`） |
| `firmware_version` | 设备 OTA 状态 `/userdata/ota/state.json` 中的 `current_version` |

**Langfuse trace 字段映射：**

| Langfuse 字段 | 来源 |
| --- | --- |
| `version` | Agent 构建版本 `agent_build`，若无则 `firmware_version` |
| `release` | git commit `agent_commit`，若无则 `firmware_version` |
| `metadata.model` | LLM 模型（如 `openrouter/google/gemini-3.5-flash`） |
| `metadata` | 上述全部字段 + episode metrics |
| `tags` | 配置 tags + `model:{provider/model}` |

**Langfuse Dataset → Benchmark 字段对应：**

| Langfuse Dataset Item | Benchmark TaskSpec |
| --- | --- |
| `input` | `prompt` |
| trace metadata `user_goal` | 同上或 `description_for_judge` 参考 |
| expected output / 人工标注 | `rubric` checks |
| trace tool 调用次数 | `hard_assertions.min/max_tool_calls` 参考 |
| 截图 artifact | `input_screenshot`（静态感知类任务） |

### 4. 回归验证

```bash
cd benchmark
uv run python -m runner.main --suite suites/phone_control_v1.json --agent-url http://device:8080
```

新 task 加入 suite 后，用 benchmark runner 做自动化回归；Langfuse 继续收集新一轮生产 trace，形成闭环。

## 故障排查

| 现象 | 可能原因 |
| --- | --- |
| 日志 `[telemetry] export episode failed` | `base_url` 不可达、密钥错误、超时 |
| Trace 无截图 / media not yet uploaded | Agent 未 PATCH 上传状态（已修复）；或 MinIO presigned URL 使用 `localhost:9090`，设备无法访问；检查 agent 日志 `[telemetry] screenshot upload failed` |

截图上传完整链路：

1. Agent `POST {base_url}/api/public/media` 获取 `mediaId` + presigned `uploadUrl`
2. Agent `PUT uploadUrl` 直传 MinIO
3. Agent `PATCH {base_url}/api/public/media/{mediaId}` 写入 `uploadHttpStatus=200`（**缺少此步会显示 media not yet uploaded**）
4. Agent `POST /api/public/ingestion` 发送含 `@@@langfuseMedia:...@@@` 的 trace

若 Agent 运行在 Luckfox 等设备上，Langfuse `.env` 需设置设备可达的 MinIO 地址：

```bash
LANGFUSE_S3_MEDIA_UPLOAD_ENDPOINT=http://192.168.50.246:9090
```

修改后 `docker compose up -d` 重启 langfuse-web / langfuse-worker。
| 无 trace | `telemetry.enabled=false` 或 episode 未 commit（空 `user_goal`） |

上报失败不会影响任务执行或本地 memory plane 写入。
