# Agent Overview

The Aiden Go Agent is located in `src/agent/` and is built on `github.com/tmc/langchaingo`. It serves as both a long-running daemon and the device-side tool control plane.

## Binaries

| Entry Point  | Description                                                     |
| ------------ | --------------------------------------------------------------- |
| `cmd/daemon` | Long-running daemon supporting Web UI mode or device voice mode |
| `cmd/demo`   | Local CLI runner for development testing                        |

After cross-compilation, the daemon binary is:

```text
build/bin/agent
```

In the firmware, it is installed by default to:

```text
/oem/usr/bin/agent
```

## Current Capabilities

- OpenAI-compatible model calls: `openai`, `openrouter`
- Local text models: `ollama`
- Built-in tool calling: HID, screenshots, audio volume, shell
- HTTP Tool API for Web UI, external agents, or manual invocation
- Auto-discovery and runtime activation of skills from `SKILL.md`
- Three-stage role loop (`default` / `plan` / `execution`): simple tasks are directly executed by the planner, complex tasks go through `commit_plan` into executor-verifier collaboration; see [Agent Context Lifecycle](context-lifecycle.md)
- Conversation memory persistence, session memory compaction; see [Session Memory Compaction](session-memory.md)
- Device / Task Episode memory design; see [Memory Plane Design](memory-plane.md)
- Web UI: chat history, browser recording, attachments, Tool Lab, Skill Export
- iOS Live Activity / Dynamic Island task status; see [Live Activity / Dynamic Island](live-activity.md)
- Device-side voice pipeline: VAD / STT / TTS

## Run Modes

Determined by `input_mode` in `agent.toml`:

| Mode   | Behavior                                 |
| ------ | ---------------------------------------- |
| `text` | Start HTTP server and Web UI             |
| `stt`  | Device recording → VAD → STT → LLM → TTS |

Currently, one daemon instance can only run in one mode: Web UI mode and device voice mode cannot run simultaneously in the same process.

## Startup

Local development:

```bash
cd src/agent
go run ./cmd/daemon -config ./config -addr :8080
```

Device service:

```bash
/oem/usr/bin/aiden-env-run /oem/usr/bin/agent -config /userdata/agent -addr :8080
```

CLI runner:

```bash
cd src/agent
go run ./cmd/demo -config ./config -input "What tools do you have?"
go run ./cmd/demo -config ./config -skills my-skill -input "Inspect the UI"
go run ./cmd/demo -config ./config -clear-memory -show-memory -input "Start fresh"
```

## Built-in Tools

The conversational Agent has access to the full built-in tool set: memory, time, skill read/list/manage, screenshot, stable-screen wait, quick action, touch gesture, mouse and keyboard input, verified text entry, app launch, audio volume, image diff, shell, web research (search / wikipedia / scraper), weather, calculator, phone data (clipboard / calendar / contacts / notification), and prepared script execution (`run_script`).

The only tools withheld from the default Agent catalog are the script-file authoring tools — `list_scripts`, `read_script`, and `write_script`. The Agent runs prepared scripts through `run_script`; authoring script files stays behind the HTTP Tool Lab. Separately, the skill-maintenance tools `skill_manage` and `skill_mark_used` remain callable by the Agent but are hidden from the HTTP Tool API.

For tool details and HTTP invocation methods, see [Tools HTTP API](tools-http-api.md).
