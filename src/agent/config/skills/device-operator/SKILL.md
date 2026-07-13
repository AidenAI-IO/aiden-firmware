---
name: device-operator
description: Use when controlling a visible target device UI through screenshots, touch, mouse, keyboard, text entry, scrolling, app switching, or capture recovery.
metadata:
  preferred_model: primary
  allowed_tools:
    [
      screenshot,
      wait_for_stable_screen,
      image_diff,
      quick_action,
      touch_gesture,
      mouse_click,
      mouse_move,
      mouse_scroll,
      keyboard_tap,
      keyboard_text,
      enter_text_in_field,
      enter_text_via_bridge,
      search_launch_app,
      request_human_handoff,
      recall_memory,
      save_memory,
      skill_read,
      run_script,
      list_scripts,
      read_script,
      write_script,
      shell,
    ]
---

Use this skill when the task requires operating a visible connected device UI. This is the complete generic device-operation playbook; do not split routine app switching, text entry, scrolling, picker, or screenshot recovery work into child skills.

Use `run_script` only when the user explicitly asks to run a prepared demo script; pass only a script file name from the config directory's `scripts/` folder. It executes JSONL script lines directly without LLM planning between steps, and `tts` lines start playback asynchronously without waiting for speech to finish. Use `list_scripts` to see which scripts exist, `read_script` to inspect a script's content, and `write_script` to create or update one; for the script file format see the script-author skill.

## Core Loop

Always operate through a visual feedback loop:

1. Observe the current screen with `screenshot` or `wait_for_stable_screen`.
2. Decide the smallest next UI action that could make progress.
3. Act with one input or semantic tool.
4. Inspect the resulting screen.
5. Continue only after confirming what changed.

Do not perform multiple blind UI actions in a row. Base every coordinate, tap, swipe, and typed input on the latest visual state.

For actions that were expected to visibly change the UI, treat `screen_changed=false` in a post-action screenshot or `wait_for_stable_screen` result as "effect not yet verified". In that case, do not say the action succeeded just because `action_output` is `ok`; inspect the screenshot, compare it with the expected target change, and continue checking or choose a different action if the UI still looks unchanged.

If `touch_gesture` returns `screen_changed=false` and the configured touch mode does not match the target platform, stop instead of retrying blind touches: Android expects `hid.pointer_mode="touchscreen"`, while iOS/iPadOS expects `hid.pointer_mode="absolute"`. Ask the user to switch the pointer mode and restart the agent before continuing.

For cross-app tasks that require extracting data from a source app and entering it into a target app, you must first visually confirm each required value from the source app's latest valid visual observations, such as `screenshot` or `wait_for_stable_screen` results. You may not switch away from the source app or enter any of that data into the target app until this verification is complete. Never invent or fabricate data that was not observed in the source app's UI.

## Tool Choice

Prefer the highest-level reliable tool for the job:

- Use `quick_action` first when a catalog shortcut clearly matches the goal, such as back, home, app switch, search, copy/paste, or browser actions. Pass the observed `platform` when known.
- Use `touch_gesture` for mobile taps, swipes, drag, back, and home gestures.
- Use `enter_text_in_field` for normal text input into fields, including Chinese/CJK, emoji, IME, and verified field entry.
- Use `keyboard_tap` for enter, escape, tab, arrows, shortcuts, and simple key actions.
- Use `keyboard_text` only for simple standalone ASCII typing. Never use it for Chinese/CJK or emoji field entry.
- Use `mouse_click`, `mouse_move`, and `mouse_scroll` only when touch-style controls are not appropriate.

If a semantic tool fails, read the message and choose a different approach. Do not retry the same binding unless the tool explicitly offers a distinct alternative.

## Coordinate Discipline

Before using coordinates:

