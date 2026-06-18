# Agent Context Lifecycle

This document gives the end-to-end view of what enters an Aiden Agent run, where each context layer comes from, and when it is refreshed. It complements [Session Memory Compaction](session-memory.md), which focuses on conversation-window compression, and [Memory Plane](memory-plane.md), which focuses on device, long-term, and task-episode memory.

## Context Layers

Each run is assembled from several layers. Some layers are persisted memory, while others are request-local prompt context.

| Layer | Source | Visibility | Persistence |
| --- | --- | --- | --- |
| Base instruction | built-in runtime instruction, optional `agent.toml` `custom_instruction`, and `additional_prompt` | planner and executor | configuration |
| Runtime defaults | built-in prompt rules, current date, host runtime information | planner and executor receive all defaults; verifier receives current date and verifier role rules only | not persisted |
| Skills | skill index plus active `SKILL.md` content | planner and executor | skill files and skill state |
| Runtime context | `RunRequest.RuntimeContext`, for example phone bridge state | planner and executor | not persisted |
| Tool catalog | resolved built-in tools plus skill tools | planner and executor can call; verifier does not receive a tool catalog | not persisted in memory |
| Planner loop meta tools | `use_simple_mode`, `enter_plan_mode`, `commit_plan`, `cancel_plan` | planner only | not persisted in memory |
| Executor loop meta tools | `finish_step`, `abort_step` | executor only | not persisted in memory |
| Retrieved memory context | `MemoryPlane.Retrieve` output | planner receives common and planner memory; verifier receives failure/conflict caution memory only | filesystem memory |
| Planner conversation history | hot-window memory, optional compressed-history markers, optional persisted chat history | planner only | filesystem memory |
| Role loop state | phase, objective, plan, `plan_step_index`, next step, tool evidence, verifier feedback, world state | role-specific | current run only |
| Input attachments | `RunRequest.Attachments`, plus latest screenshot image when available | role user messages | current run only |

The executor intentionally does not receive global memory or full conversation history. It receives the planner-approved `next_step`, latest world state, and the latest local execution result only.

## Phased Role Loop

`roleCollaborativeExecutor` drives a three-phase state machine. All phases share one global `MaxIterations` budget.

| Phase | Planner behavior | Other roles |
| --- | --- | --- |
| `decision` | Route to direct answer, `default`, or `plan` before ordinary tools are exposed | not used |
| `default` | Call built-in tools directly, return a final answer when done, or call `enter_plan_mode` for complex work | not used |
| `plan` | Explore with tools, maintain a draft plan, then call `commit_plan` or `cancel_plan` | not used |
| `execution` | not called | `executor` runs one approved step, then `verifier` reviews evidence |

Phase transitions:

```text
Run starts in decision
  |
  +-- direct_answer --> run ends (no verifier)
  |
  +-- simple / use_simple_mode --> default
  |     |
  |     +-- planner finishes --> run ends (no verifier)
  |     |
  |     +-- planner calls domain tool --> default
  |     |
  |     +-- enter_plan_mode --> plan
  |
  +-- plan / enter_plan_mode --> plan
        |
        +-- cancel_plan --> default
        |
        +-- commit_plan --> execution
              |
              +-- executor finish_step / abort_step --> verifier
                    |
                    +-- verifier can_finish --> run ends
                    |
                    +-- verifier needs_replan --> plan
                    |
                    +-- verifier continues and plan has more steps --> next execution step
                    |
                    +-- verifier continues and plan is exhausted --> plan
```

Planner loop meta tools are visible only to the planner and are intercepted by the runtime controller. Executor loop meta tools are visible only to the executor for step-boundary review. They never reach the device tool layer.

| Meta tool | Role | Allowed in | Effect |
| --- | --- | --- | --- |
| `use_simple_mode` | planner | `decision` | route to `default` |
| `enter_plan_mode` | planner | `decision`, `default` | switch to `plan` |
| `commit_plan` | planner | `plan` only | commit objective, completion criteria, and plan steps; switch to `execution` |
| `cancel_plan` | planner | `plan` | clear draft planning state; switch to `default` |
| `finish_step` | executor | `execution` | mark the current step ready for verifier review |
| `abort_step` | executor | `execution` | mark the current step blocked or failed for verifier review |

After `commit_plan`, runtime owns step progression. `plan_step_index` selects the current committed step, and `next_step` is synchronized from that index before each executor turn. The executor must not reorder or rewrite the plan.

Simple tasks should stay in `default` (direct answer, one tool call, or at most two short steps). If completing the request will likely need three or more steps, the planner must call `enter_plan_mode` before executing further. Also use `plan` for branching, sustained tracking, or explicit completion criteria before delegation.

## Prompt Shape

For the current multi-role loop, `Runtime.Run` builds `RoleProfiles` before execution. Planner and executor system prompts contain:

1. role identity;
2. base instruction;
3. built-in default behavior;
4. available skills summary;
5. active skill instructions;
6. request-local runtime context, if provided;
7. role rules;
8. available tool information;
9. role-specific rendered memory context, if any.

