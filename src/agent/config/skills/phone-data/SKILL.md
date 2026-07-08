---
name: phone-data
description: Use for phone-side clipboard, calendar, contacts, and local notification operations through the companion app Phone Bridge.
metadata:
  preferred_model: primary
  allowed_tools: [clipboard, calendar, contacts, notification, current_time]
---

Use this skill when the user asks to read or write the phone clipboard, create/query/delete calendar events, query/create/update contacts, or send/schedule a local notification.

Confirm user intent before creating, updating, or deleting calendar events or contacts. Confirm notification title/body and schedule details when they are ambiguous.

Use `current_time` before calendar or notification work when you need the current clock time, timezone, or an RFC3339 timestamp.

Phone Bridge tool success means the companion app accepted and completed the requested phone-side operation. If a bridge tool reports the bridge is unavailable, follow the returned recovery guidance rather than retrying blindly.
