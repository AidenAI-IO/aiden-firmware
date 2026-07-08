---
name: phone-text-entry
description: Use for detailed phone text-entry troubleshooting, clipboard-first input, IME fallback, and send verification.
metadata:
  preferred_model: primary
  allowed_tools: [enter_text_in_field, enter_text_via_bridge, clipboard, keyboard_tap, touch_gesture, screenshot, wait_for_stable_screen]
---

Use this skill when ordinary text entry is failing, when the user explicitly asks to use the companion app or clipboard path, or when you need detailed control over phone input verification.

Prefer `enter_text_in_field` for normal field entry. It can choose clipboard-first and fall back to HID/IME when appropriate.

Use `enter_text_via_bridge` directly only when the task explicitly requires the Phone Bridge clipboard path or when debugging that path. Treat `committed:true` as the success condition for field entry.

For message composers, use `send_after_commit` only after the correct chat or target destination is visible and the user has asked for the final send.

Use screenshots before and after text entry to verify focus, committed field text, and post-send state.