The verifier system prompt is intentionally narrower. It contains the current date, verifier role rules, and an optional `## Verifier memory cautions` block. It does not receive base instruction, default behavior, skills, runtime context, tool catalog, common memory, or planner memory.

The per-call user message is then built from the current loop state:

- planner sees the current loop mode, world state, original request, conversation history, draft or committed plan, executor results, and verifier feedback;
- executor sees the current world state and the planner-approved `next_step`;
- verifier sees the current world state, the step under verification (`step_index`, `step_text`, `is_final_committed_step`), and the latest executor result for that step only.

Planner and executor receive the tool scratchpad after tools have run. The latest screenshot image is attached to the role message when the world state has screenshot bytes.

## Retrieved Memory Context

`MemoryPlane.Retrieve` builds a `MemoryContext` with three buckets:

```go
type MemoryContext struct {
    Planner  RoleMemoryContext
    Verifier RoleMemoryContext
    Common   RoleMemoryContext
}
```

`Common` is rendered into planner memory context. It currently includes:

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

Verifier-specific retrieval is rendered as `## Verifier memory cautions`. These entries are historical failure/conflict warnings only; they are not proof of completion. Verifier approval must still be based on the current executor outcome, tool observations, screenshots, or current step evidence.

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
begin session: detect/rotate boundary and append current user input
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
phased role loop (decision / default / plan / execution)
  |
  v
commit session: append assistant output and save snapshot
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

At run begin, session management detects whether the input starts a new session
and appends the current user input to `memory/session/events.jsonl`. At run
commit, it appends the assistant output, persists the current snapshot with
`SaveSnapshot`, and schedules filesystem maintenance with
`RequestMaintenance`.

The hot window lives in:

```text
memory/session/events.jsonl
```

At prompt time, retained hot-window events are converted into native chat
messages and inserted between the planner system prompt and the current planner
task message. Prompt construction does not render them inside a
`Conversation history:` text block and does not add hot-window labels or
boundary markers.

When session-boundary detection classifies a user turn as a new session, the
runtime archives the whole active `memory/session/` directory into
`memory/session_archive/<closed_session_id>/`, recreates an empty
`memory/session/events.jsonl`, and clears the in-memory conversation window.
Archived sessions are preserved as logs only; they are not restored, listed,
switched back into, injected into prompts, or searched by
`recall_session_chunks`.

### Session Compaction

Session compaction is handled by `MemoryManager.maintainFilesystemMemory`. It reads active `events.jsonl`, decides whether compaction is needed, writes compacted chunks, updates active `summary.md`, and replaces `events.jsonl` with the retained hot window.

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

The episode recorder captures loop phase changes, default-mode finishes, planner decisions, planner and executor tool calls, tool results, verifier decisions, observed world state, and outcome data during the run. `MemoryPlane.CommitEpisode` writes the task episode, extracts reusable lessons, updates device memory, and updates outcomes on referenced memories.

Common episode event types:

| Event type | When recorded |
| --- | --- |
| `loop_phase` | `enter_plan_mode`, `commit_plan`, `cancel_plan`, `plan_exhausted`, or `needs_replan` |
| `default_finish` | planner returns a final answer directly in `default` |
| `planner_decision` | `commit_plan` commits objective, criteria, and plan steps |
| `tool_call` / `tool_result` | planner tool use in `default`/`plan`, or executor tool use in `execution` |
| `verifier_decision` | verifier review in `execution` |

The main stores are:

```text
memory/device/
memory/episodes/
memory/lifecycle/
```

Successful episodes can create or update procedures, navigation memory, app profiles, calibration notes, and task summaries. Failed episodes can create failure memories that are routed to verifier on future similar tasks.

### Persisted Chat History

**NOTE: As of this branch, chat_history injection into planner context is DISABLED.**

The `chat_history/` store persists UI-level conversation logs but is NOT automatically injected into the planner's context. This prevents:
- Duplicate, uncompressed context competing with the session system
- Unbounded growth of planner prompts
- Confusion between active session and archived history

For "resume interrupted task" scenarios, use explicit session restore or recall tools instead. The session system already provides comprehensive history management with compaction, archiving, and recall capabilities.

The chat_history store remains available for:
- UI display of conversation history across sessions
- Audit logging
- Future explicit session restore features

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
|-- session_archive/             # closed session logs, not active context
|   `-- <closed_session_id>/
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
- Active session summaries are durable within the current session; archived
  session summaries are logs and are not prompt or recall context.
- Hot-window labels or boundary markers are not injected into prompts or
  durable memory.
- Planner owns plans and sees retrieved experience memory.
- In `default`, planner may call tools and finish the run without verifier review.
- In `execution`, executor executes exactly one approved step and does not receive global memory.
- In `execution`, verifier validates only the current committed step; intermediate step success advances the plan, and `can_finish` is allowed only on the final committed step.
- `commit_plan` is only valid in `plan`; runtime owns `plan_step_index` after commit.
- Loop meta tools are planner/executor control tools handled by the runtime controller, not the device tool layer.
- Tool results and screenshots are evidence for the current run; reusable lessons are written only after episode commit or explicit memory-tool calls.
- Device actions must be based on current tool observations or screenshots, not on stale remembered state alone.
