---
name: device-operator
description: Use when controlling a visible target device UI through screenshots, touch, mouse, keyboard, text entry, scrolling, app switching, or capture recovery.
metadata:
  preferred_model: primary
  allowed_tools:
    [
      screenshot,
      wait_for_stable_screen,
      quick_action,
      touch_gesture,
      wheel_nudge,
      mouse_move,
      mouse_scroll,
      keyboard_tap,
      enter_text,
      open_app,
      open_url,
      request_user_action,
      recall_memory,
      save_memory,
      skill_read,
      shell,
    ]
---

Use this skill when the task requires operating a visible connected device UI. This is the complete generic device-operation playbook; do not split routine app switching, text entry, scrolling, picker, or screenshot recovery work into child skills.

## Core Loop

Always operate through a visual feedback loop:

1. Observe the current screen with `screenshot` or `wait_for_stable_screen`.
2. Decide the smallest next UI action that could make progress.
3. Act with one input or semantic tool.
4. Inspect the resulting screen.
5. Continue only after confirming what changed.

Do not perform multiple blind UI actions in a row. Base every coordinate, tap, swipe, and typed input on the latest visual state.

For actions that were expected to visibly change the UI, treat `screen_changed=false` in a post-action screenshot as "effect not yet verified". Post-action `screen_changed` compares the immediate pre-action screenshot with the final settled screenshot using structural change detection that ignores the top status area and minor image noise. The standalone `wait_for_stable_screen` tool instead reports whether motion occurred during its own wait window. In either case, do not say the action succeeded just because `action_output` is `ok`; inspect the screenshot, compare it with the expected target change, and continue checking or choose a different action if the UI still looks unchanged.

If `touch_gesture` returns `screen_changed=false` and the configured touch mode does not match the target platform, stop instead of retrying blind touches: Android expects `[device].device_type="Android"` (derived `hid.pointer_mode="touchscreen"`), while iOS/iPadOS expects `[device].device_type="iOS"` (derived `hid.pointer_mode="absolute"`). Ask the user to switch `device_type` and restart the agent before continuing.

For cross-app tasks that require extracting data from a source app and entering it into a target app, you must first visually confirm each required value from the source app's latest valid visual observations, such as `screenshot` or `wait_for_stable_screen` results. You may not switch away from the source app or enter any of that data into the target app until this verification is complete. Never invent or fabricate data that was not observed in the source app's UI.

## Tool Choice

Prefer the highest-level reliable tool for the job:

- When the user's intent clearly matches a cataloged semantic action, you MUST use `quick_action`. This includes back, home, app switching or switching back, system/global search, copy, paste, cut, select all, semantic backward/forward deletion, undo/redo, find, send, and browser actions. Use documented fallback fields such as `alternative` and `alternative_index` only when selecting an alternative binding. Use `{"action":"list"}` to inspect the configured device catalog when availability is uncertain.
  - A `ctrl`/`meta` `keyboard_tap` chord is allowed only when the user explicitly asks to press those exact physical keys, the shortcut is app-specific or not cataloged, or a `quick_action` result in the current run explicitly reports the matching action as `reserved`/unavailable before executing a binding.
  - Do not infer that `quick_action` is unavailable from an unrelated tool failure, text-entry failure, stale screenshot, HID problem, or your own assumption.
  - If an active quick action executed but returned failure or produced no visible effect, use a listed alternative or a non-shortcut UI strategy. Never replay the same binding through an equivalent `keyboard_tap` modifier chord.
  - If `ok=true` but the screenshot shows no expected change, treat it as ineffective: try `alternative=true` once when alternatives are listed, otherwise switch tools. Never loop on the same binding.
- When an app icon, app card, or requested control is clearly visible, unique, and unobscured in the latest screenshot, you MUST use `touch_gesture` to tap its visible non-overlapping center. This direct visible-target rule takes priority over `open_app` and system/app search, even when the user phrases the request as "open <app>". Do not call `open_app` merely because it is semantically available.
- Use the standard `touch_gesture` `type` forms for normal taps, long presses, swipes, and drags on mobile or desktop targets, and as a listed or non-shortcut fallback for back/home gestures. Back/home gesture fallbacks are ordinary `type:"swipe"` calls with explicit edge coordinates. Atomic `actions` are a low-frequency advanced option only for an uninterrupted custom contact sequence that no standard gesture type can express; do not use them for ordinary taps, long presses, swipes, scrolling, or draggable targets.
  - Decide whether the request moves a draggable target before selecting the gesture form. Moving an app icon, card, widget, list item, or other draggable UI target MUST use this sequence: call `drag_start` at its current center and let its internal screen-stability wait finish. `drag_start` presses for 500ms, then moves 200 normalized units at 500 normalized units per second (a 400ms interpolated move) in a bounded direction and keeps the contact down. Do not call `wait_for_stable_screen` separately in the normal drag flow. Only when the `drag_start` result reports `screen_stable=true`, inspect its returned stable screenshot and confirm the final destination, then call `drag_release` at that confirmed point. Never determine or guess the destination from an intermediate or `screen_stable=false` result, translate this sequence into atomic `actions`, use the removed one-call `drag` type, or perform another input action while the contact remains down.
