# Session Memory Compaction

This document describes Aiden Agent's session-level conversation memory compaction. The mechanism turns a growing event stream into chunk summaries and keeps only the latest hot window in the planner prompt.

This is separate from the [Memory Plane design](memory-plane.md). Memory Plane handles device and task-episode experience memory; session memory handles the dialogue history for one active session.

## Flow

After each runtime turn, the Agent persists the hot-window events and schedules `MemoryManager.RequestMaintenance`. `MemoryManager.Save` runs the same maintenance path synchronously for callers that use it directly.

```text
read session/events.jsonl under FileLock
  |
  v
shouldCompress? -- no --> leave events unchanged
  |
 yes
  v
planCompaction -- choose cut point (token first, count fallback)
  |
  v
summarize history (with split-turn prefix when needed)
  |
  v
compressEvents --> write chunk and update summary.md / index
  |
  v
re-read session/events.jsonl under FileLock and merge new appends
  |
  v
replaceEvents --> keep only the hot window in events.jsonl
```

The storage layout reuses `/userdata/agent/memory/session/`:

```text
session/
├── events.jsonl          # hot window: recent uncompacted events
├── summary.md            # Rolling Summary + Recent Chunks, injected into the prompt
├── summary_archive.md    # older chunk summaries after summary_max_chunks overflow
└── chunks/
    ├── index.yaml        # chunk index, with optional cut_meta
    └── <chunk_id>.jsonl  # full compacted events, retrievable by recall_session_chunks
```

## Concurrency and Event Preservation

Maintenance may run asynchronously while the next turn is being appended. To avoid blocking the hot path on LLM summarization, `maintainFilesystemMemory` uses a two-phase file-lock pattern:

1. Read the current `events.jsonl` snapshot under `FileLock`.
2. Release the lock while planning the cut point and generating summaries.
3. Re-acquire `FileLock`, re-read `events.jsonl`, and append any events added after the original snapshot to the retained hot window before replacing the file.

`RequestMaintenance` coalesces concurrent requests with a pending flag, so repeated turn completions schedule another maintenance pass instead of running overlapping compactions. After a successful compaction, `lastPromptTokens` is reset to the estimated size of the retained hot window; this prevents the pending pass from immediately re-compacting the same short window with stale pre-compaction token data.

## Compression Trigger

`shouldCompress` has two paths:

- With prompt-token data and a known context window, compaction triggers when either condition is true:
  - `prompt_tokens >= context_window - reserve_tokens`, after clamping reserve to at most half of the window.
  - `prompt_tokens / context_window >= compress_at_percent%`.
- Without prompt-token data, such as cold start, a provider response without usage metadata, or before the first LLM call, compaction triggers when `event_count > count_compress_after_events`.

The prompt-token value is the largest single LLM prompt observed in the latest run. This avoids missing compaction when the planner prompt is large but a later verifier prompt is small.

`context_window` prefers the active model window from `ModelResolver`, so runtime model swaps take effect without restart. Unknown models fall back to `context_window` from `extraction.yaml`.

After compaction, the stored prompt-token value is reset to the estimated size of the retained hot window. This prevents the maintenance loop from immediately compacting again with the stale pre-compaction value.

## Cut-Point Selection

`planCompaction` chooses where to split the event stream. It prefers the token path, falls back to the count path when token selection cannot produce a useful cut, and snaps both paths to a legal cut point.

### Legal Cut Points

Each event is classified by `Type`, falling back to `Role`:

| Class | Event types | Can start the hot window |
| --- | --- | --- |
| `cutTurnBoundary` | `user_input` | Yes. This is a complete turn boundary and does not create a split turn. |
| `cutSplitAllowed` | `assistant_output` / `role_output` / `tool_call` | Yes, but it cuts inside a turn and creates a split turn. |
| `cutForbidden` | `tool_result` / `system_event` / `screen_context` | No. These must remain attached to the preceding event. |

### Token Path

The token path walks backward from the newest event and accumulates estimated tokens until it reaches `keep_recent_tokens`. It then snaps the cut forward to the nearest legal cut point. If all events fit within `keep_recent_tokens`, no token cut is produced.

Token estimation splits text by script. CJK characters count as roughly one token per character, while ASCII and Latin text uses a `chars/4` heuristic. This intentionally avoids undercounting Chinese text in the hot window.

