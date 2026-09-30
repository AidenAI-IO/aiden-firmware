---
sidebar_position: 11
---

# Agent Files Report

The Agent Files Report provides a browser-readable view of the Agent's memory
and Skills files. It extracts references between files and lets you navigate
from a file to the files it references.

## Report contents

The generator recognizes these reference identifiers:

- `ep_...` for episodes;
- `proc_...` for procedures;
- `app_...` for application records; and
- `fail_...` for failures.

Each matching file shows the reference type, identifier, and resolved path.
Selecting **Jump** opens the target file, scrolls it into view, and highlights
it briefly.

## Generate and view the report

On a device, run:

```bash
ssh root@<DEVICE_IP>
cd /userdata/agent_tools
./view_agent_files.sh
```

The generated report is `/userdata/agent/files_report.html`. Either copy it
to a local machine:

```bash
scp root@<DEVICE_IP>:/userdata/agent/files_report.html ~/Desktop/
open ~/Desktop/files_report.html
```

or stream it over SSH:

```bash
ssh root@<DEVICE_IP> "cat /userdata/agent/files_report.html" > report.html
```

The Agent Web service also exposes:

- `GET /user_files` to view the current report; and
- `POST /user_files/regenerate` to regenerate it.

These routes are served by the Agent Web endpoint on port 8080.

## Local implementation

The report tooling is implemented by:

- `scripts/generate_agent_files_report.py`;
- `scripts/agent_files_template.html`;
- `scripts/view_agent_files.sh`; and
- `src/agent/internal/agent/user_files.go`.

The source data is normally under `/userdata/agent/memory/` and
`/userdata/agent/skills/`. The report is a generated view and can be
regenerated after files change; it is not itself a source of truth.