- For a numeric picker, use `wheel_nudge` directly from the latest screenshot. Do not tap the selected row to probe for keyboard/edit mode, do not use `enter_text` for picker values, and do not drag picker columns with `touch_gesture`. After a successful wheel nudge, runtime reserves that region so generic input cannot activate a field outside the picker.
- Use `enter_text` for normal text input into fields, including Chinese/CJK, emoji, IME, and verified field entry.
- Use `keyboard_tap` for literal keys such as enter, escape, tab, and arrows; for exact physical chords the user explicitly asks to press; for app-specific shortcuts not represented by `quick_action`; and only for the evidence-gated reserved/unavailable fallback above. When a familiar Ctrl/Cmd chord merely describes a cataloged semantic goal, `quick_action` is mandatory.
- Use `mouse_move` and `mouse_scroll` only when pointer movement or wheel input is specifically appropriate; use `touch_gesture` with `type:"tap"` for coordinate clicks.

If a semantic tool fails, read the message and choose a different approach. Do not retry the same binding unless the tool explicitly offers a distinct alternative.

## Coordinate Discipline

Before using coordinates:

- Inspect the screenshot and identify the intended target visually.
- Use normalized 0-1000 coordinates: `(0,0)` is top-left, `(1000,1000)` is bottom-right, `(500,500)` is center.
- Never pass screenshot pixels directly to a coordinate tool. Convert point measurements from the latest returned image first: `x_normalized = pixel_x / max(screenshot_width - 1, 1) * 1000` and `y_normalized = pixel_y / max(screenshot_height - 1, 1) * 1000`.
- Choose the visual center of the target. For small controls, estimate the control bounds and aim for the midpoint, biased slightly inward.
- Avoid edges unless performing an edge gesture. For phone edge gestures, do not use conservative insets like 50-100: left-edge `back` starts at normalized `x=1`, and bottom-edge `home` starts at normalized `y=999`.
- Do not guess a coordinate if the target is not visible or the screen is stale.
- If a tap misses, observe again before adjusting. Do not repeat the exact same coordinate blindly.

## Text Entry

Use `enter_text` for normal input boxes such as search fields, forms, and chat composers.

Required pattern:

```json
{
  "text": "你好",
  "focus": { "x": 450, "y": 105 }
}
```

- Focus coordinates must come from the latest screenshot.
- Before calling `enter_text`, the latest screenshot must clearly show the actual editable field or composer, and `focus` must be inside that visible field. An app home screen, folder/list view, blank area, or screen that only shows a create/new button is not input-ready; first create/open the document or message and observe its editor.
- Treat `open_app` success as app-open confirmation only. It does not prove an in-app editor or input field is ready.
- Treat `ok:true` as successful text entry. When visual confirmation matters, also inspect the screenshot returned with the tool result.
- `ok:false` includes a next-step suggestion; follow it instead of inferring internal IME state from fields that are not part of the public result.
- For Chinese/CJK composition, provide only the exact target text. `enter_text` derives IME parts and keystrokes internally.
- Never pass Chinese, emoji, or romanization blobs to `keyboard_text`.
- When English text must be entered while a Chinese IME is active, switch to the English/Latin keyboard first, commonly via the globe/input-method key. Do not leave the English text in Chinese IME preedit/candidate state.
- If text remains in the IME candidate/preedit area instead of the field, retry once with corrected focus or follow the returned suggestion.

`enter_text` automatically prefers a usable Phone Bridge clipboard path, then falls back to ordered ASCII and IME runs. If its structured result conflicts with its attached screenshot, treat this as uncertain verification rather than immediate input failure. Call `wait_for_stable_screen` once and compare the requested text with the fresh observation. Preserve the current field while evidence conflicts; do not perform corrective input until the fresh observation identifies a concrete mismatch.

