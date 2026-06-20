# Memory Plane Design

This document designs Aiden's new memory component. The goal is to add device environment memory and task episode memory beyond existing conversation memory, and have runtime automatically retrieve relevant experience before each task starts and automatically consolidate successful paths and failure reasons after tasks end.

## Current Architecture Constraints

The existing Go Agent memory already has several capabilities:

- `MemoryManager` maintains langchaingo's conversation window and writes session events to `/userdata/agent/memory/session/events.jsonl`.
- `SessionMemoryStore` compresses older events from the active session into chunks, writes `session/summary.md`, and provides `recall_session_chunks` for active-session chunks only.
- `LongTermMemoryStore` stores profile, rule, preference, procedure, fact as markdown frontmatter, generates `long_term/index.yaml` and `long_term/profile.md`, and provides `recall_memory`, `save_memory`, `forget_memory`.
- `Runtime.Run` reads only the current active `session/summary.md` and `long_term/profile.md` for prompt context. Long-term memory retrieval mainly depends on the model calling tools; it is not stable input for every planning pass. Closed sessions under `session_archive/` are logs and are not prompt or recall context.
- Agent loop is split into `planner`, `executor`, `verifier`, and uses a `default` / `plan` / `execution` three-phase state machine. Simple tasks in `default` have planner directly call tools and finish; complex tasks go through `enter_plan_mode` -> `commit_plan` into `execution`, then `executor` / `verifier` collaborate. `planner` and `verifier` can see history and world state; `executor` is deliberately limited to only executing the planner's committed `next_step`.

The new design should preserve this layering: automatic retrieval goes to `planner` and `verifier`, don't expose all historical experience directly to `executor`.

## Design Goals

1. Automatically retrieve device experience before running, reducing warm-up cost for each task.
2. Automatically write task episodes after running, saving goal, environment, state sequence, tool sequence, result, and experience.
3. Long-term memory supports TTL, evidence, applicability conditions, and conflict handling, avoiding expired experience polluting planning.
4. Similar successful tasks go to planner, failed experience goes to verifier; if a separate policy role is added in the future, the same failed experience can be directly routed to policy.
5. Continue using existing filesystem persistence, atomic writes, index rebuilding, and lifecycle validation approach; do not introduce database yet.

## Overall Structure

Add `MemoryPlane` as runtime internal orchestration layer. It is not a model tool, but a fixed pre/post step of `Runtime.Run`.

```text
RunRequest
  │
  ▼
MemoryPlane.Retrieve
  │
  ├─ active session summary/profile
  ├─ device/app/procedure/calibration/failure memory
  └─ task episode retrieval
  │
  ▼
phased role loop (default / plan / execution)
  │
  ▼
TaskEpisodeWriter
  │
  ├─ write task episode
  ├─ extract reusable procedures
  ├─ extract failure memories
  └─ mark conflicts / refresh validation
```

Suggested new code boundaries:

| File | Responsibility |
| --- | --- |
| `memory_plane.go` | `MemoryPlane.Retrieve`, role memory context routing, ranking |
| `device_memory.go` | device/app/procedure/calibration/failure schema, store, search |
| `task_episode.go` | `TaskEpisode`, `TaskEpisodeStore`, `TaskEpisodeWriter` |
| `memory_lifecycle.go` or extend `lifecycle.go` | TTL, expiration cleanup, traceability verification |
| Extend `runtime.go` | Retrieve before building role profiles, CommitEpisode after run ends |
| Extend `role_profile.go` | planner/verifier use different memory context |
| Extend `role_executor.go` | Output structured planner/verifier/tool trace to writer |

## Storage Layout

Continue using `/userdata/agent/memory`, add `device` and `episodes`. Existing directories remain compatible.

```text
/userdata/agent/memory/
├── default.json                 # existing conversation window snapshot
├── session/
│   ├── events.jsonl             # existing hot session events
│   ├── summary.md               # existing compressed summary
│   └── chunks/
├── session_archive/
│   └── <closed_session_id>/      # closed session logs, excluded from active context
├── long_term/
│   ├── profile.md               # existing user profile
│   ├── index.yaml
│   └── memories/
├── device/
│   ├── profile.yaml             # Device Profile
│   ├── apps/
│   │   ├── index.yaml
│   │   └── <app_id>.yaml        # App Profile
│   ├── procedures/
│   │   ├── index.yaml
│   │   └── <procedure_id>.yaml
│   ├── calibration/
│   │   ├── index.yaml
│   │   └── <calibration_id>.yaml
│   └── failures/
│       ├── index.yaml
│       └── <failure_id>.yaml
├── episodes/
│   ├── index.yaml
│   └── <yyyy>/<episode_id>/
│       ├── episode.yaml
│       ├── events.jsonl
│       └── artifacts/
└── lifecycle/
    ├── retention.yaml
    ├── tombstones.jsonl
    └── conflicts.jsonl
```