### Count Fallback

When the token path does not produce a useful cut, the count path targets `hot_window_events` recent events and uses `snapToLegalCutAtOrBefore` to avoid opening the hot window on forbidden events such as `tool_result` or `system_event`.

### Leading Context Events

After a cut is selected, adjacent `system_event` and `screen_context` events immediately before the cut are pulled into the hot window. This keeps the hot window from starting with detached ambient context.

### Root User Input Pinning

The earliest `user_input` in the live event stream is treated as the root task objective for the active session. If a cut would move that event into compressed history, maintenance pins it as the first event in the retained hot window instead.

Pinned root input is excluded from the chunk evidence and from the generated summaries. The goal is to keep the original task instruction available verbatim without depending on summary quality. If pinning the root input would leave no compactable events, maintenance advances to the next legal cut point; if no useful cut remains, it skips compaction.

## Split Turns

When the cut lands inside a turn rather than on a `user_input` boundary, the hot window would otherwise start without the user input that caused the assistant/tool output. Split-turn handling preserves that context:

1. `history` is the event range before the turn start and is summarized separately.
2. `turn prefix` is the event range from turn start to cut point and is summarized separately.
3. The chunk summary stores `history + "\n\n---\n\nTurn Context (split turn):\n" + prefixSummary` for recall.
4. The hot window receives a synthetic `system_event` containing the prefix summary for immediate prompt context.

The prefix summary is intentionally written to both the chunk summary and the hot window: the former serves historical retrieval, while the latter serves the next planner prompt.

## summary.md and Rolling Summary

`summary.md` is injected into the planner prompt and has two sections:

```markdown
# Session History (compressed chunks)

## Rolling Summary

<accumulated summaries of older chunks pushed out of the active summary window>

## Recent Chunks

- **chunk_xxx**
  <summary>
```

`summary_max_chunks` controls how many chunk summaries stay in Recent Chunks. When the limit is exceeded, the oldest summary moves to `summary_archive.md` and is folded into the Rolling Summary.

The Rolling Summary currently has a 100-line cap (`maxRollingSummaryLines`). When it overflows, older lines are dropped and a truncation marker is added. A future rolling checkpoint can summarize the Rolling Summary itself.

## Hot-Window Boundary Markers

When compressed history exists (`HasCompressedHistory`, meaning `summary.md` exists), prompt construction wraps the hot window with boundary markers:

```text
=== Recent session context (hot window) ===
...hot window events...
=== End of recent context ===
```

These markers are injected only while building the prompt through `hotWindowBoundaryMemory`. They are never persisted into `ChatMessageHistory`. Persisting them would desynchronize `eventCount` from the real event stream because `Snapshot()` reads history verbatim and `appendSessionEvents()` writes records by index.

## Screenshot Data Scrubbing

Screenshot tool results include base64 image payloads. Before persistence, `stripScreenshotData` removes the `data` field and keeps metadata such as `width`, `height`, `format`, `size`, and `action_output`. Without this, several KB of base64 would inflate hot-window token estimates as ASCII text.

The scrubber is idempotent and is used at every session-memory persistence boundary:

- `sanitizeMessageRecords` before writing the legacy `default.json` snapshot.
- `sessionEventFromRecord` when converting langchain `MessageRecord` values into `SessionEvent` values.
- `SessionMemoryStore.AppendEvent` for direct writes that bypass the conversion path.

`compactToolResultForChatHistory` also uses the same function for the `chat_history` persistence path.

## chunk cut_meta

Chunk index entries may include optional `cut_meta` for token-cut diagnostics. The metadata is only for debugging and benchmarks; it does not affect recall or summary rendering.

| Field | Meaning |
| --- | --- |
| `first_kept_event_id` | Event ID of the first retained hot-window event. |
| `tokens_before` | Estimated tokens across all events before compaction. |
| `kept_tokens_estimate` | Estimated tokens in the retained hot window. |
| `is_split_turn` | Whether the cut landed inside a turn. |
| `turn_start_event_id` | The opening `user_input` event ID for a split turn. |

All fields use `omitempty`, so older indexes and count-path compaction remain YAML-compatible.

## Configuration

Compaction is configured by `<config>/memory/extraction.yaml`. See the [`memory/extraction.yaml` section in configuration.md](configuration.md#memoryextractionyaml).
