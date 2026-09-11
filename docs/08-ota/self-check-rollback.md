---
sidebar_position: 5
---

# OTA Self-check and Rollback Implementation

The OTA runtime now performs read-only service probes before committing a
pending A/B slot. The Agent checks frame_service, audio_service, its HTTP
health endpoint, USB HID nodes, BLE through `/run/ble_service/ble_service.sock`,
Phone Bridge connection state, and optional HDMI, Wi-Fi, and ECM conditions.
External conditions are warnings unless configured as required. Config Web is
probed through `/api/device/status`, which is the device's actual status route.

The OTA updater snapshots protected configuration before writing the target
slot and exposes:

```text
ota self-check   # emit and persist a health report
ota rollback     # select the previous successful slot and reboot
ota recover      # restore an interrupted protected-data transaction at boot
```

The early `S21aiden_ota_recovery` init script invokes `ota recover` before the
Agent and Config Web start. Recovery reconciles a rollback only when the
running slot and misc metadata agree: the selected old slot becomes
`rolled-back`, while an explicitly abandoned request returns to `committed`.
Snapshots contain only configuration and service identity files. User memory,
notification JSONL, recordings, skills, caches, logs, BLE bonds, and other
append-only data are deliberately not bulk restored.

Restore first validates the complete manifest and stages all replacement files
and undo copies beside their destinations. A preparation error changes no live
files. A rename or directory-sync error during installation triggers undo of
the files already replaced. The snapshot and pending state remain available
for retry on any restore error. If undo also fails, the error includes the
retained backup path; do not delete the snapshot or mark the slot committed.
Multi-file installation is not atomic across power loss. Recovery must run
before configuration consumers start, and both slot images need the recovery
components. If `S21aiden_ota_recovery` fails, `rcS` stops later init scripts,
including networking and SSH. Use the serial console to repair the reported
storage/snapshot or executable error, rerun `ota recover`, and reboot after it
succeeds. A failure is recorded in `/var/log/ota/ota-recovery.log`.

Snapshot creation removes incomplete directories on ordinary errors. If saving
the new state fails, it removes the snapshot only when the on-disk state proves
it is unreferenced; a rename followed by an fsync failure keeps it. Creation
and boot recovery scan `transactions/pre-*` directly, including orphaned and
incomplete snapshots left by power loss. Retention keeps at most eight snapshot
directories, protecting the snapshot referenced by state and the one being
created. It does not depend on every snapshot having a state record.

Each subprocess and BLE probe receives its own child context with a default
four-second timeout. A probe timeout does not consume later probes' budgets;
the parent context can impose a shorter deadline. There are six timed probes
by default, seven if USB ECM is present, and at most nine with Wi-Fi and HDMI
required: nominal timeout sums are 24, 28, and 36 seconds respectively, plus
filesystem and process scheduling overhead. HTTP probes also set curl's
three-second limit. Local filesystem operations are not context-cancellable.
The daemon waits three seconds initially and five seconds after each failed
attempt, for up to 60 attempts; this is not a five-minute wall-clock bound.
The independent `S54ota` health monitor defaults to a five-minute marker wait,
so retries must succeed within that monitor window.

The full design, including the planned observation window and future schema
migration work, is maintained at:

[OTA 自检、数据兼容与 A/B 回退设计](../ota-self-check-rollback-design.md)
