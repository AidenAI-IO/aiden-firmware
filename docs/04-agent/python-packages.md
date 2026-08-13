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

The active user base is derived from the firmware interpreter's major/minor
version:

```text
/userdata/agent/python/py<major>.<minor>
```

For the current Python 3.11 firmware this resolves to
`/userdata/agent/python/py3.11`. pip owns the internal `bin/` and
`lib/python3.11/site-packages/` layout.

## Goals

- Make pip available in newly built firmware images.
- Keep runtime-installed packages out of `/usr` and the A/B rootfs slots.
- Reuse pip's standard user-install behavior rather than creating another
  package abstraction.
- Keep pip downloads and extraction out of the small `/tmp` tmpfs.
- Preserve packages across reboot and rootfs OTA while the selected environment
  remains compatible.
- Let `StorageMonitor` reclaim abandoned temporary data and obsolete
  Python-version directories without deleting the current environment.

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

The managed root contains only versioned pip user bases and a temporary work
directory:

```text
/userdata/agent/python/
├── py<major>.<minor>/
└── tmp/
```

`py<major>.<minor>` is a package user base, not another Python interpreter.
The executable remains the firmware-provided `/usr/bin/python3`. With Python
3.11, pip creates the ordinary user-install layout:

```text
/userdata/agent/python/py3.11/
├── bin/
└── lib/python3.11/site-packages/
```

The Agent queries `/usr/bin/python3` for `sys.version_info.major` and
`sys.version_info.minor`. Python 3.11 selects `py3.11`; a later Python 3.12
firmware automatically selects `py3.12`.

The root path is fixed and is not an Agent configuration field. It is under
`/userdata`, the filesystem already monitored by `StorageMonitor`, and is not
migrated to removable SD storage.

## Shell Environment

The Agent must not globally set `PYTHONUSERBASE`, `PIP_USER`, `TMPDIR`, or add
the package `bin` directory to `PATH`. A Python user site precedes the system
site-packages directory on `sys.path`, so global injection could make an
Agent-installed package shadow a firmware package for unrelated system Python
commands.

Instead, the Agent shell receives neutral path hints:

```text
AIDEN_PYTHON_USERBASE=/userdata/agent/python/py<major>.<minor>
AIDEN_PYTHON_TMP=/userdata/agent/python/tmp
```

These names have no built-in meaning to Python or pip. The runtime derives the
version suffix once from the firmware `/usr/bin/python3` interpreter and injects the same
values into foreground, background, and PTY shell commands.

When installing a package, the Agent scopes the Python variables to that shell
command:

```bash
mkdir -p "$AIDEN_PYTHON_USERBASE" "$AIDEN_PYTHON_TMP"

PYTHONUSERBASE="$AIDEN_PYTHON_USERBASE" \
PIP_USER=1 \
PIP_NO_CACHE_DIR=1 \
PIP_DISABLE_PIP_VERSION_CHECK=1 \
TMPDIR="$AIDEN_PYTHON_TMP" \
/usr/bin/python3 -m pip install --only-binary=:all: --no-cache-dir \
  'packaging==24.2'

PYTHONUSERBASE="$AIDEN_PYTHON_USERBASE" /usr/bin/python3 -m pip check
```

When running Python code that needs an installed dependency, the Agent scopes
the same user base to that command:

```bash
PYTHONUSERBASE="$AIDEN_PYTHON_USERBASE" /usr/bin/python3 script.py
```

For a package-provided CLI, append its directory only for that invocation:

```bash
PYTHONUSERBASE="$AIDEN_PYTHON_USERBASE" \
PATH="$PATH:$AIDEN_PYTHON_USERBASE/bin" \
package-command
```

## Agent Installation Guidance

The default Agent prompt instructs the model to:

- Prefer the Python standard library and already installed packages.
- Use an exact top-level `name==version` requirement.
- Use `--only-binary=:all:` so pip never builds a source distribution on the
  board.
- Never self-upgrade the firmware-provided `pip`, `setuptools`, or `wheel`.
- Check the scoped managed user base before deciding a package is absent, so an
  existing runtime-installed package is reused instead of installed again.
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

Shell environment injection and Python cleanup need the same path rules. Keep
those rules in one small helper inside the existing `agent` package rather than
creating a new `pythonenv` package.

The helper is responsible for:

- Returning the fixed root and temporary directory.
- Querying the firmware `/usr/bin/python3` interpreter and deriving the active `py<major>.<minor>`
  directory.
- Creating the root, active version directory, and temporary directory when
  needed.
- Touching the active version directory during Agent startup and periodic
  storage maintenance.
- Applying the neutral `AIDEN_PYTHON_USERBASE` and `AIDEN_PYTHON_TMP` hints to
  shell environments.

Host tests may inject a temporary root and a fake interpreter-version command.
Those are internal test seams, not device configuration fields.

## StorageMonitor Integration

Register one cleaner named:

```text
python_userbase
```

One cleaner performs both cleanup phases so StorageMonitor's stop-after-Normal
behavior cannot prevent the second phase from running.

### Normal cleanup

- Remove direct children of `tmp/` whose modification time is older than 24
  hours.
- Remove non-current `py<major>.<minor>` directories whose modification time is
  older than 7 days.
- Never enter or remove the active version directory.

### Force cleanup

- Keep the 24-hour age rule for temporary entries, because a recent entry may
  belong to a running pip command.
- Remove non-current Python-version directories without waiting for the seven-day
  retention period.
- Still never enter or remove the active version directory.

### Filesystem safety

The cleaner must:

- Examine only direct children of `/userdata/agent/python` and its `tmp/`
  directory.
- Match version directories with a strict `py<digits>.<digits>` name rule.
- Use `Lstat` and never follow symlinks.
- Ignore unknown files and directory names.
- Resolve the active version immediately before estimating or cleaning.
- Touch the active directory before scanning so a long-running device keeps an
  accurate last-active mtime.

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

| Event | pip in rootfs | Dynamic packages |
| --- | --- | --- |
| Reboot | Remains installed | Reuse the active version directory |
| A/B switch with Python 3.11 | Present in selected rootfs | Reuse `py3.11` |
| Rootfs OTA with Python 3.11 | Present if the new image retains pip | Reuse `py3.11`; reinstall packages that prove incompatible |
| Upgrade to Python 3.12 | New rootfs pip is used | Start with `py3.12`; retain `py3.11` for 7 days |
| Rollback during retention | Old rootfs pip is used | Reuse retained `py3.11` |
| Rollback after cleanup | Old rootfs pip is used | Packages must be reinstalled |

## Security and Reliability Constraints

- Downloaded packages are executable code. Exact versions and binary-only
  installation do not make an untrusted package safe.
- The package index, proxy, and TLS behavior are inherited from the existing
  shell environment.
- No system directory is mutated at runtime.
- Dynamic package paths are scoped to commands that need them, avoiding global
  system-package shadowing.
- The active version directory is protected from automatic and forced cleanup.
- Direct pip installation is non-transactional and has no enforced storage
  quota or concurrency lock in the MVP.

## Implementation Locations

| Area | Files |
| --- | --- |
| Buildroot pip and compatible Python package set | Two Luckfox defconfigs and the project-owned `python-charset-normalizer-aiden` recipe override in the `pico-sdk` submodule |
| Build policy | `scripts/test_reproducible_rootfs_policy.sh` or a focused adjacent test |
| Shared path helper | `src/agent/internal/agent/python_packages.go` |
| Shell environment | `src/agent/internal/agent/tools.go` and `tools_shell_session.go` |
| Storage cleanup | `storage_cleaner_python_userbase.go` and `storage_runtime.go` |
| Agent guidance | `src/agent/internal/agent/prompt.go` |
| Documentation | This document, firmware guide, StorageMonitor guide, and Agent overview |

No new Agent tool, HTTP tool metadata, package configuration, C++ service, or
phone companion app change is required.

## Verification Plan

### Host tests

- Interpreter discovery maps Python 3.11 and 3.12 to the expected version
  directories.
- Shell commands receive neutral path hints but not global `PYTHONUSERBASE`,
  `PIP_USER`, `TMPDIR`, or package `PATH` changes.
- Foreground, background, and PTY shell paths receive the same hints.
- The combined cleaner runs temporary and stale-version cleanup in one call.
- Normal cleanup respects 24-hour and seven-day retention.
- Force cleanup keeps the temporary-data age rule and only bypasses stale-version
  retention.
- The active directory, symlinks, nested paths, and unknown names are never
  selected.
- `python_userbase` is accepted as a manual StorageMonitor cleanup target.
- Both Buildroot defconfigs retain `BR2_PACKAGE_PYTHON_PIP=y`.
- The default prompt contains the scoped shell installation policy.

### Board verification

1. Build and install a full rootfs image.
2. Verify `/usr/bin/python3 -m pip --version` reports the firmware pip.
3. Verify ordinary `/usr/bin/python3` commands do not include the managed user site.
4. Install an exact binary wheel through the scoped shell command.
5. Verify the package is importable only when `PYTHONUSERBASE` is scoped into
   the command.
6. Reboot and verify the package remains available through the scoped command.
7. Verify a source-only package fails without starting an on-device build.
8. Verify pip failure followed by `pip check` produces a clear diagnostic.
9. Verify normal, force, active-directory, and symlink cleanup behavior.
10. Perform an A/B switch or same-version OTA and verify reuse or explicit
    reinstall on incompatibility.

## Related Documentation

- [Firmware Build and Flashing](../01-getting-started/firmware.md)
- [Storage Subsystem: StorageManager and StorageMonitor](storage-manager.md)
- [Agent Overview](overview.md)
- [Agent Configuration Reference](configuration.md)
