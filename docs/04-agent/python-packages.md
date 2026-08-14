# Persistent Python Package Environment

> Status: implemented.

## Overview

The firmware includes pip in the Buildroot root filesystem. The Agent may
use its existing shell tool to install a missing Python dependency; no new
package-install tool or package-manager module is introduced.

Runtime-installed packages live under the fixed root:

```text
/userdata/agent/python
```

`PYTHONUSERBASE` points directly at this root. pip creates its standard
version-specific import directory, such as `lib/python3.11/site-packages/`,
under the fixed base. The firmware-provided `/usr/bin/python3` remains the only
Python interpreter.

## Goals

- Make pip available in newly built firmware images.
- Keep runtime-installed packages out of `/usr` and the A/B rootfs slots.
- Reuse pip's standard user-install behavior rather than creating another
  package abstraction.
- Keep pip downloads and extraction out of the small `/tmp` tmpfs.
- Preserve packages across reboot and rootfs OTA while they remain compatible.
- Let `StorageMonitor` reclaim abandoned temporary data and, only at the highest
  storage alert level, discard the rebuildable package environment.

## Non-goals

- Adding a dedicated Python package Agent tool or HTTP endpoint.
- Adding a `[python_packages]` configuration section.
- Enforcing package policy through a security boundary. The Agent already has
  a general shell tool.
- Providing transactional installation or automatic rollback after pip errors.
- Building source distributions or native extensions on the board.
- Maintaining virtual environments, package generations, manifests, reports,
  dependency lockfiles, or a separate package database.
- Automatically installing system libraries through `opkg` or Entware.

## Firmware Integration

Enable the SDK-provided pip package in both active Luckfox Buildroot defconfigs:

```text
BR2_PACKAGE_PYTHON_PIP=y
```

The affected files are in the `pico-sdk` submodule:

```text
pico-sdk/sysdrv/tools/board/buildroot/luckfox_pico_defconfig
pico-sdk/sysdrv/tools/board/buildroot/luckfox_pico_w_defconfig
```

The pinned SDK uses Buildroot 2023.02.6, Python 3.11.6, and pip 22.3.1. The root
repository build-policy assertion requires both defconfigs to retain the pip
option. Buildroot 2023.02.6 otherwise combines `aiohttp` 3.8.3 with incompatible
`charset-normalizer` 3.0.1, so the SDK reproducibly overrides that package
recipe with compatible version 2.1.1. Runtime code must not self-upgrade the
rootfs pip installation.

## Persistent Layout

The managed user base uses pip's normal user-install layout:

```text
/userdata/agent/python/
├── bin/
└── lib/
    ├── python3.11/site-packages/
    └── python3.12/site-packages/
```

Python import packages are naturally separated by the firmware interpreter's
major/minor version. Package-provided command-line programs share `bin/`, so an
OTA that changes Python versions may leave an incompatible CLI until the
package is reinstalled or the whole user base is reclaimed at Emergency.

pip download and extraction work uses the separate system temporary directory
`/userdata/tmp`. It is outside the package root and is treated as disposable
temporary storage, not persistent application data.

The root path is fixed and is not an Agent configuration field. It is under
`/userdata`, the filesystem already monitored by `StorageMonitor`, and is not
migrated to removable SD storage.

## Shell Environment

The firmware configures the real Python and pip variables once for services and
login shells:

```text
PYTHONUSERBASE=/userdata/agent/python
PIP_USER=1
PIP_NO_CACHE_DIR=1
PIP_DISABLE_PIP_VERSION_CHECK=1
```

`/etc/profile.d/aiden-python.sh` owns these fixed values. Login shells source it
through `/etc/profile.d`; `aiden-env-run` sources that fixed profile path after
`/userdata/system/env` before launching services. The system environment cannot
redirect or bypass the profile. The Agent and user shells therefore use the
same package environment without command-scoped user-base or pip-variable
injection.

`S53agent` creates `/userdata/agent/python` as `0755` and `/userdata/tmp` as
`01777` at service startup. The profile only exports variables, so opening a
login shell never creates or changes host directories. The Go runtime validates
and prepares the same paths again as a fallback before enabling the shell
temporary-directory override.

The agent's shell tool injects `TMPDIR=/userdata/tmp` command-scoped to avoid
the small `/tmp` tmpfs (73 MB) when running pip, while preventing storage wear
from a global `TMPDIR` override that would affect all services.

