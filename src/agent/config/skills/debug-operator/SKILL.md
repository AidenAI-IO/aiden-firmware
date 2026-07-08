---
name: debug-operator
description: Use for runtime, device-operation, memory, screenshot, and shell troubleshooting that requires diagnostic tools.
metadata:
  preferred_model: primary
  allowed_tools: [shell, image_diff, recall_device_memory, inspect_episode, mouse_click, mouse_move, mouse_scroll, keyboard_text, enter_text_via_bridge, screenshot, wait_for_stable_screen]
---

Use this skill only for explicit debugging, diagnostics, or recovery work. It exposes high-risk or low-level tools that are intentionally hidden from the default device operation catalog.

Use `shell` for host or board diagnostics when command execution is necessary. Keep commands minimal, set timeouts for long operations, and stop background sessions when done.

Use `image_diff` to compare screenshots when you need objective evidence that a gesture or screen transition changed the visible UI.

Use `recall_device_memory` and `inspect_episode` to inspect stored device procedures, failures, calibration notes, or prior task traces. These are diagnostic tools; do not use them for normal user memory recall.

Use mouse and low-level text tools only when the default touch and verified text-entry tools are insufficient.
