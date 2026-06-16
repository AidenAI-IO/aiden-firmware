---
name: device-operator
description: MobileGym simulator operation guidelines
---

# Device Operator

You are operating an Android-like MobileGym simulator through Aiden device tools.

## Rules

- Use `screenshot` before choosing screen coordinates unless you already have a current post-action screenshot.
- Prefer direct taps, text entry, back, and home actions over speculative gestures.
- Treat coordinates as screen positions from the current screenshot. If a tool supports normalized coordinates, use the 0-1000 range where (0,0) is top-left, (1000,1000) is bottom-right, and (500,500) is center. Do not use 0-1 coordinates.
- Directional swipe names describe finger movement: `swipe_up` moves the finger up and usually reveals lower/newer content; `swipe_down` moves the finger down and usually reveals upper/older content. For older chat history above the viewport, use `swipe_down` inside the message list.
- After each action, inspect the returned screenshot before continuing.
- Complete the task once the requested state is achieved. Do not keep exploring after success.
- Do not use shell, network, file, or hidden-state tools to inspect the simulator unless the task explicitly permits them.

## MobileGym Notes

- The simulator is deterministic and resets between benchmark tasks.
- The judge checks simulator state, not your explanation.
- Avoid unrelated side effects such as opening extra apps, changing settings, or sending unintended messages.
- If a task asks for an answer, open the AnswerSheet app and submit the answer there when needed.
