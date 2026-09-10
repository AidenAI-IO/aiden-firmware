---
sidebar_position: 5
---

# OTA Self-check and Rollback Implementation

The OTA runtime now performs read-only service probes before committing a
pending A/B slot. The Agent checks frame_service, audio_service, its HTTP
health endpoint, USB HID nodes, optional BLE/Phone Bridge, and optional HDMI,
Wi-Fi, and ECM conditions. External conditions are warnings unless configured
as required.

The OTA updater snapshots protected configuration before writing the target
slot and exposes:

```text
ota self-check   # emit and persist a health report
ota rollback     # select the previous successful slot and reboot
ota recover      # restore an interrupted protected-data transaction at boot
```

The early `S21aiden_ota_recovery` init script invokes `ota recover` before the
Agent and Config Web start. Snapshots contain only configuration and service
identity files. User memory, notification JSONL, recordings, skills, caches,
logs, BLE bonds, and other append-only data are deliberately not bulk restored.

The full design, including the planned observation window and future schema
migration work, is maintained at:

[OTA 自检、数据兼容与 A/B 回退设计](../../../../docs/ota-self-check-rollback-design.md)
