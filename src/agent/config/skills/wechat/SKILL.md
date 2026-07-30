---
name: wechat
description: Use for WeChat tasks on Mac or iPhone, with platform-specific search and send rules.
metadata:
  preferred_model: primary
  allowed_tools: [bridge_open_app, screenshot, touch_gesture, mouse_click, keyboard_tap, enter_text]
---

Use this skill together with `device-operator` for visible WeChat UI work.

## Scope

Use this skill when the user asks to open WeChat, find a contact or chat, read messages, compose a message, or send a WeChat message.

## Common rules

- Prefer `bridge_open_app` with `{"app":"WeChat"}` to launch WeChat.
- Prefer search over blind scrolling.
- Verify the chat title before sending anything.
- Use `enter_text` for the WeChat chat input field.
- Treat message entry as successful when `enter_text` returns `ok:true`; inspect its returned screenshot when visual confirmation is needed.
- Sending a message is a sensitive external action. Do not send unless the user explicitly asked for the final send.

## Mac

- Use `keyboard_tap` with `["cmd", "F"]` to open search for a contact or history message.
- After the text is committed and the user confirmed sending, use `keyboard_tap` with `["enter"]` to send the message.
- If Enter does not send, inspect the latest screenshot before trying another action.

## iPhone

- Do not use any keyboard shortcut for WeChat navigation or sending.
- After the text is committed and the user confirmed sending, use `touch_gesture` or another direct tap action on the visible send button labeled `Send` or `发送`.
- If the `Send` button is missing or disabled, inspect the latest screenshot and fix focus or input state first. Do not use Enter as a fallback.

## Verification

- After sending, verify the new outgoing message bubble appears before reporting success.
