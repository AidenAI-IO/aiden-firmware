# Agent Context Lifecycle

This document gives the end-to-end view of what enters an Aiden Agent run, where each context layer comes from, and when it is refreshed. It complements [Session Memory Compaction](session-memory.md), which focuses on conversation-window compression, and [Memory Plane](memory-plane.md), which focuses on device, long-term, and task-episode memory.

## Context Layers

Each run is assembled from several layers. Some layers are persisted memory, while others are request-local prompt context.

| Layer | Source | Visibility | Persistence |
| --- | --- | --- | --- |
| Base instruction | `agent.toml` `instruction` and `additional_prompt` | planner, executor, verifier | configuration |
| Runtime defaults | built-in prompt rules, current date, host runtime information | planner, executor, verifier | not persisted |
| Skills | skill index plus active `SKILL.md` content | planner, executor, verifier | skill files and skill state |
| Runtime context | `RunRequest.RuntimeContext`, for example phone bridge state | planner, executor, verifier | not persisted |
| Tool catalog | resolved built-in tools plus skill tools | executor can call; planner/verifier can inspect | not persisted in memory |
| Retrieved memory context | `MemoryPlane.Retrieve` output | planner and verifier only | filesystem memory |
| Planner conversation history | hot-window memory, optional compressed-history markers, optional persisted chat history | planner only | filesystem memory |
| Role loop state | objective, plan, next step, tool evidence, verifier feedback, world state | role-specific | current run only |
| Input attachments | `RunRequest.Attachments`, plus latest screenshot image when available | role user messages | current run only |

The executor intentionally does not receive global memory or full conversation history. It receives the planner-approved `next_step`, latest world state, and the latest local execution result only.

## Prompt Shape

For the current multi-role loop, `Runtime.Run` builds `RoleProfiles` before execution. Each role system prompt contains:

1. role identity;
2. base instruction;
3. built-in default behavior;
4. available skills summary;
5. active skill instructions;
6. request-local runtime context, if provided;
7. role rules;
8. available tool information;
9. role-specific rendered memory context, if any.

The per-call user message is then built from the current loop state:

- planner sees the current world state, original request, conversation history, current plan, executor results, and verifier feedback;
- executor sees the current world state and the planner-approved `next_step`;
- verifier sees the current world state, original request, completion criteria, accumulated executor evidence, and mandatory completion checklist.

Planner and verifier also receive the tool scratchpad after tools have run. The latest screenshot image is attached to the role message when the world state has screenshot bytes.

## Retrieved Memory Context

`MemoryPlane.Retrieve` builds a `MemoryContext` with three buckets:

```go
type MemoryContext struct {
    Planner  RoleMemoryContext
    Verifier RoleMemoryContext
    Common   RoleMemoryContext
}
```

`Common` is rendered for both planner and verifier. It currently includes:

- `session/summary.md`, the compressed session summary;
- `long_term/profile.md`, the synthesized user profile.

Planner-specific retrieval includes:

- device profile;
- app profiles;
- verified procedures and navigation memory;
- similar successful episodes;
- calibration notes.

Verifier-specific retrieval includes:

- failure memories;
- conflicting memories;
- failed task episodes relevant to the current request.

Each hit is filtered by applicability, ranked by the store search logic, routed by memory type, and trimmed per category before rendering.

## Run Lifecycle

At a high level, one run follows this sequence:

```text
RunRequest
  |
  v
normalize input and activate requested skills
  |
  v
resolve model, context window, tools, and memory handle
  |
  v
MemoryPlane.Retrieve(input, attachments, skills, tools, current hints)
  |
  v
start EpisodeRecorder
  |
  v
build role profiles and planner memory
  |
  v
planner -> executor -> verifier loop
  |
  v
append conversation exchange and save snapshot
  |
  v
request session-memory maintenance
  |
  v
commit task episode and extract reusable memory
```

`CurrentEnvironmentHints` are lightweight facts already known to runtime, such as screenshot size, language, and last observed app name. They must not perform device actions. If fresh screen evidence is needed, the planner must ask the executor to use tools.

## Update Mechanisms

### Runtime Context

