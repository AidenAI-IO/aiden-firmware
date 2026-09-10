# Luckfox OTA deployment validation — 2026-09-10

Validated implementation commit `530d2765` on `luckfox`, using the existing
`feat/ota-network-rollback` worktree. The board is left running **slot B** with
the new Agent, OTA CLI, and early recovery script. Both slots remain successful.
This was a binary deployment and controlled rollback drill, not a full signed
firmware update or an exhaustive test of the self-check design.

## Deployment

The previous session completed `./build.sh binaries`, `go test ./...`,
`scripts/test_ota_init.sh`, and `git diff --check`. This session used those
artifacts without changing runtime source code.

Agent and Config Web were stopped for replacement because both use the Agent
binary. They were restarted after deploying:

| Board file | SHA-256 |
| --- | --- |
| `/oem/usr/bin/agent` | `45bf79789a1538da1e65cdafe59e9953f124735483a80a29ff8068c15fdc6c16` |
| `/oem/usr/bin/ota` | `b3a50b6670bb5e85a925a135bf295f00f9f5c06684acb6db172fcc5045a784f6` |
| `/etc/init.d/S21aiden_ota_recovery` | `eae29056c5281b2c7caa8538ddacf17c5b45d878c29792e50db6f813d3b9625a` |

These hashes still matched after the user rebooted the board and after the
subsequent B → A → B drill. Frame/audio binaries were not replaced.

## Checks performed

| Check | Result |
| --- | --- |
| Normal self-check, including after reboot | 10 pass, 4 warn, 0 fail |
| Required-service failure | With Agent HTTP available and frame service stopped, daemon self-check reported 9 pass, 4 warn, 1 fail; no health marker was written |
| Recovery of required service | After frame service restarted, the next successful daemon check wrote a marker matching the test nonce and current boot ID |
| Health processing | `ota health` accepted that marker and removed the synthetic pending boot record; both real slots stayed successful |
| Protected-config restore | An isolated test transaction restored `agent.toml` after an added TOML comment; its bytes matched the snapshot afterward |
| Repeat recovery | A second `ota recover` on that transaction succeeded without changing the restored file |
| Early recovery with no pending transaction | `S21aiden_ota_recovery start` returned success |
| Real rollback | `ota rollback` on B rebooted into A; `/proc/cmdline` reported both `rootfs_a` and `aiden.slot_suffix=_a` |
| Old-slot services | SSH, Agent `/health`, frame UDS health, and audio UDS health worked on A |
| Return to B | `abctl set-active ... b --successful 1 --tries 0`, sync and reboot returned to B |
| Final services | Agent, frame and audio were running; Agent `/health` and Config Web `/api/device/status` returned HTTP 200 |

The daemon rejected the missing frame service on two consecutive attempts at
07:54:17 and 07:54:22 UTC, then wrote the marker at 07:54:28 UTC after recovery.
The final B self-check completed at 09:54:52 UTC.

The protected files present on this board (`agent/agent.toml`, `system/env`,
`wpa_supplicant.conf`, and `system/wifi-proxies.json`) retained their pre-deploy
hashes throughout. The playback-volume file was absent and was not tested.

For existing long-term memory, episode files, and notification storage, 34 of
35 sampled file hashes were unchanged on A. The remaining file,
`memory/notifications/state.json`, differed only in its runtime `generation`
field. Notification event files and fingerprints were unchanged. This checks
the board's existing data; it does not prove compatibility for future schema
migrations.

## Findings and limits

1. **Rollback metadata does not reconcile automatically.** On A, misc and the
   kernel correctly selected A, but `state.json` remained `rollback-requested`,
   with B's `current_version` and `active_slot`. Returning to B did not clear it
   either. After preserving the evidence and confirming B with no pending
   update, the drill restored the pre-rollback B state file. Final phase is
   `committed`; this cleanup was manual, not evidence that reconciliation works.
2. **Two optional probes target unavailable interfaces.** Config Web is healthy
   at `/api/device/status`, while the check calls `/api/status` and gets 404.
   The BLE check calls `/oem/usr/bin/ble_service_cli`, which is absent on this
   board; the implementation needs to use the existing BLE UDS status client.
   The other two warnings are the intentionally optional Wi-Fi and HDMI checks.
3. **Phone Bridge HTTP success is not connection health.** The report marks
   `phone_bridge` as pass even when the response says `connected:false`.
   This drill does not establish phone-link or Internet reachability.
4. **CLI exit status alone is insufficient.** During the frame outage,
   `ota self-check` emitted failures in JSON but still exited successfully.
   Consumers currently need to inspect `failures`; the daemon already does so.
5. **Automatic boot-failure rollback remains a separate test.** No boot-try
   exhaustion, sustained network failure, observation-window monitoring,
   incompatible schema migration, or power interruption during restore was
   injected. Configuration recovery used an isolated transaction and a benign
   comment change, not an OTA image write. The previous slot is older firmware,
   so deploying a recovery hook on B alone does not establish early recovery
   support in A.

The A slot's recorded partition version is `20260902-104817-fc3910d`; B's is
`20260904-102600-b0dc22b`. Manual binary deployment does not change these firmware
version records; the hashes above identify the deployed test binaries.

## Final state and retained evidence

- Running kernel/rootfs: B.
- Slot A: priority 14, tries 0, successful true.
- Slot B: priority 15, tries 0, successful true.
- OTA phase: `committed`; no `pending_boot.json` or test `health.ok` remains.
- Runtime code stays at `530d2765`; the branch has not been pushed.
- Original binaries, protected configuration backups, incoming build artifacts,
  and diagnostic reports are archived on the board under
  `/userdata/ota-rollback-validation-530d2765/deployment/`.
- Before the real slot switch, memory, sessions, skills and skill-state were
  also backed up to
  `/userdata/ota-rollback-validation-530d2765/agent-userdata-before.tar`.
- Reports are under the archive's `validation/` directory. Recovery-test state
  files are historical evidence, not active OTA transactions.

The archive was moved off the dedicated `/userdata/ota` filesystem after the
drill so deployment backups do not consume OTA download capacity. Backups may
contain private configuration and are intentionally not committed to Git.
