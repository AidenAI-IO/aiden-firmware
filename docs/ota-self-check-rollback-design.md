---
sidebar_position: 8
---

# OTA 自检、数据兼容与 A/B 回退设计

OTA updates write only the inactive A/B slot. Before writing, the updater
creates a durable snapshot of the protected configuration files listed in the
transaction manifest. Append-only user data remains in place.

After reboot, the Agent runs read-only service probes. Required probe failures
prevent the health marker from being written, so the boot attempt remains
eligible for rollback. A successful marker commits the selected slot and clears
the pending transaction.

Rollback selects the last successful slot and restores the protected snapshot
before committing the bootloader selection. Early boot recovery reconciles
completed and abandoned rollback requests without restoring unrelated user
data. See [the operational self-check and rollback guide](08-ota/self-check-rollback.md)
for commands and validation details.
