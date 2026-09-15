# Live Activity Agent Tool Status Design

> Related task: **Aiden — Show More Detailed Real-Time Information in Dynamic Island: Task Status**
> Feishu GUID: `2e106f68-d8a1-4a4b-a833-2de2eecb5896`

## 1. Goals and Boundaries

Dynamic Island should reduce uncertainty while the Agent is working. At any
time, the user should be able to tell what the Agent is doing, which lifecycle
state the current tool is in, and whether the Aiden App or the user must take
over.

The Live Activity must not expose the model's full reasoning or reconstruct its
chain of thought. The thinking state uses safe stage-level copy. Tool states
prioritize the current action and lifecycle rather than raw inputs or results.

The presentation has two layers:

- **Compact:** a glanceable action and status. It does not show the task title,
  raw tool name, URL, or sensitive parameters.
- **Expanded:** the latest status, an optional tool line, and one safe step
  description. It is a snapshot, not a complete event log.

The examples below use English semantic labels. The companion app may localize
those labels for the user-facing interface.

## 2. Compact Presentation

The compact layout is:

```text
[Logo + current action]        [elapsed time / result / wait state]
```

When there is no task, the leading side shows only the logo:

```text
[Logo]                         [Ready]
[Logo]                         [Connected]
[Logo]                         [Connecting]
[Logo]                         [Disconnected]
```

While a tool is actively timed, the trailing side shows its elapsed time
instead of a redundant Running label:

```text
[Logo Check screen]            [8s]
[Logo Open app]                [2s]
[Logo Tap]                     [1s]
[Logo Type]                    [4s]
[Logo Scroll]                  [3s]
[Logo Search]                  [6s]
[Logo Clipboard]               [2s]
```

After completion, failure, or an external wait transition, the trailing side
briefly shows the outcome:

```text
[Logo Check screen]            [Done]
[Logo Open app]                [Failed]
[Logo Retry]                   [Retry]
[Logo Wait for app]            [Open]
[Logo Your turn]               [Take over]
```

Compact action labels must remain short. Recommended semantic mappings are:

| Tool or phase | Compact action |
| --- | --- |
| `screenshot` | Check screen |
| `open_app` / `bridge_open_app` / `open_url` | Open app |
| `touch_gesture` / `quick_action` | Tap |
| `keyboard_text` / `enter_text` | Type |
| `mouse_scroll` | Scroll |
| `web_search` | Search |
| `bridge_clipboard` | Clipboard |
| `bridge_calendar` | Calendar |
| `bridge_contacts` | Contacts |
| `bridge_notification` | Notify |
| Verification phase | Verify |
| Planning or ordinary model-output phase | Processing |
| Answering phase | Preparing result |

A short target name may be included by a localized client, such as Open
Settings. Long targets, URLs, and potentially sensitive values must fall back
to the generic action.

## 3. Expanded Presentation

The expanded view shows the latest state in a small number of lines: a title,
an optional tool/status line, and `current_step`. It does not maintain a
history or expose a separate raw next-step field.

### 3.1 Processing and Thinking

Ordinary model output is represented as `processing`. Only a non-empty
`reasoning_delta` changes the state to `thinking`:

```text
Thinking · 8s

Analyzing the current task and preparing the next action
```

The first version deliberately uses fixed safe copy for thinking. It does not
render `reasoning_content`, a raw reasoning stream, or a generated reasoning
summary in the Live Activity.

After `reasoning_reset`, the state returns to `processing`:

```text
Processing

Processing the request
```

### 3.2 Tool Execution

A tool call takes display priority over model thinking:

```text
Working · Check screen

Tool: Screenshot · Running
Checking the current screen
```

The app calculates elapsed time locally from `tool_started_at` while the tool
is in an actively timed state.

### 3.3 Tool Completion and Verification

```text
Completed · Check screen

Tool: Screenshot · Completed
Screen checked
```

An accepted `open_app` or `open_url` result is not final visual proof. Those
results move to `verifying` until the next screen observation can confirm the
target page.

### 3.4 Retry, Waiting, and Human Handoff

```text
Retrying

Tool: Open app · Retrying
The device did not respond
```

```text
Waiting for Aiden App

Current action: Read clipboard
Open Aiden App to continue
```

```text
Waiting for you

Enter the verification code on the phone
The Agent will continue afterward
```

`retrying` is part of the client contract, but the Agent must receive or emit
an explicit retry progress event before using it. The current implementation
does not infer retry counts from repeated tool calls.

## 4. State Projection

The existing task-level `status` and `phase` fields remain authoritative.
`tool_status` adds the more detailed current lifecycle:

