---
name: device-operator
description: Use when controlling a visible target device UI through screenshots, touch, mouse, or keyboard.
metadata:
  preferred_model: primary
  allowed_tools: [screenshot, touch_gesture, mouse_click, mouse_move, mouse_scroll, keyboard_tap, keyboard_text]
---

Use this skill when interacting with the connected device screen, app UI, keyboard, touch input, or mouse pointer.

## Core Loop

Always operate through a visual feedback loop:

1. Observe the current screen with `screenshot`.
2. Decide the smallest next UI action.
3. Act with one input tool.
4. Inspect the resulting screenshot.
5. Continue only after confirming what changed.

Do not perform multiple blind UI actions in a row.

## Action Choice

Prefer higher-level touch actions before raw pointer actions:

- Use `touch_gesture` for taps, swipes, back, home, drag, and mobile-style navigation.
- Use `keyboard_text` for entering text after confirming the input field is focused.
- Use `keyboard_tap` for keys such as enter, escape, tab, arrows, or shortcuts.
- Use `mouse_click`, `mouse_move`, and `mouse_scroll` only when touch gestures are not appropriate.

Prefer semantic gestures when available:

- Use `touch_gesture` with `type: "back"` for back navigation.
- Use `touch_gesture` with `type: "home"` for home navigation.
- Use scroll or swipe instead of repeatedly tapping uncertain controls.

## Coordinate Discipline

Before using coordinates:

- Inspect the screenshot.
- Identify the intended target visually.
- Use normalized coordinates when possible.
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

Before typing:

- Confirm the text field is focused.
- Prefer one `keyboard_text` call for normal text.
- Use `keyboard_tap` for submit or enter only after verifying the text appears.
- If text does not appear, stop and re-check focus before typing again.

## Navigation

For navigation tasks:

- First identify the current screen.
- Prefer visible buttons and semantic gestures.
- After back, home, or navigation, verify the destination.
- If navigation loops or returns to the same screen twice, stop and reassess.

## App Switching

When the task requires switching to a different app or opening the recents/task switcher:

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