MVP can continue writing reusable experience to `long_term/memories`, using new `type` to distinguish `device_profile`, `app_profile`, `procedure`, `calibration`, `failure`, `task_episode_summary`. But episode bodies should be independently placed in `episodes/` to avoid stuffing complete traces into long-term markdown.

## MemoryPlane API

Runtime should only depend on a narrow interface:

```go
type MemoryPlane interface {
    Retrieve(ctx context.Context, req MemoryRetrieveRequest) (MemoryContext, error)
    NewEpisodeRecorder(req MemoryRetrieveRequest, retrieved MemoryContext) *EpisodeRecorder
    CommitEpisode(ctx context.Context, episode TaskEpisode) error
}
```

`MemoryRetrieveRequest`:

```go
type MemoryRetrieveRequest struct {
    Input        string
    Attachments  []InputAttachment
    Skills       []string
    ToolNames    []string
    DeviceID     string
    CurrentHints CurrentEnvironmentHints
}
```

`CurrentHints` should not actively operate on devices. It only carries information already known to runtime, such as configured frame socket, most recent screenshot dimensions, last recognized app/page. Whether to take screenshots should still be decided by planner.

`MemoryContext` splits by role:

```go
type MemoryContext struct {
    Planner  RoleMemoryContext
    Verifier RoleMemoryContext
    Common   RoleMemoryContext
}

type RoleMemoryContext struct {
    DeviceProfile      []MemoryHit
    AppProfiles        []MemoryHit
    Procedures         []MemoryHit
    SimilarEpisodes    []MemoryHit
    CalibrationNotes   []MemoryHit
    FailureModes       []MemoryHit
    Conflicts          []MemoryHit
}
```

Routing rules:

- `planner` receives Device Profile, App Profile, Interaction Procedures, Calibration Memory, similar successful Task Episodes.
- `verifier` receives Failure Memory, conflicting memories, validation experience related to the task, and key evidence references used by planner.
- `executor` does not receive global memory by default; it still only sees `next_step`, latest world state, and previous step local result. When experience needs to be used, planner compresses experience into specific next steps.

Current code has no independent `policy` role, so failure experience goes to `verifier` first. When policy is added in the future, can directly reuse `MemoryContext.Verifier.FailureModes` as `PolicyHints`.

## Data Models

### Device Profile

Device-level stable information, low-frequency updates.

```yaml
id: device_default
type: device_profile
status: active
device_id: default
model: ""
os_name: android
os_version: ""
language: zh-CN
screen:
  screenshot_width: 640
  screenshot_height: 1200
  density: unknown
confidence: 0.8
evidence_refs:
  - type: episode
    id: ep_xxx
ttl: 90d
updated_at: "..."
```

### App Profile

Describes app name, aliases, entry points, login status, common page structure, and dialogs.

```yaml
id: app_wechat
type: app_profile
status: active
device_id: default
app_id: wechat
display_names: ["WeChat", "weixin"]
aliases: ["wx", "we chat"]
open_methods:
  - method: system_search
    query: weixin
    success_count: 3
    failure_count: 0
known_entries:
  search_box: "top search field on main page"
common_dialogs:
  - permission_contacts
login_state: unknown
applicability:
  language: zh-CN
  screen: "640x1200"
confidence: 0.75
ttl: 30d
```

### Interaction Procedure

Verified operational procedures. Not a complete episode, but reusable strategy extracted from episodes.

```yaml
id: proc_open_app_system_search
type: procedure
status: active
scope: device
title: "Open apps through system search"
goal_pattern: "open_app"
steps:
  - "Use touch_gesture home."
  - "Open system search."
  - "Input pinyin or English alias with keyboard_text."
  - "Select the visible app result."
applicability:
  device_id: default
  language: zh-CN
  input_method: pinyin
evidence_refs:
  - type: episode
    id: ep_xxx
success_count: 4
failure_count: 1
confidence: 0.82
ttl: 45d
```

### Calibration Memory

Device control chain experience, must include applicability conditions and evidence.

