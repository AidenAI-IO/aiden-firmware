# VPhone iOS Benchmark Test Guide

## 1. Scope

In this guide, the Mac used to run the tests is referred to as `mac-black`.

There are two ways to run the benchmark. Both use the preparation and Bridge
validation procedures in Sections 3–6:

- **Command-line workflow (Sections 7–9):** Manually start the Agent daemon and
  then run tasks step by step with `runner run`. This workflow is suitable for
  debugging individual tasks and troubleshooting the execution path.
- **WebUI workflow (Section 7A):** Select a suite, configure the environment, and
  start a run from the browser. The WebUI manages the daemon and task IDs
  automatically. This workflow is suitable for routine regression testing and
  reviewing reports.

The actual paths, guest IP address, ports, and fixed benchmark task ID for
mac-black are stored in:

```text
path_to_project/benchmark/vphone/vphone.env
```

Run the following commands in each new terminal to export the configuration as
environment variables:

```bash
set -a
source path_to_project/benchmark/vphone/vphone.env
set +a
```

Subsequent commands do not repeatedly hard-code the project path or guest IP
address. The current key variables include:

- `$VPHONE_HARDWARE_DEMO_ROOT`
- `$VPHONE_BENCHMARK_ROOT`
- `$VPHONE_AGENT_CONFIG`
- `$VPHONE_CLI_ROOT`
- `$VPHONE_SOCKET`
- `$VPHONE_GUEST_SSH_HOST`
- `$VPHONE_BRIDGE_ENDPOINT`
- `$VPHONE_BENCHMARK_TASK_ID`

### Launcher Script (No Manual `source`)

To start the Bridge, Agent daemon, or WebUI, use the wrapper script
`benchmark/vphone/start.sh`. It sources `vphone.env` itself, so you do not have
to export the variables in each terminal first:

```bash
cd path_to_project/benchmark/vphone

./start.sh bridge      # start the Bridge (foreground)
./start.sh agent       # start the Agent daemon
./start.sh webui       # start the WebUI (:8765)
./start.sh env         # print the loaded config (API key not shown)
```

The script selects the config file in the order `$VPHONE_ENV_FILE` → the
adjacent `vphone.env` → `--env-file <path>`, and checks whether the target port
is already in use before starting; if it is, it prints the holding process PID
and a hint instead of a Python `Address already in use` traceback. After editing
`vphone.env`, rerun the same `./start.sh` command to pick up the new values (the
Bridge still has to be restarted to read a new guest IP).

When you run the §4–§9 check commands by hand (for example
`test -r "$VPHONE_AGENT_CONFIG"` or `curl "$VPHONE_BRIDGE_ENDPOINT/health"`),
those reference `$VPHONE_*` shell variables and still require
`source vphone.env` in that terminal first. `start.sh` only removes the manual
`source` step for launching services.

It is recommended to prepare three terminals:

- Terminal A: Keep the VPhone VM running.
- Terminal B: Keep the VPhone iOS Environment Bridge running.
- Terminal C: Run checks, start the Agent daemon, and run the benchmark.

## 2. Current Implementation Baseline

The current `vphone_ios_basic` suite version is `1.6` and contains nine tasks:
one `warmup` task (category `diagnostic`) plus eight scored tasks.

| Task ID | Validation |
| --- | --- |
| `warmup` | Warm-up call; only takes one screenshot, to absorb daemon cold start |
| `screenshot_home` | Capture and describe the screen |
| `go_home` | Return to the Home Screen |
| `open_settings` | Open Settings |
| `swipe_screen` | Swipe upward |
| `open_web_url` | Open Baidu through `quick_action.open_url` |
| `clock_count_alarms` | Open Clock and count the alarms |
| `settings_read_ethernet_ipv4` | Read the Ethernet IP address, subnet mask, and router |
| `open_app_library` | Open the App Library and identify visible apps |