For simple keys:

- Use `keyboard_tap` for literal enter, escape, tab, arrows, or backspace. If the intent is semantic submit/send, use `quick_action`; if the user explicitly asks to press Enter, use `keyboard_tap` with `{"keys":["enter"]}`.
- For semantic backward/forward deletion, use `quick_action` with `delete_backward` or `delete_forward`. Use `keyboard_tap` with `backspace` or `delete` only for an explicitly requested literal key press or the controlled unavailable/reserved fallback above; `delete` is forward-delete.

If text does not appear or appears in the wrong place, stop typing, take a fresh screenshot, re-check focus and field identity, then retry once with corrected focus or input method. If still failing, summarize observed field state and ask for help or use bridge if appropriate.

If `enter_text` reports missing HID devices such as `/dev/hidg0` or `/dev/hidg1`, treat direct local text entry as unavailable in this environment. Take at most one fresh screenshot to confirm the current state; unless the target is already clearly visible and reachable without text entry, stop and report the blocker with the exact tool error and ask for help.

## App Switching and Launch

Use this flow for app switcher, recents, returning to Aiden, and cross-app navigation workflows.

1. Use global `[device].device_type` as the platform authority. Do not re-classify iOS/Android from screenshots; use screenshots only to locate visible controls and verify results.
2. Observe the screen.
3. If the target app icon or app card is clearly visible, unique, and unobscured, tap its visible non-overlapping center with `touch_gesture`.
4. If the target is not directly visible, try `quick_action` for `app_switch`, home, back, or app search when that semantic navigation step is appropriate.
5. Use `open_app` only when the target app is not clearly and reliably tappable in the latest screenshot, or when one direct visible-target tap produced no verified effect. It selects Phone Bridge or visible system search internally.
6. Verify the result with the post-action screenshot before continuing.

Before probing app-switch behavior, call `recall_memory` with tags such as `["app-switch", "device"]`. If a matching calibration exists for the configured `[device].device_type`, use it directly.

If no known quick action or cached method works:

1. Try bottom-edge swipe-and-hold: start near `y≈990`, end around `y≈550`, hold after gesture. Screenshot after the gesture.
2. If Android 3-button nav is visible, tap Recents.
3. If still on the same app, go home once, then retry app switcher/search from the home screen.

After each probe, verify whether the switcher appeared before trying the next method.

Selecting an app:

- If the target app card is visible, tap the visible non-overlapping center of that card.
- If not visible, swipe within the switcher to bring it into view.
- If still not found, dismiss the switcher and use `open_app`, system search, or the home/app drawer.
- If multiple plausible cards appear, ask the user to choose instead of guessing.

After successfully opening the switcher via a non-obvious method, call `save_memory` with the configured `[device].device_type`, method, gesture coordinates, and tags `["app-switch", "device"]`.

On iOS, if Phone Bridge context says the companion app is backgrounded/inactive and `return_entry=dynamic_island`, treat Dynamic Island as the fastest way back to the Aiden App. Do not blind-tap lock-screen Live Activity cards; use screenshot/HID fallback or visual confirmation for those cases. Opening the Aiden App restores the companion app shortcut channel.

## Scrolling and Picker Controls

Swipe `direction` describes finger movement, not content direction:

- `direction:"up"`: finger moves up → viewport scrolls down → reveals content below.
- `direction:"down"`: finger moves down → viewport scrolls up → reveals content above.

Scrollable region discipline:

- Start and end points must be inside the intended scrollable region.
- Avoid fixed headers, bottom navigation bars, modal edges, and screen borders.
- If nothing moves, adjust the start point inward before increasing distance.

Calibration loop:

1. Start with an explicit start/end path and the default `speed:2500` from the latest screenshot.
2. Read the gesture result's automatic post-action screenshot.
3. Use the returned screenshot and its `screen_changed` field to confirm movement.
   An omitted `screen_changed` means the baseline comparison was unavailable; judge the returned screenshot directly.
4. If far from target, increase the start/end distance; if close, shorten it.
5. If overshot, reverse the path and reduce its distance.
6. Do not repeat the same path and speed after a failed attempt.

If the same list boundary appears again, stop searching in that direction. Try search/filter, a different tab, or ask the user.