```yaml
id: cal_normalized_coord_reliable
type: calibration
status: active
title: "Prefer normalized coordinates"
content: "normalized coordinates align better with current screenshot than pixel coordinates."
applicability:
  screen: "640x1200"
  hid_profile: default
evidence_refs:
  - type: episode
    id: ep_xxx
confidence: 0.9
ttl: 30d
```

### Failure Memory

Failure modes go to verifier/policy, avoiding repeated bad paths.

```yaml
id: fail_direct_chinese_input
type: failure
status: active
failure_mode: "keyboard_text_cannot_input_chinese"
trigger:
  tool: keyboard_text
  input_contains_non_ascii: true
lesson: "Use pinyin or English keywords, then select candidate/search result."
applicability:
  device_id: default
  language: zh-CN
evidence_refs:
  - type: episode
    id: ep_xxx
    event_ids: ["evt_1", "evt_2"]
confidence: 0.95
ttl: 60d
```

### Task Episode

Complete task experience, used for similar task retrieval and experience extraction.

```yaml
id: ep_20260601_xxx
status: active
started_at: "..."
ended_at: "..."
user_goal: "Open WeChat, find Zhang San, prepare to send message"
normalized_goal:
  intent: open_contact_chat
  app: WeChat
  target: Zhang San
device_scope:
  device_id: default
  language: zh-CN
  screen: "640x1200"
initial_state:
  summary: "home screen"
  screenshot_ref: artifacts/initial.jpg
outcome:
  success: true
  final_state: "Zhang San chat page"
  verifier_reason: "visible chat title matches target"
retrieved_memory_refs:
  - proc_open_app_system_search
reusable_lessons:
  - "Opening WeChat via system search is more stable than desktop paging."
failure_causes: []
conflicts: []
```

`events.jsonl` records complete sequence:

```json
{"type":"planner_decision","plan":["home","system search","open WeChat"],"next_step":"go home"}
{"type":"tool_call","tool_name":"touch_gesture","tool_input":"{\"type\":\"home\"}"}
{"type":"tool_result","tool_name":"touch_gesture","is_error":false,"screenshot_ref":"artifacts/step_001.jpg"}
{"type":"verifier_decision","can_finish":false,"reason":"contact not open yet"}
```

Episodes should not long-term save large base64 blocks. Screenshots go to `artifacts/`, events only contain relative paths, dimensions, hash, necessary summary, and short evidence excerpts.

## Retrieval Strategy

`MemoryPlane.Retrieve` has three steps:

1. Parse query: Extract intent, app, entities, person names, operation type, tool requirements from user goal. MVP can reuse `MemoryExtractionConfig` tag/entity rules and supplement app alias table; can add lightweight LLM query normalizer later.
2. Candidate recall: Recall active and unexpired candidates from `device/`, `episodes/index.yaml`, `long_term/index.yaml`.
3. Sort and trim: Keep a small number of high-value results per category to avoid prompt inflation.

Suggested scoring:

```text
score =
  0.35 * goal_similarity
+ 0.20 * entity_or_app_match
+ 0.15 * applicability_match
+ 0.10 * confidence
+ 0.10 * recency
+ 0.10 * validation_score
- 0.30 * conflict_or_stale_penalty
```

Hard filtering:

- `status != active` does not enter normal retrieval.
- `expires_at < now` does not enter prompt, but can enter background refresh candidates.
- When `applicability` obviously does not match current device, does not enter prompt.
- Memories with missing evidence or broken traceability are downweighted; key calibration memories with missing evidence are not used.

## Prompt Injection Format

Replace current single `memoryContextForPrompt()`, generate role-specific context:

```text
# Retrieved Device Experience

## Device/App Context
- [mem_id confidence=0.82] ...

## Applicable Procedures
- [proc_id] When goal is ..., prefer ...

## Similar Successful Episodes
- [ep_id] Goal: ... Path: ... Outcome: ...

## Calibration Notes
- [cal_id] ...
```

Verifier additional injection:

```text
# Known Failure Modes And Conflicts

- [fail_id confidence=0.95] Trigger: ... Avoid/verify: ...
- [conflict_id] Memory A conflicts with Memory B. Do not rely on either unless current observation proves it.
```

Role constraints:

- planner can use experience to modify plans, but must still be based on current screenshot and tool results.
- verifier cannot approve this completion just because historical episode succeeded, must require current observation to prove completion conditions.
- executor does not directly see experience, reducing probability of it bypassing planner or having blind spots based on old experience.

## Write Strategy

`TaskEpisodeWriter` executes after `Runtime.Run` ends, writing for both success and failure.