`warmup` is the first task and only requires the agent to call `screenshot`
once. It does not reflect model capability; it exists so the Agent daemon
finishes its cold start before the scored tasks begin. With a larger model the
daemon takes longer to become ready on first use, and without a warmup the
runner marks the leading scored tasks as `skipped (agent not ready)`. Ignore the
`warmup` result when scoring.

This version does not depend on keyboard input. `open_web_url` opens the URL
through the iOS URL handler instead of typing it character by character in the
Safari address bar. The Bridge first attempts to use `open_url` through the
VPhone socket. If the current host-control does not support that command, it
invokes `uiopen -u` through guest SSH.

When testing directly on `mac-black`, the data path is:

```text
Benchmark Runner
  -> Aiden Agent daemon (Docker)
  -> host.docker.internal:8899
  -> mac-black 127.0.0.1:8899 (VPhone Bridge)
  -> $VPHONE_SOCKET
  -> iOS VM

Compatibility fallback for open_url/app_launch:
VPhone Bridge -> guest SSH $VPHONE_GUEST_SSH_HOST:$VPHONE_GUEST_SSH_PORT -> uiopen -> iOS VM
```

## 3. Preparation Before the First Test

### 3.1 Confirm the Code and Task Versions

Run the following in Terminal C:

```bash
cd "$VPHONE_HARDWARE_DEMO_ROOT"

git status --short
git branch --show-current
git log -1 --oneline

cd "$VPHONE_BENCHMARK_ROOT"
uv sync --group dev

uv run python -c 'import json; p=json.load(open("suites/vphone_ios_basic.json")); print(p["version"], len(p["tasks"]), [t["id"] for t in p["tasks"]])'
```

The expected version is `1.6`, and the expected task count is `9` (including the
leading `warmup`). If the branch
or task content does not match the version supplied by the person responsible,
stop testing and confirm the code version first. Do not switch branches directly
when uncommitted changes are present.

### 3.2 Run Tests Directly Related to This Functionality

```bash
cd "$VPHONE_BENCHMARK_ROOT"

uv run pytest -q \
  tests/test_vphone_bridge.py \
  tests/test_vphone_client.py \
  tests/test_vphone_device.py \
  tests/test_vphone_runner_integration.py \
  tests/test_vphone_start_bridge.py \
  tests/test_vphone_tools.py
```

The current baseline should show `44 passed`.

Then validate the Agent's `quick_action` schema:

```bash
cd "$VPHONE_HARDWARE_DEMO_ROOT/src/agent"
go test ./internal/agent -run 'Test.*QuickAction' -count=1
```

The expected output includes `ok aiden-agent/internal/agent`.

### 3.3 Confirm That Docker Desktop Is Operational

The Agent daemon runs in Docker:

```bash
docker info >/dev/null && echo "Docker is ready"
```

If this command fails, start Docker Desktop and wait for it to finish
initializing before continuing.

### 3.4 Check the Agent Model Configuration

The model configuration used by the Agent and the Judge configuration are
separate. This test uses `$VPHONE_AGENT_CONFIG`. Before starting the test, only
verify that the file exists and is readable by the current user:

`--no-judge` disables only result evaluation; it does not disable Agent model
calls. Even when `--no-judge` is used, the Agent's OpenRouter API key must still
be valid.

```bash
test -r "$VPHONE_AGENT_CONFIG" \
  && echo "Agent config is ready" \
  || echo "Agent config is missing or unreadable"
```

Continue by checking whether the OpenRouter configuration contains credentials,
without printing the credential value:

```bash
cd "$VPHONE_BENCHMARK_ROOT"

uv run python - <<'PY'
import os
import tomllib
from pathlib import Path

path = Path(os.environ["VPHONE_AGENT_CONFIG"])
model = tomllib.loads(path.read_text(encoding="utf-8")).get("model", {})
provider = str(model.get("provider", "")).strip()
model_name = str(model.get("model", "")).strip()
has_api_key = bool(str(model.get("api_key", "")).strip())

print(f"provider={provider}")
print(f"model={model_name}")
print(f"api_key_present={str(has_api_key).lower()}")

if provider == "openrouter" and not has_api_key:
    raise SystemExit("OpenRouter api_key is missing from VPHONE_AGENT_CONFIG")
PY
```

