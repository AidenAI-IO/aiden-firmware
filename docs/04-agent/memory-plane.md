---
sidebar_position: 9
---

# Memory Plane

The memory plane records task execution, stores reusable experience, and keeps different kinds of memory separate. It complements [Session Memory Compaction](session-memory.md), which manages the active conversation window.

## Memory Layers

| Layer | Purpose | Default path |
| --- | --- | --- |
| Session memory | Recent conversation events, summaries, and compressed chunks | `/userdata/agent/memory/session/` |
| Session archive | Closed-session logs available through session recall | `/userdata/agent/memory/session_archive/` |
| Long-term memory | User profile, preferences, rules, facts, and reusable procedures | `/userdata/agent/memory/long_term/` |
| Device memory | Device, app, navigation, procedure, calibration, and failure experience | `/userdata/agent/memory/device/` |
| Task episodes | Per-task metadata, event traces, and captured artifacts | `/userdata/agent/memory/episodes/` |

The runtime does not place every stored memory into every prompt. Long-term and device memories are recalled on demand so unrelated or stale records do not consume the normal conversation context.

## Runtime Flow

For each Agent run, the runtime:

1. Continues or rotates the active session according to the session-boundary rules.
2. Starts a task episode and appends runtime events as the task proceeds.
3. Lets the Agent recall session, long-term, or device memory when it needs prior context.
4. Records the final outcome and closes the episode.
5. Extracts reusable procedures, app knowledge, navigation facts, calibration notes, or failure patterns from tasks that contain an execution trace.
6. Updates confidence only for memories that were actually returned by a recall tool during that run.

Episode persistence is best effort for Agent execution: a maintenance or extraction failure is logged without replacing the result of the user task.

## Recall and Management Tools

| Tool | Use |
| --- | --- |
| `recall_session_chunks` | Search compressed history from the active session and archived sessions. |
| `recall_memory` | Search long-term preferences, rules, procedures, facts, and profile information. |
| `save_memory` | Store durable user-provided or observed long-term information. |
| `forget_memory` | Delete a long-term memory by ID. |
| `recall_device_memory` | Inspect device profiles, app profiles, procedures, navigation, calibration, failures, and conflicts. |
| `inspect_episode` | Inspect one stored task episode and its compact event trace. |

Recall results include stable IDs. When a recalled memory influences a task, the episode records that ID so later success or failure can update the correct record.

## Task Episodes

An episode captures:

- the user goal and device scope;
- start and end timestamps;
- tool calls, results, observations, and screenshots;
- the final answer and verifier outcome;
- failure causes and referenced memory IDs;
- tags and entities used for later search.

The on-disk layout is:

```text
memory/episodes/
├── index.yaml
└── <year>/
    └── <episode_id>/
        ├── episode.yaml
        ├── events.jsonl
        └── artifacts/
```

Episodes begin with `running` status. A completed run is indexed for search; unfinished runs found during recovery are marked as interrupted. Sensitive tool results are omitted from persisted event content where the runtime identifies them as sensitive.

## Automatically Extracted Experience

After a task with an execution trace completes, the memory plane derives records from observed evidence.

### Successful tasks

- A long-term procedure summarizing the verified tool path.
- A device profile when screenshot dimensions were observed.
- App profiles containing observed pages and tools used.
- A structured procedure with tool parameters and observed page transitions.
- Navigation records for app/page transitions.
- A calibration note when normalized coordinates were used successfully.

### Failed tasks

- A long-term failure pattern with the verifier or runtime failure reason.
- A device failure record tied to the task and its evidence.
- Known issues added to any observed app profiles.

Automatic extraction requires an actual task trace. Plain text conversations without tool execution do not generate device procedures.

## Device Memory Types

| Type | Contents |
| --- | --- |
| `device_profile` | Observed device properties such as screenshot resolution. |
| `app_profile` | Pages seen, tools used, procedure references, and known issues for an app. |
| `procedure` | A verified sequence of tool calls and observations for a goal. |
| `navigation` | An observed transition between apps or pages, including the action that caused it. |
| `calibration` | Device-control guidance supported by successful evidence. |
| `failure` | A failed task pattern that should be treated as a caution. |
| `conflict` | A record whose later outcomes contradict its previous guidance. |

Device memory files carry confidence, priority, timestamps, applicability fields, evidence references, success/failure counts, and an optional TTL.

## Confidence and Conflict Handling

When a recalled memory is followed by a successful task, the runtime increases its success count and confidence and refreshes its expiry. When the task fails, eligible memory types lose confidence and gain a failure count.

A record becomes conflicted when failures dominate its successful evidence. A successful task that contradicts a recalled failure record also marks that failure record as conflicted. Normal device-memory search excludes expired and inactive records; conflicted records are returned only as the `conflict` type so callers cannot mistake them for an active procedure.

Current automatically generated TTLs are intentionally finite:

| Record | TTL |
| --- | --- |
| Device profile | 90 days |
| App profile and failure memory | 60 days |
| Verified procedure | 45 days |
| Navigation and calibration | 30 days |

These values keep observations from being treated as permanent facts after apps, layouts, or device configuration change.

## Search Behavior

Memory search uses terms, tags, entities, type filters, and device scope. Results are ranked by textual relevance, priority, and confidence. This is filesystem-backed indexed search, not vector retrieval.

Use specific app names, page names, or task concepts when recalling device experience. Use `recall_memory` rather than `recall_device_memory` for user preferences and rules, and use `recall_session_chunks` for details from earlier conversation turns.

## Current Boundaries

- Device memories are recalled explicitly; they are not automatically injected into every planning prompt.
- Search is based on normalized text, tags, and entities rather than embeddings.
- Device records represent observed evidence, not guaranteed future UI state. A fresh screenshot remains the source of truth before acting.
- The filesystem layout is the persistence contract; no external database is required.

## Related Documentation

- [Context Lifecycle](context-lifecycle.md)
- [Session Memory Compaction](session-memory.md)
- [Agent Configuration](configuration.md)
- [Tools HTTP API](tools-http-api.md)