| Runtime event | Projected state |
| --- | --- |
| Task start | `status=running`, `tool_status=processing` |
| `role_output` / `assistant_output` | `processing` |
| Non-empty `reasoning_delta` | `thinking` |
| `reasoning_reset` | `processing` |
| `tool_call` | `running`, or `waiting_user` for a handoff tool |
| Explicit tool progress | `preparing`, `running`, `verifying`, or another supported progress state |
| Successful `tool_result` | `succeeded`; app-launch results use `verifying` |
| Recoverable tool error | `failed` while the task remains `running` |
| Phone Bridge unavailable | `status=needs_app`, `tool_status=waiting_app` |
| User action required | `status=needs_app`, `tool_status=waiting_user` |
| Task completed | `status=completed`, `tool_status=succeeded` |
| Task failed | `status=failed`, `tool_status=failed` |
| Task canceled | `status=canceled`, with `tool_status` omitted |

## 5. Opening a Third-Party App While Phone Bridge Is Offline

When Phone Bridge is connected, `open_app` can be executed directly by the
companion app. When it is unavailable, the existing flow may restore the
Aiden App through a confirmed Dynamic Island return entry and then send the
third-party launch command.

Recommended flow:

1. The Agent determines that Phone Bridge is unavailable but a usable Aiden
   App return path exists.
2. Before restoring Aiden, publish a `preparing` progress state that explains
   that the app is about to change and Aiden is being restored.
3. Restore Aiden through the confirmed Dynamic Island return entry.
4. After the foreground WebSocket reconnects, send the third-party launch
   command. If automatic restoration cannot be confirmed, ask the user to open
   Aiden instead of assuming success.
5. Continue with a screenshot-based observation. An `ok:true` bridge result
   only means that the operating system accepted the launch request.

If no usable return entry exists, the state should explain that Aiden must be
opened manually or that the Agent must use a visible search/HID fallback.
The UI must not claim that the target app opened until the screen has been
observed.

## 6. Protocol Scope

The endpoint and transport remain unchanged:

- `GET /api/live-activity/current`
- coalesced BLE Wake notification
- USB ECM snapshot fetch

The existing state remains available:

```text
status
phase
current_step
current_action
current_target
current_app
last_tool_name
last_error
started_at
updated_at
```

This change adds two backward-compatible optional fields:

| Field | Meaning |
| --- | --- |
| `tool_status` | `processing`, `thinking`, `running`, `preparing`, `verifying`, `succeeded`, `failed`, `retrying`, `waiting_app`, or `waiting_user` |
| `tool_started_at` | Start time used for the current actively timed tool state |

Old app versions ignore unknown fields. The fields are represented in the Go
`LiveActivityState`, the app's TypeScript state, and ActivityKit
`ContentState`.

Raw tool inputs, results, errors, and reasoning must not be copied directly
into compact UI. Display text continues to use bounded, display-safe summaries
and stage copy.

State changes trigger the existing coalesced local notification path, limited
to approximately one BLE Wake every 750 ms. Elapsed time advances locally in
the iOS presentation; the Agent does not need to publish once per second.

## 7. Implementation Responsibilities

### Agent

- Record `tool_started_at` when a `tool_call` begins.
- Publish explicit progress events for Phone Bridge restoration and
  post-action verification.
- Keep ordinary model output (`processing`) distinct from actual streamed
  reasoning (`thinking`).
- Keep launch requests in `verifying` until a later observation can confirm
  the target screen.
- Preserve existing routes, task-level statuses, and local notification flow.

### Companion App / iOS

- Render compact action plus elapsed time or outcome.
- Render the latest expanded title, tool/status line, and safe step.
- Use `tool_started_at` for local elapsed-time updates.
- Localize semantic actions and tool names without exposing raw compact text.
- Preserve the last valid state when iOS coalesces or delays updates.

## 8. Acceptance Criteria

1. The compact standby view shows only the logo and localized ready state.
2. An actively timed tool shows a natural compact action and elapsed time.
3. Ordinary model output and actual reasoning are displayed as distinct
   `processing` and `thinking` states.
4. Running, preparing, verifying, completed, failed, waiting for app, and
   waiting for user states are distinguishable.
5. When Phone Bridge is offline, an app launch shows the restoration/transition
   state before the target app request is sent.
6. The UI does not claim that an app opened before a later screen observation.
7. Compact UI does not expose raw tool names, URLs, sensitive parameters, or
   unbounded text.
8. Delayed or coalesced iOS updates preserve the last valid state without
   showing a blank presentation.