Do not copy, modify, or commit this configuration file. Do not expose its
contents in test records, terminal screenshots, or logs, as this could disclose
the model API key. If the check fails, contact the configuration file
maintainer.

## 4. Start and Check the VPhone VM

If the VPhone VM is already running normally, do not run `make boot` again.
Check the socket directly.

Run the following in Terminal A:

```bash
test -S "$VPHONE_SOCKET" \
  && echo "vphone.sock is ready" \
  || echo "vphone.sock is missing"
```

If the socket does not exist, start the VM in Terminal A and leave the command
running:

```bash
cd "$VPHONE_CLI_ROOT"
make boot
```

Open another terminal and check host-control:

```bash
printf '%s\n' '{"t":"status","screen":false}' \
  | nc -U "$VPHONE_SOCKET"
```

The newer host-control should return status and capability information. If an
older version returns `unknown command`, the Bridge can still fall back to a
real screenshot for its readiness probe, so testing does not need to stop.

Perform one more host screenshot check. Different host-control versions return
different response formats. The following command supports both protocols; it
is sufficient to determine whether the socket can produce an image:

```bash
HOST_SMOKE_SHOT=/tmp/vphone-host-smoke.png

printf '{"t":"screenshot","path":"%s","screen":false}\n' "$HOST_SMOKE_SHOT" \
  | nc -U "$VPHONE_SOCKET" \
  | head -c 200
echo
file "$HOST_SMOKE_SHOT" 2>/dev/null
```

- The newer host-control returns `{"ok":true}`, writes the PNG to `path`, and
  allows `file` to identify the image.
- The older version (`legacy_host_control`) does not write to `path`; instead,
  it returns `{"image":"/9j/4AAQSkZJRg..."}` inline. The `/9j/` prefix indicates
  a base64-encoded JPEG and confirms that screenshot capability is working. It
  is expected for the `file` command to report that the file does not exist.
  Testing can continue in either case. The actual screenshot path is validated
  consistently by `validate_bridge` in Section 6.2.

Before starting automation, also confirm the following manually:

- iOS has completed its initial setup and is unlocked and operable.
- No system update, permission request, or first-use guidance dialog is visible.
- The VM can access the internet; `open_web_url` needs to load
  `https://www.baidu.com`.
- An "Ethernet" entry is present on the main Settings page. IPv4 values are
  assigned dynamically by DHCP; do not assume fixed answers.

## 5. Start the VPhone Bridge

Run the following in Terminal B and leave it running in the foreground:

```bash
cd "$VPHONE_BENCHMARK_ROOT"

uv run python -m vphone.scripts.start_bridge \
  --env-file "$VPHONE_ENV_FILE"
```

Or use the no-`source` launcher (equivalent, and it reports clearly when the
port is already in use):

```bash
cd path_to_project/benchmark/vphone
./start.sh bridge
```

The expected output is:

```text
VPhone iOS environment started
environment_url=http://127.0.0.1:8899
screen_width=<native width>
screen_height=<native height>
```

The guest IP address, port, user, and key are provided by
`VPHONE_GUEST_SSH_HOST`, `VPHONE_GUEST_SSH_PORT`, `VPHONE_GUEST_SSH_USER`, and
`VPHONE_GUEST_SSH_IDENTITY`, respectively. After changing the guest network
configuration, update only `vphone.env` and restart the Bridge; do not modify the
Python code.

Using `--no-guest-ssh-fallback` is not recommended during initial integration.
Even if the newer socket directly supports `open_url`, retaining the fallback
does not change the normal path.

## 6. Validate the Bridge

### 6.1 HTTP Health Check

Run the following in Terminal C:

```bash
curl -sS "$VPHONE_BRIDGE_ENDPOINT/health"
```

The result should include:

