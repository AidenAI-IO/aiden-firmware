---
name: deeplink
description: Phone app launch guidance. Use semantic bridge_open_app requests; do not pass platform launch targets from the board.
metadata:
  preferred_model: primary
  allowed_tools: [bridge_open_app, screenshot, touch_gesture, mouse_click, keyboard_tap, enter_text]
---

The board-side `bridge_open_app` tool is semantic. Do not pass iOS URL schemes,
Android package names, intent URIs, or deeplinks. The companion app owns those
platform-specific launch details.

Use:

- `{"app":"微信"}`, `{"app":"WeChat"}`, or `{"app":"weixin"}` to open WeChat.
- `{"app":"支付宝"}` to open Alipay.
- `{"app":"browser"}` to open the browser itself.
- `{"url":"https://example.com"}` to open a fixed webpage.
- `{"phone_number":"10086"}` to open the dialer for a number.

For specific in-app destinations such as QR scan, settings pages, chats, or
payment screens, first open the target app semantically, then use screenshot and
HID/touch navigation to reach the visible UI state.
