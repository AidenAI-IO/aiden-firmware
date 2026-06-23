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
  → Phased role loop (default / plan / execution)
  → EpisodeRecorder records events
  → CommitEpisode persists to disk (episode.yaml + events.jsonl + artifacts/)
  → exportEpisodeBestEffort async reports to Langfuse
```

### Langfuse Mapping

| Aiden Episode | Langfuse |
| --- | --- |
| `TaskEpisode` | Trace (`aiden-episode`) |
| Run start | Span `phase/default` (created for all runs by default, covers default phase activity) |
| `loop_phase` | Span `phase/{content}` (`default` / `plan` / `execution`), metadata contains `reason` (e.g., `enter_plan_mode`, `commit_plan`) |
| `default_finish` | Span `planner/default_finish` (nested under current phase span) |
| `planner_decision` | Span `iteration_N` + child Span `planner` (committed plan from `commit_plan`) |
| `tool_call` / `tool_result` | Planner tools: `planner/tool/{name}` + `planner/tool_result/{name}`; Executor tools: `tool/{name}` + `tool_result/{name}` |
| `verifier_decision` | Span `verifier` (nested under `iteration_N`, execution phase only) |
| `Outcome.Success` | Boolean Score `success=1/0` |
| `artifacts/*.jpeg` | Media upload + observation reference |
| `Extra` metrics | Trace metadata + generation model/cost/usage fields |

Typical trace structure:

```text
aiden-episode (trace)
├── phase/default
│   ├── planner/tool/audio_volume
│   │   └── planner/tool_result/audio_volume
│   └── planner/default_finish
└── phase/plan
    └── loop_phase/enter_plan_mode (via phase/plan span metadata)

aiden-episode (committed execution)
├── phase/default
├── phase/plan
├── phase/execution
├── iteration_1
│   ├── planner
│   ├── tool/mouse_click
│   │   └── tool_result/mouse_click
│   └── verifier
└── generation (planner/executor/verifier LLM calls)
```

### Trace Metadata and Tags

In addition to tokens, duration, and model info in `episode.Extra`, the exporter derives loop metrics from the event chain and writes them into trace `metadata`:

| Field | Description |
| --- | --- |
| `loop_mode` | `default` (direct finish) or `committed` (went through `commit_plan`) |
| `final_phase` | Phase at completion |
| `phase_transitions` | Phase transition sequence, e.g., `["plan:enter_plan_mode","execution:commit_plan"]` |
| `loop_phase_count` | Number of phase transitions |
| `enter_plan_mode_count` / `commit_plan_count` / `cancel_plan_count` / `plan_exhausted_count` | Trigger count for each meta tool |
| `default_finish` | Whether finished directly in default phase |
| `planner_tool_call_count` / `executor_tool_call_count` | Tool call count split by role |
| `replan_count` | Number of replans triggered by verifier |

Additional trace `tags`:

| Tag | Condition |
| --- | --- |
| `loop:default_finish` | `default_finish` event exists |
| `loop:committed` | `planner_decision` (commit) exists |
| `loop:plan` | Entered plan mode |
| `loop:execution` | Entered execution after commit |
| `loop:cancelled` | cancel_plan |
| `loop:exhausted` | Plan steps exhausted |
| `loop:replan` | verifier `needs_replan` |

In Langfuse UI, you can quickly filter simple tasks vs. multi-step delegated tasks using `loop:default_finish` and `loop:committed`.

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
   - Contains `phase/default` (and `phase/plan`, `phase/execution` as needed)
   - Simple tasks show `planner/tool/*` and `planner/default_finish`
   - Delegated tasks show `iteration_N` + `tool/*` + `verifier`
   - Screenshots can be previewed in tool_result observations
   - Metadata contains `loop_mode`, `phase_transitions`, `planner_tool_call_count`, `executor_tool_call_count`, token stats, tool/error/replan counts
   - Tags contain `loop:default_finish` or `loop:committed`
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
| `agent_commit` | Git commit when Agent binary was built (`_build.sh` ldflags injection) |
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

If Agent runs on devices like Luckfox, Langfuse `.env` needs to set a device-reachable MinIO address:

```bash
LANGFUSE_S3_MEDIA_UPLOAD_ENDPOINT=http://192.168.50.246:9090
```

After modification, restart langfuse-web / langfuse-worker with `docker compose up -d`.
| No trace | `telemetry.enabled=false` or episode not committed (empty `user_goal`) |

Report failures do not affect task execution or local memory plane writes.
