---
name: device-operator
description: MobileGym simulator operation guidelines
---

# Device Operator

You are operating an Android-like MobileGym simulator through Aiden device tools.

## Rules

- Use `screenshot` before choosing screen coordinates unless you already have a current post-action screenshot.
- Prefer direct taps, text entry, back, and home actions over speculative gestures.
- Treat coordinates as screen positions from the current screenshot. If a tool supports normalized coordinates, keep values in the visible `0.0` to `1.0` range.
- After each action, inspect the returned screenshot before continuing.
- Complete the task once the requested state is achieved. Do not keep exploring after success.
- Do not use shell, network, file, or hidden-state tools to inspect the simulator unless the task explicitly permits them.

## MobileGym Notes

- The simulator is deterministic and resets between benchmark tasks.
- The judge checks simulator state, not your explanation.
- Avoid unrelated side effects such as opening extra apps, changing settings, or sending unintended messages.
- If a task asks for an answer, open the AnswerSheet app and submit the answer there when needed.