```json
{"ok":true,"data":{"bridge_type":"vphone_ios","concurrent":1}}
```

Additional fields are permitted, but `ok`, `bridge_type`, and `concurrent` must
have the expected values.

### 6.2 Run the Automated Validation Script

```bash
cd "$VPHONE_BENCHMARK_ROOT"

uv run python -m vphone.scripts.validate_bridge \
  --endpoint "$VPHONE_BRIDGE_ENDPOINT" \
  --benchmark-task-id "$VPHONE_BENCHMARK_TASK_ID" \
  --screenshot-out /tmp/vphone-bridge-smoke.jpg

file /tmp/vphone-bridge-smoke.jpg
```

The script should return `"ok": true` and list `health`, `concurrent=1`,
`screen-jpeg`, `setup-home`, `ownership-429`, `tool-catalog`,
`screenshot-tool`, and `release`.

### 6.3 Validate `open_url` Separately

This step validates both `quick_action.open_url`, which is required by the
latest task, and the SSH fallback when necessary:

```bash
curl -sS -X POST "$VPHONE_BRIDGE_ENDPOINT/api/setup" \
  -H 'Content-Type: application/json' \
  -H "benchmark-task-id: $VPHONE_BENCHMARK_TASK_ID" \
  --data '{}'

curl -sS -X POST "$VPHONE_BRIDGE_ENDPOINT/api/tools/quick_action" \
  -H 'Content-Type: application/json' \
  -H "benchmark-task-id: $VPHONE_BENCHMARK_TASK_ID" \
  --data '{"input":"{\"platform\":\"ios\",\"action\":\"open_url\",\"url\":\"https://www.baidu.com\"}"}'

curl -sS -X POST "$VPHONE_BRIDGE_ENDPOINT/api/release" \
  -H 'Content-Type: application/json' \
  -H "benchmark-task-id: $VPHONE_BENCHMARK_TASK_ID" \
  --data '{}'
```

The second response should have HTTP status 200 and include
`"is_error": false`. The iOS display should open Safari and show the Baidu
page. Regardless of whether validation succeeds, always call the final
`/api/release` endpoint to avoid leaving device ownership assigned.

## 7A. Run the Benchmark Through the WebUI

The WebUI automatically builds and starts the Agent daemon, generates a
separate benchmark task ID for each task, and handles setup and release.
Therefore, when using this workflow, Sections 7 (manual
`start-agent-daemon`) and 8 (manual `runner run`) are **not required**.
Preparation and Bridge validation in Sections 3–6 must still be performed as
usual, and both the VM (Section 4) and Bridge (Section 5) must remain running.

### 7A.1 Start the WebUI Service

Open another terminal on mac-black, or reuse Terminal C, and leave the process
running in the foreground:

```bash
set -a; source path_to_project/benchmark/vphone/vphone.env; set +a
cd "$VPHONE_BENCHMARK_ROOT"

uv run python -m runner webui \
  --host 127.0.0.1 \
  --port 8765 \
  --agent-config "$VPHONE_AGENT_CONFIG"
```

Or use the no-`source` launcher (equivalent):

```bash
cd path_to_project/benchmark/vphone
./start.sh webui
```

- `--agent-config` makes the WebUI's "Agent config" use the same model
  configuration as the command-line workflow
  (`provider=openrouter`, `model=moonshotai/kimi-k2.6`) instead of the default
  template.
- After a successful start, the log prints
  `Benchmark Web UI: http://127.0.0.1:8765`.
- The first WebUI job builds the `aiden-agent-daemon:local` image. If the image
  already exists, it is reused directly. Add `--no-build-daemon-image` to skip
  rebuilding it.

### 7A.2 Access the WebUI From a Local Browser Across Network Segments

The WebUI listens on `127.0.0.1:8765` on mac-black. If your browser is not
running on mac-black and is on a different network segment, create an SSH port
forward through the jump host. The following example assumes that the local
`~/.ssh/config` already contains a `mac-black` alias configured with
`ProxyJump`:

```bash
# Local port 18765 -> mac-black 127.0.0.1:8765 (two hops through the jump host)
ssh -f -N -o ExitOnForwardFailure=yes \
  -L 18765:127.0.0.1:8765 mac-black
```

Then open `http://127.0.0.1:18765` in the local browser. When operating directly
on mac-black, no tunnel is required; open `http://127.0.0.1:8765` directly.

### 7A.3 Select the Suite and Configure the Environment

1. In the **Suites** list on the left, select `vphone_ios_basic` (category:
   Other; 8 tasks). The "Run configuration" section at the top displays
   `1 suites selected`.
2. Click the blue **Run selected suites** button in the upper-right corner to
   open the **Choose Environment** dialog.
3. On the **Device** tab, which is selected by default, enter:
   - **Name:** Any recognizable name, such as `vphone-ios`.
   - **Endpoint:** `http://127.0.0.1:8899`, which is the Bridge address from
     Section 5 and does not need to be entered manually.
4. Click **Save device**. The environment appears in the list on the right and
   is selected automatically. The "Run configuration" text at the top of the
   dialog changes from `No environment` to `vphone-ios`.

Saved device environments persist. On subsequent runs, select the existing
environment in the dialog instead of entering it again.

### 7A.4 Run

- **First run only a path validation:** Keep only `vphone_ios_basic` selected
  and leave **Enable judge** unchecked, which is equivalent to the command-line
  `--no-judge` option. Click **Run selected suites** at the bottom of the
  Choose Environment dialog to start.
- The **Progress** section at the top shows
  `STARTING_AGENT → RUNNING`. The **Tasks / Passed / Failed / Skipped / Judge /
  Timeout** counters below it update in real time, and a new row with status
  `RUNNING` appears in the **Jobs** table.
- To abort the run, click **Stop** in the Progress section or the corresponding
  Jobs row.

The benchmark task IDs generated by the WebUI have the format
`vphone_ios_basic.json:<task_id>`. Each task uses a separate ID, so the `429`
ownership conflict associated with a fixed command-line task ID does not occur.
In a device environment, these tasks run sequentially on a single VPhone VM.
Concurrency is determined by the `concurrent` field returned by Bridge health;
for vphone iOS, it is 1.

### 7A.5 Enable the Judge for Formal Acceptance Testing

After confirming that the path works correctly, select **Enable judge**, enter
`anthropic/claude-sonnet-4-6` in **Judge model**, enter the OpenRouter key for
the Judge in **API key**, and click **Run selected suites** again. The Judge key
and Agent model key are separate and must not be used interchangeably. The
WebUI stores this setting only on the server and does not display the key in
plain text.

### 7A.6 Review the Report

After the job finishes, a `report` link appears in the **Report** column of the
corresponding **Jobs** row. Open it to view the combined HTML report. Click the
run ID in the **Job** column to review screenshots, traces, and logs for an
individual task. Raw artifacts are stored under
`benchmark/runs/webui/<job-id>/`.

## 7. Start the Agent Daemon (Command-Line Workflow)

The Agent configuration is copied only when the daemon starts. If a
`vphone-ios` daemon was started previously, changing environment variables or
the startup command does not update the old container. First run the
`stop_command` shown by the previous startup command.

If that command is no longer available, stop the old instance by its fixed
service name:

```bash
cd "$VPHONE_BENCHMARK_ROOT"

docker compose \
  -f docker/docker-compose.agent-daemon.yml \
  -p aiden-benchmark-agent-agent-vphone-ios \
  down --remove-orphans
```

Do not continue using the old `$VPHONE_AGENT_URL` after stopping the instance.

Run the following in Terminal C:

```bash
cd "$VPHONE_BENCHMARK_ROOT"

uv run python -m runner start-agent-daemon \
  --name vphone-ios \
  --environment-bridge-endpoint "$VPHONE_BRIDGE_ENDPOINT" \
  --benchmark-task-id "$VPHONE_BENCHMARK_TASK_ID" \
  --agent-config "$VPHONE_AGENT_CONFIG"
```