Success episode writes:

- User goal, completion criteria, final state.
- Planner decision sequence and plan revisions.
- Tool call sequence, tool results, post-action screenshot refs.
- Verifier final approval reason.
- Reusable experience candidates, e.g. more stable entry points, verified coordinate systems, wait times.

Failed episode writes:

- Failure phase: planning, tool_error, state_mismatch, verification_timeout, max_iterations, model_parse_error.
- Failure reason: tool error, page unchanged, dialog blocking, input not accepted, OCR/vision uncertain, repeated taps ineffective, etc.
- Negative experience candidates go to Failure Memory.
- If failure occurs after using a procedure, lower that procedure's confidence or increase `failure_count`.

Write timing:

- Normal completion: synchronously write episode metadata and event summary, background extract long-term experience.
- Run error or timeout: write partial episode as much as possible, `outcome.success=false`.
- If writer fails, only log, don't affect user's final response.

## Conflict Handling

Long-term memory adds unified lifecycle fields:

```yaml
status: active | replaced | deleted | conflicted | expired
ttl: 30d
expires_at: "..."
applicability: {}
evidence_refs: []
last_validated_at: "..."
success_count: 0
failure_count: 0
conflicts_with: []
supersedes: ""
superseded_by: ""
```

Conflict sources:

- New observation clearly contradicts active memory.
- Under same app/goal/procedure, success path and failure path negate each other.
- Current device profile changes, e.g. language, resolution, input method changes.
- Verifier fails multiple times due to same experience.

Handling rules:

1. More specific new evidence for same fact supersedes old memory first.
2. When cannot determine who is correct, mark both as `conflicted`, don't enter planner's normal experience, only enter verifier's conflict reminder.
3. Expiration is not deletion. First mark `expired` or filter from index, then GC deletes according to retention.
4. User explicit instructions have higher priority than automatic experience. When conflicting with user rules, automatic experience is downweighted or marked conflict.

## TTL Recommendations

| Type | Default TTL | Renewal Condition |
| --- | --- | --- |
| Device Profile | 90d | Re-verified by current screenshot/configuration |
| App Profile | 30d | App opened, page structure or entry re-verified |
| Interaction Procedure | 45d | Process succeeded and verifier approved |
| Calibration Memory | 30d | Coordinates, waits, screenshot dimensions re-verified |
| Failure Memory | 60d | Same type of failure occurs again |
| Task Episode | 30d full trace, 180d summary | Retain evidence excerpt if referenced by long-term memory |

`LifecycleManager.Verify` should extend to:

- Rebuild all indexes.
- Filter expired/conflicted/replaced memory.
- Check if episode/artifact references exist.
- Downgrade long-term memories missing original traces to `traceability: excerpt_only`.
- Clean unreferenced episode artifacts according to retention.

## Integration Points with Existing Code

### Runtime

In `Runtime.Run`, execute before `r.buildRoleProfiles(...)`:

```go
retrieveReq := MemoryRetrieveRequest{
    Input:       normalizedInput,
    Attachments: req.Attachments,
    Skills:      skillNames,
    ToolNames:   toolNames(availableTools),
}
memoryContext, _ := r.memoryPlane.Retrieve(ctx, retrieveReq)
recorder := r.memoryPlane.NewEpisodeRecorder(retrieveReq, memoryContext)
```

Then pass `memoryContext` to `buildRoleProfiles`. After run ends:

```go
episode := recorder.Finish(output, metrics, err, tags, entities)
go r.memoryPlane.CommitEpisode(context.Background(), episode)
```

### Role executor

`roleLoopState` already has objective, criteria, plan, tool steps, verifier results, and world state. Need to supplement:

- Parsed planner decision event.
- Parsed verifier decision event.
- Initial/final world state summary.
- Tool result screenshot artifact export hook.
- Max iteration / parse error / tool error failure reason.

Suggest recording through `EpisodeRecorder` interface rather than inferring from HTTP server's `history`. Server history is a UI artifact and should not be the sole source for memory writer.

### Role profiles

`buildRoleProfiles` receives structured `MemoryContext` and returns `RoleProfiles`:

```go
func buildRoleProfiles(..., memory MemoryContext) RoleProfiles
```

The planner prompt receives `memory.Planner` plus `memory.Common`. The verifier prompt receives only `memory.Verifier.FailureModes` and `memory.Verifier.Conflicts` as a caution block; `memory.Common` is not injected into verifier. The executor prompt receives no memory context.

### Tools

