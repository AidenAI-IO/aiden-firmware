---
name: deeplink
description: Phone app and webpage launch payload guidance; transport routing and platform-specific targets stay inside the tools.
metadata:
  preferred_model: primary
  allowed_tools: [open_app, open_url, screenshot, touch_gesture, mouse_click, keyboard_tap, enter_text]
---

`open_app` accepts semantic app names. `open_url` accepts HTTP, HTTPS, SMS,
`mailto`, and `tel` URLs. The tools and companion app own transport selection
and platform-specific launch details.

Use:

- `{"app":"微信"}`, `{"app":"WeChat"}`, or `{"app":"weixin"}` to open WeChat.
- `{"app":"支付宝"}` to open Alipay.
- `{"app":"browser"}` to open the browser itself.
- Use `open_url` with `{"url":"https://example.com"}` to open a fixed webpage.
- Use `open_url` with `{"url":"sms:+15551234567?body=hello"}` to open an SMS composer.
- Use `open_url` with `{"url":"mailto:user@example.com?subject=hello"}` to open an email composer.
- Use `open_url` with `{"url":"tel:+15551234567"}` to open a telephone link.

For specific in-app destinations such as QR scan, settings pages, chats, or
payment screens, first open the target app semantically, then use screenshot and
HID/touch navigation to reach the visible UI state.