Or use the no-`source` launcher (equivalent; `--name`,
`--environment-bridge-endpoint`, `--benchmark-task-id`, and `--agent-config`
are filled in from `vphone.env`, and any extra arguments are passed through):

```bash
cd path_to_project/benchmark/vphone
./start.sh agent
```

This command builds or reuses the Agent image and starts the container in the
background. Record the following values from its output:

- `agent_url`: The address used by subsequent `runner run --agent-url`
  commands.
- `docker_environment_bridge_endpoint`: This should be
  `http://host.docker.internal:8899`.
- `benchmark_task_id`: This must match `$VPHONE_BENCHMARK_TASK_ID`.
- `stop_command`: Use this when testing is complete.

The `config_dir` in the output must be the service configuration directory
created for this run. The `agent.toml` in that directory should be copied from
`$VPHONE_AGENT_CONFIG`, rather than from the default `qwen3.6-35b` template.

Save the actual Agent URL in a variable:

```bash
export VPHONE_AGENT_URL="http://127.0.0.1:<port printed by start-agent-daemon>"
# The daemon's liveness probe uses the root path and returns HTTP 200.
# It does not provide a /health endpoint.
curl -sS -o /dev/null -w '%{http_code}\n' "$VPHONE_AGENT_URL/"
```

The `start-agent-daemon` output also prints a ready-to-use `run_command`
containing `--benchmark-token-file <config_dir>/control_token`. The runner uses
this token to communicate with the daemon's control interface. Therefore, every
`runner run` command in Section 8 must include `--benchmark-token-file`;
otherwise, authentication fails. Save it in a variable for reuse:

```bash
export VPHONE_AGENT_TOKEN_FILE="<config_dir>/control_token"
```

Replace the text in angle brackets; do not leave it unchanged. If
`--name vphone-ios` reports that a service with the same name is already
running, first confirm whether it is the daemon required for the current test.
When a restart is required, use the `stop_command` from the previous output or
choose a new `--name`.

## 8. Run the Benchmark From Low to High Risk (Command-Line Workflow)

> This section applies only to the command-line workflow. If using the WebUI in
> Section 7A, runs and reports are handled in the browser; skip this section.

All commands below must use the same
`--benchmark-task-id "$VPHONE_BENCHMARK_TASK_ID"`. If the daemon and runner use
different task IDs, tool requests receive `429 no_bridge_env_available`.

### 8.1 Run the Screenshot Task First

```bash
cd "$VPHONE_BENCHMARK_ROOT"

uv run python -m runner run \
  --suite suites/vphone_ios_basic.json \
  --task-id screenshot_home \
  --agent-url "$VPHONE_AGENT_URL" \
  --benchmark-token-file "$VPHONE_AGENT_TOKEN_FILE" \
  --environment-url "$VPHONE_BRIDGE_ENDPOINT" \
  --benchmark-task-id "$VPHONE_BENCHMARK_TASK_ID" \
  --no-judge \
  -v
```

### 8.2 Run the New URL Task Next

```bash
uv run python -m runner run \
  --suite suites/vphone_ios_basic.json \
  --task-id open_web_url \
  --agent-url "$VPHONE_AGENT_URL" \
  --benchmark-token-file "$VPHONE_AGENT_TOKEN_FILE" \
  --environment-url "$VPHONE_BRIDGE_ENDPOINT" \
  --benchmark-task-id "$VPHONE_BENCHMARK_TASK_ID" \
  --no-judge \
  -v
```

This step checks not only the Bridge, but also whether the Agent's
`quick_action.url` schema is passed to the Bridge correctly.

### 8.3 Run the Full Task Set (1 warmup + 8 scored tasks)

```bash
uv run python -m runner run \
  --suite suites/vphone_ios_basic.json \
  --agent-url "$VPHONE_AGENT_URL" \
  --benchmark-token-file "$VPHONE_AGENT_TOKEN_FILE" \
  --environment-url "$VPHONE_BRIDGE_ENDPOINT" \
  --benchmark-task-id "$VPHONE_BENCHMARK_TASK_ID" \
  --no-judge \
  -v
```