Existing `recall_memory`, `save_memory` continue to be available for model and user explicit memory use. Automated task experience does not require model to actively call tools.

Optional new read-only tools:

- `recall_device_memory`: For debugging, view Device/App/Procedure/Failure.
- `inspect_episode`: For debugging, view trace by episode id.

These two tools are not MVP required; core path should be automatically retrieved by runtime.

## MVP Phases

Phase 1: Episode recording and retrieval injection

- Add `TaskEpisodeStore` and `TaskEpisodeWriter`.
- Record planner/tool/verifier trace.
- Add `MemoryPlane.Retrieve`, first do keyword recall from `long_term` and `episodes/index.yaml`.
- Planner/verifier inject retrieved context by role.

Phase 2: Device memory types

- Add `device/` store and schema.
- Automatically extract App Profile, Procedure, Calibration, Failure from episodes.
- Extend `LongTermMemoryStore` frontmatter: TTL, applicability, evidence refs, conflict fields.

Phase 3: Conflict and lifecycle

- Extend `LifecycleManager.Verify` and retention GC.
- Filter expired/conflicted/replaced during retrieval.
- Writer updates confidence, success_count, failure_count based on success/failure.

Phase 4: Benchmark

- Add episode memory benchmark: does second execution of same task type reduce tool steps, does it avoid historical failure paths.
- Add conflict benchmark: old experience should not continue to pollute planner after language/resolution changes.
- Add failure experience benchmark: failure modes should enter verifier, preventing repeated approval of completions without evidence.

## Acceptance Criteria

- After clearing session conversation, similar tasks on same device can still obtain experience from episode/procedure.
- Similar successful episodes will change planner's plan or next_step, and memory ref is visible in role_output.
- Known failure modes enter verifier prompt; verifier will not pass current task based solely on historical success experience.
- Expired, conflicted, or inapplicable memories do not enter planner's normal experience.
- Task failures also produce episodes and can extract searchable failure memory.
- Memory write failures do not cause user task failure, but must have logs.

---

## Implementation Progress (2026-06-02)

### Completed: Phase 1 + Phase 2 Core

#### Episode Recording and Retrieval (Phase 1)

✅ Complete episode recording implemented:
- `TaskEpisodeStore` and `EpisodeRecorder` fully implemented
- Record planner/tool/verifier trace, generate structured `events.jsonl`
- `MemoryPlane.Retrieve` recalls from `long_term`, `device`, `episodes` and routes by role
- Planner/verifier inject retrieved context by role

#### Device Memory Types (Phase 2 Core)

✅ All device memory types implemented:

**1. Device Profile**  
- Automatically records screen dimensions, language, and other device info
- Infers and updates device profile from episodes

**2. App Profile (Enhanced)**  
- **Cumulative updates**: Accumulates `pages_seen`, `tools_used`, `success_count` across multiple episodes
- **Failure tracking**: Records `known_issues` list
- **Readable content**: Renders as "Pages observed: Home, Cart / Tools used: launch_app, touch_gesture"
- Supports ASCII-safe paths (handles Chinese app names)

