---
name: device-operator
description: Use when controlling a visible target device UI through screenshots, touch, mouse, or keyboard.
metadata:
  preferred_model: primary
  allowed_tools: [screenshot, quick_action, touch_gesture, mouse_click, mouse_move, mouse_scroll, keyboard_tap, keyboard_text, enter_text_in_field, enter_text_via_bridge, run_script, list_scripts, read_script, write_script, shell]
---

Use this skill when interacting with the connected device screen, app UI, keyboard, touch input, or mouse pointer.

Use `run_script` only when the user explicitly asks to run a prepared demo script; pass only a script file name from the config directory's `scripts/` folder. It executes JSONL script lines directly without LLM planning between steps, and `tts` lines start playback asynchronously without waiting for speech to finish. Use `list_scripts` to see which scripts exist, `read_script` to inspect a script's content, and `write_script` to create or update one; for the script file format see the script-author skill.

## Core Loop

Always operate through a visual feedback loop:

1. Observe the current screen with `screenshot`.
2. Decide the smallest next UI action.
3. Act with one input tool.
4. Inspect the resulting screenshot.
5. Continue only after confirming what changed.

Do not perform multiple blind UI actions in a row.

## Screenshot Failure Recovery

If `screenshot` fails, a post-action screenshot fails, or the tool output mentions `SERVICE_RECOVERING`, socket errors, empty image data, or invalid screenshot JSON, stop UI actions. Do not tap, type, swipe, or guess from stale visual state.

Recovery sequence:

1. Retry `screenshot` once if the error suggests transient recovery, such as `SERVICE_RECOVERING`.
2. If the second screenshot fails, diagnose the frame service with `shell`:
   ```bash
   /etc/init.d/S52frame_service status
   frame_service_cli --socket /run/frame_service/frame_service.sock health
   ls -l /run/frame_service/frame_service.sock
   ```
3. If health reports a bad state or the service is recovering, request a capture-manager restart first:
   ```bash
   frame_service_cli --socket /run/frame_service/frame_service.sock restart
   ```
4. Verify recovery with `health`, then call `screenshot` again.
5. If CLI restart fails, the socket is missing, or the service is not running, restart the init service:
   ```bash
   /etc/init.d/S52frame_service restart
   ```
6. After service restart, verify in order: `status`, `health`, then `screenshot`.

If recovery still fails, inspect recent logs before asking the user to intervene:

```bash
tail -n 80 /var/log/frame_service/frame_service.log
```

When reporting a blocker, include the screenshot error, which recovery commands were tried, and the latest `health` or log signal. Do not claim the device UI task is complete without a fresh screenshot confirming the target screen.

## Action Choice

- Use `quick_action` first when the goal matches a catalog shortcut (back, home, app switch, search, copy/paste, browser ops, etc.). Pass the correct `platform` (ios/android/mac).
- If `quick_action` is reserved, returns `ok=false`, or the screen does not change as expected: do not retry the same binding. Try `alternative=true` once when listed, then fall back to direct input tools and continue.
- Use `touch_gesture` for taps, swipes, drag, and mobile-style navigation.
- For **input field text entry** (search boxes, forms, chat inputs), use **`enter_text_in_field` once** with field coordinates for normal typing. It handles IME switch, typing, candidate clicks, and field verification internally.
- If the user explicitly says to use the companion app, bridge, or clipboard instead of direct typing, use **`enter_text_via_bridge`**. Do not switch to it unless the user asks for that path.
- Use `keyboard_text` only for simple standalone ASCII typing when not using `enter_text_in_field`.
- Never pass Chinese, emoji, or romanization blobs to `keyboard_text`.
- Use `keyboard_tap` for keys such as enter, escape, tab, arrows, or shortcuts not covered by quick_action.
- Use `mouse_click`, `mouse_move`, and `mouse_scroll` only when touch gestures are not appropriate.

Prefer semantic shortcuts and gestures when available:

- Use `quick_action` for cataloged keyboard/touch bindings before improvising raw `keyboard_tap` or custom swipes.
- Use `touch_gesture` with `type: "back"` or `type: "home"` when quick_action is unavailable or ineffective.
- Use scroll or swipe instead of repeatedly tapping uncertain controls.
- In app switchers with overlapping cards, tap the visible non-overlapping center of the target card.
- If the target card is not visible, swipe within the switcher by layout until it enters the visible area, then tap it.

## Coordinate Discipline

Before using coordinates:

- Inspect the screenshot.
- Identify the intended target visually.
- Use `coord_space: "normalized"` with 0-1000 coordinates when possible: (0,0) is top-left, (1000,1000) is bottom-right, and (500,500) is center. Do not use 0-1 coordinates.
- Avoid edges unless performing an edge gesture.
- Do not guess a coordinate if the target is not visible.

If a tap misses, do not repeat the exact same coordinate blindly.

## Failed Attempt Handling

Track failed UI actions during the current task.

A failed attempt is any action where:

- The expected screen change did not happen.
- The same control still appears unchanged.
- Text was not entered.
- Navigation did not move.
- The screen changed to an unexpected state.
- The action output or screenshot indicates an error.

After a failed attempt:

1. Observe with `screenshot`.
2. Compare expected vs observed result.
3. Do not repeat the exact same action more than once.
4. Change one variable at a time: target location, gesture type, coordinate space, navigation path, or input method.
5. After 2 failed attempts on the same goal, choose a different strategy.
6. After 3 failed attempts total, summarize what was tried and ask the user or switch to diagnosis.

Keep an internal attempt log:

```text
Goal:
Attempt:
Expected:
Observed:
Next adjustment:
```

Only report the log when the task is blocked or the user asks.

## Recovery Strategies

If a tap does not work:

- Observe the screen again before retrying.
- If the app may be slow, allow one extra observation before changing strategy.
- Retry at most once with a slightly adjusted target.
- If it still does not work, use another visible control, back navigation, or a different path.

If a swipe does not work:

- Directional `touch_gesture` swipe names describe finger movement, not the content you want to reveal: `swipe_up` moves the finger up and usually scrolls the viewport down to lower/newer items; `swipe_down` moves the finger down and usually scrolls the viewport up to upper/older items. In chat/message history, to see older messages above the current viewport, use `swipe_down` from inside the message list.
- If the target is a partial scrollable region (picker, embedded list, modal, or in-page scroll area), place both start and end points well inside the visible bounds of that region. A gesture that starts on a fixed header, bottom navigation bar, or outside the scrollable area will be consumed by the outer container and the inner control will not move.
- For list scrolling and search results, use calibrated strength:
  1. Start with `strength: "medium"` and take a screenshot immediately after.
  2. Use `image_diff` or visual inspection to confirm scrolling occurred and estimate rows moved.
  3. If far from target: use `large`. If close: use `small` or `tiny`.
  4. If you overshoot, reverse direction and drop one strength level.
  5. If the screen did not move at all (same content, no diff), the gesture likely missed the scrollable area — adjust the start point inward, not just the distance.
  6. Do not repeat the same `distance` or `strength` if it failed once. Change one variable per attempt.
- Change the start point away from screen edges, fixed headers, or bottom navigation bars.
- If the content appears to be at an edge, try the opposite direction once.
- If the same list boundary appears again, stop searching that direction.

If the current screen is unrelated to the task:

- Use `touch_gesture` with `type: "back"` to return when possible.
- If back does not change the screen, look for a visible back, close, cancel, or X control.
- After recovery, observe again before continuing the original task.

## Search, Lists, and Choices

Prefer search or filtering controls before long manual browsing when looking for an app, contact, setting, file, item, or page content.

If the target is not found:

- Try one alternate search term when reasonable.
- Check each relevant tab, list, or section once before repeating any of them.
- Do not repeatedly search or scroll the same unchanged list.
- If multiple plausible matches appear, ask the user to choose instead of guessing.

When selecting from a list, verify the selected item matches the user's requested name, label, or visible details before acting on it.

## Sensitive Actions

Stop and ask the user before actions involving:

- payment, purchase, order placement, or subscription
- deleting data or changing account settings
- login, verification code, captcha, or identity verification
- privacy permissions, contacts, photos, microphone, camera, or location
- sending messages, emails, posts, or comments on behalf of the user
- starting calls, video calls, or other real-world communication

Do not confirm sensitive dialogs unless the user explicitly asked for that exact action.

## Text Entry

Use **`enter_text_in_field`** for putting text into an input box:

```json
{"text":"你好","platform":"android","focus":{"x":450,"y":105,"coord_space":"normalized"},"segments":["ni","hao"]}
```

- One call runs: focus → type segments → vision verify field (not IME candidate bar) → candidate clicks → retry with IME switch when wrong.
- Success requires `committed:true` and `field_text` exactly matching `text`. **Do not** tell the user text was entered unless `committed:true`.
- `committed:false` means failure (e.g. text still in IME candidates/preedit, or field still shows pinyin). Retry with a fresh screenshot and corrected focus/segments, or report failure.
- **Required** `segments` for composition/CJK: IME romanization syllables in typing order. Example: `"text":"你好","segments":["ni","hao"]`. Do not omit segments.

`keyboard_text` remains ASCII-only for non-field or legacy paths. Do not use it for Chinese/CJK field entry.

Before typing:
- For ordinary deletion in an input field, use `keyboard_tap` with `{"keys":["backspace"]}`. The `delete` key is forward-delete and should only be used when intentionally deleting the character after the cursor.
- Use `keyboard_tap` for submit or enter only after verifying the text appears.
- If text does not appear, stop and re-check focus before typing again.

## Navigation

For navigation tasks:

- First identify the current screen.
- Prefer visible buttons and semantic gestures.
- After back, home, or navigation, verify the destination.
- If navigation loops or returns to the same screen twice, stop and reassess.

## App Switching

When the task requires switching apps or opening recents:

**Step 0 — prefer quick_action when platform is known.**

- iOS/Android/macOS: try `quick_action` with `action=app_switch` and the correct `platform` first.
- Android search: try `quick_action` `spotlight_search` (Meta) before manual UI search.
- If quick_action fails or the screen does not change, continue with the probe flow below without asking the user.

**Step 1 — recall cached method first.**
Call `recall_memory` with tags `["app-switch", "device"]`. If a record exists for the current device, use it directly and skip probing.

**Step 2 — if no cache, identify OS from the screenshot:**
- iOS/iPadOS: home bar at bottom, no nav buttons
- Android gesture nav: thin gesture bar, no buttons
- Android 3-button nav: visible Back / Home / Recents buttons at bottom
- Unknown: treat as gesture nav and probe

**Step 3 — probe for the task switcher. Try in order, stop at first success:**

1. Bottom-edge swipe and hold: `type: "swipe"`, start y≈990, end y≈550, `hold_after_ms: 500`. Take a screenshot — if the task switcher appeared, this is the method.
2. If 3-button nav is visible: tap the Recents button (bottom-right square icon).
3. If still on the same app screen, try home first (`type: "home"`), then retry method 1 from the home screen.

After each probe take a screenshot to confirm whether the switcher appeared before trying the next method.

**Step 4 — once the switcher is open:**
- If the target app card is visible, tap its center.
- If not visible, swipe left/right in the switcher to find it.
- If still not found, dismiss the switcher (`type: "home"`) and find the app icon on the home screen or via system search (Spotlight swipe-down on iOS, app drawer swipe-up on Android).

**Step 5 — save on success.**
After successfully opening the switcher, call `save_memory` with: device name or model (from screenshot or prior context), OS, the method that worked, and the exact gesture parameters used. Tags: `["app-switch", "device"]`.

**If switching fails after all probes:**
- Do not loop. Report the blocker to the user and suggest they open the target app manually.

## Completion

A device operation is complete only when the screenshot confirms the requested outcome.

Before saying the task is complete:

- Observe the screen one last time.
- Check that the requested target, selection, text, or destination is correct.
- Check for wrong selections, missing selections, duplicate selections, and unfinished dialogs.
- If a failed action was skipped, mention it in the final answer.

If the outcome cannot be verified visually, say what was done and what remains uncertain.