The first task to run is `warmup`, which waits for the daemon to become ready;
the eight scored tasks follow. A pre-started daemon with a fixed task ID is
suitable for sequential debugging. Do not add `--repeats` in this topology. To
repeat a test, run `runner run` multiple times separately.

`--no-judge` is used only to validate the execution path. In this mode,
acceptance of a completed run means:

- There are no Bridge protocol errors.
- There are no 429 ownership errors.
- There are no Agent or device timeouts.
- None of the eight scored tasks are `skipped` (every task after `warmup`
  should actually run).
- The tasks produce traces, screenshots, and reports.

It does not mean that multi-step task results are correct. Without the Judge,
the rubric may show `0/N`.

If leading scored tasks are still marked `skipped (agent not ready)`, the daemon
cold start is slower than the warmup allows (for example after switching to a
larger model). Send one request to the daemon manually to warm it up, or raise
`warmup`'s `must_complete_within_sec`, then rerun.

## 9. Enable the Judge for Formal Acceptance Testing (Command-Line Workflow)

After confirming that the entire path works without the Judge, set the
OpenRouter key used by the Judge in Terminal C:

```bash
export OPENROUTER_API_KEY="<OpenRouter API key used by the Judge>"
```

Then remove `--no-judge`:

```bash
uv run python -m runner run \
  --suite suites/vphone_ios_basic.json \
  --agent-url "$VPHONE_AGENT_URL" \
  --benchmark-token-file "$VPHONE_AGENT_TOKEN_FILE" \
  --environment-url "$VPHONE_BRIDGE_ENDPOINT" \
  --benchmark-task-id "$VPHONE_BENCHMARK_TASK_ID" \
  --judge-model anthropic/claude-sonnet-4-6 \
  -v
```

Manually review the following aspects of the formal results:

- Whether the final screenshot for `open_web_url` actually shows the Baidu page.
- Whether the answer from `clock_count_alarms` matches the alarm list.
- Whether `settings_read_ethernet_ipv4` remains on the Ethernet details page;
  whether the IP address, subnet mask, and router in the final answer match the
  screenshot item by item; and whether the network settings were left
  unchanged.
- Whether the apps listed by `open_app_library` are actually visible.

## 10. Review Artifacts

Each run creates a separate result directory with the following structure:

```text
$VPHONE_BENCHMARK_ROOT/runs/<run-id>/
├── manifest.json
├── results.jsonl
├── summary.md
├── report.html
└── tasks/<task-id>/
```

Review `report.html` first. For a failed task, inspect its corresponding
`tasks/<task-id>/` directory for the trace, screenshots, and error details.

## 11. End the Test

Clean up in the following order:

1. Stop the Agent container:
   - Command-line workflow: Run the `stop_command` printed by
     `start-agent-daemon`.
   - WebUI workflow: Press `Ctrl-C` in the terminal to stop `runner webui`; it
     also stops the daemon container that it started. If a local SSH port
     forward was created, close the tunnel with
     `pkill -f 'L 18765:127.0.0.1:8765'`.
2. Press `Ctrl-C` in the terminal running the Bridge to stop the VPhone Bridge.
3. If the VM is no longer needed, terminate `make boot` using the normal
   procedure for the `vphone-cli` project. Do not delete `vm/` directly or
   forcibly clear VM data.

## 12. Troubleshooting

### `vphone.sock` Does Not Exist

Confirm that `make boot` is still running and that the
`vm/vphone.sock` path belongs to the same VPhone repository.

### The Bridge Reports That the Port Is in Use During Startup

```bash
lsof -nP -iTCP:"$VPHONE_BRIDGE_PORT" -sTCP:LISTEN
```

After identifying the process using the port, decide whether to stop the old
Bridge or use another port. If the port is changed, update the Bridge, daemon,
and runner commands consistently.

