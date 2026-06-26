---
name: wechat
description: Use for WeChat tasks on Mac or phone, with platform-specific search and send rules.
metadata:
  preferred_model: primary
  allowed_tools: [open_app, clipboard, screenshot, quick_action, touch_gesture, mouse_click, keyboard_tap, enter_text_in_field, enter_text_via_bridge]
---

Use this skill together with `device-operator` for visible WeChat UI work.

## Scope

Use this skill when the user asks to open WeChat, find a contact or chat, read messages, compose a message, or send a WeChat message.

## Common rules

- Prefer `open_app` with `{"app":"WeChat"}` to launch WeChat.
- Prefer search over blind scrolling.
- Verify the chat title before sending anything.
- Use WeChat's own visible search field to find contacts or chats. Do not use global `spotlight_search` while already inside WeChat.
- For iOS/Android chat message body input, use `enter_text_in_field` with the input box coordinates. On iOS, if the message text is known while Aiden is foreground, call `clipboard` with `{"action":"write","text":"..."}` before opening or navigating WeChat; after the chat is open, `enter_text_in_field` focuses the WeChat input, pastes the prepared clipboard, and verifies without reopening Aiden. On Android, it can use connected bridge clipboard/paste without leaving WeChat. It then falls back to HID/IME typing only if clipboard input fails.
- Do not restore Aiden from an already open WeChat chat just to write clipboard unless the user explicitly asks for that heavier bridge path.
- If the user mentions a WeChat name and a separate Contacts name, keep them separate: query Contacts using the Contacts name to get phone data, then search/open the WeChat chat using the WeChat name.
- Treat message entry as successful only when `committed:true` and the field text exactly matches the requested message.
- Sending a message is a sensitive external action. Do not send unless the user explicitly asked for the final send.

## Mac

- Use `keyboard_tap` with `["cmd", "F"]` to open search for a contact or history message.
- After the text is committed and the user confirmed sending, use `keyboard_tap` with `["enter"]` to send the message.
- If Enter does not send, inspect the latest screenshot before trying another action.

## iPhone / Android

- Do not use global keyboard shortcuts for WeChat navigation or contact search.
- After the text is committed and the user explicitly asked to send, try `quick_action` with `action=send` first.
- If `quick_action` send has no visible effect, inspect the latest screenshot, then tap the visible send button labeled `Send` or `发送`.
- If the `Send` button is missing or disabled, inspect the latest screenshot and fix focus or input state first.

## Verification

- After sending, verify the new outgoing message bubble appears before reporting success.
