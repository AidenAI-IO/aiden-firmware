# Agent Overview

The Aiden Go Agent is located in `src/agent/` and is built on `github.com/tmc/langchaingo`. It serves as both a long-running daemon and the device-side tool control plane.

## Binaries

| Entry Point  | Description                                                     |
| ------------ | --------------------------------------------------------------- |
| `cmd/daemon` | Long-running daemon providing the Web UI and optional device voice loop |

After cross-compilation, the daemon binary is:

```text
build/bin/agent
```

In the firmware, it is installed by default to:

```text
/oem/usr/bin/agent
```

## Current Capabilities

- Registered model providers: `openai`, `openrouter`, `kimi`, `kimi-cn`, `volcengine`, `ollama`
- Built-in tool calling: HID, screenshots, audio volume, shell
- HTTP Tool API for Web UI, external agents, or manual invocation
- Auto-discovery and runtime activation of skills from `SKILL.md`
- Single-agent execution loop with streamlined tool calling and response handling
- Conversation memory persistence, session memory compaction; see [Session Memory Compaction](session-memory.md)
- Device / Task Episode memory design; see [Memory Plane Design](memory-plane.md)
- Web UI: chat history, browser recording, attachments, Tool Lab, Skill Export
- iOS Live Activity / Dynamic Island task status; see [Live Activity / Dynamic Island](live-activity.md)
- Device-side voice pipeline: VAD / STT / TTS

## Run Modes

The HTTP server and Web UI start in every input mode. `input_mode` controls whether the daemon also runs the device-side voice loop:

| Mode   | Behavior                                                                    |
| ------ | --------------------------------------------------------------------------- |
| `text` | HTTP server and Web UI only                                                  |
| `stt`  | HTTP server and Web UI plus device recording → VAD → STT → LLM → TTS loop    |

In `stt` mode, Web UI requests and device voice interactions share the same Agent runtime and run in the same daemon process.

## Startup

Local development:

```bash
cd src/agent
go run ./cmd/daemon -dir ./config -addr :8080
```

Device service:

```bash
/oem/usr/bin/aiden-env-run /oem/usr/bin/agent -dir /userdata/agent -addr :8080
```

## Built-in Tools

The conversational Agent receives every registered, currently available tool needed for memory, device operation, skill management, shell and web research, phone data, and prepared script execution (`run_script`).

`current_time` and `calculator` are not registered built-in tools; the Agent uses `shell` for controller-local precise time, timezone, and deterministic calculations. The script-file authoring tools (`list_scripts`, `read_script`, and `write_script`) are omitted from the default LLM `tools` request and can be restored with `load_all_tools = true`. This switch does not change HTTP exposure: `skill_manage` and `skill_mark_used` remain unavailable through the HTTP Tool API.

For tool details and HTTP invocation methods, see [Tools HTTP API](tools-http-api.md).