**3. Procedure (Enhanced)**  
- **Action detail storage**: New `ProcedureStep` structure records for each step:
  - Tool name, content (from tool_call event's `content`)
  - Coordinates (`x=500,y=850`), input text
  - app_name, page_name, outcome_note
- **Page-based indexing**: procedure ID changed to `proc_<hash(app, page, goal)>`, page_name enters entities/tags
- **Structured rendering**: Expands first 5 steps in prompt, showing complete operation path

**4. Navigation Memory (New)**  
- Extracts **page transition rules**: `Meituan/Home → Meituan/Cart`
- Records tools, coordinates, tool-call content, decoupled from specific task goals
- Routes to Planner at same level as procedure

**5. Calibration Memory**  
- Records calibration info like normalized coordinate preference
- Includes applicability conditions and evidence refs

**6. Failure Memory**  
- Writes failure-type memories on failure
- Routes to Verifier to indicate known failure modes

#### Data Extraction Capabilities

New `episode_extraction.go` provides complete episode event parsing:

- **Tool call parsing**:
  - `extractToolCallCoords`: Parse tap/swipe coordinates
  - `extractToolCallText`: Extract input text parameters

- **Step extraction**:
  - `episodeProcedureSteps`: Pair tool_call with tool_result, absorb verifier observed_state
  - `summarizeProcedureSteps`: Generate LLM-friendly multi-line summary

- **Statistical analysis**:
  - `observedPagesByApp`: Collect pages that appeared for each app
  - `observedToolsByApp`: Count tools used for each app
  - `pageTransitions`: Extract page transition rules

#### Retrieval and Rendering

- ✅ `routeHit`: Route by memory type to different Planner/Verifier fields
- ✅ `renderMemoryHitLine`: Expand Steps rendering for procedure/navigation
- ✅ `RenderForRole`: Generate different memory context by role

#### Directory Structure

Complete directory layout implemented:
```text
memory/device/
├── profile.yaml           # device profile
├── apps/                  # app profile (cumulative updates)
├── procedures/            # task procedure (with Steps)
├── navigation/            # page transition rules (new)
├── calibration/
└── failures/

memory/episodes/
├── index.yaml
└── <yyyy>/<episode_id>/
    ├── episode.yaml
    └── events.jsonl
```

#### Test Coverage

New `device_memory_enhancements_test.go` covers:
- ✅ Procedure Steps extraction, coordinate parsing, tool-call content preservation
- ✅ Page_name indexing and entities include page
- ✅ Navigation rule extraction
- ✅ App profile cumulative across episodes
- ✅ Recording known_issues on failure

All existing tests pass.

### Remaining: Phase 3 + Phase 4

#### Conflict and Lifecycle (Phase 3)

🔲 Base fields added (`success_count`, `failure_count`, `conflicts_with`, `last_validated_at`), but automatic conflict detection logic needs enhancement:
- ⏳ Automatically mark as conflicted based on multiple failures
- ⏳ Hard filter conflicted/expired memory during retrieval
- ⏳ Update confidence of referenced memories (partially implemented in `updateReferencedMemoryOutcomes`)

#### Benchmark (Phase 4)

🔲 Need to add automated benchmarks:
- ⏳ Second execution of same task type reduces tool steps
- ⏳ Failure experience prevents verifier misjudgment
- ⏳ Old experience does not pollute after device environment changes

### Changed Files

| File | Changes |
|------|---------|
| `device_memory.go` | Extended `DeviceMemoryItem` (Steps, AppName, PageName, PagesSeen, ToolsUsed, KnownIssues); new `ProcedureStep`; new `Get()` method; support navigation/ui_anchor directories |
| `memory_plane.go` | Rewrote `extractDeviceLessons`, added 6 helper functions; extended `routeHit` and `renderMemoryHitLine`; new `mergeUniqueStrings`, `appendUniqueMemoryRef` |
| `task_episode.go` | `TaskEpisodeEvent` records tool-call content; `RecordExecution` writes content from action log |
| `episode_extraction.go` | **New file**: Complete tool call parsing, step extraction, page/tool statistics, transition rule extraction |
| `device_memory_enhancements_test.go` | **New file**: 5 test cases covering all enhancement points |

### Effect Comparison

| Scenario | Design Goal | Current Implementation |
|----------|-------------|------------------------|
| Similar task reuse | After "order Mixue Ice City on Meituan", executing "order Starbucks on Meituan" can reuse navigation | ✅ Hits "Meituan/Home→Cart" navigation + "Cart" page procedure |
| Planner sees procedure | Detailed steps, coordinates, page transitions | ✅ "step 1: launch_app(Meituan)→Home / step 2: touch_gesture(@x=500,y=850)→Cart" |
| App prior knowledge | First time using an app can see commonly used pages and tools | ✅ app_profile accumulates pages_seen, tools_used |
| Failure experience tracking | Failure modes enter verifier | ✅ failure memory routes to Verifier.FailureModes |
| Page-level indexing | Retrieve procedure by page_name | ✅ procedure ID includes page, page_name in entities/tags |

### Differences from Design Doc

1. **UI Anchor Not Implemented**: Directory and type reserved, but "accumulate normalized coordinates from successful taps" logic not yet implemented
2. **Conflict Detection Partially Implemented**: Base fields complete, automatic detection logic has partial implementation in `updateReferencedMemoryOutcomes`, needs further refinement
3. **Query Normalizer Simplified**: Currently directly uses keyword matching + inferEpisodeApps, no independent LLM query normalizer

### Next Optimization Directions

1. **UI anchor implementation**: Accumulate normalized coordinates from successful taps, record "element usually at (x,y) under certain app/page"
2. **Enhanced conflict detection**: Proactively lower old procedure confidence when device environment (language/resolution) changes
3. **Semantic retrieval**: Integrate lightweight embedding to improve recall precision
4. **Benchmark coverage**: Verify actual impact of memory on task success rate and step count
