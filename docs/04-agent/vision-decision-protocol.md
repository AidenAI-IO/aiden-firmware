# Screenshot decision protocol

`open_app` and text input use `requestVisionDecision` to turn screenshot analysis
into typed decisions. This boundary is shared by input-mode probing, probe
cleanup, field verification, candidate selection, app search, app-open
confirmation, and paste-menu detection.

## Three separate contracts

1. **Provider request/response.** The Chat Completions adapter puts
   `response_format: {"type":"json_object"}` at the request's top level, alongside
   `messages`. It reads generated text from `choices[0].message.content`. The
   format configuration is not an output schema or part of the screenshot prompt.
2. **JSON syntax.** JSON mode asks for a JSON object. An object such as
   `{"type":"json_object"}` or `{}` is valid JSON but contains no task decision.
3. **Operation contract.** A parser checks required fields, types, enums and
   action bounds before the caller receives a decision. JSON mode cannot replace
   this validation, and validation cannot prove that a visual judgment is correct.

## Operation contracts

Required fields must be present and non-null. Explicit `false` and `0` are valid
values, distinct from absent fields. Additional fields are ignored for forward
compatibility. A single surrounding Markdown code fence is accepted; prose,
multiple JSON values, arrays and `null` are rejected.

| Operation | Required decision fields | Additional validation |
| --- | --- | --- |
| `ime_probe` | `mode`, `typed_a_visible`, `inline_preedit_visible`, `candidate_popup_visible`, `cjk_candidate_visible`, `onscreen_keyboard_visible` | Recognized input mode; composition evidence overrides a contradictory ASCII classification |
| `probe_cleanup` | `probe_character_visible`, `cleanup_safe` | A remaining probe character must be confirmed safe to remove |
| `screen_analysis` | `observed_mode`, `field_text`, `target_matched`, `composition_pending` | Recognized input mode; optional IME-switch hints default to false |
| `candidate_action` | `action`; `select` also requires `offset`, `text`, `completes_part` | Action is `none`, `select`, `expand`, or `up`; select has nonempty text and an integer offset within ±20 |
| `app_search`, `paste_menu` | `found`; true also requires `tap_point.x`, `tap_point.y` | Finite coordinates within 0–1000; missing coordinates never default to zero |
| `app_open_confirmation` | `opened` | Missing decision never becomes `opened=false` |

`label`, `reason` and `evidence` are optional diagnostics. Each operation's prompt
describes the expected object and the visual criteria for its decisions.

## Retry and action boundaries

The shared boundary makes at most three model requests within one 45-second
deadline. A missing/empty choice, incomplete finish status, unexpected tool call,
invalid JSON, or failed operation contract triggers a retry with validation
feedback. The retry keeps the same screenshot(s), model and provider options.
It performs no HID actions. Transport errors and cancellation are propagated
without this validation retry.

Exhaustion returns `visionDecisionResponseError`, including operation and attempt
count. It never returns a successful negative observation. Valid `found=false`,
`opened=false`, `mode=unknown` and `action=none` are returned immediately for the
flow to handle; a later screenshot after waiting or acting is a new observation.
There is no provider-specific response-content match or automatic JSON-mode
disablement. The hardware-flow layer does not wrap this boundary in another
protocol retry loop.

For `open_app`, successful local text input already confirms committed text, so
the flow reuses that decision. Unverified local input is inspected while the
keyboard isolation batch is still active, allowing pending composition to be
selected and verified before a pointer action restores HID. Only a validated
search result can authorize its tap. After tapping, the flow waits and takes a
new screenshot to confirm the target app opened.

Local search input owns the entire query field. After its temporary `a` input-mode
probe, it clears the field with Select All + Backspace before typing the query,
without Undo or a pointer action. Undo may restore the previous search term when
the probe is still uncommitted IME text. This replacement intent is internal to
the search call; ordinary `enter_text` preserves its existing probe cleanup
behavior because a caller may be appending to existing content. A failed clear
stops query typing.

Logs distinguish `phase=vision_response`, `phase=vision_contract`,
`phase=result_tap` and `phase=open_confirm`. A contract failure before
`result_tap start` means no result tap was requested. A completed tap followed by
an unsuccessful visual confirmation is a separate action/observation failure.

Text-only IME partition and keystroke planning retain their separate exact-text
contracts and bounded retries; they do not use the screenshot boundary.