Package installation uses the inherited environment directly:

```bash
/usr/bin/python3 -m pip install --only-binary=:all: \
  'packaging==24.2'

/usr/bin/python3 -m pip check
```

Ordinary firmware Python automatically sees the managed user site:

```bash
/usr/bin/python3 script.py
```

For a package-provided CLI, append its directory only for that invocation:

```bash
PATH="$PATH:$PYTHONUSERBASE/bin" \
package-command
```

The package `bin/` directory is not added globally to `PATH`, so package CLIs
cannot shadow firmware commands by default.

## Agent Installation Guidance

The default Agent prompt instructs the model to:

- Prefer the Python standard library and already installed packages.
- Use an exact top-level `name==version` requirement.
- Use `--only-binary=:all:` so pip never builds a source distribution on the
  board.
- Never self-upgrade the firmware-provided `pip`, `setuptools`, or `wheel`.
- Reuse an already importable runtime package instead of installing it again.
- Run `pip check` after installation with a Shell timeout of at least 120
  seconds. If it times out, retry once with a longer bounded timeout, and do
  not report installation success unless the check completes successfully.
- Avoid starting a package installation while `/run/agent/storage_level`
  reports a non-Normal level or `/userdata` is visibly low on free space.
- Inspect the pip error and retry with a compatible version when appropriate;
  avoid unbounded retry loops.
- Report native or system-library failures rather than automatically invoking
  `opkg`.

`--only-binary=:all:` prevents source builds, but it does not mean every wheel
is pure Python. pip may accept a compatible platform-specific wheel. Dynamic
packages are therefore rebuildable runtime data, not part of the firmware
compatibility contract. If a package fails after OTA, the Agent may reinstall a
compatible version or report the incompatibility.

Installation is intentionally non-transactional. A pip or storage failure may
leave partial files in the active user base. The Agent should run `pip check`,
retry only when the error is recoverable, and report persistent failure.

## Shared Path Helper

Runtime preparation and Python cleanup use the same small path helper inside the
existing `agent` package rather than a separate package manager abstraction.

The helper is responsible for:

- Returning the fixed root and temporary directory.
- Creating `/userdata/agent/python` with mode `0755`.
- Creating `/userdata/tmp` with mode `01777`.
- Rejecting symlinks in place of either managed directory.

Host tests pass temporary root and tmp paths into the helper. Those parameters
are internal test seams, not device configuration fields.

## StorageMonitor Integration

Register one cleaner named:

```text
python_userbase
```

The cleaner implements regular temporary cleanup plus an Emergency-only cleanup
extension understood by StorageMonitor.

### Normal cleanup

- Remove direct children of `/userdata/tmp` whose modification time is older
  than 24 hours.
- Keep all content under `/userdata/agent/python`, including packages installed
  by earlier firmware Python versions.

### Manual force cleanup

- Keep the 24-hour age rule for temporary entries, because a recent entry may
  belong to a running pip command.
- Keep the entire Python user base while the effective storage level is below
  Emergency.

### Emergency cleanup

- When the effective storage level reaches `Emergency`, remove
  `/userdata/agent/python` as one rebuildable package environment.
- Continue to apply the 24-hour age rule to `/userdata/tmp`; recent temporary
  entries may belong to a running command.
- Do not activate this destructive behavior merely because a manual request uses
  `force=true` at a lower storage level.

### Filesystem safety

The cleaner must:

- Examine only direct children of `/userdata/tmp` during regular cleanup.
- Use `Lstat` and never follow symlinks.
- Reject a symlink in place of the managed user-base root.
- Never treat `/userdata/tmp` as durable storage; other services must keep
  persistent state elsewhere.

Manual cleanup uses the existing category target:

```json
{
  "force": false,
  "targets": ["python_userbase"]
}
```

Normal Agent runs are already serialized by the runtime. The MVP does not add
an installation mutex or filesystem lock. Background shell installations and
manual SSH pip commands may overlap and are explicitly outside the supported
concurrency model.

## OTA Behavior