- Inspect the screenshot and identify the intended target visually.
- Use `coord_space: "normalized"` with 0-1000 coordinates when possible: `(0,0)` is top-left, `(1000,1000)` is bottom-right, `(500,500)` is center.
- Choose the visual center of the target. For small controls, estimate the control bounds and aim for the midpoint, biased slightly inward.
- Prefer normalized coordinates over `coord_space: "pixel"`. Use pixel only when calibrated; pixel coordinates need a recent screenshot, go stale after ~30s, and are rejected outside cached bounds.
- Avoid edges unless performing an edge gesture. For phone edge gestures, do not use conservative insets like 50-100: left-edge `back` starts at normalized `x=1`, and bottom-edge `home` starts at normalized `y=999`.
- Do not guess a coordinate if the target is not visible or the screen is stale.
- If a tap misses, observe again before adjusting. Do not repeat the exact same coordinate blindly.

## Text Entry

Use `enter_text_in_field` for normal input boxes such as search fields, forms, and chat composers.

Required pattern:

```json
{
  "text": "你好",
  "platform": "android",
  "focus": { "x": 450, "y": 105, "coord_space": "normalized" },
  "segments": ["ni", "hao"]
}
```

- Focus coordinates must come from the latest screenshot.
- Success requires `committed:true` and `field_text` matching the requested text, or a fresh screenshot that visibly confirms the field content.
- `committed:false` means failure; do not tell the user text was entered.
- For Chinese/CJK composition, provide `segments` as romanization syllables in typing order, e.g. `"你好"` -> `["ni","hao"]`.
- Never pass Chinese, emoji, or romanization blobs to `keyboard_text`.
- If text remains in the IME candidate/preedit area instead of the field, retry once with corrected focus/segments or report the blocker.

Use `enter_text_via_bridge` only when:

- the user explicitly asks to use the companion app, bridge, or clipboard;
- direct field entry failed and the bridge is available;
- the text is long, emoji-heavy, or otherwise unsuitable for HID typing.

After bridge entry, verify the target field or submitted result before reporting success.

For simple keys:

- Use `keyboard_tap` for submit, enter, escape, tab, arrows, shortcuts, or backspace.
- Use `keyboard_text` only for simple standalone ASCII text when not entering a normal field.
- For ordinary deletion in a field, prefer `keyboard_tap` with `{"keys":["backspace"]}`; `delete` is forward-delete.

If text does not appear or appears in the wrong place, stop typing, take a fresh screenshot, re-check focus and field identity, then retry once with corrected focus or input method. If still failing, summarize observed field state and ask for help or use bridge if appropriate.

If a text or keyboard tool reports missing HID devices such as `/dev/hidg0` or `/dev/hidg1`, treat direct text entry as unavailable in this environment. Do not fall back to `keyboard_text` for Chinese/CJK, emoji, or romanization guessing. Take at most one fresh screenshot to confirm the current state; unless the target is already clearly visible and reachable without text entry, stop and report the blocker with the exact tool error and ask for help or use an explicitly available bridge path.

## App Switching and Launch

Use this flow for app switcher, recents, returning to Aiden, and cross-app navigation workflows.

1. Observe the screen.
2. If platform is known, try `quick_action` for `app_switch`, home, back, or app search before manual gestures.
3. Use `search_launch_app` when opening a target app via system search is the clearest path.
4. Verify the result with a screenshot before continuing.

Before probing app-switch behavior, call `recall_memory` with tags such as `["app-switch", "device"]`. If a matching calibration exists for this device/platform, use it directly.

Platform clues:

- iOS/iPadOS: home bar at bottom, no Android navigation buttons.
- Android gesture navigation: thin gesture bar, no 3-button nav.
- Android 3-button navigation: visible Back / Home / Recents buttons.
- Unknown: treat as gesture navigation and probe conservatively.

If no known quick action or cached method works:

1. Try bottom-edge swipe-and-hold: start near `y≈990`, end around `y≈550`, hold after gesture. Screenshot after the gesture.
2. If Android 3-button nav is visible, tap Recents.
3. If still on the same app, go home once, then retry app switcher/search from the home screen.

After each probe, verify whether the switcher appeared before trying the next method.

Selecting an app:

- If the target app card is visible, tap the visible non-overlapping center of that card.
- If not visible, swipe within the switcher to bring it into view.
- If still not found, dismiss the switcher and use `search_launch_app`, system search, or the home/app drawer.
- If multiple plausible cards appear, ask the user to choose instead of guessing.

After successfully opening the switcher via a non-obvious method, call `save_memory` with device/platform, method, gesture coordinates, and tags `["app-switch", "device"]`.

On iOS, if Phone Bridge context says the Aiden companion app is backgrounded/inactive and `return_entry=dynamic_island`, treat Dynamic Island as the fastest way back to Aiden. Do not blind-tap lock-screen Live Activity cards; use screenshot/HID fallback or visual confirmation for those cases. Opening Aiden restores the companion app shortcut channel.

## Scrolling and Picker Controls

Directional swipe names describe finger movement, not content direction:

- `swipe_up`: finger moves up; content usually moves down to lower/newer items.
- `swipe_down`: finger moves down; content usually moves up to upper/older items.

In chat/message history, to see older messages above the current viewport, usually use `swipe_down` inside the message list.

Scrollable region discipline:

- Start and end points must be inside the intended scrollable region.
- Avoid fixed headers, bottom navigation bars, modal edges, and screen borders.
- If nothing moves, adjust the start point inward before increasing distance.

Calibration loop:

1. Start with medium strength.
2. Screenshot immediately after the gesture.
3. Use visual inspection or `image_diff` to confirm movement and estimate rows/items moved.
4. If far from target, increase strength; if close, use small/tiny.
5. If overshot, reverse direction and reduce strength.
6. Do not repeat the same strength/distance after a failed attempt.

If the same list boundary appears again, stop searching in that direction. Try search/filter, a different tab, or ask the user.

Before manipulating a picker or wheel control:

1. Call `recall_memory` for similar picker calibration when relevant.
2. Without cache, probe once with medium movement.
3. Observe how many values changed.
4. Choose subsequent strength by remaining distance.
5. Screenshot after each adjustment before continuing.

On success, save calibration with app/page/control location, direction, gesture strength/distance, observed delta, and tags `["swipe", "picker", "calibration"]`.

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

If an action was expected to change the UI and the returned observation says `screen_changed=false`, treat that as a failed or unverified attempt until the screenshot itself proves otherwise. Do not report success from tool output alone.

After a failed attempt:

1. Observe with `screenshot`.
2. Compare expected vs observed result.
3. Avoid repeating the exact same failed action; if one repeat is justified, change one variable and verify the result before trying again.
4. Change one variable at a time: target location, gesture type, coordinate space, navigation path, input method, or semantic shortcut.
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

Stop and ask the user, or call `request_human_handoff`, before actions involving:

- payment, purchase, order placement, transfer, or subscription
- deleting data or changing account/security settings
- login, verification code, captcha, biometric, or identity verification
- privacy permissions for contacts, photos, microphone, camera, location, or files
- sending messages, emails, posts, comments, or starting calls on behalf of the user

Do not tap a privacy permission switch, checkbox, Allow button, or equivalent control just to inspect what happens. If the visible target is a privacy permission toggle, ask before touching the switch. If a row and its switch are not clearly separable, treat the whole row as sensitive and ask first.

When the next required step is user confirmation for a sensitive action, call `request_human_handoff` immediately with the specific control/action and suggested user reply. Do not ask in prose and then continue using tools.

Do not confirm sensitive dialogs unless the user explicitly asked for that exact final action and the target/action still matches the current screen.

## Verification Checklist

Before reporting success:

- [ ] The latest observation proves the requested state or action completed.
- [ ] No sensitive/irreversible action was taken without explicit confirmation.
- [ ] Text entry was only reported successful when the tool returned committed success or the latest screen visibly confirms it.
- [ ] Any failed attempts were not repeated blindly.
- [ ] If blocked, the response says what was tried and what evidence blocked progress.
- [ ] Each required cross-app source value was visually confirmed from the source app's UI before being entered into a target app, with no invented or fabricated values.
