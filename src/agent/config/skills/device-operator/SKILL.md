---
name: device-operator
description: Operate the connected device UI through screenshot-driven actions.
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

## Completion

A device operation is complete only when the screenshot confirms the requested outcome.

If the outcome cannot be verified visually, say what was done and what remains uncertain.