| Event                       | pip in rootfs                        | Dynamic packages                                                        |
| --------------------------- | ------------------------------------ | ----------------------------------------------------------------------- |
| Reboot                      | Remains installed                    | Reuse the fixed user base                                               |
| A/B switch with Python 3.11 | Present in selected rootfs           | Reuse `lib/python3.11/site-packages`                                    |
| Rootfs OTA with Python 3.11 | Present if the new image retains pip | Reuse installed packages; reinstall any incompatible package           |
| Upgrade to Python 3.12      | New rootfs pip is used               | Use `lib/python3.12/site-packages`; older 3.11 packages remain retained |
| Rollback to Python 3.11     | Old rootfs pip is used               | Reuse retained `lib/python3.11/site-packages`                           |
| Emergency cleanup           | Remains installed                    | Entire user base is removed; packages must be reinstalled               |

## Security and Reliability Constraints

- Downloaded packages are executable code. Exact versions and binary-only
  installation do not make an untrusted package safe.
- The package index, proxy, and TLS behavior are inherited from the existing
  shell environment.
- No rootfs system directory is mutated at runtime.
- Because `PYTHONUSERBASE` is global, an installed user package can shadow a
  firmware Python package. This is an accepted consequence of sharing one
  package environment between the Agent and user shells.
- Package CLIs are not placed on global `PATH`.
- The entire user base is protected below Emergency, including manual force
  cleanup, but is intentionally reclaimable at Emergency.
- Direct pip installation is non-transactional and has no enforced storage
  quota or concurrency lock in the MVP.

## Implementation Locations

| Area                                            | Files                                                                                                                      |
| ----------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| Buildroot pip and compatible Python package set | Two Luckfox defconfigs and the project-owned `python-charset-normalizer-aiden` recipe override in the `pico-sdk` submodule |
| Build policy                                    | `scripts/test_reproducible_rootfs_policy.sh` or a focused adjacent test                                                    |
| Shared path helper                              | `src/agent/internal/agent/python_packages.go`                                                                              |
| Shell environment                               | `overlay/etc/profile.d/aiden-python.sh`, `overlay/oem/usr/bin/aiden-env-run`, `overlay/etc/init.d/S53agent`, and `tools_shell_session.go` |
| Storage cleanup                                 | `storage_cleaner_python_userbase.go` and `storage_runtime.go`                                                              |
| Agent guidance                                  | `src/agent/internal/agent/prompt.go`                                                                                       |
| Documentation                                   | This document, firmware guide, StorageMonitor guide, and Agent overview                                                    |

No new Agent tool, HTTP tool metadata, package configuration, C++ service, or
phone companion app change is required.

## Verification Plan

### Host tests

- Startup and runtime path preparation create the user-base/tmp directories
  with the expected permissions and reject symlink roots.
- `aiden-env-run` and login shells receive the same fixed `PYTHONUSERBASE` and
  pip variables, overriding conflicting values in `/userdata/system/env`; the
  system environment cannot redirect the fixed Python profile.
- Sourcing the Python profile has no filesystem side effects.
- Shell commands inherit the global Python/pip variables and receive only the
  command-scoped `TMPDIR` override.
- Foreground, background, and PTY shell paths receive the same temporary
  directory.
- Normal and manual force cleanup preserve the user base and remove only temp
  entries older than 24 hours.
- Automatic Emergency cleanup removes the whole user base; lower levels never
  activate that behavior.
- Symlink roots and temporary symlink entries are never followed.
- `python_userbase` is accepted as a manual StorageMonitor cleanup target.
- Both Buildroot defconfigs retain `BR2_PACKAGE_PYTHON_PIP=y`.
- The default prompt contains the direct shell installation policy.

### Board verification

1. Build and install a full rootfs image.
2. Verify `/usr/bin/python3 -m pip --version` reports the firmware pip.
3. Verify the Agent service and an SSH login shell report the same four fixed
   Python/pip variables.
4. Install an exact binary wheel through the Agent shell.
5. Verify an ordinary `/usr/bin/python3` command imports the package.
6. Reboot and verify the package remains available.
7. Verify a source-only package fails without starting an on-device build.
8. Verify pip failure followed by `pip check` produces a clear diagnostic.
9. Verify normal and manual force cleanup preserve packages, while Emergency
   cleanup removes the entire user base.
10. Perform an A/B switch or Python-version OTA and verify version-specific
    imports reuse retained packages or require an explicit reinstall.

## Related Documentation

- [Firmware Build and Flashing](../01-getting-started/firmware.md)
- [Storage Subsystem: StorageManager and StorageMonitor](storage-manager.md)
- [Agent Overview](overview.md)
- [Agent Configuration Reference](configuration.md)
