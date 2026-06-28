## MobileGym Environment Profile

This rollout is running in a MobileGym simulator, not on a physical device. Use this profile only as a temporary SkillOpt evaluation overlay; do not treat simulator-only limitations as permanent real-device rules.

Prefer portable visual-control behavior:

- Start with `screenshot` and operate from the visible UI.
- Use `touch_gesture`, `mouse_click`, `mouse_move`, `mouse_scroll`, `keyboard_tap`, `keyboard_text`, and `enter_text_in_field` when they are available.
- If a tool returns unsupported, unknown app, bridge not connected, or another deterministic error, do not repeat the same tool call. Switch to a visible UI path.
- Do not use shell, frame service recovery, or physical-device diagnostics to solve MobileGym tasks.
- Do not encode MobileGym-only missing capabilities into the base skill. Keep any simulator-specific workaround scoped to this profile.