### `open_url` Returns `guest_ssh_failed`

This means that the socket does not support `open_url`, and the Bridge entered
the SSH fallback path, but login or `uiopen` failed. Check the default key and
guest environment:

```bash
ssh -i "$VPHONE_GUEST_SSH_IDENTITY" \
  -p "$VPHONE_GUEST_SSH_PORT" \
  -o BatchMode=yes \
  "$VPHONE_GUEST_SSH_USER@$VPHONE_GUEST_SSH_HOST" \
  'test -x /var/jb/usr/bin/uiopen && echo uiopen-ready'
```

If the IP address, port, user, or key changes, update `vphone.env` and restart
the Bridge.

### All Tool Calls Return 429

Check that both the daemon startup command and runner command explicitly use
`--benchmark-task-id "$VPHONE_BENCHMARK_TASK_ID"`. If a previous manual
validation did not release the environment, call `/api/release` first to
release the same task ID.

### The Agent Container Cannot Access the Bridge

Check whether `docker_environment_bridge_endpoint` in the
`start-agent-daemon` output is `http://host.docker.internal:8899`, and confirm
that Docker Desktop is running normally. When testing directly on mac-black,
do not add another SSH tunnel.

### The Agent Reports `missing the OpenRouter API key`

This means that the failure occurred during model initialization, before the
Bridge or VPhone received any tool calls. If `tools=0` and `screenshots=0` are
also shown, proceed in this order:

1. Run the check in Section 3.4 and confirm that `$VPHONE_AGENT_CONFIG` shows
   `api_key_present=true`.
2. Stop the current `vphone-ios` daemon. A running container does not
   automatically reload the configuration.
3. Restart it using the exact command in Section 7 and confirm that the command
   includes `--agent-config "$VPHONE_AGENT_CONFIG"`.
4. Set `$VPHONE_AGENT_URL` again using the new `agent_url`; do not reuse the old
   port.
5. Run `screenshot_home` again.

### Click Positions Are Systematically Offset

Screenshots are scaled, but touch input must use `normalized` coordinates from
0 to 1000. Do not treat screenshot dimensions as native device pixel
coordinates. The current public tools reject the `pixel` coordinate space.

### The Rubric Shows 0/N With `--no-judge`

This is expected. `--no-judge` validates only actions, protocols, and timeouts;
it cannot prove that multi-step task results are correct. To obtain a
correctness assessment, enable the Judge or manually inspect the final
screenshots and answers in the report.

### The WebUI Does Not Open or the Tunnel Is Inaccessible

First confirm that the WebUI process is still running on mac-black and that its
log contains a `Benchmark Web UI:` line. When accessing it through a local
tunnel, confirm that `ssh -L 18765:127.0.0.1:8765 mac-black` did not exit with
an error. With `-f -N`, it runs in the background; use
`lsof -nP -iTCP:18765 -sTCP:LISTEN` to confirm that the local port is listening.
Then open `http://127.0.0.1:18765` in the browser. If ports are changed, keep
the local forwarding port and `--port` setting consistent.

### A WebUI Job Remains at `STARTING_AGENT` for a Long Time

The first run builds the `aiden-agent-daemon:local` image, which can take some
time. The docker build progress is visible in the **Log** panel. If the job
never enters `RUNNING`, check whether Docker Desktop is operating normally with
`docker info`, and whether the **Agent config** panel shows
`provider=openrouter` and `model=moonshotai/kimi-k2.6` rather than the default
template. If the configuration is incorrect, restart the WebUI with
`--agent-config "$VPHONE_AGENT_CONFIG"`.

### Final Screenshots and Conclusions in WebUI Reports Still Require Manual Review

As with the command-line workflow, when **Enable judge** is not selected, the
rubric may show `0/N`. This indicates only that the execution path and timeout
checks passed; it does not prove that the multi-step task result is correct.
For a correctness assessment, enable the Judge or manually compare the final
screenshots and answers in the `report`.
