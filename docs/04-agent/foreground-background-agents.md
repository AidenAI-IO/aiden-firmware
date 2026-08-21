# Foreground and Background Agents

Realtime voice mode uses two cooperating agents:

- The realtime voice model is the foreground agent. It owns the live
  conversation and must not wait for device operations or long-running work.
- The legacy agent loop is the background agent. It executes queued tasks one
  at a time with the existing runtime tools, memory, and episode recording.

The orchestration layer lives in `internal/agenttask`. It owns task data,
state transitions, queueing, cancellation, and terminal notifications without
depending on `internal/agent`. The daemon supplies a narrow runner adapter that
maps `Run(ctx, prompt)` to the legacy `agent.Runtime`.

The foreground realtime session can be activated by either the physical GPIO
wakeup signal or a text request to `/api/chat`. A text request submitted while
no realtime session is connected stays queued while the daemon connects, then
becomes the first user message in that session. GPIO initialization failure does
not disable `/api/chat` activation, which keeps the same foreground path usable
on PC and other hosts without board GPIO.

## Foreground tools

The realtime model receives this focused catalog:

| Tool | Purpose |
| --- | --- |
| `get_current_time` | Return controller-local date, time, timezone, and UTC offset. |
| `recall_memory` | Recall long-term user preferences, facts, rules, and procedures. |
| `create_agent_task` | Queue background work and return immediately with a task ID. |
| `cancel_agent_task` | Cancel queued work or request cancellation of running work. |
| `query_agent_task` | Read the latest task state and terminal result. |
| `response_user_action` | Resume a background task after the user completes the requested device action. |

`create_agent_task` only enqueues work. The foreground response never waits for
the background agent to start or finish.

## Task lifecycle

Tasks use the following states:

```text
created -> queued -> running -> completed
                            -> failed
                            -> cancelling -> cancelled
                            -> running (waiting for user action)
                 -> cancelled
```

Queued cancellation is immediate. Running cancellation first publishes
`cancelling`; it becomes `cancelled` after the legacy runtime returns from
context cancellation.

## Result delivery

Completed, failed, and cancelled tasks are delivered to the foreground model as
user messages. Delivery follows two rules:

1. A terminal update starts a 500 ms sliding debounce window. Every additional
   update resets that window, so results finishing close together are included
   in one message and one foreground response.
2. An update is injected only while the foreground session is idle. It never
   interrupts live user speech, an active response, or a text request forwarded
   through the realtime chat bridge.

Undelivered updates are returned to the pending queue if the realtime session
ends. The next session can then deliver them.

## User action handoff

`request_user_action` is mode-aware:

- In an ordinary legacy run, it preserves the existing human-handoff response
  (`HUMAN_HANDOFF_REQUESTED`).
- In a background task run, it publishes a pending action while leaving the
  task in `running`. The background agent loop returns at that point, but the
  task is not terminal and its pending action remains queryable.

The foreground agent receives the request when it is idle and tells the user
what to do on the device, including that they should say when it is complete.
After the user confirms completion, the foreground calls
`response_user_action` with the task ID and a concise `user_message`. The
manager clears the pending action and starts another loop on the same backend
runtime/session. The `user_message` is appended as the next user message on
the existing context, so the background agent can verify the new state and
continue its original task. If the realtime session ends before delivery, the
pending action is retained for the next foreground session.