For picker/wheel controls, discover columns from the current UI rather than assuming hour/minute positions. Give the current visible picker screen a stable `picker_id` (for example `alarm-create` or `date-editor`) and change it after navigating to another picker screen. Before the first `wheel_nudge` in a task, take a fresh screenshot of the current screen; do not reuse picker state remembered from an earlier task or a screen the user may have changed manually. For each column, identify the selected value, target value, adjacent values, `column_x`, `center_y`, and a best estimate of `row_spacing`. `center_y` is mandatory on every `wheel_nudge` call: measure it from the selected center row in the latest screenshot, and never omit it or substitute a fixed default. Runtime independently measures repeated row geometry from the latest screenshot and replaces an inaccurate `row_spacing` estimate when confidence is sufficient. Use numeric values or stable ordinal indices for textual lists.

All `wheel_nudge` geometry uses normalized 0-1000 coordinates. Convert horizontal `column_x` with `pixel_x / max(screenshot_width - 1, 1) * 1000`. Convert vertical `center_y`, the caller's `row_spacing` estimate, and `visible_target_y` with `pixel_y_or_distance / max(screenshot_height - 1, 1) * 1000`; never divide a vertical distance by screenshot width. Runtime uses image-derived row spacing when confident and falls back to the caller estimate otherwise.

Use `wheel_nudge` as the conservative path for the whole picker interaction. Do not tap the selected row to expose a hidden editor and do not use `enter_text` or `keyboard_tap` to change picker values, even if the control appears temporarily editable. Picker focus and replacement behavior varies across apps and cannot be verified reliably from HID success alone. Read the latest screenshot, issue one bounded `wheel_nudge`, then read the returned screenshot before the next move. Direct taps on unselected picker rows and all picker drags also belong to `wheel_nudge`.

- Pass `cycle_size:0` for a non-cyclic ordered column. For cyclic columns, `cycle_size` is the numeric domain span/modulus, not the number of displayed rows: a `00..59` minute wheel with `value_step:5` still uses `cycle_size:60`, while months use `cycle_start:1, cycle_size:12`; calendar-day size depends on the selected year/month.
- `value_step` is the signed value change for one visible row downward. Therefore `value_step > 0` means finger-up increases the selected value, and `value_step < 0` means finger-down increases it.
- Keep `target_value` equal to the user's final requested value for that column across the entire picker interaction. Never replace it with a nearer intermediate row such as changing a requested hour `01` to `15`; runtime locks the first executed target for the active column. Use `visible_target_y` only when that final target itself is visibly adjacent.
- Call `wheel_nudge` with the latest current/target/domain metadata for both exact row selection and bounded movement. Report `value_step` as the signed numeric change for one visible row downward. Runtime derives the shortest row gap, numeric direction, finger movement, and coarse-to-fine travel from the values, so do not calculate `remaining_gap` or supply/guess direction fields. When the target equals a row that is visibly present in the latest screenshot, pass its exact Y coordinate as `visible_target_y`; runtime verifies that it matches one adjacent row before tapping it. Omit `visible_target_y` when the row is not actually visible, and the tool will use a bounded drag instead. A confident runtime row-spacing measurement enables a faster low-inertia profile of at most 6/4/3/2/1 rows for gaps of 9+/5-8/3-4/2/1; low-confidence images retain the conservative profile. If visible ordering is genuinely insufficient, omit `value_step` and let the tool perform one fixed finger-up micro probe; then use the next screenshot to report `value_step`. Runtime expands the column budget after that known-step call.
- Never extrapolate `visible_target_y` beyond rows actually visible in the latest screenshot. If the target is not visibly adjacent, omit it and call `wheel_nudge` again after the bounded drag; do not guess a lower Y coordinate because picker columns often have editable fields directly underneath them.
- Take a fresh screenshot after every tap or nudge and re-read the centered value; runtime recalculates the remaining gap. Stop if the value cannot be measured, a probe produces no change, the screen becomes stale, or the gesture leaves the picker.

## Screenshot and Capture Recovery

Use this only when `screenshot`, post-action screenshots, or capture-related tools fail and visual operation cannot continue.

If `screenshot` fails, output mentions `SERVICE_RECOVERING`, socket errors, empty image data, invalid screenshot JSON, or repeated post-action screenshot failure:

Immediately pause all UI actions (tapping, typing, swiping, navigation) before executing the recovery sequence; no further UI actions may be taken until the capture service is restored and a valid screenshot is obtained.

