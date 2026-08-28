---
sidebar_position: 16
---

# Episode Telemetry and Langfuse Integration

After each task completes, Aiden Agent can asynchronously report the complete task episode (metadata, event chain, screenshots) to [Langfuse](https://langfuse.com/) for trace browsing, dataset construction, and evaluation.

## Feature Toggle

Configure the `[telemetry]` section in `agent.toml`. Disabled by default; when enabled, it does not affect task execution (best-effort async reporting).

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

| Field | Description |
| --- | --- |
| `enabled` | Master switch; zero overhead when `false` |
| `base_url` | Langfuse Web address (without path) |
| `public_key` / `secret_key` | Langfuse API keys |
| `upload_screenshots` | Whether to upload `artifacts/step_*.jpeg` screenshots |
| `upload_timeout_sec` | Timeout for single report |
| `max_retry` | Retry count on failure |
| `environment` | Langfuse trace environment tag |
| `tags` | Tags attached to each trace |

Credentials are written directly into the `[telemetry]` section of `agent.toml`.

## Data Flow

```text
Runtime.Run()
  → Agent execution loop
  → EpisodeRecorder records events
  → CommitEpisode persists to disk (episode.yaml + events.jsonl + artifacts/)
  → exportEpisodeBestEffort async reports to Langfuse
```

### Langfuse Mapping

| Aiden Episode | Langfuse |
| --- | --- |
| `TaskEpisode` | Trace (`aiden-episode`) |
| Run start | Span `run` (created for all runs, covers main execution activity) |
| `tool_call` / `tool_result` | Span `tool/{name}` + `tool_result/{name}` |
| `Outcome.Success` | Boolean Score `success=1/0` |
| `artifacts/*.jpeg` | Media upload + observation reference |
| `Extra` metrics | Trace metadata + generation model/cost/usage fields |

Typical trace structure:

```text
aiden-episode (trace)
├── run
│   ├── tool/audio_volume
│   │   └── tool_result/audio_volume
│   ├── tool/screenshot
│   │   └── tool_result/screenshot
│   └── tool/touch_gesture
│       └── tool_result/touch_gesture
└── generation (LLM calls)
```

### Trace Metadata and Tags

In addition to tokens, duration, and model info in `episode.Extra`, the exporter derives execution metrics from the event chain and writes them into trace `metadata`:

| Field | Description |
| --- | --- |
| `tool_call_count` | Total number of tool calls in the episode |
| `iteration_count` | Number of agent iterations (from episode recorder counter or episode.Extra) |

Additional trace `tags`:

| Tag | Condition |
| --- | --- |
| `success` | Task completed successfully |
| `failure` | Task failed |

In Langfuse UI, you can filter tasks by success/failure and tool usage patterns.

Local episodes are still written to `/userdata/agent/memory/episodes/`; Langfuse serves as an additional copy for centralized analysis and dataset management.

## Self-Hosted Langfuse

The project provides a Docker Compose configuration: [`deploy/langfuse/docker-compose.yml`](../../deploy/langfuse/docker-compose.yml)

```bash
cd deploy/langfuse
cp .env.example .env
# Edit .env, set ENCRYPTION_KEY=$(openssl rand -hex 32)
docker compose up -d
```

After startup, visit `http://localhost:3000`, create an Organization / Project, and copy the Public Key and Secret Key to device environment variables.

Components: Langfuse Web + Worker, Postgres, ClickHouse, Redis, MinIO (screenshot and event blob storage).

## Verification

1. Start Langfuse (local or remote)
2. Set `telemetry.enabled = true` in device `agent.toml`
3. Execute a task (Web UI or benchmark)
4. Confirm in Langfuse UI → Traces:
   - `aiden-episode` trace exists
   - Contains `run` span with nested tool calls
   - Tool calls show as `tool/*` and `tool_result/*` spans
   - Screenshots can be previewed in tool_result observations
   - Metadata contains `tool_call_count`, `iteration_count`, token stats
   - Tags contain `success` or `failure`
   - Trace contains `userId` (device ID) and `sessionId` (runtime session ID)

## Trace → Dataset → Benchmark Workflow

Langfuse is used to filter high-quality samples from production episodes and convert them into project benchmark suites.

### 1. Filter Traces in Langfuse

- Open **Traces**, filter by tag (`success` / `failure`) or metadata
- Review iteration spans and screenshots to confirm task quality
- Add labels or scores to qualified traces

### 2. Create Dataset

1. Langfuse UI → **Datasets** → New Dataset (e.g., `phone_control_candidates_v2`)
2. From trace detail page → **Add to dataset**
3. Fill in input (user goal `user_goal`) and expected output (`final_answer` or rubric description)
4. Optional: Reference screenshot media as dataset item metadata

### 3. Export and Convert to Benchmark Task

Langfuse supports exporting dataset items (UI or [Public API](https://langfuse.com/docs/api-and-data-platform/features/public-api)).

Convert selected items manually or via script to [`benchmark/suites/`](../../benchmark/suites/) format:

```json
{
  "id": "open_settings_from_prod_001",
  "category": "single_step",
  "description_for_judge": "Agent must open Settings from home screen.",
  "prompt": "Please open system settings.",
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

Field mapping:

**Episode / trace metadata additional fields:**

| Field | Description |
| --- | --- |
| `model` / `model_name` / `model_provider` | LLM used for this run (from `[model]` in `agent.toml`) |
| `agent_commit` | Git commit when Agent binary was built (application build task ldflags injection) |
| `agent_build` | Agent build version number (`YYYYMMDD-HHMMSS-<commit>`) |
| `firmware_version` | `current_version` from device OTA state `/userdata/ota/state.json` |
| `session_boundary_decision` / `session_boundary_reason` | Session-boundary classifier output for the run. |
| `session_rotated` | Whether the run archived the previous active session before handling the user turn. |
| `pending_chunks_recalled` | Number of `recall_session_chunks` results whose explicit result `source` is `pending`; `chunk_id` prefixes are ignored. |

**Langfuse trace field mapping:**

| Langfuse Field | Source |
| --- | --- |
| `version` | Agent build version `agent_build`, or `firmware_version` if absent |
| `release` | Git commit `agent_commit`, or `firmware_version` if absent |
| `metadata.model` | LLM model (e.g., `openrouter/google/gemini-3.5-flash`) |
| `metadata` | All above fields + episode metrics |
| `tags` | Configured tags + `model:{provider/model}` |
| `userId` | `device_scope.device_id`, or `extra.user_id` |
| `sessionId` | Runtime session ID, or `extra.session_id` |
| generation `modelParameters` | Invocation parameters like `temperature`, `max_tokens`, tool count, etc. |
| generation `usageDetails` / `costDetails` | Token usage and provider/local estimated cost |
| score `success` | Written for every task, `1` for success, `0` for failure |

**Langfuse Dataset → Benchmark field mapping:**

| Langfuse Dataset Item | Benchmark TaskSpec |
| --- | --- |
| `input` | `prompt` |
| trace metadata `user_goal` | Same or reference for `description_for_judge` |
| expected output / manual annotation | `rubric` checks |
| trace tool call count | Reference for `hard_assertions.min/max_tool_calls` |
| Screenshot artifact | `input_screenshot` (for static perception tasks) |

### 4. Regression Validation

```bash
cd benchmark
uv run python -m runner run --suite suites/phone_control_v1.json --agent-url http://device:8080
```

After new tasks are added to the suite, use the benchmark runner for automated regression; Langfuse continues collecting new production traces, forming a closed loop.

## Troubleshooting

| Symptom | Possible Cause |
| --- | --- |
| Log `[telemetry] export episode failed` | `base_url` unreachable, incorrect credentials, timeout |
| Trace has no screenshots / media not yet uploaded | Agent did not PATCH upload status (fixed); or MinIO presigned URL uses `localhost:9090`, device cannot access; check agent log `[telemetry] screenshot upload failed` |

Screenshot upload complete flow:

1. Agent `POST {base_url}/api/public/media` gets `mediaId` + presigned `uploadUrl`
2. Agent `PUT uploadUrl` direct upload to MinIO
3. Agent `PATCH {base_url}/api/public/media/{mediaId}` writes `uploadHttpStatus=200` (**missing this step shows media not yet uploaded**)
4. Agent `POST /api/public/ingestion` sends trace containing `@@@langfuseMedia:...@@@`

When Agent runs on a device such as Luckfox, the Langfuse `.env` must use a MinIO address reachable from that device:

```bash
LANGFUSE_S3_MEDIA_UPLOAD_ENDPOINT=http://192.168.50.246:9090
```

After modification, restart langfuse-web / langfuse-worker with `docker compose up -d`.
| No trace | `telemetry.enabled=false` or episode not committed (empty `user_goal`) |

Report failures do not affect task execution or local memory plane writes.