Runtime context is supplied per request and is not written back into memory. The phone bridge is the main producer today. It keeps connection status, platform, heartbeat time, and the latest phone environment. Each run receives only a compact summary: connection state, system type and version, locale/timezone, screen size, and confirmed launchable third-party app candidates. When the bridge disconnects, stale environment data is cleared.

### Session Hot Window

After a run completes, `MemoryManager.AppendExchange` records the user input and final answer. `SaveSnapshot` persists the current window, and `RequestMaintenance` schedules filesystem maintenance.

The hot window lives in:

```text
memory/session/events.jsonl
```

When compressed history exists, prompt construction wraps the rendered hot window with boundary markers. Those markers are prompt-only and are never persisted.

### Session Compaction

Session compaction is handled by `MemoryManager.maintainFilesystemMemory`. It reads `events.jsonl`, decides whether compaction is needed, writes compacted chunks, updates `summary.md`, and replaces `events.jsonl` with the retained hot window.

Compression can be triggered by:

- prompt-token usage relative to the active model context window;
- reserved token headroom;
- configured percentage threshold;
- event count fallback when prompt-token data is unavailable.

The active context window comes from `ModelResolver.Spec()` when available. Unknown models fall back to `memory/extraction.yaml`.

Compaction preserves context by:

- snapping cuts to legal event boundaries;
- keeping adjacent leading `system_event` and `screen_context` events;
- pinning the root user input when needed;
- summarizing split-turn prefixes and injecting a synthetic hot-window event;
- scrubbing screenshot base64 payloads before persistence;
- resetting `lastPromptTokens` to the retained hot-window estimate after compaction.

The compressed artifacts live in:

```text
memory/session/summary.md
memory/session/summary_archive.md
memory/session/chunks/index.yaml
memory/session/chunks/<chunk_id>.jsonl
```

### Long-Term Memory And Profile

Long-term memory stores user preferences, rules, facts, procedures, and manually saved memories under:

```text
memory/long_term/
```

The `save_memory`, `forget_memory`, and episode extraction paths update this store. `profile.md` is rebuilt through the long-term memory profile pipeline, with debouncing to avoid repeated rebuilds during bursts of writes.

### Device And Episode Memory

The episode recorder captures planner decisions, executor actions, tool results, verifier decisions, observed world state, and outcome data during the run. `MemoryPlane.CommitEpisode` writes the task episode, extracts reusable lessons, updates device memory, and updates outcomes on referenced memories.

The main stores are:

```text
memory/device/
memory/episodes/
memory/lifecycle/
```

Successful episodes can create or update procedures, navigation memory, app profiles, calibration notes, and task summaries. Failed episodes can create failure memories that are routed to verifier on future similar tasks.

### Persisted Chat History

When a chat-history store exists, planner memory also loads a compact view of recent persisted UI chat history. This is appended to the planner conversation-history string and capped by message and rune limits. It is separate from `session/events.jsonl` and does not change the executor visibility boundary.

## Storage Map

```text
/userdata/agent/memory/
|-- default.json                 # conversation-window snapshot
|-- chat_history/                # optional persisted UI chat history
|-- session/
|   |-- events.jsonl             # hot window
|   |-- summary.md               # compressed session summary
|   |-- summary_archive.md
|   `-- chunks/
|-- long_term/
|   |-- profile.md               # synthesized profile
|   |-- index.yaml
|   `-- memories/
|-- device/
|   |-- profile.yaml
|   |-- apps/
|   |-- procedures/
|   |-- navigation/
|   |-- calibration/
|   `-- failures/
|-- episodes/
|   |-- index.yaml
|   `-- <yyyy>/<episode_id>/
`-- lifecycle/
```

## Invariants

- Runtime context is request-local; do not use it as durable memory.
- Session summaries are durable memory; hot-window boundary markers are not.
- Planner owns plans and sees retrieved experience memory.
- Executor executes exactly one approved step and does not receive global memory.
- Verifier is the only role allowed to finish a run and receives failure/conflict memory.
- Tool results and screenshots are evidence for the current run; reusable lessons are written only after episode commit or explicit memory-tool calls.
- Device actions must be based on current tool observations or screenshots, not on stale remembered state alone.