- Stop tapping, typing, swiping, and guessing from stale visual state.
- Do not claim a UI task is complete without a fresh screenshot proving the target screen.

Recovery sequence:

1. Retry `screenshot` once if the error suggests transient recovery.
2. If it fails again, diagnose frame service:

```bash
/etc/init.d/S52frame_service status
frame_service_cli --socket /run/frame_service/frame_service.sock health
ls -l /run/frame_service/frame_service.sock
```

3. If health reports a bad state or recovering capture manager, request capture-manager restart first:

```bash
frame_service_cli --socket /run/frame_service/frame_service.sock restart
```

4. Verify recovery with health, then call `screenshot` again.
5. If CLI restart fails, socket is missing, or service is not running, restart the init service:

```bash
/etc/init.d/S52frame_service restart
```

6. After service restart, verify in order: service status, health, then `screenshot`.

If recovery still fails, inspect recent logs before asking the user to intervene:

```bash
tail -n 80 /var/log/frame_service/frame_service.log
```

When reporting a blocker, include the screenshot error, recovery commands tried, and latest health or log signal.

## Failed Attempt Handling

Treat an attempt as failed when the expected change did not happen, text was not entered, navigation did not move, the screen changed unexpectedly, or a tool result reports an error.

If an action was expected to change the UI and its returned post-action observation says `screen_changed=false`, no meaningful structural difference was detected between the pre-action and final settled screenshots. Treat that as a failed or unverified attempt until the screenshot itself proves otherwise. Do not report success from tool output alone.

After a failed attempt:

1. Observe with `screenshot`.
2. Compare expected vs observed result.
3. Avoid repeating the exact same failed action; if one repeat is justified, change one variable and verify the result before trying again.
4. Change one variable at a time: target location, gesture type, navigation path, input method, or semantic shortcut.
5. After 2 failed attempts on the same goal, change strategy instead of retrying the same path.
6. After 3 failed attempts total on the same goal, pause repeated UI actions, summarize what changed, then switch to diagnosis, a different path, user-facing blocker, or human handoff if no new evidence suggests progress.

Keep an internal attempt log with goal, attempt, expected result, observed result, and next adjustment. Report it only when blocked or asked.

If the device is detected to be locked and standard unlock gestures (swipe up from bottom, home quick_action) fail twice consecutively, stop repeating unlock gestures; switch to diagnosis or report the locked device as a blocker with the attempts tried. Do not keep repeating unlock gestures unless fresh evidence shows a different unlock path.

## Navigation and Search

- First identify the current screen and app when possible.
- Prefer visible buttons, semantic shortcuts, and search/filter controls over blind scrolling.
- When looking for an app, contact, setting, file, item, or page content, search before long manual browsing.
- Try one alternate search term when reasonable before switching to manual browsing.
- Check each relevant tab, list, or section once before repeating any of them.
- Do not repeatedly search or scroll the same unchanged list.
- If multiple plausible matches appear, ask the user to choose instead of guessing.
- After back, home, app switch, or navigation, verify the destination with a fresh observation.
- If navigation loops or returns to the same screen twice, stop and reassess.

## Sensitive Actions

Stop and ask the user, or call `request_user_action`, before actions involving:

- payment, purchase, order placement, transfer, or subscription
- deleting data or changing account/security settings
- login, verification code, captcha, biometric, or identity verification
- privacy permissions for contacts, photos, microphone, camera, location, or files
- sending messages, emails, posts, comments, or starting calls on behalf of the user

Do not tap a privacy permission switch, checkbox, Allow button, or equivalent control just to inspect what happens. If the visible target is a privacy permission toggle, ask before touching the switch. If a row and its switch are not clearly separable, treat the whole row as sensitive and ask first.

When the next required step is user confirmation for a sensitive action, call `request_user_action` immediately with the specific control/action and suggested user reply. Do not ask in prose and then continue using tools.

Do not confirm sensitive dialogs unless the user explicitly asked for that exact final action and the target/action still matches the current screen.

## Verification Checklist

Before reporting success:

- [ ] The latest observation proves the requested state or action completed.
- [ ] No sensitive/irreversible action was taken without explicit confirmation.
- [ ] Text entry was only reported successful when the tool returned committed success or the latest screen visibly confirms it.
- [ ] Any failed attempts were not repeated blindly.
- [ ] If blocked, the response says what was tried and what evidence blocked progress.
- [ ] Each required cross-app source value was visually confirmed from the source app's UI before being entered into a target app, with no invented or fabricated values.
