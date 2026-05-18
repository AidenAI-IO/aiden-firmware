# Aiden Go Agent

Go-based agent daemon and CLI built on `github.com/tmc/langchaingo` for the
Aiden hardware demo.

This directory contains two user-facing binaries:

- `cmd/daemon`: long-running daemon with either Web UI mode or device-side audio mode
- `cmd/demo`: simple CLI runner for local testing

## Current capabilities

- OpenAI-compatible model calls via `openai` and `openrouter`
- Local text-model support via `ollama`
- Built-in tool calling with device-control tools
- Skill discovery and runtime activation from `SKILL.md`
- Conversation memory persisted under the config directory
- Web UI with chat history, browser audio recording, and attachment support
- Device-side speech pipeline with VAD, STT, and TTS

## Current tool set

Built-in tools are registered in [internal/agent/tools.go](./internal/agent/tools.go#L12):

- `activate_skill`
- `keyboard_tap`
- `keyboard_text`
- `mouse_click`
- `mouse_move`
- `mouse_scroll`
- `touch_gesture`
- `screenshot`
- `audio_volume`
- `shell`

## Config layout

The daemon expects a config directory, not a single config file.

Typical layout:

```text
your-config-dir/
├── agent.toml
├── skills/
│   └── my-skill/
│       └── SKILL.md
├── log/
└── memory/
```

- `agent.toml` is required
- `skills/` is optional and is auto-discovered if present
- `log/` and `memory/` are created/used by the runtime under `configDir`

TOML is the supported format. JSON config is deprecated.

See the baseline example at [config/agent.toml](./config/agent.toml#L1).

## Quick start

Assume you are in `src/agent`:

```bash
go build ./cmd/daemon
go build ./cmd/demo
```

Run the CLI demo:

```bash
go run ./cmd/demo -config ./config -input "What tools do you have?"
```

Run the daemon in Web UI mode:

```bash
go run ./cmd/daemon -config ./config -addr :8080
```

Then open `http://localhost:8080`.

## Example `agent.toml`

Minimal Web UI setup:

```toml
instruction = "You are a helpful assistant. Use tools when they help."
max_iterations = 6
input_mode = "text"

[model]
provider = "openrouter"
model = "bytedance-seed/seed-2.0-lite"
token_env = "OPENROUTER_API_KEY"
temperature = 0.2
max_tokens = 1000

[audio]
socket = "/run/audio_service/audio_service.sock"
sample_rate = 32000
channels = 1
bit_width = 16

[hid]
keyboard_device = "/dev/hidg0"
mouse_device = "/dev/hidg1"
frame_socket = "/run/frame_service/frame_service.sock"
```

Minimal speech-to-text setup:

```toml
instruction = "You are a helpful assistant. Use tools when they help."
input_mode = "stt"
trigger_mode = "manual"
energy_threshold = 500
silence_ms = 1000
min_speech_ms = 300

[model]
provider = "openrouter"
model = "bytedance-seed/seed-2.0-lite"
token_env = "OPENROUTER_API_KEY"

[stt]
provider = "openai-whisper"
api_key = "sk-..."
model = "whisper-1"

[tts]
provider = "minimax"
api_key = "..."
voice_id = "male-qn-qingse"
emotion = "happy"
speed = 1.0

[audio]
socket = "/run/audio_service/audio_service.sock"
sample_rate = 16000
channels = 1
bit_width = 16

[hid]
keyboard_device = "/dev/hidg0"
mouse_device = "/dev/hidg1"
frame_socket = "/run/frame_service/frame_service.sock"
```

## Configuration reference

Top-level fields from [internal/agent/config.go](./internal/agent/config.go#L11):

- `instruction`: base system instruction for the agent
- `additional_prompt`: extra prompt text field in config; currently not wired into prompt construction
- `max_iterations`: max tool-usage loop iterations, default `6`
- `input_mode`: `text`, `stt`, or `audio`
- `trigger_mode`: `manual` or `wakeup`; only used in `stt`/`audio`
- `energy_threshold`, `silence_ms`, `min_speech_ms`: VAD tuning

Model config:

- `provider`: `openai`, `openrouter`, `ollama`, or `fake`
- `model`: model name, required except for `fake`
- `base_url`: optional custom endpoint
- `api_key`: literal API key
- `token_env`: environment variable name for model auth
- `temperature`, `max_tokens`

Notes:

- `token_env` exists only on `[model]`
- `[stt]` and `[tts]` currently use literal `api_key` fields
- `model_text` exists in the config struct but is not currently used by the runtime

## Input modes

### `input_mode = "text"`

Starts the HTTP server and embedded Web UI.

Available behavior:

- text chat
- browser audio recording and upload
- binary attachments such as image/audio
- optional TTS playback of assistant replies through `audio_service`

Web audio behavior in text mode:

- if `[stt]` is configured, uploaded/recorded browser audio is transcribed first
- otherwise audio is forwarded to the model as an audio attachment

### `input_mode = "stt"`

Starts the device-side audio loop:

1. record PCM from `audio_service`
2. detect utterance boundaries with VAD
3. transcribe WAV via STT
4. send text to the agent runtime
5. speak the reply via TTS

### `input_mode = "audio"`

Starts the device-side audio loop, but forwards the utterance to the model as
an audio attachment instead of transcribing it first.

Use this only with a model/backend that actually supports audio input in the
selected provider path. The OpenAI-compatible path in
[openai_compatible_model.go](./internal/agent/openai_compatible_model.go#L117)
can serialize audio attachments as `input_audio`.

## Trigger modes

### `trigger_mode = "manual"`

Used in device audio modes. Press Enter to start recording and Enter again to
stop.

### `trigger_mode = "wakeup"`

Used in device audio modes. Waits for the GPIO wakeup path implemented in
[cmd/daemon/main.go](./cmd/daemon/main.go#L154)
before recording.

## Web UI and audio mode relationship

One daemon instance currently runs exactly one of these:

- Web UI mode via `input_mode = "text"`
- device-side audio loop via `input_mode = "stt"` or `input_mode = "audio"`

They do not run together in the same process today.

This is an important current limitation of
[cmd/daemon/main.go](./cmd/daemon/main.go#L43).

## Skills

Skills are loaded from `configDir/skills/**/SKILL.md`.

Example:

```markdown
---
name: ui-operator
description: Prefer screenshot-driven UI inspection before clicking.
metadata:
  allowed_tools: [screenshot, mouse_click, mouse_move, keyboard_text]
---

Take a screenshot before interacting with an unfamiliar UI.
Prefer describing what you see before clicking.
```

What is currently implemented:

- skill discovery by scanning `SKILL.md`
- runtime activation via `activate_skill`
- `allowed_tools` restriction enforcement
- instruction injection into the system prompt

What is only parsed, not actively enforced by the runtime today:

- `preferred_model`
- `allowed_children`

The sample skill files under [config/skills](./config/skills/planner/SKILL.md#L1) are older placeholders and may reference tools that do not exist in the current Go runtime. Treat them as format examples, not guaranteed-valid production skills.

## Runtime behavior

The runtime:

- keeps windowed memory for agent name `default`
- persists memory under `configDir/memory`
- writes logs under `configDir/log`
- emits tool-call and tool-result events to the Web UI
- records token usage when the backend returns usage metadata

Relevant code:

- [internal/agent/runtime.go](./internal/agent/runtime.go#L20)
- [internal/agent/server.go](./internal/agent/server.go#L16)

## External dependencies

Depending on which tools and modes you use, the agent expects these external services/devices:

- `audio_service` for recording, playback, and `audio_volume`
- `frame_service` for `screenshot`
- HID gadget devices such as `/dev/hidg0` and `/dev/hidg1`

If these are unavailable, the corresponding tools or audio paths will fail at runtime.

## CLI usage

`cmd/demo`:

```bash
go run ./cmd/demo -config ./config -input "Hello"
go run ./cmd/demo -config ./config -skills my-skill,another-skill -input "Inspect the UI"
go run ./cmd/demo -config ./config -clear-memory -show-memory -input "Start fresh"
```

`cmd/daemon`:

```bash
go run ./cmd/daemon -config ./config -addr :8080
```

## Known limitations

- Web UI mode and device-side audio mode are mutually exclusive per daemon instance
- Tencent ASR is declared but not implemented yet
- `preferred_model`, `allowed_children`, and `model_text` are not wired into execution yet
- Some sample skills in `config/skills/` reference legacy tools and should be updated before real use
